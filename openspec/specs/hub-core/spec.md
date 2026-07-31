# hub-core

## Purpose

The dumb, LLM-free, HTTP-only message hub (`workwire serve`): one canonical envelope, an
NDJSON segment store with hub-assigned per-recipient sequence cursors, long-poll inbox
delivery with read-time thread-context projection, and env-overridable file config —
identical behavior on a laptop, in a `FROM scratch` container, or behind a reverse proxy.

## Requirements

### R1: The system SHALL store and serve exactly one envelope shape: `{id, from, to, thread_id, reply_to, text, ts, kind, meta, attachments}`, where `id` and `ts` (UTC, RFC 3339) are hub-generated at ingest and `from` is stamped server-side from the sender's authenticated identity, never taken from the request body.

#### Scenario: hub stamps id, ts, and from
- GIVEN an authenticated agent `alice` posts a valid envelope to `POST /send`
- WHEN the hub ingests it
- THEN the stored envelope has a hub-generated unique `id`, a hub-clock UTC `ts`, and `from:"alice"` regardless of any `from` value in the request body

#### Scenario: forged from is impossible
- GIVEN agent `bob` posts an envelope whose body contains `"from":"alice"`
- WHEN the hub ingests it
- THEN the stored and delivered envelope carries `from:"bob"` (the authenticated identity), and no error leaks that a `from` was supplied

#### Scenario: clock skew between hub and clients is irrelevant
- GIVEN a client whose local clock is hours off
- WHEN it sends and polls
- THEN ordering and cursors are unaffected, because `id`, `ts`, and cursors are all hub-generated and cursors are sequence numbers, not times

### R2: The system SHALL accept `POST /send` with a JSON body containing at least `to` and `text` (optional: `thread_id`, `reply_to`, `kind`, `meta`, `attachments`), respond `200` with `{"id": "<id>", "thread_id": "<thread_id>", "ts": "<ts>"}`, and assign a new `thread_id` when none is given.

#### Scenario: send without thread_id starts a thread
- GIVEN a body `{"to":"repoA","text":"where is auth?"}`
- WHEN posted to `/send`
- THEN the response is `200` with a hub-generated `id` and a new hub-generated `thread_id`, and the stored envelope carries both

#### Scenario: send onto an existing thread
- GIVEN a body with `thread_id:"t-1"` for an existing thread
- WHEN posted to `/send`
- THEN the envelope is appended to thread `t-1` and becomes deliverable to its `to` recipient

#### Scenario: malformed send is rejected
- GIVEN a body missing `to`, or that is not valid JSON
- WHEN posted to `/send`
- THEN the hub responds `400` with a JSON error body and stores nothing

### R3: The system SHALL resolve `reply_to:"last"` exactly once at hub ingest, thread-scoped, to the id of the newest envelope on the thread not authored by the sender (the newest inbound), persist the resolved concrete `id` in the stored envelope, and respond `409` when the thread has no such inbound envelope (including an empty thread or a thread containing only the sender's own messages).

#### Scenario: last resolves to the newest inbound
- GIVEN thread `t-1` contains messages from `alice` then `bob`, newest from `bob`
- WHEN `alice` sends with `reply_to:"last"` on `t-1`
- THEN the stored envelope has `reply_to` set to the id of `bob`'s newest message, and redelivery/replay always shows that concrete id, never the literal `"last"`

#### Scenario: last on a thread with only your own messages
- GIVEN thread `t-2` contains only messages authored by `alice`
- WHEN `alice` sends with `reply_to:"last"` on `t-2`
- THEN the hub responds `409` with a JSON error body and stores nothing

#### Scenario: answering a delivered question never uses last
- GIVEN a listener answers a question it received via `/inbox`
- WHEN it posts the answer
- THEN it stamps the question envelope's concrete `id` as `reply_to`; the hub does not special-case this path — `"last"` from an answerer is resolved (or 409s) like any other send

### R4: The system SHALL persist envelopes in append-only NDJSON segment files under the data dir, rotating segments per the retention policy (default: 30 days OR 1 GB, whichever is hit first, both configurable), such that rotation and compaction are invisible to clients: cursors, thread reads, and dedupe ids remain valid across rotation, hub restart, and container redeploy on the same volume.

