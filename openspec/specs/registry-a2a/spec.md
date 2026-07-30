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
