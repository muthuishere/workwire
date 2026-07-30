---
title: HTTP API
description: The three surfaces — register/heartbeat, envelopes with cursors and context, and the served A2A face — with curl examples straight from the specs.
---

Everything is plain HTTP on one port (default **14411**). Three surfaces:

1. **Registry** — `POST /agents`, `GET /agents`, heartbeat/TTL, listen lease.
2. **Envelopes** — `POST /send`, `GET /inbox`, `GET /threads`, deletion.
3. **A2A** — per-agent card, `/ask`, JSON-RPC `message/send` shim.

Plus unauthenticated `GET /health` for discovery and probes.

## Auth in one paragraph

`authMode` is explicit: `"token"` (default) or `"open"` — never inferred from the bind
address. In token mode the hub auto-mints a local admin token (file mode 0600; local
clients auto-read it — zero ceremony on localhost). First registration of an agent name
returns `{agentId, agentSecret}`, and **every** subsequent action as that agent
authenticates with it; `from` is stamped server-side from that identity. Status codes
are consistent: **401** = missing/invalid credential, **403** = valid credential acting
as a different agent (or an ask blocked by the target's `askPolicy`). Remote clients
take the token from the env var named by `WORKWIRE_TOKEN_ENV` — values never appear in
config, code, or logs.

## Registry

```bash
# register (first time returns credentials — shown once)
curl -s -X POST "$HUB/agents" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"repoA","description":"repo A session","project":"~/src/repoA"}'
# → 201 {"agentId":"…","agentSecret":"…"}
# name taken? → 409 {"error":"name taken","name":"repoA","suggestion":"repoA-2"}

# who's live (within the 120s TTL)
curl -s "$HUB/agents" -H "Authorization: Bearer $TOKEN"
```

Liveness: heartbeat interval 30 s, TTL 120 s — and **any authenticated request
refreshes liveness**, so a back-to-back long-poll loop never flaps. The registry
survives hub restarts (last-seen cache in the data dir) and is discovery-only, never
authorization.

`POST /agents/<name>/listen-lease` is the cross-machine singleton guarantee for
listeners: one live lease per agentId; expired holders are claimable; releases via
`DELETE` with the current `leaseId`.

## Envelopes

```bash
# send (thread_id optional — omitted starts a thread; reply_to:"last" resolves at ingest)
curl -s -X POST "$HUB/send" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"repoA","text":"where is auth?"}'
# → 200 {"id":"m-1","thread_id":"t-1","ts":"…"}

# receive — the single receive shape (agent param is MANDATORY; no firehose)
curl -s "$HUB/inbox?agent=repoA&since=0&wait=25&context=5" \
  -H "Authorization: Bearer $TOKEN"
# → 200 {"messages":[{…, "context":[…kind:"context"…]}], "next":7}
# cursor fell behind retention? → {"messages":[],"next":<earliest>,"reset":true}

# thread history beyond the projected context (cap 20)
curl -s "$HUB/threads/t-1?last=50" -H "Authorization: Bearer $TOKEN"

# excision — tombstones: id survives, content is gone from ALL reads
curl -s -X DELETE "$HUB/messages/m-1" -H "Authorization: Bearer $TOKEN"
curl -s -X DELETE "$HUB/threads/t-1"  -H "Authorization: Bearer $TOKEN"
```

The receive loop, in full — no reconnect state machine:

```bash
C=0
while :; do
  R=$(curl -s "$HUB/inbox?agent=repoA&since=$C&wait=25" -H "Authorization: Bearer $TOKEN")
  # …handle $R.messages, dedupe by id…
  C=$(jq .next <<<"$R")
done
```

Measured: **1.1 ms** average delivery latency via long-poll vs **2228 ms** for a
5-second tick — and `wait=25` returns cleanly under 30 s proxy idle timeouts.

## A2A surface

```bash
# spec-conformant A2A v0.3.0 agent card
curl -s "$HUB/agents/repoA/card"

# plain ask — hub writes a kind:"question" envelope, holds no task state
curl -s -X POST "$HUB/agents/repoA/ask" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"text":"where does auth live?"}'
# → 202 {"thread_id":"t-2","message_id":"m-9"}

# wait for the answer: complete iff reply_to == m-9 lands on the thread
curl -s "$HUB/threads/t-2?wait=25" -H "Authorization: Bearer $TOKEN"

# strict A2A clients: JSON-RPC message/send at the card url → a Task object
curl -s -X POST "$HUB/agents/repoA/rpc" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send",
       "params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}'
# → {"jsonrpc":"2.0","id":1,"result":{…Task, status.state:"submitted"…}}
```

Asks are queued even for aged-out agents (delivered on their next poll), but the
target's `askPolicy` (`"any"` authenticated peer by default, or an `allowPeers`
allowlist) is enforced before queueing — a disallowed peer gets 403. Unknown JSON-RPC
methods get `-32601` per spec.

## Health

```bash
curl -s "$HUB/health"
# → 200 {"service":"workwire","schemaVersion":…,"apiVersion":…}
```

Unauthenticated by design (LB probes), leaks nothing, and doubles as the
discover-don't-start probe and the version-negotiation surface.

The full normative surface lives in the [openspec](/workwire/references/) — every
requirement with GIVEN/WHEN/THEN scenarios.
