# ADR-004: channels are external adapter peers, not built-ins

Status: accepted · Date: 2026-07-30

## Context

The old messenger compiled telegram/whatsapp/teams adapters into the hub, which dragged
platform credentials, webhook auth zoos, and restart-to-add-a-channel ceremony into the core.
The open-source core must hold zero platform code.

## Decision

- A channel adapter (telegram, whatsapp, teams, …) is **just another peer**: a separate
  process that registers on the hub like an agent (ADR-002), forwards platform-inbound
  messages as envelopes, and delivers hub messages addressed to it back to the platform.
- Adapters live in this repo under `adapters/` as independently runnable binaries/scripts,
  but the hub neither knows nor cares — anyone can publish an adapter.
- Platform secrets stay in the adapter's environment (by NAME), never in hub config or the
  hub process.
- Humans on a channel are addressable the same way agents are: an adapter's registration
  makes `send to telegram/<name>` work through the uniform registry.

## Consequences

- The hub core stays credential-free and small — the open-source boundary is clean.
- Adding a channel = starting a process. No hub restart, no config edit.
- Each adapter can be spiked/shipped independently; telegram first (Spike-03).
