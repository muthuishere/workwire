# ADR-002: hub-native A2A registry and relay (no client library dependency)

Status: accepted · Date: 2026-07-30

## Context

We want any agent/terminal to discover other agents and ask them questions. toolnexus can
expose one process's toolkit as an A2A agent, but the hub's role is different: it serves
agent cards *on behalf of* every registered agent and relays tasks into their subscriber
loops. The A2A surface needed is small — a card JSON, task send, result poll.

## Decision

- The hub implements the **minimal A2A spec natively**: no SDK, no toolnexus dependency.
- **Version pinned: A2A v0.3.0.** The hub serves the real 0.3.0 transport, not just a card:
  JSON-RPC `message/send` at the card URL (a thin shim over the thread store returning a
  Task object), alongside our plain `/ask` + `/threads` surface. Spike-03 proved the card
  validates but ALSO proved a strict SDK client needs `message/send` — so the shim is in
  scope, and SDK-client interop is claimed only once a real SDK client passes conformance.
  Migration trigger: adopt A2A 1.0.0 (`interfaces[]` card restructure) as a v2 change when
  the ecosystem's SDKs default to it.
- **Dynamic registry, zero config:** peers `POST /agents` an agent card (name, description,
  capabilities/skills, project/cwd hint) at startup and refresh with a heartbeat. Stale agents
  age out. `GET /agents` lists who is on the hub now. Persisted only as a last-seen cache in
  the data dir.
- Per registered agent the hub serves:
  - `GET /agents/<name>/card` — A2A agent card (spec-conformant JSON)
  - `POST /agents/<name>/ask` — plain serving, no relay machinery: the hub writes an
    envelope addressed to the agent and returns `{thread_id, message_id}`; the asker reads
    the answer off the thread (`GET /threads/<id>`), optionally with `?wait=` long-poll
    sugar. The hub never holds tasks or tracks completion state.
- **/ask completion semantics:** an ask is complete when an envelope with
  `reply_to == the question's message_id` arrives on the thread — nothing else terminates
  a `?wait=`. Context projections are read-only and stripped (`kind: "context"`), so they
  can never be mistaken for the answer.
- **/health carries versions:** the response gains `schemaVersion` (envelope) and
  `apiVersion` (hub surface) fields, giving polling peers a negotiation path when the
  envelope evolves.
- A2A tasks are **not a second data model** — a task is a threaded envelope with a completion
  semantic. Conversation (multi-party, async) stays plain envelopes; A2A is the
  request/response face over the same store.

## Consequences

- "Find people and send data" = `GET /agents` + `POST /agents/<name>/ask` — two calls.
- The riskiest piece is not the hub (plain serving) but the delivery of a question into a
  running agent session and its answer coming back → Spike-01 proves the full round trip.
- Any external peer registers through the same door (ADR-004) — bridges someone else runs,
  scripts, other hubs — so every participant is uniformly addressable over plain HTTP.
