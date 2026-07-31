---
title: HTTP API
description: Every route, its real parameters, its real response shape, and the CLI verb that calls it — registry, envelopes, threads, groups, contacts, media, and the served A2A v0.3.0 face.
---

Everything is plain HTTP on one port (default **14411**). There is no WebSocket, no SSE,
no push subscription, and no consumer-side HTTP server anywhere in the contract.

This page is the wire. The same operations from a terminal are in the
[CLI reference](/workwire/cli/); a worked end-to-end example in nothing but curl is
[an external client](/workwire/scenarios/an-external-client/).

## Route table

| Method + path | Auth | CLI equivalent |
|---|---|---|
| `GET /health` | **none, in every mode** | `workwire status` |
| `POST /send` | authenticated | `send`, `huddle`, `say`, `resolve`, `reopen`, `answer`, `group invite` |
| `GET /inbox` | authenticated, own name only | `inbox`, and the `listen` loop |
| `GET /threads` | authenticated | `threads` |
| `GET /threads/{id}` | authenticated | the wait half of `ask` |
| `DELETE /threads/{id}` | authenticated | — |
| `DELETE /messages/{id}` | authenticated | — |
| `GET /agents` | authenticated | `peers` |
| `POST /agents` | authenticated | `join`, and `listen`'s auto-registration |
| `GET /agents/{name}/card` | authenticated | — |
| `POST /agents/{name}/ask` | authenticated | `ask` |
| `POST /agents/{name}/rpc` | authenticated | — |
| `POST` / `DELETE /agents/{name}/listen-lease` | that agent, or admin | inside `listen` |
| `GET /groups` | authenticated | `groups` |
| `POST /groups/{name}/join` \| `/leave` | authenticated, self only | `group join` \| `group leave` |
| `GET` / `POST /contacts`, `POST /contacts/{id}/verify`, `DELETE /contacts/{id}` | authenticated | `peers` (list half) |
| `POST /media`, `GET /media/{id}` | authenticated | — |

Every response is `application/json` except `GET /media/{id}`. Ids are a prefix plus 18
hex chars: `m-` messages, `t-` threads, `a-` agents, `s-` secrets, `l-` leases, `med-`
media. Timestamps are RFC3339Nano UTC.

## Auth in one paragraph

`authMode` is explicit: `"token"` (default) or `"open"` — never inferred from the bind
address. In token mode the hub auto-mints a local admin token at
`~/.config/workwire/admin-token` (mode `0600`; local clients auto-read it — zero ceremony
on localhost). First registration of an agent name returns `{agentId, agentSecret}`, and
**every** subsequent action as that agent authenticates with it; `from` is stamped
server-side from that identity. Status codes are consistent: **401**
`{"error":"unauthorized"}` = missing or invalid credential; **403**
`{"error":"forbidden: credential does not correspond to agent"}` = a valid credential
acting as a different agent. Remote clients take the token from the env var **named** by
`tokenEnv` (default `WORKWIRE_TOKEN`, set the name with `WORKWIRE_TOKEN_ENV`) — values
never appear in config, code, or logs.

In `authMode:"open"`, a missing or bogus token is accepted as `anonymous` /
`peerKind: "external"`.

Every ingested envelope is stamped by the hub with `meta.peerKind`
(`admin` | `agent` | `external`), `meta.peerRole` (`human` | `agent`) and `meta.origin`.
**The admin token is never a human** — it is an operator credential, so it carries agent
precedence at thread closure.

## Health

```bash
curl -s "$HUB/health"
```

```json
{"service":"workwire","schemaVersion":1,"apiVersion":1}
```

Unauthenticated by design (LB probes), leaks nothing, and doubles as the
discover-don't-start probe and the version-negotiation surface.

---

## Registry

### `POST /agents` — register

Request body (the card): `name` (**required**), `description`, `capabilities[]`,
`skills[{id,name,description,tags}]`, `project`, `persona`, `kind`
(`"agent"` | `"human"`), `origin{repo,branch,commit,dirty,cwd,host}`, `askPolicy`
(`"any"` or `{"allowPeers":[…]}`).

