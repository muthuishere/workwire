# registry-a2a

## Purpose

The hub's dynamic agent registry and its A2A v0.3.0 serving surface: peers register agent
cards and stay discoverable via heartbeat/TTL; the hub serves a spec-conformant card,
a plain `/ask` verb, and a JSON-RPC `message/send` shim per registered agent, plus the
hub-side listen lease that makes `workwire listen` a cross-machine singleton. (ADR-002,
ADR-007 identity parts, ADR-008 liveness.)

## Requirements

### R1: The system SHALL accept agent registration via `POST /agents` with an agent-card body and return hub-issued credentials

The request body is a JSON agent card: `{"name": "<string, required>", "description":
"<string>", "capabilities": [...], "skills": [{"id","name","description","tags"} ...],
"project": "<cwd/project hint>", "askPolicy": "any" | {"allowPeers": ["<name>", ...]}}`.
On first registration of a name the hub responds `201` with
`{"agentId": "<string>", "agentSecret": "<string>"}`. The secret is returned exactly once;
all subsequent actions as that agent (send, inbox reads, ask, heartbeat, re-register)
MUST authenticate with it. `askPolicy` defaults to any **authenticated** peer.

#### Scenario: first registration of a fresh name
- GIVEN no agent named `repoA` exists on the hub
- WHEN a peer sends `POST /agents` with body `{"name":"repoA","description":"repo A session"}`
- THEN the hub responds `201` with a JSON body containing non-empty `agentId` and `agentSecret`
- AND `GET /agents` subsequently lists `repoA`

#### Scenario: registration without a name
- GIVEN any hub state
- WHEN a peer sends `POST /agents` with a body missing `name` (or a non-JSON body)
- THEN the hub responds `400` with a JSON error body and registers nothing

#### Scenario: re-registration by the credential holder
- GIVEN `repoA` is registered and the caller authenticates with `repoA`'s `agentSecret`
- WHEN it sends `POST /agents` with `{"name":"repoA", ...updated card...}`
- THEN the hub responds `200`, updates the stored card, refreshes liveness, and does NOT rotate the secret

#### Scenario: `kind` is pinned once established
- GIVEN `muthu` is registered with `kind:"human"` and the caller holds `muthu`'s secret
- WHEN it re-registers with a card carrying `kind:"agent"` (or an agent re-registers as `"human"`)
- THEN the hub responds `409` with a body naming the kind that stands, changes nothing, and the peer keeps the decision precedence it had (ADR-011 §3) — a peer's kind SHALL NOT change on re-registration
- AND a card that omits `kind` entirely re-registers normally with `200`, keeping the stored kind: omitting the flag is never a demotion

### R2: The system SHALL reject a second registration of a taken name with `409` and a suggested free name

No silent takeover, no card overwrite (ADR-007). The `409` body is
`{"error": "name taken", "name": "<requested>", "suggestion": "<free name>"}` where the
suggestion follows the `<name>-2` pattern (incrementing until free).

#### Scenario: name collision
- GIVEN `repoA` is registered by another identity
- WHEN a different (or unauthenticated) peer sends `POST /agents` with `{"name":"repoA"}`
- THEN the hub responds `409` with `suggestion: "repoA-2"`
- AND the existing `repoA` registration, card, and secret are unchanged

#### Scenario: suggestion itself is taken
- GIVEN `repoA` and `repoA-2` are both registered
- WHEN a peer attempts to register `repoA`
- THEN the `409` suggestion is `repoA-3` (first free increment)

### R3: The system SHALL age agents out of the registry on a 120s TTL, refreshed by a 30s heartbeat or by ANY authenticated request

TTL 120s, heartbeat interval 30s, satisfying `TTL >= 2 × max(heartbeat interval, wait=25)`
(ADR-008). Any authenticated request from an agent — issuing or completing a long-poll,
sending, asking, re-registering — refreshes its liveness; a single-threaded listen loop
needs no separate heartbeat thread. The registry is discovery-only, never authorization:
aging out never blocks asks addressed to the agent (R7).

#### Scenario: agent dies without deregistering (stress-test #9)
- GIVEN `repoA` registered and its last authenticated request was more than 120s ago
- WHEN a peer calls `GET /agents`
- THEN `repoA` is absent from the listing

