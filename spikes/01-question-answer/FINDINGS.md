# Spike-01 findings: question → running session → answer

Date: 2026-07-30 · Status: **round trip proven, well under the 60s budget**

## What was built

Single Go module (`spike01`, `main.go`, ~450 lines) with subcommands:

- `serve` — stub hub per ADR-001: in-memory store + NDJSON append/replay
  (`state/hub/messages.ndjson`), `POST /send`, `GET /inbox?agent=X&since=N&wait=25`
  (real long-poll via a broadcast channel, not busy-wait), `POST /agents`, `GET /agents`,
  `GET /threads/<id>?last=N`, `/health` → `{"service":"agenthub"}`. Envelope is the ADR-001
  shape; inbox delivery attaches `context: [last 5 thread msgs]` at read time.
- `listen` — the ADR-003 prototype: singleton via pid lockfile with liveness check +
  stale-lock reaping (`state/run/<agent>.lock`), registers the agent card, long-polls with a
  locally persisted cursor (`state/<agent>.cursor`), delivers each `kind:question` (envelope
  + attached context) into the "running session" via a selectable mechanism.
- `responder` — simulates the running Claude session: tails the session inbox file, answers
  from a knowledge file (stand-in for live repo context) + the delivered thread context, and
  `POST /send`s an answer envelope with `reply_to` + same `thread_id`.
- `ask` — posts a question envelope, polls the thread, prints the answer with latency.

Runner: `run-scenarios.sh` · raw outputs in `out/`.

## Measured results (real runs)

| Scenario | Result |
|---|---|
| Online round trip (file mechanism) | **0.30s** end to end (`out/ask1.out`) |
| Callback mechanism round trip | 0.60s (`out/ask-cb.out`) |
| Singleton lock | second `listen` for same agent refused: `listener already running for repoA (pid 78402)` |
| FIFO with no attached reader | delivery **failed**: `no reader on fifo (session not waiting): device not configured` (ENXIO) |
| Offline target (question sent, listener+responder started 3s later) | answer arrived, **3.32s** total = the 3s downtime + ~0.3s work (`out/ask-offline.out`) |
| Hub restart | NDJSON replayed; full thread (question + answer) intact after restart (`out/thread-after-restart.json`) |

Answers demonstrably used target-side context: e.g. "where does the auth module live and
does it use JWT?" → "The auth module lives in internal/auth and uses session cookies, not
JWT" — content that exists only in the responder's knowledge file, never in the question.

## Delivery mechanism evaluation — the spike's main output

**(a) Session inbox file (append NDJSON; session-side hook/loop tails it) — RECOMMENDED.**
- Worked first try, 0.3s round trip. Durable by construction: if the session is down, the
  question sits in the file and is consumed on next start (this is exactly how the offline
  case passed — the responder resumed from a persisted byte offset).
- Decouples listen-loop liveness from session liveness — each side can restart independently;
  at-least-once with dedupe by envelope `id` / offset file.
- Maps 1:1 onto the proven agent-telegram pattern and onto Claude Code reality: a Stop-hook
  or skill-driven loop can check the file between turns; no exotic IPC.
- Trivially debuggable (`cat` the file), works over any filesystem, language-agnostic.

**(b) Named pipe / FIFO — rejected.**
- Concretely demonstrated failure: non-blocking open gets ENXIO whenever the session isn't
  currently blocked reading — the exact normal state of an agent session that's busy on a
  turn. The alternatives are worse: blocking open stalls the whole listen loop; buffering in
  the listener reinvents the file. Also zero durability (kernel buffer, lost on restart) and
  no replay. FIFOs only fit a consumer that is *always* parked on a read — agent sessions are not.

**(c) Callback command — viable secondary, not the default.**
- Worked (0.6s, `out/cb.sh` posting the answer via curl). Good when the "session" is actually
  a spawnable one-shot (e.g. `claude -p`, a webhook). But it makes the listener responsible
  for execution (timeouts, crashes, concurrency), loses durability if the command fails, and
  does not reach an *already-running interactive* session at all — it spawns a new context,
  which defeats the "answer from live session context" promise.

**Decision: (a) file inbox is THE mechanism**; keep (c) as an opt-in for headless/scripted
responders. The listen loop stays a dumb waiter: poll hub → append to session inbox →
persist cursor.

## Offline-target behavior

Two independent buffers make this robust with no special code:
1. Hub-side: cursor-based inbox — a question posted while the listener is down is picked up
   on reconnect (`since=N` persisted per agent).
2. Session-side: the inbox file — delivered questions survive session restarts; the responder
   resumes from its offset file.

The asker just polls the thread; it never needs to know the target was down.

## Open risks

1. **Waking a real interactive Claude session** is still simulated. The responder polls the
   file every 300ms; a real session only "wakes" at hook points (Stop hook, next turn) or via
   a background skill loop — worst-case latency is one session turn, not 0.3s. Needs the
   two-real-sessions test (spike plan step 4).
2. **Context manifest shape** (ADR-003) unexplored — the knowledge file is a placeholder;
   read-only answerer enforcement untested.
3. **Registry is memory-only**: after hub restart `GET /agents` returned empty (scenario 5).
   Needs the ADR-002 last-seen cache in the data dir + heartbeat/age-out.
4. **Inbox scan is O(all messages)** per poll; needs per-agent indexing later.
5. `reply_to:"last"` resolution and multi-question interleaving on one thread not exercised;
   dedupe currently relies on cursor monotonicity only.
6. Session inbox file grows unboundedly; needs rotation/compaction.

## Success criteria check

- Round trip < 60s with both sides live: **yes, 0.3s.**
- Answer uses target's context: **yes** (content only present target-side).
- Listen singleton + restart survival: **yes** (lockfile with liveness check + stale reap,
  persisted cursor demonstrated).
- Delivery mechanism decided and documented: **yes — session inbox file (a).**
