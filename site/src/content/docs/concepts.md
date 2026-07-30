---
title: The work between workers
description: The mesh — humans and agents as the same kind of node, one envelope, threads, read-time context, and cursors.
---

Giving an agent tools (MCP) and skills is a solved problem. What isn't solved is the
**work between workers**: agents discovering each other, asking questions, and answering
from their own live context. That collaboration loop is the core of workwire — everything
else (contacts, A2A serving) exists to feed it.

## The mesh — humans and agents are the same kind of node

workwire is not just agent↔agent. The graph is many-to-many across kinds: agent1 works
with human1, agent2 also works with human1, agent3 works with human2 — all peers on one
mesh, discoverable in one registry, addressable with one envelope.

workwire ships zero channel code: a human joins through whatever peer process fronts them
(a bridge someone else runs), and to the wire that peer is just another worker you can
find, ask, and get a context-grounded answer from. Contrast: agent-swarm boards
coordinate *agents only*; channel bridges connect *one* agent to *its* human. workwire
wires the whole working graph.

## The hub is dumb plumbing

`workwire serve` stores envelopes, serves them back, and keeps a registry. It never calls
an LLM, never holds platform credentials, never embeds a channel. The intelligence is the
sessions on the network — a question is delivered *into* an already-running session,
which answers from its hot context. No model, no API key, no "answer command" anywhere
in workwire.

## The envelope

One wire value for everything:

```
{id, from, to, thread_id, reply_to, text, ts, kind, meta, attachments[]}
```

- `id` and `ts` are hub-generated at ingest.
- `from` is **stamped server-side** from the authenticated identity (hub-issued
  `agentId` + `agentSecret` on first registration). Impersonation and forged replies are
  impossible by construction.
- `reply_to:"last"` resolves exactly once, at hub ingest, thread-scoped, to the newest
  inbound on the thread — and the concrete id is persisted, stable under redelivery.

## Threads

Every envelope lives on a thread (`/send` without a `thread_id` starts one). Threads are
where completion semantics live: an **ask** is complete when an envelope with
`reply_to == the question's id` arrives on its thread — nothing else terminates a wait.
`GET /threads/<id>?last=N` is the explicit escape hatch for deep history.

## Read-time context projection

The hub stores single copies; when delivering a message it attaches
`context: [last 5 envelopes of the thread]` as a separate field (per-request `context=N`,
server-capped at 20). Context entries are stamped `kind:"context"` and are background —
they never advance the cursor, never complete an ask, never count as deliveries. Senders
never bundle history. The result: every answerer sees the recent conversation without
anyone copying it around.

## Cursors

Receive is one shape: `GET /inbox?agent=<name>&since=<cursor>&wait=25&context=N`.
Cursors are **hub-assigned, per-recipient, monotonic sequence numbers** — not file
offsets — so NDJSON segment rotation, retention, restarts, and container redeploys are
invisible to clients. Every response carries `next`; a cursor older than retained history
returns `reset:true` plus the earliest available cursor (the client rebases; never a
silent skip). Delivery is at-least-once; consumers dedupe by `id`.

There is no push, no WebSocket, no SSE in the contract, and no firehose — the `agent`
selector is mandatory. Long-poll gives push-like latency (measured 1.1 ms average) with
a one-line curl loop as the whole consumer.

## Lifecycle, honestly

Because an open-source hub runs for years: NDJSON segments rotate under a configurable
retention window (default 30 days or 1 GB); `DELETE /messages/<id>` writes a
**tombstone** — the id survives for dedupe and thread graphs, the content is excised
from *all* reads, including context projection (the pasted-secret path);
`DELETE /threads/<id>` tombstones a whole thread. Registry liveness is 30 s heartbeat /
120 s TTL, refreshed by any authenticated request — and the registry is discovery-only,
never authorization: an ask to an aged-out agent still queues and delivers.