#### Scenario: active long-poller never flaps
- GIVEN `repoA` runs a listen loop of back-to-back `GET /inbox?agent=repoA&wait=25` requests, each authenticated
- WHEN 10 minutes pass with no explicit heartbeat call
- THEN `repoA` remains listed in `GET /agents` throughout (each poll refreshed the TTL)

#### Scenario: explicit heartbeat
- GIVEN `repoA` registered
- WHEN it re-sends its authenticated `POST /agents` (or the dedicated heartbeat form of it) every 30s
- THEN its `lastSeen` advances and it never ages out

### R4: The system SHALL list live agents via `GET /agents`

Response: `200` with `{"agents": [{"name","description","lastSeen","project", ...card
summary...}, ...]}` containing only agents within TTL. `agentSecret` and `agentId`
private material MUST never appear in the listing.

#### Scenario: listing after mixed liveness
- GIVEN `repoA` seen 10s ago and `repoB` seen 300s ago
- WHEN a peer calls `GET /agents`
- THEN the response contains `repoA` and not `repoB`
- AND no entry contains a secret field

### R5: The system SHALL persist a last-seen registry cache in the data dir so restarts do not empty discovery

Spike-01 risk 3: after a hub restart the in-memory registry was empty. The hub persists
registrations (card + identity + lastSeen) in the data dir and reloads them on start;
TTL expiry still applies against the persisted `lastSeen`.

#### Scenario: hub restart within TTL (stress-test #4)
- GIVEN `repoA` registered and last seen 20s before the hub process restarts on the same data dir
- WHEN a peer calls `GET /agents` immediately after restart
- THEN `repoA` is listed (from the last-seen cache) without re-registration

#### Scenario: hub restart after TTL
- GIVEN `repoA`'s persisted `lastSeen` is older than 120s at restart
- WHEN a peer calls `GET /agents`
- THEN `repoA` is not listed, but its name/identity remain reserved: registering `repoA` without its secret still returns `409`

### R6: The system SHALL serve an A2A v0.3.0-conformant agent card at `GET /agents/<name>/card`

The card validates against the vendored A2A v0.3.0 `AgentCard` JSON schema (Spike-03) and
carries at minimum: `protocolVersion: "0.3.0"`, `name`, `description`, `url` (the agent's
ask endpoint URL on this hub), `preferredTransport: "HTTP+JSON"`, `version`,
`capabilities: {"streaming": false, "pushNotifications": false}`,
`defaultInputModes: ["text/plain"]`, `defaultOutputModes: ["text/plain"]`, and `skills[]`
(each with `id`, `name`, `description`, `tags`); when the registration carried no skills,
a default `ask` skill is synthesized.

#### Scenario: card for a registered agent
- GIVEN `repoA` registered with no skills in its card
- WHEN a client calls `GET /agents/repoA/card`
- THEN the hub responds `200` with JSON that validates against the A2A v0.3.0 AgentCard schema
- AND `skills` contains the synthesized `ask` skill

#### Scenario: card for an unknown agent
- GIVEN no agent named `ghost` was ever registered
- WHEN a client calls `GET /agents/ghost/card`
- THEN the hub responds `404`

### R7: The system SHALL accept `POST /agents/<name>/ask` and respond with `{thread_id, message_id}` — plain serving, no relay machinery

Request body: `{"text": "<question, required>", "thread_id": "<optional existing thread>"}`.
The hub writes a `kind:"question"` envelope addressed to `<name>` (with `from` stamped
server-side from the authenticated asker, ADR-007) and responds `202` with
`{"thread_id": "<id>", "message_id": "<envelope id>"}`. The hub never holds tasks or
tracks completion. The ask is accepted and queued regardless of the target's registry
presence (registry is discovery-only, ADR-008), but the target's `askPolicy` is enforced
before queueing: a disallowed peer gets `403`. In token mode an unauthenticated ask gets
`401`.

