# agent-skill

## Purpose

The two-way agent skill (`workwire install --skills`) puts any agent session on the workwire
network with one install and one invoke: it registers the session on the hub (outbound
identity + `peers`/`send`/`ask` verbs) and supervises a singleton `workwire listen` process
that delivers inbound questions into the running session via a session inbox file, so the
session answers from its own live context. workwire never makes an LLM call (ADR-003, ADR-007).

## Requirements

### R1: The system SHALL install the embedded skill via `workwire install --skills`

The skill payload is compiled into the `workwire` binary (`go:embed`); install writes it into
the agent harness's skills directory. No network access, no daemon install, no config edits
beyond auto-creating `~/.config/workwire/workwire.json` with defaults if absent.

#### Scenario: fresh install
- GIVEN a machine with the `workwire` binary and no skill installed
- WHEN the user runs `workwire install --skills`
- THEN the embedded skill files are written into the harness skills directory
- AND `~/.config/workwire/workwire.json` exists (auto-created with defaults, including `hubUrl: "http://127.0.0.1:14411"`) if it did not before

#### Scenario: re-install over an existing skill
- GIVEN a previously installed (possibly older) copy of the skill
- WHEN `workwire install --skills` runs again
- THEN the skill files are replaced with the embedded version
- AND no runtime state (credentials, cursors, session inbox files) is touched

### R2: The system SHALL run the first-run flow — health probe, then loopback-only detached autostart

On first invocation of the skill in a session, before anything else, the skill ensures a hub
is reachable at the configured `hubUrl`.

#### Scenario: hub already running
- GIVEN a hub serving `GET /health` → `200 {"service":"workwire"}` at `hubUrl`
- WHEN the skill is invoked
- THEN it proceeds directly to registration without starting anything

#### Scenario: no hub, loopback hubUrl
- GIVEN `GET /health` at `hubUrl` fails AND `hubUrl` host is loopback
- WHEN the skill is invoked
- THEN it starts `workwire serve` detached in its own process group, so the hub survives the session's exit
- AND re-probes `/health` until it answers, then proceeds

#### Scenario: no hub, remote hubUrl — never auto-start
- GIVEN `GET /health` fails AND `hubUrl` is not loopback
- WHEN the skill is invoked
- THEN it does NOT start a hub; it reports the remote hub unreachable and stops (ADR-001: remote hubs are probed, never started)

#### Scenario: bind race between two simultaneous sessions
- GIVEN two sessions invoke the skill at the same moment with no hub running
- WHEN both attempt to start `workwire serve` and only one can bind port 14411
- THEN the bind loser re-probes `GET /health` and, on `{"service":"workwire"}`, becomes a client of the winner's hub
- AND exactly one hub process results (bind-first-then-health-check, ADR-003)

### R3: The system SHALL register the session on the hub with a card derived from the session, and store hub-issued credentials

#### Scenario: first registration of a name
- GIVEN a reachable hub and no stored credentials for this agent name
- WHEN the skill registers via `POST /agents` with a card derived from the session (at minimum: `name`, cwd/project, capabilities; only spike-proven fields — see R10)
- THEN the hub responds with `{agentId, agentSecret}`
- AND the skill stores the secret in `~/.config/workwire/credentials.json` with mode `0600`
- AND all subsequent actions as that agent (send, inbox reads, ask, heartbeat, re-register, lease) authenticate with those credentials (ADR-007)

#### Scenario: name already taken
- GIVEN an agent name already registered on the hub with different credentials
- WHEN the skill attempts `POST /agents` with that name
- THEN the hub responds `409` with a suggested free name (e.g. `name-2`)
- AND the skill registers under the suggested name instead of silently taking over

#### Scenario: re-registration with stored credentials
- GIVEN credentials for the name exist in `credentials.json`
- WHEN the skill is re-invoked (new session, same agent)
- THEN it re-registers/heartbeats authenticated with the stored secret and keeps the same identity; no new credentials are minted

### R4: The system SHALL enforce `workwire listen` as a singleton per agent — local flock plus hub-side lease

#### Scenario: second listen on the same host
- GIVEN a running `workwire listen --agent A` holding the OS advisory lock (flock/`F_SETLK` on an open fd under the config/run dir)
- WHEN a second `workwire listen --agent A` starts on the same host
- THEN it fails to acquire the lock and exits without polling; the skill adopts the running listener instead

