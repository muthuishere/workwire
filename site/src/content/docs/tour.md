---
title: The 60-second tour
description: One envelope, one receive shape, one registry — everything workwire does, at a glance.
---

## One envelope

Every message on the wire — agent chatter, human traffic through a bridge peer, an A2A
task — is the same value:

```json
{ "id": "m-9", "from": "repoA", "to": "repoB", "thread_id": "t-1",
  "reply_to": "m-7", "text": "…", "ts": "2026-07-30T09:00:00Z",
  "kind": "question", "meta": {}, "attachments": [] }
```

`id`, `ts`, and `from` are hub-generated/stamped at ingest — `from` comes from the
authenticated identity, never the request body, so a forged sender is impossible by
construction. `reply_to:"last"` is resolved exactly once at ingest, thread-scoped, and
persisted as a concrete id.

## One receive shape

```bash
GET /inbox?agent=repoB&since=41&wait=25&context=5
```

That is the *entire* receive contract, everywhere. Long-poll, not push: the request
returns instantly when messages exist, otherwise holds up to `wait` (default 25 s —
deliberately under common 30–60 s proxy idle timeouts). Measured: **1.1 ms** average
delivery latency, vs **2228 ms** average for a 5-second tick poll — ~2000× better,
with a one-line `while`/`curl` loop as the whole consumer.

Cursors are hub-assigned per-recipient sequence numbers — decoupled from file layout,
so store rotation, hub restart, and container redeploy never invalidate them. Fall
behind retention and you get `reset:true` plus a cursor to rebase on; never a silent
skip. Delivery is at-least-once; dedupe by `id`.

## Context rides along at read time

Each delivered message carries `context`: the last 5 envelopes of its thread (request
up to 20 with `context=N`), each stamped `kind:"context"`. Context is background — it
never advances the cursor and is never mistaken for an answer. Senders never bundle
history; the hub stores single copies and projects at read time. Need more?
`GET /threads/<id>?last=N`.

## One registry, humans included

Peers `POST /agents` a card at startup and stay live via heartbeat (30 s interval,
120 s TTL — and *any* authenticated request refreshes liveness, so a listen loop needs
no heartbeat thread). `workwire peers` merges the live registry with a contacts
directory harvested automatically from traffic (trust-on-first-use — unverified
contacts need confirmation before you send to them).

Humans are not a special case: a human joins through whatever peer process fronts them,
and to the wire that peer is just another worker. workwire itself ships **zero channel
code**.

## A2A, plainly served

For every registered agent the hub serves an A2A v0.3.0 card at
`/agents/<name>/card`, a JSON-RPC `message/send` shim at the card URL, and the plain
`/ask` verb. A task is a threaded envelope with a completion semantic — never a second
data model. External A2A clients interoperate from the spec, no SDK on either side.

## The answer loop

1. Someone asks: `POST /agents/repoA/ask` → the hub writes a `kind:"question"` envelope
   and returns `{thread_id, message_id}`. The hub holds no task state.
2. repoA's singleton listener long-polls its inbox and drops the question into the
   session inbox file of the *already-running* agent session.
3. The session answers from its own live context and replies with
   `reply_to: <question id>`.
4. The asker's `GET /threads/<id>?wait=25` completes the moment that reply lands.

Proven on a real interactive session: **2.8–6 s** end to end.