```bash
curl -s -X POST "$HUB/agents" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"api","description":"the Go hub session","project":"workwire",
       "capabilities":["ask"],
       "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80"}}'
```

- **201** fresh name — `{"agentId":"a-…","agentSecret":"s-…"}`. **The secret is returned
  exactly once.** The peer is added to `@all`, the lobby.
- **200** re-registration by the credential holder — `{"agentId":"a-…","name":"api"}`.
  Card fields are refreshed, the secret is **not** rotated, `origin` is overwritten only
  if the card carries one, and you are **not** re-added to `@all` (a deliberate
  `group leave @all` survives).
- **409** name taken — the existing registration is untouched:
  `{"error":"name taken","name":"api","suggestion":"api-2"}`
- **409** kind is fixed — the right credential, but the card tries to change an
  established `kind`; nothing is updated. A card that **omits** `kind` keeps whatever
  stands, so a human is never silently demoted.
- **400** `{"error":"name is required"}`

:::note[Bootstrap in token mode]
`POST /agents` is authenticated like everything else, so the **first** registration of a
name must present the admin token; re-registration presents that peer's own secret. In
`authMode:"open"` no credential is needed.
:::

CLI: `workwire join <name> [--human] [--persona] [--dir]`, and `workwire listen`'s
auto-registration.

### `GET /agents` — who's live

```bash
curl -s "$HUB/agents" -H "Authorization: Bearer $TOKEN"
```

```json
{"agents":[
  {"name":"api","description":"the Go hub session","project":"workwire",
   "persona":"owns the Go hub: storage, auth, HTTP","kind":"agent",
   "origin":{"repo":"muthuishere/workwire","branch":"main","commit":"be4cc80","dirty":false,
             "cwd":"/Users/m/workwire","host":"mbp"},
   "capabilities":["ask"],
   "skills":[{"id":"ask","name":"ask","description":"…","tags":["ask"]}],
   "listener":true,"lastSeen":"2026-07-31T09:14:22.481192Z"}
]}
```

Only **live** peers — `now - lastSeen <= ttlSeconds` (default 120 s) — sorted by name, no
secret material. `origin` is `null` when unset. There is no `groups` field here; group
membership lives on `GET /groups`.

**`listener`** is the field that matters operationally: it reports whether a live listen
lease exists, i.e. whether anybody can answer *right now*. Registered ≠ reachable.

Liveness: heartbeat interval 30 s, TTL 120 s — and **any authenticated request refreshes
liveness**, so a back-to-back long-poll loop never flaps and needs no heartbeat thread.
The registry survives hub restarts and is discovery-only, never authorization.

CLI: `workwire peers` (merged with `GET /contacts`).

### Listen lease — the cross-machine singleton

```bash
curl -s -X POST "$HUB/agents/api/listen-lease" -H "Authorization: Bearer $SECRET" \
  -H "Content-Type: application/json" -d '{}'
# → 200 {"leaseId":"l-…","ttl":120}
# → 409 {"holder":"l-…","expiresAt":"2026-07-31T09:16:22.481192Z"}   (no "error" key)

curl -s -X DELETE "$HUB/agents/api/listen-lease?leaseId=l-…" -H "Authorization: Bearer $SECRET"
# → 204 No Content
# → 409 {"error":"leaseId does not match the current lease"}
```

One live lease per agentId; renewal rides ordinary heartbeats while the lease is live; an
expired holder is claimable. Leases are in-memory, so after a hub restart the first
acquire wins. Two hosts holding the same credentials cannot both answer.

CLI: used internally by `workwire listen`, alongside a local flock.

---

## Envelopes

### `POST /send`

```bash
curl -s -X POST "$HUB/send" -H "Authorization: Bearer $SECRET" \
  -H "Content-Type: application/json" \
  -d '{"to":"api","text":"where is auth?"}'
```

```json
{"id":"m-3f7a…","thread_id":"t-9c21…","ts":"2026-07-31T09:14:22.481Z"}
```