#### Scenario: hub restart preserves the store
- GIVEN envelopes have been stored and the hub process is killed
- WHEN the hub restarts on the same data dir
- THEN `GET /threads/<id>` returns the full pre-restart thread and a pre-restart cursor delivers exactly the messages sent after it — no loss, no duplicates

#### Scenario: segment rotation does not invalidate cursors
- GIVEN the store rotates a segment (age or size threshold hit) while a consumer holds cursor N within retained history
- WHEN the consumer polls with `since=N`
- THEN delivery continues normally with no `reset` and no skipped messages

#### Scenario: retention expiry
- GIVEN segments older than the retention window (or beyond the size budget) exist
- WHEN retention runs
- THEN those segments are removed; the hub can run indefinitely on one volume without unbounded growth

### R5: The system SHALL assign each recipient a monotonic, hub-generated sequence cursor decoupled from file layout; every `/inbox` response SHALL carry `next` (the authoritative cursor to poll with); and a `since` cursor older than retained history SHALL return `{"messages":[],"next":<earliest available cursor>,"reset":true}` — the client rebases; there is never a silent skip. Delivery is at-least-once and consumers MUST dedupe by envelope `id`.

#### Scenario: normal cursor advance
- GIVEN agent `repoA` polls with `since=3` and two new messages exist for it
- WHEN the poll returns
- THEN the response contains those two messages in order and `next` advanced past them; polling again with that `next` returns nothing new

#### Scenario: cursor older than retained history
- GIVEN retention has removed the segments containing cursor position 3
- WHEN `repoA` polls with `since=3`
- THEN the response is `{"messages":[],"next":<earliest available>,"reset":true}` and the client adopts the returned cursor

#### Scenario: duplicate delivery after a crashed consumer
- GIVEN a consumer received a message but crashed before persisting its cursor
- WHEN it re-polls with the old cursor
- THEN the same envelope (same `id`) is delivered again; the consumer dedupes by `id`

### R6: The system SHALL serve `GET /inbox?agent=<name>&since=<cursor>&wait=<seconds>&context=<X>` as the single receive shape: `agent` is MANDATORY (an unscoped poll is rejected with `400`; no firehose), `wait` defaults to `25` seconds (proxy-safe long-poll; the request returns immediately when messages are available, otherwise holds up to `wait` and returns `200` with an empty `messages` array), and the response body is `{"messages":[...], "next":<cursor>}` (plus `"reset":true` per R5).

#### Scenario: missing agent param
- GIVEN a request `GET /inbox?since=0`
- WHEN the hub handles it
- THEN it responds `400` with a JSON error body

#### Scenario: long-poll delivers within the wait window
- GIVEN `repoA` is parked on `GET /inbox?agent=repoA&since=5&wait=25` with nothing pending
- WHEN a message addressed to `repoA` is ingested 3 seconds in
- THEN the held request completes immediately with that message (push-like latency, ~ms after ingest)

#### Scenario: empty long-poll under a proxy
- GIVEN a reverse proxy with a 30s idle timeout fronts the hub
- WHEN a poll with the default `wait=25` finds nothing
- THEN the hub returns `200 {"messages":[],"next":<cursor>}` at ~25s — before the proxy timeout — and the client re-polls immediately

#### Scenario: wait clamping
- GIVEN a client requests `wait=3600`
- WHEN the hub handles it
- THEN `wait` is clamped to the server's configured maximum rather than holding the connection for an hour

### R7: The system SHALL attach read-time thread context to each delivered inbox message as a separate `context` field: an array of the last X envelopes of that message's thread (default `X = lastMessages = 5`, overridable per request via `context=<X>` and server-clamped to a cap of `20`), each projected entry stamped `kind:"context"`; context entries are background — they never advance the cursor and consumers MUST NOT treat them as inbound deliveries or as an ask's answer.

#### Scenario: default context depth
- GIVEN a new message arrives on a 50-message thread
- WHEN `repoA` receives it via `/inbox` with no `context` param
- THEN the delivered entry carries `context` with the last 5 thread envelopes, each with `kind:"context"`, and `next` advances only for the one delivered message

#### Scenario: context param clamped to cap
- GIVEN a poll with `context=50`
- WHEN the hub projects context
- THEN at most 20 context entries are attached; deeper history requires an explicit `GET /threads/<id>?last=N`