#### Scenario: normal ask
- GIVEN `repoA` registered and the asker authenticated
- WHEN the asker sends `POST /agents/repoA/ask` with `{"text":"where does auth live?"}`
- THEN the hub responds `202` with non-empty `thread_id` and `message_id`
- AND an envelope with that id, `to:"repoA"`, `kind:"question"`, server-stamped `from` is deliverable on `repoA`'s inbox

#### Scenario: ask to an aged-out agent (stress-test #11)
- GIVEN `repoA` has aged out of `GET /agents` but was once registered
- WHEN a peer asks `POST /agents/repoA/ask`
- THEN the hub still responds `202` and queues the envelope; it is delivered when `repoA` next polls

#### Scenario: ask blocked by policy
- GIVEN `repoA` registered with `askPolicy: {"allowPeers":["repoB"]}`
- WHEN authenticated agent `repoC` asks `repoA`
- THEN the hub responds `403` and writes no envelope

#### Scenario: ask with empty text
- WHEN a peer sends `POST /agents/repoA/ask` with `{"text":""}` or no body
- THEN the hub responds `400`

### R8: The system SHALL define ask completion as an envelope whose `reply_to` equals the question's `message_id` — nothing else terminates a `?wait=`

The asker reads the answer via `GET /threads/<thread_id>` (optionally `?wait=<s>` long-poll
sugar, default/cap per ADR-001: `wait=25`). Context projections (`kind:"context"`) are
read-only, stripped, and can never be mistaken for the answer.

#### Scenario: answer completes the ask
- GIVEN a question envelope `q1` on thread `t1` addressed to `repoA`
- WHEN `repoA` sends an envelope on `t1` with `reply_to:"q1"`
- THEN a `GET /threads/t1?wait=25` in flight returns immediately with that answer envelope

#### Scenario: unrelated traffic does not complete the ask
- GIVEN the same pending `q1` and a `GET /threads/t1?wait=25` in flight
- WHEN another envelope arrives on `t1` with `reply_to` absent or set to a different id, or a `kind:"context"` entry is projected
- THEN the wait does NOT complete on it as the answer; on `wait` expiry the response returns the thread state with no completion marker and the asker re-polls

#### Scenario: multiple questions interleaved on one thread
- GIVEN questions `q1` and `q2` both live on thread `t1`
- WHEN an answer with `reply_to:"q2"` arrives
- THEN only the `q2` ask is complete; `q1` remains pending

### R9: The system SHALL serve a JSON-RPC 2.0 `message/send` shim at the card `url`, returning an A2A v0.3.0 Task object

A thin shim over the same thread store (a task is a threaded envelope with a completion
semantic — never a second data model, ADR-002). Request: JSON-RPC 2.0
`{"jsonrpc":"2.0","id":<client id>,"method":"message/send","params":{"message":{...A2A
Message with text part...}}}` POSTed to the agent's card `url`. The hub maps it to the R7
ask path and responds `200` with `{"jsonrpc":"2.0","id":<same>,"result":{...Task...}}`
where the Task carries `id` (mapped to `thread_id`), `contextId`, and `status.state` —
`"submitted"`/`"working"` while unanswered, `"completed"` with the answer text in the
task's artifacts/history once an envelope with `reply_to == message_id` exists. Unknown
methods get a JSON-RPC error `-32601`; malformed JSON-RPC gets `-32600`/`-32700` with
HTTP `200` per JSON-RPC-over-HTTP convention.

#### Scenario: strict SDK client sends message/send (stress-test #14)
- GIVEN `repoA` registered and an external A2A v0.3.0 client that read the card
- WHEN it POSTs a valid `message/send` request to the card `url`
- THEN the hub responds with a JSON-RPC result containing a Task whose `status.state` is `"submitted"` or `"working"`
- AND the question is delivered to `repoA` through the same envelope store as a plain `/ask`

#### Scenario: polling the task after the answer
- GIVEN a `message/send` task whose underlying question has been answered (`reply_to == message_id`)
- WHEN the client retrieves the task state (`tasks/get` with the task `id`)
- THEN `status.state` is `"completed"` and the answer text is present in the task result

#### Scenario: unknown JSON-RPC method
- WHEN a client POSTs `{"jsonrpc":"2.0","id":1,"method":"tasks/teleport","params":{}}` to the card `url`
- THEN the hub responds HTTP `200` with JSON-RPC error code `-32601`

