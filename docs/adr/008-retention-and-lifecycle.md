# ADR-008: retention, deletion, and liveness lifecycle

Status: accepted · Date: 2026-07-30

## Context

The stress-team review (findings 17, 18, 20) found three append-only NDJSON stores that
grow forever, no way to delete or excise anything (a compliance problem for an open-source
product), and a heartbeat contract with no TTL — an agent in a 25s long-poll could age out
of the registry while actively listening.

## Decision

### Store growth

- The hub store is **NDJSON segments with rotation**. Cursors are hub-assigned sequence
  numbers (ADR-001), so rotation is invisible to clients.
- **Retention window is configurable**: default 30 days OR 1 GB, whichever is hit first.
  A cursor older than retained history returns `reset:true` per ADR-001 — never a silent
  skip.

### Deletion and excision

- `DELETE /messages/<id>` writes a **tombstone**: the id remains (dedupe and thread graphs
  stay stable), the content is excised from all reads — inbox replay, `/threads`, and the
  read-time context projection all honor tombstones. This is the pasted-secret excision path.
- **Contact purge verb**: `DELETE /contacts/<id>` removes an entry from the directory.
- **Thread excision**: `DELETE /threads/<id>` tombstones every envelope on the thread.
- Rationale: an open-source product needs GDPR-ish hygiene from day one.

### Heartbeat and liveness

- Heartbeat interval **30s**, agent TTL **120s**. Rule: `TTL >= 2 × max(heartbeat
  interval, wait)` so one dropped long-poll can never age an active agent out.
- **ANY authenticated request from an agent refreshes its liveness** — issuing or completing
  a long-poll counts; a single-threaded listen loop needs no separate heartbeat thread.
- **The registry is discovery-only, never authorization.** `POST /agents/<name>/ask` accepts
  and queues the envelope regardless of registry presence (authorization is ADR-007's job).

### Session inbox files

- The skill rotates its session inbox file at a size threshold; cursors into it survive
  rotation the same way hub cursors do (resolves Spike-01 risk 6).

## Consequences

- The hub can run for years on one volume without ENOSPC; clients never notice rotation.
- Secrets pasted into a thread can actually be excised, not just aged out.
- Registry flapping under proxies/long-polls is impossible within the TTL rule.