#### Scenario: context is never a delivery
- GIVEN a consumer completing an ask by watching for `reply_to == question id`
- WHEN context entries (including ones whose original `reply_to` matches) ride along
- THEN they are identifiable by `kind:"context"` and are ignored for completion, cursor, and dedupe purposes

#### Scenario: context=0 disables projection
- GIVEN a poll with `context=0`
- WHEN messages are delivered
- THEN no `context` field bloats the payload (bare envelopes only)

### R8: The system SHALL serve `GET /threads/<id>?last=N` returning `200` with `{"thread_id":"<id>","messages":[...]}` — the last N envelopes of the thread in order (all retained envelopes when `last` is absent) — as the explicit escape hatch when projected context depth is insufficient; an unknown thread returns `404`. The endpoint also accepts `?wait=<s>` long-poll sugar (default/cap per R6) used by ask completion — semantics defined in registry-a2a R8.

#### Scenario: fetch deeper history than the context cap
- GIVEN a 200-message thread and a consumer that got only 5 context entries
- WHEN it calls `GET /threads/t-big?last=50`
- THEN it receives the last 50 envelopes of the thread

#### Scenario: unknown thread
- GIVEN no thread `t-nope` exists
- WHEN `GET /threads/t-nope` is called
- THEN the hub responds `404` with a JSON error body

### R9: The system SHALL serve `GET /health` unauthenticated, returning `200` with `{"service":"workwire","schemaVersion":<envelope schema version>,"apiVersion":<hub surface version>}` (no channel or registry data — workwire ships zero channel code per ADR-004, and `/health` leaks nothing per auth R7), usable as the discover-don't-start probe (a peer that gets this response never starts a second hub) and as the version-negotiation surface for polling peers.

#### Scenario: discovery probe
- GIVEN a hub is already serving on the configured loopback port
- WHEN a client verb probes `GET /health` before auto-starting
- THEN it sees `"service":"workwire"` and reuses the running hub instead of starting one

#### Scenario: health stays open under token auth
- GIVEN the hub runs with `authMode=token`
- WHEN `GET /health` is called with no credentials
- THEN it returns `200` (load-balancer probes need no token); all other endpoints still require auth

### R10: The system SHALL serve attachment bytes at `GET /media/<id>` so attachments are fetched from the hub, never by host path: `attachments[]` entries reference hub-generated media ids, a remote client of a container hub can retrieve them, and an unknown media id returns `404`.

#### Scenario: remote client fetches an attachment
- GIVEN an envelope stored on a container hub carries an attachment with media id `m-1`
- WHEN a laptop client calls `GET /media/m-1` on the hub URL
- THEN it receives the attachment bytes with an appropriate `Content-Type`; no host filesystem path appears anywhere in the envelope

#### Scenario: unknown media id
- WHEN `GET /media/m-nope` is called
- THEN the hub responds `404`

### R11: The system SHALL auto-create `~/.config/workwire/workwire.json` with defaults on the first run of any workwire verb (users edit, never bootstrap), and SHALL honor a `WORKWIRE_*` environment override for every config key — including at least `WORKWIRE_BIND` (default `127.0.0.1`), port (default `14411`), `WORKWIRE_DATA_DIR` (default `~/.config/workwire/data`), `hubUrl` (default `http://127.0.0.1:14411`), `lastMessages`, timeouts, retention limits, and the auth token env NAME — such that a container with no home dir and no config file operates env-only. Secret values never appear in the config file.

#### Scenario: first run on a fresh host
- GIVEN no `~/.config/workwire/workwire.json` exists
- WHEN any workwire verb runs
- THEN the file is created with documented defaults before the verb proceeds

#### Scenario: env-only container
- GIVEN a `FROM scratch` container with no home dir and a read-only root FS except the `/data` volume
- WHEN `workwire serve` starts with `WORKWIRE_BIND=0.0.0.0` and `WORKWIRE_DATA_DIR=/data`
- THEN it serves normally with no config file ever written

#### Scenario: env override beats file
- GIVEN `workwire.json` sets `lastMessages: 5` and the environment sets `WORKWIRE_LAST_MESSAGES=3`
- WHEN the hub projects context
- THEN the env value (3) wins