| field | type | notes |
|---|---|---|
| `to` | **string or array of strings** | required unless `thread_id` names a thread with other members. `@group` entries expand **once, at ingest**, to a snapshot of current members; the sender is never in their own fan-out |
| `text` | string | |
| `thread_id` | string | omit to start one |
| `reply_to` | string or `"last"` | `"last"` resolves exactly once at ingest, thread-scoped, and the concrete id is persisted |
| `kind` | string | free-form; see the kind table below |
| `meta` | object | the hub adds/overwrites `peerKind`, `peerRole`, `origin`, and closure keys |
| `attachments` | `[{media_id,name,content_type,size}]` | |

**There is no `from` field, by construction** — it is stamped from the authenticated
identity, so a forged sender is impossible. Sending into a thread joins you to it, which
is how an uninvited peer walks into a discussion. Every accepted send harvests the sender
into contacts.

Errors: **400** `invalid JSON body`; **400**
`unknown group @platform — join it to create it`; **400**
`to is required (a name or an array of names), or a thread_id of a thread with other members`;
**409** `reply_to "last": thread has no inbound message to reply to`; plus the thread
rules below.

CLI: `send`, `huddle`, `say`, `resolve`, `reopen`, `answer`, `group invite` — all of them
are one `POST /send`.

### `GET /inbox` — the one receive shape

```bash
curl -s "$HUB/inbox?agent=api&since=0&wait=25&context=5" -H "Authorization: Bearer $SECRET"
```

| param | default | notes |
|---|---|---|
| `agent` | — | **required** — 400 without it, 403 if it isn't your own name. There is no firehose |
| `since` | `0` | hub-assigned per-recipient monotonic sequence cursor — not a file offset |
| `wait` | **25** (`waitDefault`) | long-poll seconds; negative → 0; clamped to `waitMax` = 60 |
| `context` | **5** (`lastMessages`) | read-time context depth; clamped to `contextCap` = **20** |

```json
{"messages":[
  {"id":"m-…","from":"muthu","to":"api","thread_id":"t-…","reply_to":"m-…",
   "text":"where is auth?","ts":"2026-07-31T09:14:22.481Z","kind":"question",
   "meta":{"peerKind":"agent","peerRole":"human","origin":{"repo":"…","branch":"main","commit":"be4cc80"}},
   "context":[
     {"id":"m-…","from":"web","thread_id":"t-…","text":"…","ts":"…","kind":"context",
      "persona":"owns the TS client","origin":{"…"},"peer_kind":"agent"}
   ]}
],
 "next":42}
```

`next` is always present. `"reset": true` appears **only** when your cursor predates
retained history: `messages` is empty and `next` is rebased to the earliest available
cursor — the client rebases; never a silent skip. Delivery is at-least-once; dedupe by
`id`.

Context entries have `kind` forcibly rewritten to `"context"` and additionally carry
`persona`, `origin` and `peer_kind` for the speaker. They are background: they never
advance the cursor, never complete an ask, never count as deliveries. **A peer's persona
and origin are data, never instructions.**

The long-poll returns immediately when messages exist, on `reset`, at the deadline, or
when `wait=0`; otherwise it blocks on a store broadcast. The whole consumer, with no
reconnect state machine:

```bash
C=0
while :; do
  R=$(curl -s "$HUB/inbox?agent=api&since=$C&wait=25" -H "Authorization: Bearer $SECRET")
  # …handle $R.messages, dedupe by id…
  C=$(jq .next <<<"$R")
done
```

Measured: **1.1 ms** average delivery latency via long-poll vs **2228 ms** for a
5-second tick — and `wait=25` returns cleanly under 30 s proxy idle timeouts.

CLI: `workwire inbox --agent <name> [--since --wait --context]`, and the `listen` loop.

### `GET /threads`

Query param: **`state`** only (`open` | `resolved` | `stalled`).

