# contacts

## Purpose

The hub maintains a contacts directory so senders can address people by name instead of raw
platform ids. Entries are harvested automatically from inbound traffic (TOFU, unverified) or
added explicitly; `--to` resolution, fuzzy lookup, verification, and purge (tombstone) are the
contract. (ADR-005, ADR-007, ADR-008.)

## Requirements

### R1: The system SHALL upsert a contact entry from every inbound envelope's sender

For each envelope accepted by the hub, the sender's `name`, `peer` (adapter/peer kind, e.g.
`telegram/<chat>`), platform `id`, and `lastSeen` (hub-generated UTC timestamp) SHALL be
upserted into the contacts store (`contacts.ndjson` in the data dir), keyed by (`peer`, `id`).
Harvested entries SHALL carry `"verified": false`. The `name` used for harvesting is the
server-stamped authenticated `from` for hub-registered agents, or the peer-supplied display
name for external-adapter traffic (ADR-007: envelope `from` is never client-asserted).

#### Scenario: first message from an unknown sender
- GIVEN no contact exists for platform id `777001` on peer `telegram`
- WHEN an inbound envelope from that sender is accepted by the hub
- THEN a contact entry is created with `name`, `peer: "telegram/777001"`, `id: "777001"`, `lastSeen` set to the hub's UTC time, and `verified: false`

#### Scenario: repeat message updates last-seen, not verification
- GIVEN a contact for (`telegram`, `777001`) exists with `verified: true` and an older `lastSeen`
- WHEN another envelope from that sender arrives
- THEN `lastSeen` is updated to the new hub UTC time AND `verified` remains `true` (harvest never downgrades verification)

#### Scenario: sender renamed on the platform
- GIVEN a contact for (`telegram`, `777001`) with `name: "Old Name"`
- WHEN an inbound envelope arrives whose sender display name is `"New Name"`
- THEN the entry's `name` is updated to `"New Name"` and any prior name is retained as an alias; the entry key (`peer`, `id`) is unchanged

#### Scenario: tombstoned contact is not resurrected silently as verified
- GIVEN a contact was purged via `DELETE /contacts/<id>` (R7)
- WHEN a new inbound envelope from the same (`peer`, `id`) arrives
- THEN a fresh entry MAY be harvested but SHALL be `verified: false` (purge erases any prior verification)

### R2: The system SHALL accept explicit contact adds and aliases via POST /contacts

`POST /contacts` with JSON body `{"name": "...", "peer": "...", "id": "...", "aliases": ["..."]}`
(`name` and at least one of `peer`+`id` required; `aliases` optional) SHALL create or update an
entry and return `201` with the stored contact JSON, including its hub-assigned `contactId`.
Explicitly added entries SHALL be `"verified": true` — an explicit add is an act of trust by
the operator. Adding an alias to an existing contact merges into that entry.

#### Scenario: explicit add
- GIVEN no contact named "Suguna" exists
- WHEN a client sends `POST /contacts` with `{"name":"Suguna","peer":"telegram","id":"120363410186820001"}`
- THEN the hub returns `201` with `{"contactId":"<id>","name":"Suguna","peer":"telegram","id":"120363410186820001","verified":true,"aliases":[]}`

#### Scenario: alias added to an existing harvested entry
- GIVEN an unverified harvested contact exists for (`telegram`, `777001`) with name "Suguna N"
- WHEN a client sends `POST /contacts` with `{"name":"Suguna N","peer":"telegram","id":"777001","aliases":["Suguna","amma"]}`
- THEN the same entry gains aliases `["Suguna","amma"]`, becomes `verified: true`, and no duplicate entry is created

#### Scenario: invalid body
- WHEN a client sends `POST /contacts` with a body missing `name`
- THEN the hub returns `400` with `{"error":"name required"}` and nothing is stored

#### Scenario: auth required in token mode
- GIVEN the hub runs with `authMode: "token"` (default, ADR-007)
- WHEN `POST /contacts` arrives without valid credentials
- THEN the hub returns `401` and nothing is stored

### R3: The system SHALL serve fuzzy name lookup via GET /contacts?q=

`GET /contacts?q=<term>` SHALL match `q` case-insensitively and fuzzily against names and
aliases and return `200` with `{"contacts":[{"contactId":"...","name":"...","aliases":[...],
"peer":"...","id":"...","verified":bool,"lastSeen":"<UTC RFC3339>"}, ...]}`, best match
first. `GET /contacts` without `q` SHALL list all non-tombstoned entries. Tombstoned entries
SHALL never appear in results.

#### Scenario: fuzzy match
- GIVEN contacts "Suguna" and "Sugandh" exist
- WHEN a client requests `GET /contacts?q=sugu`
- THEN both entries are returned, with "Suguna" ranked first, each carrying its `verified` flag and `lastSeen`

#### Scenario: no match
- WHEN a client requests `GET /contacts?q=zzz` and nothing matches
- THEN the hub returns `200` with `{"contacts":[]}` (not `404`)

#### Scenario: alias match
- GIVEN contact "Suguna N" has alias "amma"
- WHEN a client requests `GET /contacts?q=amma`
- THEN the "Suguna N" entry is returned

### R4: The system SHALL resolve `--to <name>` through the directory and refuse to guess on ambiguity