### R12: The system SHALL enforce a single writer per data dir via an OS-held advisory lock (flock/F_SETLK) taken at startup — the lock dies with the process, so it is never stale after `kill -9` or a container redeploy — and a second hub pointed at a locked data dir SHALL fail to start with a clear error.

#### Scenario: second hub on the same data dir
- GIVEN a hub is running against `WORKWIRE_DATA_DIR=/data`
- WHEN a second `workwire serve` starts with the same data dir
- THEN it exits non-zero with an error naming the data dir as locked, and writes nothing

#### Scenario: no stale lock after kill -9
- GIVEN the hub was killed with `kill -9` (or the container was removed)
- WHEN a new hub starts on the same data dir
- THEN it acquires the lock immediately — no manual lockfile cleanup, no pid-file heuristics

### R13: The system SHALL support content excision via tombstones (ADR-008): `DELETE /messages/<id>` tombstones one envelope and `DELETE /threads/<id>` tombstones every envelope on the thread — the envelope `id` remains (dedupe and thread graphs stay stable) while the content is excised from ALL reads: inbox replay, `GET /threads`, and read-time context projection. Tombstones survive restart, rotation, and NDJSON replay. Deleting an unknown id returns `404`; repeating a delete is idempotent (`200`).

#### Scenario: excise a pasted secret
- GIVEN an envelope `m-7` whose text contains a pasted secret
- WHEN `DELETE /messages/m-7` is called (authenticated)
- THEN subsequent `GET /threads/<its thread>`, inbox redelivery, and context projections show the envelope tombstoned with no original text, while `m-7` still exists as an id for dedupe and `reply_to` integrity

#### Scenario: thread excision
- GIVEN thread `t-9` with 12 envelopes
- WHEN `DELETE /threads/t-9` is called
- THEN every envelope on `t-9` is tombstoned in all reads

#### Scenario: excision survives restart
- GIVEN `m-7` was tombstoned
- WHEN the hub restarts and replays its NDJSON segments
- THEN the tombstone is honored and the content never reappears

#### Scenario: unknown id
- WHEN `DELETE /messages/m-nope` is called
- THEN the hub responds `404` with a JSON error body

### R14: The system SHALL accept `to` as either a single name (a JSON string) or an array of names, delivering ONE envelope with ONE `id` to every recipient while assigning each recipient its own sequence cursor (ADR-009). A string `to` behaves exactly as before, including its wire shape on read. Delivery stays at-least-once and consumers dedupe by envelope `id`; storage stays one stored envelope regardless of recipient count.

#### Scenario: single recipient is unchanged
- GIVEN a client sends `{"to":"repoA","text":"..."}`
- WHEN `repoA` polls its inbox
- THEN it receives exactly one message whose `to` is the plain string `"repoA"`, and its cursor advances once

#### Scenario: array fan-out shares one id
- GIVEN a client sends `{"to":["repoA","repoB","repoC"],"text":"..."}`
- WHEN each of the three polls its inbox
- THEN each receives one message carrying the SAME envelope `id`, each with its own distinct `next` cursor, and polling again from that cursor returns nothing

#### Scenario: sender is not a recipient of its own envelope
- WHEN an agent addresses a list that does not include itself
- THEN its own inbox receives nothing for that envelope

### R15: The system SHALL accrue thread membership from participation: the members of a thread are everyone who has sent into it or been addressed in it, and the first sender is recorded as its INITIATOR. A `POST /send` carrying only `thread_id` (no `to`) SHALL fan out to all current members except the sender; sending into a thread the sender was not in joins them to it. Membership and initiator SHALL survive a hub restart. A send with neither `to` nor a known thread returns `400` with a clear error.

#### Scenario: reply addressed only by thread
- GIVEN thread `t-1` with members `alice`, `repoA`, `repoB`
- WHEN `repoA` sends `{"thread_id":"t-1","text":"..."}` with no `to`
- THEN `alice` and `repoB` each receive the envelope and `repoA` does not

#### Scenario: join by sending
- GIVEN `repoC` is not a member of thread `t-1`
- WHEN `repoC` sends into `t-1`
- THEN `repoC` becomes a member and receives subsequent fan-out; the hub still stamps `from` server-side

