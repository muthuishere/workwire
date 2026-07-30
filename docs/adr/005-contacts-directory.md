# ADR-005: contacts directory (find people without raw ids)

Status: accepted · Date: 2026-07-30

## Context

The old messenger had no people lookup at all — recipients were raw chat ids / group JIDs /
conversation ids the user discovered manually. "Find people and send data" needs a directory.

## Decision

- The hub maintains a **contacts store** harvested automatically from traffic: every inbound
  envelope's sender (name, peer/adapter, platform id, last-seen) is upserted into
  `contacts.ndjson` in the data dir. Explicit adds/aliases via `POST /contacts`.
- `GET /contacts?q=` does fuzzy name lookup; `agenthub send --to "Suguna"` resolves through
  it (ambiguity → the caller gets the candidate list, no guessing).
- Registered agents (ADR-002) and contacts are both "people": `agenthub peers` merges the
  live agent registry with the contacts directory.

## Consequences

- Directory quality grows with use; zero setup.
- Names are aliases over platform ids — the envelope still carries the resolved raw id, so
  nothing downstream changes.