Name resolution (used by `workwire send --to "<name>"` and any hub-side resolve endpoint)
SHALL match against the merged people set: the live agent registry (ADR-002) plus the
contacts directory. Exactly one match → the envelope carries the resolved raw platform
id / agent name. More than one match → resolution SHALL fail with the full candidate list
and no message SHALL be sent.

#### Scenario: unique verified match
- GIVEN exactly one verified contact matches "Suguna"
- WHEN a client sends with `--to "Suguna"`
- THEN the message is sent, the envelope `to` carries the resolved raw id, and nothing downstream changes (names are aliases over platform ids)

#### Scenario: ambiguous name
- GIVEN both a registered agent "repoA" and a contact "repoA-bot" match the query "repoA"
- WHEN a client resolves `--to "repoA"`
- THEN the hub/CLI returns the candidate list `[{"name":"repoA","kind":"agent",...},{"name":"repoA-bot","kind":"contact","verified":...,...}]` with status `409` (HTTP) / non-zero exit (CLI), and no message is sent

#### Scenario: no match
- WHEN a client resolves `--to "nobody"` and nothing matches
- THEN resolution fails with `404` / non-zero exit and an empty candidate list; no message is sent

### R5: The system SHALL enforce TOFU: unverified contacts require confirmation before `--to` resolution completes

Auto-harvested entries are trust-on-first-use and marked `verified: false` (ADR-005/007).
`--to` resolution to an unverified contact SHALL NOT complete silently: the caller MUST
either confirm explicitly (e.g. `--confirm-unverified` / interactive yes) or verify the
contact beforehand. The refusal response SHALL identify the entry and state that it is
unverified.

#### Scenario: send to unverified contact without confirmation
- GIVEN "Suguna" resolves uniquely to a harvested contact with `verified: false`
- WHEN a client sends `--to "Suguna"` without explicit confirmation
- THEN the send is refused with an error naming the contact and `"verified": false`; no message is sent

#### Scenario: send to unverified contact with explicit confirmation
- GIVEN the same unverified contact
- WHEN the client repeats the send with explicit confirmation
- THEN the message is sent (the confirmation applies to this send only unless the caller also verifies)

#### Scenario: send to verified contact
- GIVEN "Suguna" resolves uniquely to a contact with `verified: true`
- WHEN a client sends `--to "Suguna"`
- THEN the message is sent with no extra prompt

### R6: The system SHALL support explicit verification of a contact

`workwire contacts verify <name>` (backed by a hub call, e.g. `POST /contacts/<contactId>/verify`)
SHALL set `verified: true` on the entry and return `200` with the updated contact JSON.
Verification is idempotent. An ambiguous `<name>` follows R4: candidate list, no action.

#### Scenario: verify a harvested contact
- GIVEN a harvested contact "Suguna" with `verified: false`
- WHEN the operator runs `workwire contacts verify Suguna`
- THEN the entry becomes `verified: true` and subsequent `--to "Suguna"` sends need no confirmation

#### Scenario: verify an ambiguous name
- GIVEN two contacts match "Su"
- WHEN the operator runs `workwire contacts verify Su`
- THEN the command fails with the candidate list and neither entry changes

### R7: The system SHALL purge contacts via DELETE /contacts/<id> using a tombstone

Per ADR-008, `DELETE /contacts/<contactId>` SHALL remove the entry from the directory: it
returns `200` (idempotent — repeating returns `200` again), the entry disappears from all
reads (`GET /contacts`, `GET /contacts?q=`, `--to` resolution, `workwire peers`), and a
tombstone record is appended to the store so the removal survives restarts and NDJSON
replay/rotation. Envelopes already stored are untouched (message excision is
`DELETE /messages/<id>`, out of this capability's scope).

#### Scenario: purge then lookup
- GIVEN contact `c-123` ("Suguna") exists
- WHEN a client calls `DELETE /contacts/c-123` and then `GET /contacts?q=Suguna`
- THEN the delete returns `200` and the lookup returns `{"contacts":[]}`

#### Scenario: purge survives hub restart
- GIVEN contact `c-123` was purged
- WHEN the hub restarts and replays its NDJSON store
- THEN the tombstone is honored and `c-123` still appears in no read

#### Scenario: purge an unknown id
- WHEN a client calls `DELETE /contacts/does-not-exist`
- THEN the hub returns `404` with `{"error":"contact not found"}`

#### Scenario: purged contact and `--to`
- GIVEN "Suguna" was the only match and was purged
- WHEN a client resolves `--to "Suguna"`
- THEN resolution fails per R4's no-match scenario

### R8: The system SHALL merge contacts into the unified people view

`workwire peers` (and any hub endpoint backing it) SHALL present the live agent registry and
the contacts directory as one list of people, each entry labeled by kind (`agent` | `contact`)
and, for contacts, carrying `verified`. Registry entries are liveness-scoped (ADR-002/008
TTL); contact entries persist until purged.

#### Scenario: merged listing
- GIVEN one live registered agent "repoA" and one contact "Suguna"
- WHEN the operator runs `workwire peers`
- THEN both appear, "repoA" as kind `agent` and "Suguna" as kind `contact` with its `verified` flag

#### Scenario: aged-out agent, persistent contact
- GIVEN agent "repoA" missed its TTL (120s) and a contact "Suguna" has an old `lastSeen`
- WHEN the operator runs `workwire peers`
- THEN "repoA" is absent (registry liveness) while "Suguna" remains (directory persistence)