#### Scenario: membership survives restart
- GIVEN thread `t-1` accrued four members
- WHEN the hub restarts and replays its segments
- THEN `t-1` still reports the same members and the same initiator

### R16: The system SHALL converge discussions by two mechanisms (ADR-009). An envelope with `kind:"resolved"` closes the thread; only the thread INITIATOR may send it — a non-initiator receives `403` naming the initiator and pointing at `kind:"proposal"`, which is accepted and never closes the thread. Further sends to a resolved thread return `409`; there is no reopen path. Independently, once a thread reaches `maxThreadMessages` envelopes (default `24`, `WORKWIRE_MAX_THREAD_MESSAGES`) it is `stalled`: fan-out stops and sends return `409` with an error naming the cap, how to raise it, and the initiator it is handed back to.

#### Scenario: participant may not resolve
- GIVEN `alice` opened thread `t-1` and `repoA` is a participant
- WHEN `repoA` sends `kind:"resolved"`
- THEN the hub responds `403` with an error naming `alice` and `kind:"proposal"`, and the thread stays open

#### Scenario: proposal is a recommendation
- WHEN `repoA` sends `kind:"proposal"`
- THEN it is stored and delivered like any contribution and the thread state remains `open`

#### Scenario: initiator resolves
- WHEN `alice` sends `kind:"resolved"` on `t-1`
- THEN the thread state becomes `resolved` and any further send by any member returns `409`

#### Scenario: round cap trips
- GIVEN `maxThreadMessages` is `6` and thread `t-1` holds 6 envelopes
- WHEN any member sends again
- THEN the hub responds `409` with an error naming the cap (`6`), `maxThreadMessages` / `WORKWIRE_MAX_THREAD_MESSAGES`, and the initiator, and the thread state reads `stalled`

### R17: The system SHALL carry an optional `persona` on the agent card — a short self-description supplied at registration — serve it on `GET /agents` and `GET /agents/<name>/card`, and include each speaker's persona alongside `from` in projected `context` entries, so a participant can weigh who said what. The hub neither invents nor validates personas. `GET /threads` SHALL list live threads with `thread_id`, `initiator`, `members`, `count` and `state` (`open` | `resolved` | `stalled`).

#### Scenario: persona round-trip
- GIVEN an agent registers with `{"name":"repoA","persona":"owns auth; will not speak for the web UI"}`
- WHEN a peer reads `GET /agents/repoA/card` or `GET /agents`
- THEN the persona is returned verbatim

#### Scenario: late joiner reads who said what
- GIVEN thread `t-1` already has several messages and `late` has never polled it
- WHEN `late` is addressed on `t-1` and polls its inbox
- THEN the delivery's `context` carries the prior thread messages, each stamped `kind:"context"` and annotated with the speaker's `persona` where one is registered

### R18: The system SHALL carry an auto-derived `origin` provenance block on every peer — `repo` (git remote normalised to `owner/name`, else the directory name), `branch`, `commit` (short SHA), `dirty`, `cwd`, `host` — supplied at registration and refreshed by re-registration, stored as given and NEVER verified (ADR-011 §1). It SHALL serve `origin` on `GET /agents/<name>/card` and `GET /agents`, stamp it on every accepted envelope, and attach it next to `from` and `persona` on every projected `context` entry. A peer that reports no repo/branch (a non-git directory) SHALL register and serve cleanly.

#### Scenario: provenance round-trip
- GIVEN a peer registers with `{"name":"api","origin":{"repo":"muthuishere/workwire","branch":"main","commit":"a1b2c3d"}}`
- WHEN another peer reads the card, `GET /agents`, or a projected `context` entry authored by `api`
- THEN the same `origin` is returned verbatim in all three

#### Scenario: branch switch mid-session
- GIVEN `api` re-registers with the same secret and `branch:"feat/dissent"`, `dirty:true`
- WHEN its card is read again
- THEN it reports the new branch and dirty flag

#### Scenario: non-git directory
- GIVEN a peer registers with an `origin` carrying only `cwd` and `host`
- THEN registration succeeds and the card reports no `repo` and no `branch`