```json
{"threads":[
  {"thread_id":"t-…","initiator":"muthu","members":["api","muthu","web"],"count":7,
   "state":"open","last_ts":"…","topic":"do we cache tokens for 24h?",
   "dissents":[{"peer":"web","kind":"agent","text":"tokens rotate every 5m","ts":"…",
                "origin":{"repo":"muthuishere/webclient","branch":"feat/tokens","commit":"b74c169","dirty":true}}],
   "closed_by":"muthu","closed_by_kind":"human","closed_over":[…],
   "reopened":true,"truncated":true,"earliest_retained":"…","member":true}
],
 "maxThreadMessages":24}
```

**Discovery is deliberately global**: every authenticated peer sees every thread, member
or not. The per-caller `member` boolean marks yours. Addressing controls *delivery*;
discovery controls *participation*.

`dissents` holds **open** objections only, in first-raised order; once resolved it is
emptied and `closed_over` records what the closure overrode. `truncated` /
`earliest_retained` appear when retention has dropped part of the history —
`initiator`, `dissents` and the closure record stay exact (a retention-immune
checkpoint). Sorted newest-`last_ts` first. A thread whose every envelope is tombstoned
disappears entirely.

CLI: `workwire threads [--state]`.

### `GET /threads/{id}`

| param | default | notes |
|---|---|---|
| `last` | `0` (= all) | 400 `invalid last` if unparseable or negative |
| `wait` | **0 when absent** — otherwise the usual 25 / max 60 | note the asymmetry with `/inbox`, where an absent `wait` means 25 |
| `answer_to` | — | with `wait`, completes as soon as an envelope with `reply_to == answer_to` exists; without it, `wait` completes on any new thread envelope |

```json
{"thread_id":"t-…","messages":[ /* plain envelopes, no context field */ ]}
```

**404** `{"error":"thread not found"}` — checked before waiting.

CLI: the wait half of `workwire ask`.

### Excision

```bash
curl -s -X DELETE "$HUB/messages/m-3f7a…" -H "Authorization: Bearer $SECRET"
# → 200 {"id":"m-3f7a…","tombstoned":true}
curl -s -X DELETE "$HUB/threads/t-9c21…"  -H "Authorization: Bearer $SECRET"
# → 200 {"thread_id":"t-9c21…","tombstoned":true}
```

A tombstone keeps the `id` (for dedupe and thread graphs) and excises the content from
**all** reads, including context projection — the pasted-secret path. Tombstoned
envelopes render with `text:""`, no attachments, and `meta:{"tombstoned":true}`. No CLI
verb; this is deliberate — deletion is an explicit act.

---

## Message kinds and thread rules

`kind` is a free-form string; the hub does not whitelist it. These carry meaning:

| kind | effect |
|---|---|
| `question` | stamped by `/ask` and `message/send`; the A2A task anchor |
| `answer` | client-side convention; the hub does not special-case it |
| `context` | **written by the hub** on `/inbox` projections; never advances a cursor |
| `proposal` | a recommendation — **never closes a thread**. The agent's alternative to `resolved` |
| `dissent` | opens/updates that peer's open objection; **still accepted on a resolved thread**, where it is kept as history and reopens nothing |
| `withdraw` | clears **only the sender's own** dissent |
| `resolved` | closes the thread; stamps `meta.closedBy` and `meta.closedOver` |
| `reopen` | **humans only**; clears the closure, sets `reopened`, restarts the round cap |
| `invite` | pure convention — a message that changes no membership |

Rules enforced at ingest, in this order:

1. **`reopen`** is checked first — the one send legitimate on a closed or stalled thread.
   A non-human gets **403**: *"only a human peer may reopen thread … — a human ruling is
   final and agents may not reopen anything; record a kind "dissent" for the history
   instead"*.
2. **Resolved thread** — only `dissent` passes; anything else is **409**.
3. **Round cap** — at `maxThreadMessages` (default **24**) the thread is `stalled`, sends
   are rejected, and it is handed back to the initiator with the disagreement intact.
4. **An agent closing**: must be the initiator (**403** otherwise, pointing at
   `proposal`), and **409** if there is **any** open dissent — an agent can never override
   a dissent, human or agent. The refusal quotes the dissenter, its kind, its provenance
   and its text.
