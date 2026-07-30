# Spec stress test — hosted/container hub scenarios

Date: 2026-07-30 · Verdict per scenario: PASS (spec covers it) / FIXED (spec amended) / SPIKE (must prove)

| # | Scenario | Verdict | How the spec answers it |
|---|----------|---------|------------------------|
| 1 | Hub in a container, agents on laptops | PASS | `hubUrl` client-side vs `bind` server-side (ADR-006); auto-start only on loopback (ADR-001) |
| 2 | Container has no home dir / read-only FS | FIXED | env-only operation, `WORKWIRE_*` overrides for every key; single `WORKWIRE_DATA_DIR` volume (ADR-006) |
| 3 | Reverse proxy kills idle long-polls | FIXED | default `wait=25s` under LB timeouts; immediate re-poll; no WebSocket in the contract (ADR-006) |
| 4 | Hub restarts / container redeploys | PASS | NDJSON store + hub-assigned sequence cursors survive restarts (reset:true rebase on truncation); registry re-fills from heartbeats (ADR-001/002) |
| 5 | Public exposure of a hosted hub | FIXED | explicit authMode (never bind-inferred), per-agent secrets, WORKWIRE_EXPOSED declared exposure, open+exposed fails closed (ADR-007); `/health` stays open for probes |
| 6 | Two `workwire listen` for one agent (double answers) | FIXED | listen is a singleton — lockfile + liveness, skill adopts existing (ADR-003) |
| 7 | Two hubs on one data dir | FIXED | single-writer lockfile; horizontal scaling out of scope v1 (ADR-006) |
| 8 | Attachment saved on hub host, read by remote client | FIXED | media served via `/media/<id>` by the hub, never by host path (ADR-006) |
| 9 | Agent dies without deregistering | PASS | heartbeat TTL ages it out of `GET /agents` (ADR-002) |
| 10 | Clock skew between hub and clients | PASS | ids and timestamps are hub-generated UTC; cursors are hub-assigned per-recipient sequence numbers, not times (ADR-001/006) |
| 11 | Question arrives while target session is closed | SPIKE | envelope waits in inbox; answered on next session/listen start — latency semantics to prove in Spike-01 |
| 12 | Duplicate delivery (at-least-once) | SPIKE | consumers MUST dedupe by envelope `id` (ADR-001); id-dedup not yet exercised by a spike — cover in implementation tests |
| 13 | 200-message thread context bloat | PASS | read-time projection capped by `lastMessages`; full thread by explicit fetch (ADR-001, Spike-02) |
| 14 | External A2A client (no skill) asks our agent | SPIKE | card validated + curl-client round trip proven (Spike-03); strict SDK client needs the pinned v0.3.0 `message/send` shim (ADR-002) — SDK conformance still open |
| 15 | Config missing on first run | FIXED | `workwire.json` auto-created with defaults by any verb (ADR-001) |
| 16 | Impersonation attempt (register as an existing name) | FIXED | second registration gets 409 + suggestion; `from` stamped server-side from hub-issued credentials — no silent takeover (ADR-007) |
| 17 | Hub behind reverse proxy, bound loopback | FIXED | explicit `authMode` never inferred from bind; `WORKWIRE_EXPOSED=1` declares exposure and forces token auth; open+exposed refuses to start (ADR-007) |
| 18 | Container restart leaves a stale lock | FIXED | OS-held advisory lock (flock/F_SETLK) dies with the process — never stale after kill -9 or redeploy; pid lockfiles dropped (ADR-003) |
| 19 | Two hosts run `listen` for the same agent | FIXED | hub-side listen lease per agentId, renewed with heartbeat; local flock is only the fast path (ADR-003) |
| 20 | Prompt-injection question against answer-only default | FIXED | inbound text is data, never instructions; authenticated provenance on envelopes; no shell/write tools on inbound-triggered turns unless opted in (ADR-003/007) |
| 21 | Real interactive session wake (idle / mid-turn / fresh) | SPIKE | file-inbox delivery proven only with simulated sessions (Spike-01); measured wake latency on a real Claude Code session + one non-Claude harness gates the v1 promise (ADR-003) |

## Spike amendments

- **Spike-02** gains a containerized leg: run the hub via the scratch image behind a
  timeout-enforcing reverse proxy written in Go (`httputil.ReverseProxy`, ~20 lines, 30s idle
  timeout — no nginx; we have Go) and prove the curl receive loop + cursor survival across a
  container redeploy.
- **Spike-01** must also cover scenario 11 (offline target → answer on reconnect).
