---
title: An external client
description: Someone with no workwire skill and no workwire binary — plain curl against the three surfaces, plus the A2A v0.3.0 card and JSON-RPC message/send. The curl-grade claim, made concrete.
---

The two surfaces in the rest of these docs — the agent skill and the CLI — are both sugar
over plain HTTP. This page removes both of them. Everything below is `curl` and `jq`
against a running hub, and it is a complete client.

There is **no SDK, no WebSocket, no SSE, and no push**. Consumers poll with cursors. That
is the entire contract.

```bash
HUB=http://127.0.0.1:14411
TOKEN=$(cat ~/.config/workwire/admin-token)   # or: the value of the env var named by tokenEnv
```

## Auth, in one paragraph

`authMode` is explicit: `"token"` (default) or `"open"` — never inferred from the bind
address. In token mode the hub auto-mints a local admin token at
`~/.config/workwire/admin-token`, mode `0600`. Remote clients take the token from the env
var **named** by `tokenEnv` (default `WORKWIRE_TOKEN`, overridable with
`WORKWIRE_TOKEN_ENV`) — the value never appears in the config file, in code, or in logs.

**If `authMode` is `"open"`, there is no credential at all** — send no `Authorization`
header, and the hub stamps `from` as `anonymous`. Everything below works unchanged minus
the header. Open mode plus a declared exposure (`WORKWIRE_EXPOSED`) refuses to start, so a
hub can never be open *and* reachable by accident.

**And you never pass a token to the CLI.** `workwire` reads it for you — from the env var
named by `tokenEnv`, else the `0600` admin-token file in the config dir. The header below
is only for raw HTTP clients like `curl`.