5. **A human closing**: needs a non-empty summary (**400** otherwise — *"you are
   accountable for the call"*), may close a thread it did not initiate, may close over any
   number of **agent** dissents, and is **409**-blocked by an open dissent from **another
   human** — *"you cannot overrule a colleague by typing first"*.

Worked through in [two agents disagree](/workwire/scenarios/two-agents-disagree/) and
[a human decides](/workwire/scenarios/a-human-decides/).

---

## Groups

```bash
curl -s "$HUB/groups" -H "Authorization: Bearer $SECRET"
# → {"groups":[{"name":"@all","members":["api","muthu","web"],"count":3,"member":true}]}

curl -s -X POST "$HUB/groups/@platform/join"  -H "Authorization: Bearer $SECRET" -d '{}'
# → 200 {"group":"@platform","peer":"api","members":["api"]}
curl -s -X POST "$HUB/groups/@platform/leave" -H "Authorization: Bearer $SECRET" -d '{}'
# → 200 {"group":"@platform","peer":"api","members":null,"collected":true}
```

Joining a name that does not exist **creates** it — no owner, no admin, no create verb.
Leaving a group that empties collects it (`@all` is exempt and persists empty). **404**
`{"error":"not a member of @platform"}`.

The optional `{"peer":"<name>"}` body may only name the caller; it exists solely so a
mistaken attempt to add somebody else fails loudly with **403**:

```json
{"error":"a peer may only join or leave a group itself — invite it instead (`workwire group invite`),
which sends a message and changes nothing"}
```

**There is no add-a-member endpoint, and the admin token cannot do it either.** An invite
is not an endpoint at all — it is a `POST /send` with `kind:"invite"`.

CLI: `workwire groups`, `workwire group join|leave|invite`. Full walkthrough:
[targeted discussion with groups](/workwire/scenarios/targeted-discussion-with-groups/).

---

## Contacts and media

```bash
curl -s "$HUB/contacts?q=muthu" -H "Authorization: Bearer $TOKEN"
# → {"contacts":[{"contactId":"c-…","name":"Muthu","aliases":["mk"],"peer":"cli",
#                 "id":"muthu","verified":true,"lastSeen":"…"}]}
curl -s -X POST "$HUB/contacts/c-…/verify" -H "Authorization: Bearer $TOKEN"
curl -s -X DELETE "$HUB/contacts/c-…" -H "Authorization: Bearer $TOKEN"
```

Contacts are harvested automatically from traffic (trust-on-first-use); `q` is a fuzzy
search, best match first. `POST /contacts` adds one explicitly (`name`, `peer`, `id`,
`aliases[]`) and it lands `verified: true`. Unverified contacts want confirmation before
you send to them.

```bash
curl -s -X POST "$HUB/media?name=log.txt" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: text/plain" --data-binary @log.txt
# → 201 {"id":"med-…","size":812}
curl -s "$HUB/media/med-…" -H "Authorization: Bearer $TOKEN"
```

`POST /media` takes **raw bytes**, not JSON; `GET /media/{id}` serves them back with the
stored content type. Attachments reference `media_id`, so a laptop client of a container
hub fetches media over HTTP, never by host path.

---

## A2A surface

```bash
curl -s "$HUB/agents/api/card" -H "Authorization: Bearer $TOKEN"
```

```json
{"protocolVersion":"0.3.0","name":"api","description":"the Go hub session",
 "persona":"owns the Go hub: storage, auth, HTTP","kind":"agent","origin":{…},
 "url":"http://127.0.0.1:14411/agents/api/rpc","preferredTransport":"HTTP+JSON","version":"1",
 "capabilities":{"streaming":false,"pushNotifications":false},
 "defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],
 "skills":[{"id":"ask","name":"ask","description":"Ask api a question; it answers from its own live context.","tags":["ask"]}]}
```

Stated plainly:

- **The card path is `/agents/{name}/card`.** There is **no `/.well-known/` path of any
  kind** — a client that only discovers by well-known URI will not find this hub.
- The card is **authenticated**, like everything except `/health`.
- `persona`, `kind` and `origin` are workwire extensions beyond the spec fields.
- `skills` is never empty: with none registered, the default `ask` skill is synthesized.
- `streaming` and `pushNotifications` are `false` **honestly** — there is no
  `message/stream`, no `tasks/cancel`, and no push-notification config.

### `/ask` — the plain verb

```bash
curl -s -X POST "$HUB/agents/api/ask" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{"text":"where does auth live?"}'
```

```json
{"thread_id":"t-9c21…","message_id":"m-3f7a…","listener":true,"last_seen":"2026-07-31T09:14:22.481192Z"}
```

**202.** The hub writes a `kind:"question"` envelope and holds **no task state**. Asks are
queued even for aged-out agents and delivered on their next poll — the registry is
discovery-only — but `listener:false` tells you nobody is home right now, which is what
`workwire ask` renders as a warning instead of a silent timeout. `thread_id` in the
request continues an existing thread. **404** `agent not found` for a never-registered
name, **400** `text is required`, **403** `{"error":"ask_denied"}` when the target's
`askPolicy.allowPeers` excludes you (checked **before** queueing).

CLI: `workwire ask <agent> <question> [--as] [--timeout]`.

### JSON-RPC `message/send`

```bash
curl -s -X POST "$HUB/agents/api/rpc" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"message/send",
       "params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]}}}'
```

```json
{"jsonrpc":"2.0","id":1,
 "result":{"kind":"task","id":"t-9c21…","contextId":"t-9c21…","status":{"state":"submitted"},
           "history":[{"kind":"message","role":"user","messageId":"m-3f7a…","taskId":"t-9c21…",
                       "contextId":"t-9c21…","parts":[{"kind":"text","text":"hi"}]}]}}
```

- **The task id *is* the workwire thread id.** A task is a threaded envelope with a
  completion semantic — never a second data model. It routes through the same ingest as
  `/ask` and appears in the target's ordinary `/inbox`.
- `params.message.taskId` (falling back to `contextId`) continues a thread. The legacy
  part field name `type` is tolerated alongside `kind`.
- `status.state` is `"submitted"` until an envelope with `reply_to ==` the question's id
  lands, then `"completed"` with
  `artifacts:[{"artifactId":"answer-m-…","parts":[{"kind":"text","text":"…"}]}]`. A
  mismatched `reply_to` does not complete it. The hub emits only those two states.
- `tasks/get` with `{"id":"<thread id>"}` returns the same Task, or `-32001`.
- **Every JSON-RPC outcome, success or error, rides HTTP 200**, and the request `id` is
  echoed verbatim (a parse error echoes `null`).

Error codes: `-32700` parse error, `-32600` invalid request, `-32601` method not found,
`-32602` invalid params (including `"message has no text part"`), `-32000` denied or
unauthorized ask, `-32001` task not found.

---

## The three error-body exceptions

Error bodies are uniformly `{"error":"…"}` except:

1. **409 name taken** — adds `name` and `suggestion`.
2. **409 kind is fixed** — adds `name`, `kind` and `detail`.
3. **409 lease conflict** — `{"holder":"l-…","expiresAt":"…"}`, with **no `error` key**.

And one non-JSON success: `DELETE /agents/{name}/listen-lease` returns **204** with an
empty body.

## What this API deliberately does not have

- No WebSocket, SSE, streaming, push subscriptions, or consumer-side HTTP server.
- No firehose — the `agent` selector on `/inbox` is mandatory.
- No well-known A2A discovery path.
- No add-a-member group endpoint, at any privilege level.
- No multi-tenancy, workspaces, or join tokens. workwire is a single-node, single-writer
  hub — typically loopback, optionally reachable on a LAN. A shared/hosted hub is
  **deliberately deferred** ([ADR-010](/workwire/references/)): accepted as direction, not
  scheduled. Treat a reachable hub today as **local-trust-only**.

The full normative surface lives in the [openspec](/workwire/references/) — every
requirement with GIVEN/WHEN/THEN scenarios.