### R19: The system SHALL accept `kind:"dissent"` as a first-class envelope that records an OPEN objection on thread state (peer, peer kind, text, provenance), and `kind:"withdraw"` which clears ONLY the sender's own dissent. Open dissents SHALL be derived from the persisted envelopes and survive a hub restart, and SHALL be reported on `GET /threads`.

#### Scenario: dissent is tracked, withdraw is scoped
- GIVEN `web` and `priya` have each sent `kind:"dissent"` on thread `t-1`
- WHEN `web` sends `kind:"withdraw"`
- THEN only `web`'s dissent is cleared and `priya`'s remains open

#### Scenario: dissent survives restart
- WHEN the hub restarts and replays its segments
- THEN the same open dissents, with the dissenters' provenance, are reported

### R20: The system SHALL enforce VALID CLOSURE (ADR-011 §3). A peer declares `kind` at registration: `agent` (default) or `human`. An AGENT initiator may send `kind:"resolved"` only when there are ZERO open dissents; otherwise `409` naming each dissenter with their provenance and both legitimate paths (withdrawal, or a human decision) — an agent can never override a dissent. A HUMAN peer may close any thread it is a member of, including one it did not initiate, with a required non-empty summary, and may close over any number of AGENT dissents; a human MAY NOT close over another HUMAN's open dissent (`409` naming that person), while their own dissent does not block their own close. The closing envelope SHALL record who closed the thread and which open dissents it closed over, and `GET /threads` SHALL report both.

#### Scenario: agent blocked by a dissent
- GIVEN thread `t-1` initiated by `api` carries an open dissent from `web`
- WHEN `api` sends `kind:"resolved"`
- THEN the hub responds `409` naming `web` and its `repo@branch`, pointing at withdrawal or a human, and the thread stays `open`

#### Scenario: withdrawal unblocks the agent close
- WHEN `web` sends `kind:"withdraw"` and `api` sends `kind:"resolved"` again
- THEN the thread becomes `resolved`

#### Scenario: a human closes over agent dissent
- GIVEN `web` and `api` both hold open dissents
- WHEN the human `muthu` sends `kind:"resolved"` with a non-empty summary
- THEN the thread closes and the record names `muthu` as the closer and `web` and `api` as closed over

#### Scenario: a human may not overrule a colleague
- GIVEN the human `priya` holds an open dissent
- WHEN the human `muthu` sends `kind:"resolved"`
- THEN the hub responds `409` naming `priya` and the withdrawal path, and the thread stays open and contested

#### Scenario: closing without a summary
- WHEN a human sends `kind:"resolved"` with empty text
- THEN the hub responds `400`

### R21: The system SHALL accept `kind:"reopen"` from a HUMAN peer on a thread that is `resolved` or `stalled`, clearing the closure and restarting the round cap; an AGENT sending `reopen` receives `403`. A `kind:"dissent"` sent to a RESOLVED thread SHALL be accepted and preserved as history without reopening it; every other send to a resolved thread stays `409`. The round-cap behaviour of R16 is otherwise unchanged.

#### Scenario: agents may not reopen
- WHEN an agent sends `kind:"reopen"` on a resolved thread
- THEN the hub responds `403` and the thread stays `resolved`

#### Scenario: a human reopens
- WHEN a human sends `kind:"reopen"` on a thread an agent closed
- THEN the state returns to `open`, the closure record is cleared, and members may send again

#### Scenario: a closed thread ends the decision, not the disagreement
- WHEN an agent sends `kind:"dissent"` on a resolved thread
- THEN it is stored as history, the thread stays `resolved`, and a plain message, `proposal`, `withdraw` or `resolved` from the same peer still returns `409`

### R22: `GET /threads` SHALL list EVERY non-deleted thread to any authenticated peer — not only those the caller is a member of — carrying `thread_id`, `topic`, `initiator`, `members`, `count`, `state`, open `dissents`, closure record, and a `member` flag for the caller. Addressing controls delivery; discovery controls participation: a peer that was never addressed can find a thread and join it by sending into it. (Local-trust assumption; a shared hub needs per-workspace scoping — ADR-010.)

#### Scenario: an uninvited peer discovers and joins
- GIVEN thread `t-1` addressed only to `web`, `muthu` and `priya`
- WHEN `dba`, who was never addressed, calls `GET /threads`
- THEN `t-1` is listed with its topic and `member:false`, and after `dba` sends into it, it is a member and the listing reports `member:true`