### R10: The system SHALL provide a hub-side listen lease per `agentId` — acquire, renew, claim — so at most one listener answers as an agent across machines

(ADR-003 via stress-test #19; the local flock is only the fast path.) Lease operations are
authenticated as the agent:
- **Acquire**: `POST /agents/<name>/listen-lease` → `200` with
  `{"leaseId":"<id>","ttl":120}` when free; `409` with `{"holder":"<lease holder hint>",
  "expiresAt":"<ts>"}` when another live lease exists.
- **Renew**: renewal rides the heartbeat/liveness refresh (R3) — any authenticated request
  from the lease holder extends the lease with the same TTL; an explicit re-`POST` with the
  current `leaseId` also renews and returns `200`.
- **Claim**: a lease whose holder's liveness has lapsed past the 120s TTL is expired; the
  next acquire succeeds (`200`, new `leaseId`).
- **Release**: `DELETE /agents/<name>/listen-lease` with the current `leaseId` → `204`.

#### Scenario: second host cannot double-listen (stress-test #19)
- GIVEN host A holds `repoA`'s listen lease and is actively long-polling
- WHEN host B, also holding `repoA`'s credentials, POSTs `/agents/repoA/listen-lease`
- THEN the hub responds `409` and host B does not start answering

#### Scenario: claiming an expired lease
- GIVEN host A held the lease but has made no authenticated request for more than 120s
- WHEN host B POSTs `/agents/repoA/listen-lease`
- THEN the hub responds `200` with a new `leaseId`; host A's old `leaseId` is rejected on its next explicit renew (`409`)

#### Scenario: re-invoking the skill adopts the running listener
- GIVEN host A holds the lease and re-acquires with its current `leaseId`
- WHEN it POSTs `/agents/repoA/listen-lease` with that `leaseId`
- THEN the hub responds `200` (renewal, same lease), never `409` against itself

#### Scenario: lease requires the agent's credentials
- WHEN a peer without `repoA`'s `agentSecret` POSTs `/agents/repoA/listen-lease` on a token-mode hub
- THEN the hub responds `401` (unauthenticated) or `403` (authenticated as someone else)

### R11: The system SHALL report ANSWERABILITY separately from lease liveness: a peer declares an attached answerer via `POST /agents/<name>/answering`, and `GET /agents` plus the `POST /agents/<name>/ask` response SHALL carry both `listener` (a live lease exists — questions are being delivered) and `answering` (something is attached to read and answer them).

A listen lease is a DELIVERY fact: it says a listener is writing inbound questions into a
session inbox file. It has never meant anyone is reading that file, and join-by-default makes
the divergence the resting state of a machine — a lease per folder, an answerer only where a
session actually engaged. Only the peer itself can know, so it declares: `{"attached":true}`
sets or renews, `{"attached":false}` stands down, and the declaration ages out on the same TTL
as liveness. The endpoint requires the agent's own credential (or admin) — nobody declares
answerability on another peer's behalf. Both fields are additive; `listener` keeps its
meaning and its consumers.

#### Scenario: a lease alone does not claim an answerer
- GIVEN `api` holds a live listen lease and nothing has declared an answerer
- WHEN a peer reads `GET /agents` or asks `api`
- THEN `listener` is `true` and `answering` is `false`
- AND the asking client warns that the peer is listening but nothing is attached to answer, instead of waiting out the timeout in silence

#### Scenario: an attached answerer declares itself
- GIVEN an answerer attached to `api`'s session inbox
- WHEN it POSTs `/agents/api/answering` with `{"attached":true}`
- THEN `answering` is `true` on `GET /agents` and on the ask response
- AND a declaration that stops being renewed ages out on the registry TTL

#### Scenario: standing down leaves the lease alone
- WHEN the answerer POSTs `{"attached":false}`
- THEN `answering` becomes `false` while `listener` stays `true` — delivery continues, and questions are queued for the session's next engagement

#### Scenario: answerability may only be declared by the peer itself
- WHEN a peer holding a different agent's credential POSTs `/agents/api/answering`
- THEN the hub responds `403`, and `404` for an unknown agent

### R12: The system SHALL treat a WORKING TREE as the identity and a NAME as a label on it — one identity, any number of names (ADR-015)

A peer name is how people address a codebase, and people accumulate names: an old one in
their notes, a new one the session derived, a third from a worktree path. On 2026-08-01
`muthuishere/toolnexus@cljc` was on the wire three times — `clojure`, `toolnexus-cljc`,
`toolnexus-clojure`, all at `2f11e8a` — and `koine` twice. Several names was never the
harm. Several *identities* was: three inboxes, three cursors, three answerers, and half the
questions delivered where nobody was reading.

A registration whose provenance matches a LIVE peer's tree SHALL NOT create a second
identity and SHALL NOT be refused. It SHALL register that name as an **alias** of the
existing identity and return that identity's `agentId` and canonical name. The tree is
identified by `repo@branch` when provenance carries both, and by `origin.cwd` otherwise; a
card with no provenance matches nothing and blocks nothing. A registration for the same
name from the same tree remains ordinary re-registration.

**Every name SHALL resolve to its identity before anything routes.** Addressing, group
expansion, `ask`, inbox reads, lease acquisition and answerer declaration all operate on the
resolved identity, so an alias shares one inbox, one cursor and one answerer. An envelope's
`from` SHALL carry the canonical name, so a thread can never show one session arguing with
itself under two labels.

`DELETE /agents/<name>/alias` SHALL drop ONE label, leaving the identity, its history and
its cursor untouched, and SHALL be permitted only to that identity (or admin). Dropping the
canonical name is `DELETE /agents/<name>` — a different and heavier act. `GET /agents` SHALL
carry each identity's `aliases`.

#### Scenario: one tree, three names, one inbox
- GIVEN `clojure` is live for `muthuishere/toolnexus@cljc`
- WHEN registrations arrive for `toolnexus-cljc` and `toolnexus-clojure` with the same provenance
- THEN both succeed as aliases of `clojure`, `GET /agents` lists ONE identity, and a message addressed to any of the three names appears exactly once in `clojure`'s inbox

#### Scenario: a different branch is a different peer
- WHEN a peer registers for `muthuishere/toolnexus@main` while `toolnexus-cljc` is live
- THEN it is created as its own identity, not an alias

#### Scenario: dropping a label keeps the identity
- GIVEN `clojure` has alias `toolnexus-cljc`
- WHEN `DELETE /agents/toolnexus-cljc/alias` runs
- THEN the alias stops resolving, and `clojure` keeps its inbox, cursor, groups and history

#### Scenario: an alias belongs to its identity alone
- WHEN another agent's credential attempts to drop the alias
- THEN the hub responds `403` and the alias stands

### R13: The hub SHALL expose its own operational state — counters, per-agent delivery facts, and a structured event log

A hub that runs unattended as a service must answer "what changed?" without an
archaeologist. On 2026-08-01 the only way to diagnose a silent mesh was reading NDJSON by
hand and correlating pids.

`GET /metrics` (authenticated) SHALL return a JSON object carrying, at minimum: hub uptime
and start time; total envelopes stored and bytes on disk; counts of live agents, listeners
and attached answerers; per-agent `{delivered, pending, cursor, last_delivered_at,
last_seen, listener, answering}`; thread counts by state (open / resolved / stalled); and
in-flight long-poll count. It SHALL NOT include secret material of any kind — no tokens, no
agent secrets — and SHALL be cheap enough to poll every few seconds.

The hub SHALL also emit a structured, one-line-per-event log (`level`, `event`, `agent`,
`thread`, `ms`, and an outcome) for registration, lease acquire/lose, delivery, stall
refusal, and any 5xx, so a failure has a greppable record rather than requiring a live
observer.

#### Scenario: diagnosing a quiet peer
- GIVEN a peer whose questions are being delivered but never answered
- WHEN an operator reads `GET /metrics`
- THEN that peer shows `listener:true`, `answering:false`, a non-zero `pending`, and a `last_delivered_at` — enough to distinguish "nothing sent" from "nothing read"

#### Scenario: metrics never leak credentials
- WHEN `GET /metrics` is served
- THEN no admin token, agent secret or credential-derived value appears anywhere in the payload
