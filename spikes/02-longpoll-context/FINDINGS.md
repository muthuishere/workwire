# Spike-02 findings — long-poll ergonomics + read-time thread context

Date: 2026-07-30 · Platform: macOS arm64, Go 1.26.3, Docker (daemon running).
Code: this directory (`module spike02`) — `store.go` (NDJSON store, line cursors,
projection, `reply_to:"last"`), `cmd/hub` (HTTP hub), `cmd/proxy` (22-line
`httputil.ReverseProxy` with 30s idle timeout), `Dockerfile` (scratch image),
`scripts/measure.py` + `scripts/container-demo.sh` (everything below is scripted
and re-runnable). Spike ports: hub 14421, proxy 14422 (14411 was occupied by a
running Spike-01 hub — a real reminder that the default port will collide across
spikes/tools; fine for prod since there's one hub per host, but spikes must not
assume 14411 is free).

## Q1 — does `GET /inbox?since=N&wait=25` give push-like latency with curl-grade simplicity?

**Yes, decisively.**

### Long-poll vs 5s tick latency (10 samples each, hub direct)

| mode | avg | max | samples (ms) |
|---|---|---|---|
| long-poll `wait=25` | **1.1 ms** | 1.4 ms | 0.8 1.4 0.9 1.1 1.4 1.4 0.8 1.3 1.4 0.7 |
| 5s tick polling | **2228 ms** | 4360 ms | 974 413 867 2492 2424 4078 2043 2872 4360 1752 |

Long-poll is ~2000x lower latency than a 5s tick and matches theory (tick avg ≈
2.5s uniform). Through the container+proxy path the first delivery still landed
within the same request (curl total 3.018s for a send fired at t+3s — ~18ms
overhead including docker port-forward + proxy hop).

### Simplicity / restart safety

- The whole receive loop is one line, no reconnect state machine:
  `C=0; while :; do R=$(curl -s "hub/inbox?since=$C&wait=25"); …; C=$(jq .cursor <<<"$R"); done`
  Demonstrated live through the proxy: two injected messages arrived on
  consecutive iterations with cursors 3→4.
- **Cursor survives hub restart**: killed the hub, restarted on the same NDJSON
  file, polled with the pre-restart cursor → got exactly the one new message,
  no loss, no duplicates. Same result across a full **container redeploy**
  (`docker rm -f` + fresh `docker run` on the same volume; pre-redeploy cursor=1
  delivered only the post-redeploy message, with context attached).
- **Cursor older/ahead of the file** (store truncated/replaced): hub answers
  `{"messages":[],"cursor":0,"reset":true}`. Verdict: explicit `reset:true` +
  authoritative `cursor` is the right contract — the client rebases and moves
  on; recommend clients treat `reset` as "adopt the returned cursor, optionally
  re-fetch threads you care about". This should go in the spec (ADR-001 doesn't
  name it yet).

### Proxy-safety (ADR-006's 25s-under-30s claim)

- Empty long-poll at `wait=25` through the timeout-enforcing proxy: **HTTP 200
  at 25.02s** — never killed. Re-poll immediately; the loop is seamless.
- Control at `wait=35`: proxy kills it at **30.01s with a 502** — proving the
  timeout is real and the 25s default is doing actual work, not luck.
- Proxy is 22 lines of Go (`cmd/proxy/main.go`): `httputil.NewSingleHostReverseProxy`
  + `Transport.ResponseHeaderTimeout=30s` + server `IdleTimeout=30s`. No nginx.

### Container leg

- `FROM scratch` + static binary: image is **2.36 MB, 1 layer**. Env-only config
  (`AGENTHUB_BIND`, `AGENTHUB_DATA_DIR`), one volume (`/data`) — matches ADR-006.

## Q2 — is read-time context projection enough without payload bloat?

**Yes.** Payload for delivering **one** new message on a 50-message thread
(~70-char lines, realistic chat length):

| lastMessages | response bytes | vs bare |
|---|---|---|
| 0 (bare) | 207 | 1.0x |
| 3 | 759 | 3.7x |
| 5 | 1119 | 5.4x |
| 10 | 2019 | 9.8x |
| 20 | 3819 | 18.5x |

- Growth is linear, ~180 bytes/context message at this text length. Even
  X=10 is 2 KB — trivially fine on the wire; the real cost is **tokens in the
  answering LLM's prompt**, which is also linear, so the knob is about prompt
  discipline, not bandwidth.
- **Grounded-answer check at default depth**: the delivered envelope after the
  container redeploy carried `context:[{"text":"hello through proxy",…}]`
  inline — the consumer could answer "what did alice say before?" from the
  single `/inbox` response with **zero extra fetches**. `GET /threads/t-big?last=3`
  works as the explicit escape hatch when depth isn't enough (verified).
- **`reply_to:"last"`** resolves at append time on the same store: on the
  50-message thread, bob's `reply_to:"last"` resolved to the newest message
  *not authored by bob* (the newest inbound), and stayed resolved in the stored
  line. Edge found: when the only thread history is your own messages, it
  resolves to `""` (no inbound exists) — correct per ADR-001 wording, but the
  spec should state it explicitly.

## Verdicts vs success criteria

| criterion | verdict |
|---|---|
| One-liner curl receive loop works and survives hub restart | **PASS** — loop shown through proxy; cursor valid across process restart AND container redeploy |
| Grounded answer from inlined context only, at default depth | **PASS** — context rides the envelope; no thread fetch needed at X=5 |
| Recommendation for default `lastMessages` and cap | **PASS** — below |
| (stress-test #3) proxy kills idle long-polls | **PASS** — wait=25 returns 200 at 25s; control wait=35 is 502 at 30s |
| (stress-test #4) restart/redeploy cursor survival | **PASS** |

## Recommendations

1. **Default `lastMessages = 5`** (~1.1 KB per delivered message here, ~5x bare).
   Five turns is enough to ground a reply in a two-party thread (question +
   answer + follow-up still fully visible); 3 goes blind on any back-and-forth,
   10 doubles prompt cost for rarely-used depth.
2. **Max cap = 20**, enforced server-side on the `context=` query param (the
   spike hub clamps to 20). Beyond that, callers must use
   `GET /threads/<id>?last=N` explicitly — keeps `/inbox` deliveries bounded
   (≤ ~4 KB/message at chat-length texts) no matter what a client asks for.
3. **Spec additions**: (a) document the `reset:true` + authoritative-cursor
   response for cursor-ahead-of-file; (b) state that `reply_to:"last"` yields
   empty when no inbound exists on the thread; (c) keep `wait=25` as the
   default — it is empirically proxy-safe with a 5s margin under a 30s idle
   timeout.
4. **Non-finding to carry forward**: the in-memory mirror of the NDJSON file is
   fine for a spike but the real hub should index thread→line offsets to avoid
   O(file) scans for projection on large stores.