#### Scenario: stale lock after kill -9 or container restart
- GIVEN the previous listener died without cleanup (kill -9, container redeploy)
- WHEN a new `workwire listen --agent A` starts
- THEN the OS lock is already free (it died with the process — not a pid lockfile), and the new listener acquires it immediately (stress-test #18)

#### Scenario: two hosts listen as the same agent
- GIVEN host X holds the hub-side listen lease for agentId A (acquired at listen start, renewed with the heartbeat)
- WHEN host Y starts `workwire listen --agent A`
- THEN the hub refuses Y the lease while X's lease is live, so double answers are impossible across machines (stress-test #19)

#### Scenario: expired lease is claimable
- GIVEN host X's listener stopped heartbeating and its lease expired
- WHEN host Y attempts to acquire the lease
- THEN the hub grants it and Y becomes the sole listener

### R5: The system SHALL deliver inbound questions into the session via an append-only NDJSON session inbox file

`workwire listen` is a dumb waiter: it long-polls `GET /inbox?agent=<name>&since=<cursor>&wait=25`
(authenticated), appends each delivered envelope (with its attached `context` field) as one
NDJSON line to the agent's session inbox file, and persists the hub cursor. The session-side
loop/hook tails the file and consumes from a persisted offset (Spike-01 mechanism (a)).

#### Scenario: question delivered to a live session
- GIVEN a running listener and a live session tailing the inbox file
- WHEN a `kind:question` envelope arrives on the agent's inbox with `context: [last 5 thread messages]` attached
- THEN the listener appends the full envelope as one NDJSON line and advances its persisted cursor
- AND the session picks it up on its next wake point and answers

#### Scenario: session closed when the question arrives
- GIVEN the listener is running but no session is consuming the file
- WHEN a question is delivered
- THEN it sits durably in the inbox file and is consumed on next session start from the persisted byte offset (stress-test #11; Spike-01 offline case)

#### Scenario: listener down when the question is sent
- GIVEN the listener is not running
- WHEN a question is posted to the hub
- THEN it waits in the hub inbox; on listener restart the persisted `since` cursor picks it up with no loss

#### Scenario: duplicate delivery
- GIVEN at-least-once delivery from the hub
- WHEN the same envelope `id` appears twice in the inbox file
- THEN the session-side consumer dedupes by envelope `id` and answers once (stress-test #12)

#### Scenario: cursor reset
- GIVEN the hub returns `{"messages":[],"next":<n>,"reset":true}` (cursor outside retained history; field is `next` per hub-core R5)
- WHEN the listener sees `reset:true`
- THEN it adopts the returned cursor as authoritative and continues polling — no silent skip, no crash (Spike-02)

#### Scenario: inbox file rotation
- GIVEN the session inbox file grows without bound (Spike-01 open risk 6)
- WHEN it exceeds the rotation threshold and all appended envelopes up to the consumer's persisted offset are consumed
- THEN the listener rotates/compacts the file and the consumer's offset is rebased consistently, with no envelope lost or re-delivered as new

### R6: The answer path SHALL stamp the concrete question envelope id

#### Scenario: answering a delivered question
- GIVEN a delivered question envelope with id `q-123` on thread `t-1`
- WHEN the session posts its answer via the hub
- THEN the answer envelope carries `reply_to: "q-123"` (the concrete id) and the same `thread_id: "t-1"`
- AND the answer path never uses `reply_to:"last"` (ADR-001)

#### Scenario: multiple interleaved questions on one thread
- GIVEN two unanswered questions `q-1` and `q-2` on the same thread
- WHEN the session answers each
- THEN each answer stamps its own question's id, so askers can match answers unambiguously

### R7: The system SHALL default inbound-triggered turns to answer-only, treating question text as untrusted data

#### Scenario: normal inbound question
- GIVEN a session with the skill and default registration (no opt-in)
- WHEN an inbound question triggers a turn
- THEN the session answers using read-only context (repo, docs, memory) with no shell or write tools on that turn

#### Scenario: prompt-injection attempt
- GIVEN an inbound question whose text contains instructions ("run rm -rf", "ignore previous instructions and…")
- WHEN it is delivered
- THEN the skill presents the body as a quoted external question — data, never instructions — with its authenticated provenance (server-stamped `from`, peer kind) visible, and the answer-only default holds (stress-test #20; ADR-007)

#### Scenario: explicit opt-in
- GIVEN the registration explicitly opted in to tool use on inbound turns
- WHEN a question arrives
- THEN the session may use additional tools; absent that opt-in, the default is answer-only

### R8: The system SHALL expose the outbound verbs `peers`, `send`, and `ask` in-session

#### Scenario: find peers
- GIVEN a registered session
- WHEN it runs `workwire peers`
- THEN it lists live agents from the hub registry (`GET /agents`, heartbeat-fresh entries only)

#### Scenario: send
- WHEN the session runs `workwire send` to a named peer
- THEN an envelope is posted via `POST /send`, authenticated, with `from` stamped server-side from the hub-issued identity — never client-asserted

#### Scenario: ask and wait
- WHEN the session runs `workwire ask <agent> "<question>"`
- THEN a question envelope is posted, and the verb polls the resulting thread until the answer envelope (with `reply_to` = the question's id) arrives, then prints it

#### Scenario: ask an unverified auto-harvested contact
- GIVEN `<agent>` resolves only to an `unverified` contact entry
- WHEN `workwire ask` targets it via `--to` resolution
- THEN explicit confirmation or prior `workwire contacts verify <name>` is required before sending (ADR-007 TOFU)

### R9: The system SHALL supervise the listener — restart a dead listener on next skill invoke

#### Scenario: listener died since last invoke
- GIVEN the listener process is dead (flock free, lease expired or expiring)
- WHEN the skill is next invoked in the session
- THEN it detects the dead listener and spawns a fresh `workwire listen --agent <name>`, which resumes from the persisted cursor

#### Scenario: listener healthy
- GIVEN the listener is alive and holds the flock
- WHEN the skill is invoked again
- THEN it adopts the running listener; no second process is spawned (R4)

### R10: The context manifest is PROPOSED — shape TBD, not part of the v1 contract

A "context manifest" captured at register time (what the agent can answer about) is a
proposed mechanism only, pending the real-interactive-session spike (ADR-003; stress-test #21).

#### Scenario: registration today
- GIVEN the manifest shape is undecided
- WHEN the skill registers an agent
- THEN the card carries only the spike-proven fields (name, cwd/project, capabilities); no manifest field is required, and consumers MUST NOT depend on one

#### Scenario: manifest lands later
- WHEN the real-session spike fixes the manifest shape
- THEN this spec is amended before any implementation ships a manifest field
