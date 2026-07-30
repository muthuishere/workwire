# ADR-001: hub-core is a dumb, LLM-free, HTTP-only message hub

Status: accepted · Date: 2026-07-30

## Context

The previous messenger (deemwarworkspace/messenger, ~9.8K lines Go) proved the value of a
per-host hub: one canonical envelope, an NDJSON inbox with integer cursors, `reply_to:"last"`,
and `/health` discovery. Its complexity came from an installed daemon (config read at boot →
restart to change anything), an owner-vs-agent role doctrine, push subscriptions that forced
consumers to run HTTP servers, and per-channel registration ceremonies.

agent-hub is a from-scratch, open-source rebuild.

## Decision

- hub-core (`agenthub serve`) is **dumb plumbing**: it stores envelopes, serves them back, and
  keeps a registry. It never calls an LLM, never holds platform credentials, never embeds a
  channel.
- **HTTP is the only required transport.** Receive is `GET /inbox?since=N` with optional
  long-poll (`&wait=30`). WebSocket/SSE may be added later as an optional accelerator, never
  the contract.
- **No push subscriptions, no consumer HTTP servers.** Every consumer polls with its own
  locally persisted cursor.
- **No daemon install ceremony.** Any peer that finds no hub on the configured port may start
  one (`/health` returns `{"service":"agenthub"}` as the discover-don't-start probe).
- **Messages carry context at read time.** The hub stores single copies; when delivering a
  message it attaches `context: [last X messages of the thread]`. Senders never bundle history.
  `GET /threads/<id>?last=N` serves more on demand.
- Static settings live in `~/.config/agenthub/agenthubconfig.json` (port, data dir,
  `lastMessages` context depth, timeouts). The file is optional — defaults work with no file.
  Everything dynamic (agents, adapters) is runtime state via the API (ADR-002), never config.
- Language: **Go**, single static binary, minimal deps. (Chosen for the one-binary OSS story;
  revisit only if a TS ecosystem play outweighs it.)

## Envelope (carried over — the best idea of the old system)

`{id, from, to, thread_id, reply_to, text, ts, kind, meta, attachments[]}` — one wire value for
human channels, agent chatter, and A2A tasks alike. `reply_to:"last"` resolves the newest
inbound on the thread/peer.

## Consequences

- Consumers are trivially writable in any language (curl-grade).
- Hot behavior changes need no restart: registry is dynamic, config is re-read.
- At-least-once delivery with client-side cursors; consumers dedupe by `id`.
- Long-poll relay for A2A (ADR-002) is the hardest part of the core → Spike-01.
