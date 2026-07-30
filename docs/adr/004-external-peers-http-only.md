# ADR-004: workwire ships only HTTP — every non-agent participant is an external peer

Status: accepted (supersedes "channels as external peers") · Date: 2026-07-30

## Context

The old messenger compiled telegram/whatsapp/teams adapters into the hub, dragging platform
credentials, webhook auth zoos, and restart-to-add-a-channel ceremony into the core. workwire
is not a channel product at all — channel bridging already exists elsewhere (e.g. the
existing messenger). workwire's scope is the work between workers.

## Decision

- **workwire ships zero channel code.** No telegram, no whatsapp, no teams — not even as
  optional adapters in this repo.
- **The only door is HTTP.** Anything can participate by speaking the same three surfaces
  every agent uses: `POST /agents` (register + heartbeat), `POST /send` / `GET
  /inbox?since=N&wait=` (envelopes), and the plainly served A2A card/ask (ADR-002).
- If someone wants humans-on-a-channel in the mesh, they run their own bridge process as an
  ordinary peer. Spike-03 proved this works with zero hub changes; that proof is about
  *external peers over HTTP*, not about telegram.
- Platform secrets, webhook formats, and channel quirks are permanently out of scope for
  this repo.

## Consequences

- The core stays credential-free, tiny, and "only HTTP" is literally the whole integration
  contract — the open-source boundary could not be cleaner.
- Human reachability is composable, not bundled: bridges are someone's peer process, and the
  registry/envelope/contacts model treats whatever they forward like any other worker.