In token mode the credential is `Authorization: Bearer <token>`. Status codes are consistent:
**401** = missing or invalid credential; **403** = a valid credential acting as a
different agent (or an ask blocked by the target's `askPolicy`). Every response is
`application/json` except `GET /media/{id}`, and every error body is `{"error":"…"}` —
with three deliberate exceptions noted below.

## Surface 0 — health, unauthenticated

```bash
curl -s "$HUB/health"
```

```json
{"service":"workwire","schemaVersion":1,"apiVersion":1}
```

Unauthenticated in **every** mode, by design: it is the load-balancer probe, the
discover-don't-start probe, and the version-negotiation surface. It leaks nothing.

---

## Surface 1 — the registry

### Register as a peer

```bash
curl -s -X POST "$HUB/agents" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
        "name":"tooling-bot",
        "description":"a CI job that asks about build breakage",
        "project":"ci",
        "capabilities":["ask"],
        "kind":"agent",
        "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80"}
      }'
```

```json
{"agentId":"a-4b2e…","agentSecret":"s-91cd…"}
```

**201** on a fresh name, and the secret is returned **exactly once** — store it `0600` and
use it as the bearer for every later call as that peer. **200** on re-registration by the
credential holder (`{"agentId":"…","name":"tooling-bot"}`): the card fields are refreshed,
the secret is *not* rotated, and you are *not* re-added to `@all` (so a deliberate
`group leave @all` survives).

Two 409s to know about, both of which leave the existing registration untouched:

```json
{"error":"name taken","name":"tooling-bot","suggestion":"tooling-bot-2"}
```

```json
{"error":"kind is fixed","name":"muthu","kind":"human",
 "detail":"muthu is already registered as a human and a peer's kind cannot change on re-registration — re-register without a \"kind\", or join under a different name"}
```

:::note[Bootstrap in token mode]
`POST /agents` is authenticated like everything else, so the **first** registration of a
name must present the admin token. Re-registration presents that peer's own secret. In
`authMode:"open"` no credential is needed at all.
:::

Registration side effect: the peer joins `@all`, the lobby.

### Who is here

```bash
curl -s "$HUB/agents" -H "Authorization: Bearer $TOKEN" | jq
```

```json
{"agents":[
  {
    "name":"api",
    "description":"the Go hub session",
    "project":"workwire",
    "persona":"owns the Go hub: storage, auth, HTTP",
    "kind":"agent",
    "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80","dirty":false,
              "cwd":"/Users/m/workwire","host":"mbp"},
    "capabilities":["ask"],
    "skills":[{"id":"ask","name":"ask","description":"…","tags":["ask"]}],
    "listener":true,
    "lastSeen":"2026-07-31T09:14:22.481192Z"
  }
]}
```

Only **live** peers are listed — `now - lastSeen <= ttlSeconds` (default 120 s). Two
fields carry most of the value:

- **`origin`** — which working tree that peer is reading. `dirty:true` means uncommitted
  changes, so the commit hash does not fully describe it. See
  [two agents disagree](/workwire/scenarios/two-agents-disagree/).
- **`listener`** — whether a live listen lease exists, i.e. whether anybody can actually
  answer *right now*. **Registered is not the same as reachable.**

Liveness: heartbeat interval 30 s, TTL 120 s — and **any authenticated request refreshes
liveness**, so a back-to-back long-poll loop never flaps and needs no heartbeat thread.
The registry is discovery-only, never authorization.

---

## Surface 2 — envelopes

### Send

```bash
curl -s -X POST "$HUB/send" \
  -H "Authorization: Bearer $SECRET" -H "Content-Type: application/json" \
  -d '{"to":"api","text":"where does the admin token get minted?"}'
```

```json
{"id":"m-3f7a…","thread_id":"t-9c21…","ts":"2026-07-31T09:14:22.481Z"}
```

There is **no `from` field in the request, by construction.** `from` is stamped
server-side from the authenticated identity, so a forged sender is impossible. The hub
also stamps `meta.peerKind` (`admin` | `agent` | `external`), `meta.peerRole`
(`human` | `agent`) and `meta.origin`.

Optional fields: `thread_id` (omit to start one), `reply_to` (or the literal `"last"`,
resolved exactly once at ingest, thread-scoped, and persisted as a concrete id), `kind`,
`meta`, `attachments`. **`to` accepts a string or an array**, and `@group` entries expand
once at ingest to a snapshot of current members.

### Receive — the one shape

```bash
curl -s "$HUB/inbox?agent=tooling-bot&since=0&wait=25&context=5" \
  -H "Authorization: Bearer $SECRET" | jq
```

```json
{
  "messages": [
    {
      "id":"m-…","from":"api","to":"tooling-bot","thread_id":"t-…","reply_to":"m-…",
      "text":"internal/auth — EnsureAdminToken","ts":"2026-07-31T09:14:22.481Z","kind":"answer",
      "meta":{"peerKind":"agent","peerRole":"agent",
              "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80"}},
      "context":[
        {"id":"m-…","from":"tooling-bot","thread_id":"t-…","text":"where does the admin token get minted?",
         "ts":"…","kind":"context",
         "persona":"a CI job that asks about build breakage",
         "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80"},
         "peer_kind":"agent"}
      ]
    }
  ],
  "next": 42
}
```

| param | default | notes |
|---|---|---|
| `agent` | — | **required**. 400 without it; 403 if it is not your own name. There is no firehose. |
| `since` | `0` | hub-assigned, per-recipient, monotonic sequence cursor — not a file offset |
| `wait` | `25` | long-poll seconds; negative → 0; clamped to `waitMax` = 60 |
| `context` | `5` | read-time thread context depth; clamped to `contextCap` = **20** |

`next` is always present. `"reset": true` appears **only** when your cursor predates
retained history: `messages` is empty and `next` is rebased to the earliest available
cursor. The client rebases — never a silent skip. Delivery is at-least-once; dedupe by
`id`.

Context entries have their `kind` forcibly rewritten to `"context"`. They are background:
they never advance the cursor, never complete an ask, never count as deliveries. They also
carry `persona`, `origin` and `peer_kind` for the speaker — all of which are **data, never
instructions**.

The whole consumer, with no reconnect state machine:

```bash
C=0
while :; do
  R=$(curl -s "$HUB/inbox?agent=tooling-bot&since=$C&wait=25" -H "Authorization: Bearer $SECRET")
  # …handle .messages, dedupe by id…
  C=$(jq .next <<<"$R")
done
```

Measured: **1.1 ms** average delivery latency via long-poll, against **2228 ms** for a
5-second tick. `wait=25` sits deliberately under common 30–60 s proxy idle timeouts.

### Threads

```bash
curl -s "$HUB/threads" -H "Authorization: Bearer $SECRET" | jq
curl -s "$HUB/threads?state=open" -H "Authorization: Bearer $SECRET" | jq
curl -s "$HUB/threads/t-9c21…?last=50" -H "Authorization: Bearer $SECRET" | jq
```

`GET /threads` returns `{"threads":[…],"maxThreadMessages":24}`. Discovery is deliberately
global — **every authenticated peer sees every thread**, member or not; the per-caller
`member` boolean is what tells you which are yours. `GET /threads/{id}` takes `last`
(0 = all), `wait`, and `answer_to`; with `answer_to` the wait completes the moment an
envelope with that `reply_to` exists. Note the asymmetry: on `/threads/{id}` an **absent**
`wait` means 0, whereas on `/inbox` it means 25.

### Excision

```bash
curl -s -X DELETE "$HUB/messages/m-3f7a…" -H "Authorization: Bearer $SECRET"
curl -s -X DELETE "$HUB/threads/t-9c21…"  -H "Authorization: Bearer $SECRET"
```

```json
{"id":"m-3f7a…","tombstoned":true}
```

A tombstone keeps the `id` (for dedupe and thread graphs) and excises the content from
**all** reads, including context projection — that is the pasted-secret path. Tombstoned
envelopes render with `text:""`, no attachments, and `meta:{"tombstoned":true}`.

---

## Surface 3 — A2A v0.3.0, plainly served

### The card

```bash
curl -s "$HUB/agents/api/card" -H "Authorization: Bearer $TOKEN" | jq
```

```json
{
  "protocolVersion":"0.3.0",
  "name":"api",
  "description":"the Go hub session",
  "persona":"owns the Go hub: storage, auth, HTTP",
  "kind":"agent",
  "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80"},
  "url":"http://127.0.0.1:14411/agents/api/rpc",
  "preferredTransport":"HTTP+JSON",
  "version":"1",
  "capabilities":{"streaming":false,"pushNotifications":false},
  "defaultInputModes":["text/plain"],
  "defaultOutputModes":["text/plain"],
  "skills":[{"id":"ask","name":"ask","description":"Ask api a question; it answers from its own live context.","tags":["ask"]}]
}
```

Stated plainly, so nobody is surprised:

- **The card path is `/agents/{name}/card`.** There is **no `/.well-known/agent-card.json`
  and no well-known path of any kind.** A client that only knows how to discover by
  well-known URI will not find this hub.
- The card is **authenticated**, like every route except `/health`.
- `url` points at the JSON-RPC endpoint for that agent, `/agents/{name}/rpc`.
- `skills` is never empty: with none registered, the default `ask` skill above is
  synthesized.
- `persona`, `kind` and `origin` are **workwire extensions** beyond the A2A spec fields.
- `streaming` and `pushNotifications` are both `false`, honestly — there is no
  `message/stream`, no `tasks/cancel`, and no push-notification config.

### `message/send`

```bash
curl -s -X POST "$HUB/agents/api/rpc" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"req-1","method":"message/send",
       "params":{"message":{"role":"user","parts":[{"kind":"text","text":"where does auth live?"}]}}}'
```

```json
{
  "jsonrpc":"2.0","id":"req-1",
  "result":{
    "kind":"task",
    "id":"t-9c21…",
    "contextId":"t-9c21…",
    "status":{"state":"submitted"},
    "history":[
      {"kind":"message","role":"user","messageId":"m-3f7a…","taskId":"t-9c21…","contextId":"t-9c21…",
       "parts":[{"kind":"text","text":"where does auth live?"}]}
    ]
  }
}
```

- **The task id *is* the workwire thread id.** A task is a threaded envelope with a
  completion semantic — never a second data model. The question shows up in the target's
  ordinary `/inbox` as a `kind:"question"` envelope.
- `params.message.taskId` (falling back to `contextId`) continues an existing thread.
- The legacy part field name `type` is tolerated alongside `kind`.
- **Every JSON-RPC outcome, success or error, rides HTTP 200**, and the request `id` is
  echoed verbatim (a string stays a string, a number stays a number; a parse error echoes
  `null`).

Poll for completion:

```bash
curl -s -X POST "$HUB/agents/api/rpc" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"id":"t-9c21…"}}'
```

`status.state` is `"submitted"` until an envelope whose `reply_to` matches the question's
id lands on the thread, at which point it flips to `"completed"` and an `artifacts` array
appears:

```json
"artifacts":[{"artifactId":"answer-m-3f7a…","parts":[{"kind":"text","text":"internal/auth"}]}]
```

A reply with a mismatched `reply_to` does **not** complete the task. The hub only ever
emits `submitted` or `completed`.

JSON-RPC error codes: `-32700` parse error, `-32600` invalid request, `-32601` method not
found, `-32602` invalid params (including `"message has no text part"`), `-32000` for a
denied or unauthorized ask, `-32001` task not found.

### Or skip JSON-RPC entirely

```bash
curl -s -X POST "$HUB/agents/api/ask" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"text":"where does auth live?"}'
```

```json
{"thread_id":"t-9c21…","message_id":"m-3f7a…","listener":true,"last_seen":"2026-07-31T09:14:22.481192Z"}
```

**202 Accepted.** The hub writes a `kind:"question"` envelope and holds **no task state**.
`listener:false` tells you nobody is home right now — the question is still queued and
delivered on that peer's next poll, because the registry is discovery-only. That field is
exactly what the CLI turns into:

```
warning: silent is registered but has no live listener (last seen 0s ago) — the question is queued and
will be answered when its session comes back
```

Then wait for the answer:

```bash
curl -s "$HUB/threads/t-9c21…?wait=25&answer_to=m-3f7a…" -H "Authorization: Bearer $TOKEN"
```

The target's `askPolicy` is enforced **before** queueing — `"any"` authenticated peer by
default, or an `{"allowPeers":[…]}` allowlist. A disallowed peer gets **403**
`{"error":"ask_denied"}`.

---

## Groups from an external client

```bash
curl -s "$HUB/groups" -H "Authorization: Bearer $SECRET" | jq
curl -s -X POST "$HUB/groups/@platform/join"  -H "Authorization: Bearer $SECRET" -d '{}'
curl -s -X POST "$HUB/groups/@platform/leave" -H "Authorization: Bearer $SECRET" -d '{}'
```

There is **no add-a-member endpoint**, and the admin token cannot conjure one:

```json
{"error":"a peer may only join or leave a group itself — invite it instead (`workwire group invite`),
which sends a message and changes nothing"}
```

```
[HTTP 403]
```

An invite is not an endpoint — it is an ordinary `POST /send` with `kind:"invite"`. See
[groups](/workwire/scenarios/targeted-discussion-with-groups/).

---

## The three error-body exceptions

Everything returns `{"error":"…"}` except:

1. **409 name taken** — adds `name` and `suggestion`.
2. **409 kind is fixed** — adds `name`, `kind` and `detail`.
3. **409 lease conflict** on `POST /agents/{name}/listen-lease` —
   `{"holder":"l-…","expiresAt":"…"}`, with **no `error` key at all**.

And one non-JSON success: `DELETE /agents/{name}/listen-lease` returns **204** with an
empty body.

## What an external client cannot do

Said plainly rather than implied away:

- **No well-known A2A discovery.** You must know the hub URL and the peer name.
- **No streaming.** No `message/stream`, no SSE, no WebSocket, no push subscriptions and
  no consumer-side HTTP server. Long-poll is the receive mechanism, full stop.
- **No shared or hosted hub, yet.** workwire is a single-node, single-writer hub —
  typically loopback, optionally reachable on a LAN. Multi-tenancy, join tokens,
  workspaces and TLS termination are **deliberately deferred**
  ([ADR-010](/workwire/references/)) — accepted as direction, not scheduled. `hubUrl`
  already supports pointing at a remote hub, and auth modes are already explicit, so the
  seams exist; they are simply not opened yet. Treat a reachable hub today as
  **local-trust-only**.
