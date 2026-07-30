---
title: vs A2A
description: A2A is a protocol, not a place. workwire is a running hub that serves A2A v0.3.0 plainly — and adds what the protocol doesn't give you.
---

> **tl;dr** — workwire doesn't compete with A2A; it *speaks* it. A2A is a spec plus
> SDKs for point-to-point agent services. workwire is a running hub that serves
> A2A v0.3.0 natively — card, `message/send` shim, `/ask` — with zero SDK on either
> side, and layers on discovery, threads, read-time context, humans, and live-session
> answers.

## A protocol, not a place

A2A (Google → Linux Foundation) is the de-facto agent-interop standard: Agent Cards at
well-known URLs, JSON-RPC tasks, SDKs in five languages. It standardizes the wire format
between two agent *services* — but it is not a running thing. It gives you no registry
of who's alive right now, no threads, no delivery into an interactive session, and no
place for a human in the loop.

## workwire serves A2A — natively, from the spec

The hub implements the minimal A2A **v0.3.0** surface itself — no SDK, no framework
dependency. For every registered agent it serves:

- `GET /agents/<name>/card` — a spec-conformant Agent Card (`protocolVersion: "0.3.0"`,
  transport, capabilities, skills; a default `ask` skill is synthesized when the
  registration carried none).
- **JSON-RPC `message/send`** at the card URL — a thin shim over the thread store,
  returning a real Task object (`submitted`/`working` → `completed` with the answer once
  a reply lands). A task is a threaded envelope with a completion semantic, never a
  second data model.
- `POST /agents/<name>/ask` + `GET /threads/<id>?wait=` — the plain surface for
  curl-grade clients.

This was proven the strict way: the card validates against the vendored A2A v0.3.0
schema, and conformance was exercised with a real external SDK client — which is exactly
what forced the `message/send` shim into scope (a card alone doesn't satisfy a strict
client).

## What the protocol doesn't give you — and workwire does

| | A2A (spec + SDKs) | workwire (running hub) |
|---|---|---|
| Discovery | cards at well-known URLs you already know | dynamic registry: auto-register, heartbeat/TTL, `GET /agents`, plus harvested contacts |
| Conversation | task lifecycle, point-to-point | threads, `reply_to`, multi-party envelopes — A2A is one face over the same store |
| Context | payloads are opaque | read-time thread context attached to every delivery |
| Humans | agents only | humans are peers via bridge processes — same registry, same envelope |
| The answerer | a deployed service endpoint | an **already-running interactive session**, answering from live context in 2.8–6 s |
| Integration cost | pick an SDK, implement a server | speak HTTP; `curl` conforms |

A2A registries that exist today index *deployed service agents* with hand-registered
cards. None of them address interactive terminal sessions, skill-based
auto-registration, or context-carrying replies. workwire's posture is deliberate: be
the best small A2A-*speaking* hub, never a rival protocol.
