# Spec stress test — hosted/container hub scenarios

Date: 2026-07-30 · Verdict per scenario: PASS (spec covers it) / FIXED (spec amended) / SPIKE (must prove)

| # | Scenario | Verdict | How the spec answers it |
|---|----------|---------|------------------------|
| 1 | Hub in a container, agents on laptops | PASS | `hubUrl` client-side vs `bind` server-side (ADR-006); auto-start only on loopback (ADR-001) |
| 2 | Container has no home dir / read-only FS | FIXED | env-only operation, `AGENTHUB_*` overrides for every key; single `AGENTHUB_DATA_DIR` volume (ADR-006) |
| 3 | Reverse proxy kills idle long-polls | FIXED | default `wait=25s` under LB timeouts; immediate re-poll; no WebSocket in the contract (ADR-006) |
| 4 | Hub restarts / container redeploys | PASS | NDJSON store + client-held integer cursors survive restarts; registry re-fills from heartbeats (ADR-001/002) |
| 5 | Public exposure of a hosted hub | FIXED | auth flips on when bound non-loopback; fails closed without a token; `/health` stays open for probes; token by env NAME only (ADR-006) |
| 6 | Two `agenthub listen` for one agent (double answers) | FIXED | listen is a singleton — lockfile + liveness, skill adopts existing (ADR-003) |
| 7 | Two hubs on one data dir | FIXED | single-writer lockfile; horizontal scaling out of scope v1 (ADR-006) |
| 8 | Attachment saved on hub host, read by remote client | FIXED | media served via `/media/<id>` by the hub, never by host path (ADR-006) |
| 9 | Agent dies without deregistering | PASS | heartbeat TTL ages it out of `GET /agents` (ADR-002) |
| 10 | Clock skew between hub and clients | PASS | ids and timestamps are hub-generated UTC; cursors are line offsets, not times (ADR-001/006) |
| 11 | Question arrives while target session is closed | SPIKE | envelope waits in inbox; answered on next session/listen start — latency semantics to prove in Spike-01 |
| 12 | Duplicate delivery (at-least-once) | PASS | consumers dedupe by envelope `id` (ADR-001) |
| 13 | 200-message thread context bloat | PASS | read-time projection capped by `lastMessages`; full thread by explicit fetch (ADR-001, Spike-02) |
| 14 | External A2A client (no skill) asks our agent | SPIKE | plain-served card + ask → thread poll; conformance proven in Spike-03 |
| 15 | Config missing on first run | FIXED | `agenthubconfig.json` auto-created with defaults by any verb (ADR-001) |

## Spike amendments

- **Spike-02** gains a containerized leg: run the hub via the scratch image behind a proxy
  (nginx, 30s idle timeout) and prove the curl receive loop + cursor survival across a
  container redeploy.
- **Spike-01** must also cover scenario 11 (offline target → answer on reconnect).
