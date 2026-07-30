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
