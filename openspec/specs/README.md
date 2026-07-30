# workwire — capability specs

Five capabilities; each `<capability>/spec.md` is the contract (requirements + scenarios).
Design rationale lives in `docs/adr/001`–`008`; coverage matrix in `docs/spec-stress-test.md`.

| Capability | Summary |
|---|---|
| [hub-core](hub-core/spec.md) | The dumb, LLM-free HTTP hub: one envelope shape, NDJSON segment store with rotation/retention, hub-assigned sequence cursors, long-poll `/inbox` with read-time context projection, `/threads`, `/health`, `/media`, tombstone excision, env-overridable config, single-writer data-dir lock. |
| [registry-a2a](registry-a2a/spec.md) | Dynamic agent registry (register/heartbeat/TTL/persisted cache) and the A2A v0.3.0 surface: agent cards, plain `/ask` with `reply_to`-matched completion, JSON-RPC `message/send` shim, and the hub-side listen lease. |
| [auth](auth/spec.md) | Explicit `authMode` (token default, never inferred from bind), auto-minted 0600 admin token, hub-issued per-agent credentials, server-stamped `from`, declared exposure that fails closed, ask policy, and authenticated provenance on delivered envelopes. |
| [agent-skill](agent-skill/spec.md) | The two-way embedded skill: install, first-run hub probe/autostart, session registration, singleton `workwire listen` (flock + lease) delivering into a durable session inbox file, concrete-id answer path, answer-only safety default, outbound `peers`/`send`/`ask`. |
| [contacts](contacts/spec.md) | People directory: TOFU harvest from inbound traffic, explicit adds/aliases, fuzzy lookup, `--to` resolution that refuses to guess, verification, tombstone purge, and the merged agent+contact `peers` view. |

Cross-spec conventions (canonical sources):
- Registration: `201` + `{agentId, agentSecret}`; collision `409` `{"error":"name taken","name","suggestion"}` — registry-a2a R1/R2.
- Auth failures: `401` missing/invalid credential, `403` valid credential for another agent or policy denial — auth R4/R8.
- Inbox/reset responses carry `next` (never `cursor`) — hub-core R5.
- `/health` body — hub-core R9.
- Excision tombstones — hub-core R13 (ADR-008).