### R23: The system SHALL keep GROUPS as runtime registry state (never config): a named, dynamic set of peers referenced with an `@` prefix, holding NO messages and NO state to converge. `POST /groups/<name>/join` SHALL create the group when it does not exist (no create verb, no owner, no admin) and `POST /groups/<name>/leave` SHALL remove the caller, garbage-collecting the group once empty — except `@all`, the default group every peer joins at registration, which persists. A group name SHALL be rejected when it collides with a registered peer name, and a peer registration SHALL be rejected (`409` with a suggestion) when it collides with an existing group name. `GET /groups` SHALL list every group with its members and a `member` flag for the caller. Membership SHALL survive a hub restart. (ADR-012.)

#### Scenario: a group is created by joining it
- GIVEN no group `@payments` exists
- WHEN `web` calls `POST /groups/@payments/join`
- THEN the group exists with `web` as its only member, and a second peer joining has exactly the same standing — there is no owner and no admin

#### Scenario: every peer lands in the lobby
- GIVEN a fresh hub
- WHEN an agent peer and a human peer each register
- THEN both are members of `@all`, and either may leave it

#### Scenario: leaving @all is how a peer goes quiet
- WHEN the only member of `@all` leaves it
- THEN `@all` still exists with zero members, while an emptied ad-hoc group `@adhoc` is garbage-collected and disappears from `GET /groups`

#### Scenario: a group and a peer may never share a name
- GIVEN the peer `web` is registered
- WHEN any peer tries to join `@web`
- THEN the hub responds `409`; and conversely, once `@platform` exists, registering a peer named `platform` responds `409` with a suggestion that does not collide either

#### Scenario: membership survives a restart
- GIVEN `web` joined `@platform` and left `@all`
- WHEN the hub is restarted on the same data dir
- THEN `web` is still in `@platform` and still absent from `@all`

### R24: The system SHALL expand a group in `to` EXACTLY ONCE, at ingest, to a snapshot of that group's current members, after which it is an ordinary fan-out onto an ordinary thread. Groups and individual names SHALL mix freely in one `to`, recipients SHALL be deduped, and the sender SHALL never appear in their own group fan-out. A peer who joins the group later SHALL NOT be added to that thread retroactively; discovery (R22) is the only way in. Addressing an unknown group SHALL respond `400` and store nothing. (ADR-012.)

#### Scenario: a group expands to a snapshot
- GIVEN `web` and `api` are in `@platform`
- WHEN `web` sends `{"to":"@platform","text":"…"}`
- THEN `api` receives one envelope whose `to` is `api` — the sender is excluded — and a thread is opened

#### Scenario: a later joiner does not enter the thread retroactively
- GIVEN that thread already exists
- WHEN `dba` joins `@platform` afterwards
- THEN `dba` receives nothing for it, but `GET /threads` lists the thread with `member:false` so `dba` can walk in by sending into it

#### Scenario: groups and names mix and dedupe
- WHEN `web` sends to `["@platform","api","dba"]` while `api` is already in `@platform`
- THEN `api` and `dba` each receive exactly one envelope, `web` receives none, and the stored recipients are `api,dba`

#### Scenario: an unknown group is a loud error
- WHEN a peer sends to `@nobody`, which no one has joined
- THEN the hub responds `400` and stores nothing

### R25: The system SHALL treat inviting as a MESSAGE and never as a mutation. NO endpoint SHALL add or remove any peer other than the authenticated caller: a join/leave request naming a different peer SHALL respond `403`, for agent and admin credentials alike. An invite is an ordinary envelope telling the invitee how to join, which the invitee may ignore. Consent to be woken stays with the peer being woken (ADR-007, ADR-012).

#### Scenario: an invite delivers a message and changes nothing
- GIVEN `web` is in `@payments`
- WHEN `web` sends `dba` an invite envelope naming `@payments`
- THEN `dba` receives the envelope with the join command in its text, and the membership of `@payments` is unchanged

#### Scenario: no peer can add another peer
- WHEN `web`, or the admin token, calls join or leave on `@payments` with a body naming `dba`
- THEN the hub responds `403` and the membership of `@payments` is unchanged — a silent add would let anyone force-wake anyone else's session
