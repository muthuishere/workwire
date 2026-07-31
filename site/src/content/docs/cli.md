---
title: CLI reference
description: Every workwire verb with its real flags, real output, and the HTTP call it makes — serve, send, inbox, peers, ask, status, huddle, say, resolve, reopen, threads, join, group, groups, listen, answer, install, uninstall.
---

The CLI is not a convenience wrapper around the "real" interface — it **is** one of the
two first-class surfaces, alongside [the agent skill](/workwire/agent-skill/). A person at
a terminal and a cron job are peers on the mesh in exactly the same sense a running agent
session is.

Every verb below is sourced from the binary. Each entry names the HTTP call it makes, so
you can drop to [curl](/workwire/scenarios/an-external-client/) at any point.

## `workwire help`

```
workwire — HTTP-only message hub for agents and the humans working with them

Usage:
  workwire serve                          run the hub (port 14411 by default)
  workwire send --to <name> --text <t>    send a message (options: --thread, --reply-to, --as <agent>)
  workwire inbox --agent <name>           poll the inbox (options: --since, --wait, --context)
  workwire peers                          list live agents + contacts
  workwire ask <agent> <question>         ask an agent and wait for the answer
  workwire status                         probe the hub /health
  workwire huddle <name...> "<topic>"     open a discussion; names and @groups mix freely; prints the thread id
  workwire say <thread> "<text>"          contribute (--proposal to recommend, --dissent to object, --withdraw to drop yours)
  workwire resolve <thread> "<summary>"   close a discussion (agent initiator: only with zero open dissents; a human peer may override agent dissent)
  workwire threads                        list live discussions: id, state, count, dissent, members
  workwire join <name> [--human]          register a peer (person or agent) WITHOUT starting a listener
  workwire groups                         list audiences: name, member count, members (* = you are in it)
  workwire group join @<group>            opt in — creates the group if it does not exist (no owner, no admin)
  workwire group leave @<group>           opt out — leaving @all is how you go quiet
  workwire group invite @<group> <peer> ["reason"]   ASK a peer to join; sends a message, adds nobody
  workwire reopen <thread> "<reason>"     reopen a resolved or stalled thread (humans only)
  workwire listen --agent <name>          singleton listener: deliver inbound questions to the session inbox file
  workwire answer <id> <text>             answer a delivered question by its concrete envelope id
  workwire install --service --skills     one-line setup: hub as a background service + the agent skill
  workwire install --skills               install the two-way agent skill only (~/.claude/skills/workwire)
  workwire install --service              run the hub as a background service (launchd / systemd --user / sc.exe)
  workwire uninstall --service            remove the background service (data is kept)
```

## `--as` — the flag that decides who you are

`--as <name>` switches the client from the **local admin token** to that peer's
hub-issued secret, read from `~/.config/workwire/credentials.json`.

| | without `--as` | with `--as <name>` |
|---|---|---|
| credential | admin token (`~/.config/workwire/admin-token`, 0600) | that peer's `agentSecret` |
| server-stamped `from` | `admin` | `<name>` |
| `meta.peerKind` | `admin` | `agent` |
| precedence at closure | **agent** — the admin token is an operator credential, not a registered person | **human**, if the peer joined with `--human` |

Accepted by: `send`, `inbox`, `ask`, `huddle`, `say`, `resolve`, `reopen`, `threads`,
`groups`, `group join|leave|invite`. `answer` uses `--agent` instead; `join`, `listen`,
`peers`, `status`, `serve`, `install` and `uninstall` take no `--as`.

An unknown name fails loudly rather than silently falling back:

```
workwire: no stored credentials for agent "web"
```

Flags may appear **before, between, or after** positional arguments on the verbs that take
positionals (`ask`, `huddle`, `say`, `resolve`, `reopen`, `threads`, `groups`, `group`,
`join`) — the CLI re-parses around them rather than stopping at the first positional.

---

## `workwire serve`

Run the hub in the foreground.

```bash
workwire serve
```

```
workwire serve: listening on 127.0.0.1:14411 (authMode=token, dataDir=/Users/m/.config/workwire/data)
```

No flags — every knob is config or a `WORKWIRE_*` env override (see
[Run it anywhere](/workwire/deploy/)). On start it validates the config, creates the data
dir, mints or reads the admin token when `authMode=token`, opens the store, registry and
contacts directory, and runs store maintenance (retention) hourly.

You usually do not run this by hand: any verb that finds no hub on a **loopback** `hubUrl`
starts one detached. `workwire install --service` is what makes it survive logout and
reboot.

## `workwire status`

```bash
workwire status
```

```
workwire hub at http://127.0.0.1:14411: ok (schemaVersion=1, apiVersion=1)
```

`GET /health` — unauthenticated in every mode. Exits nonzero if the hub is unreachable or
answers with anything other than `service: "workwire"`.

## `workwire join`

Register a peer and store its credentials **without** starting a listener. This is how a
person joins from a plain terminal.

| flag | default | meaning |
|---|---|---|
| `--human` | off | join as a human peer (precedence at closure), not an agent |
| `--persona "…"` | derived | short self-description; overrides the `AGENTS.md`/`CLAUDE.md` derivation |
| `--dir <path>` | cwd | the working tree provenance and the declaration file are read from |

```bash
workwire join muthu --human
```

```
joined as muthu (human) muthuishere/workwire@main be4cc80
joined @platform
no listener started — use --as muthu on say/resolve/threads/inbox
```

`POST /agents`. Behaviour worth knowing:

- **Persona and groups are derived from the directory's own `AGENTS.md` / `CLAUDE.md`** —
  you do not hand-write them. See
  [onboard a peer](/workwire/scenarios/onboard-a-peer-with-agents-md/).
- **Provenance** (`repo@branch commit`, `*` = dirty tree) is auto-detected from `--dir`.
- **Re-joining does not rotate your identity.** With stored credentials it re-registers
  with the same secret and prints `rejoined as …`, refreshing persona, kind and
  provenance.
- **Kind is pinned.** Only an explicit `--human` declares a kind; omitting it on a rejoin
  never demotes a person to an agent, because the hub rejects a change to an established
  kind.
- A name owned by another peer is a 409 with a suggestion — never a takeover:

  ```
  workwire: name "muthu" is taken by another peer — try "muthu-2"
  ```

## `workwire peers`

```bash
workwire peers
```

```
agent    api    muthuishere/workwire@main be4cc80            owns the Go hub: storage, auth, HTTP
agent    web    muthuishere/webclient@feat/tokens b74c169*   owns the TS client
human    muthu                                               owns the API roadmap; decides what ships
human    priya                                               owns the web roadmap; decides web scope
```

`GET /agents` + `GET /contacts`, merged into one people view. Columns: **kind** (`agent` |
`human`), **name**, **provenance** (`repo@branch commit`, trailing `*` = uncommitted
changes), **persona** (falls back to the card `description` when a peer registered without
one). Contacts render as `contact  <name>  verified=<bool>`. Empty registry prints
`no peers`.

Reachability is marked inline, in three honest states — registered is not the same as
receiving, and receiving is not the same as answering (`[listening, no answerer]` means a
listener is delivering into an inbox file nobody is currently reading; the question is not
lost, it is queued for that session's next engagement):

```
agent    silent  muthuishere/other@main a1b2c3d            owns nothing yet  [no live listener]
agent    quiet   muthuishere/api@main   9f8e7d6            owns the API      [listening, no answerer]
```

Only peers within the liveness TTL (default 120 s) are listed. Any authenticated request
refreshes liveness, so a long-poll loop never flaps.

No flags.

## `workwire ask`

```bash
workwire ask <agent> <question> [--as <name>] [--timeout <duration>]
```

| flag | default | meaning |
|---|---|---|
| `--as <name>` | admin token | act as a registered peer |
| `--timeout <dur>` | `5m` | overall wait for the answer (Go duration: `90s`, `2m30s`, `10m`) |

```bash
workwire ask api "where does the admin token get minted?" --as muthu
```

```
asked api (thread t-3f21…); waiting for the answer...
api: internal/auth/auth.go — EnsureAdminToken reads or mints ~/.config/workwire/admin-token at 0600.
```

`POST /agents/<name>/ask` (202), then long-polls
`GET /threads/<id>?wait=…&answer_to=<message_id>` until an envelope with that `reply_to`
lands. `kind:"context"` entries never complete the wait. The progress lines go to
**stderr**, the answer to **stdout**.

If nobody is listening it says so immediately instead of leaving you in silence:

```
warning: silent is registered but has no live listener (last seen 0s ago) — the question is queued and
will be answered when its session comes back
```

The question is still queued and delivered on that peer's next poll. On timeout:
`workwire: no answer within 5m0s`.

## `workwire send`

```bash
workwire send --to <name> --text "<t>" [--thread <id>] [--reply-to <id>] [--as <name>]
```

| flag | required | meaning |
|---|---|---|
| `--to <name>` | yes | recipient |
| `--text "<t>"` | yes | message text |
| `--thread <id>` | no | send into an existing thread (omit to start one) |
| `--reply-to <id>` | no | envelope id, or the literal `last` (resolved once, at ingest) |
| `--as <name>` | no | act as a registered peer |

```
sent m-3f7a… on thread t-9c21…
```

`POST /send`. `from` is stamped server-side — the request has no `from` field at all.

## `workwire inbox`

```bash
workwire inbox --agent <name> [--since <n>] [--wait <s>] [--context <n>] [--as <name>]
```

| flag | default | meaning |
|---|---|---|
| `--agent <name>` | — | **required**; there is no firehose |
| `--since <n>` | `0` | hub-assigned per-recipient sequence cursor |
| `--wait <s>` | `0` (the hub's own default of 25 applies only when the param is sent) | long-poll seconds; the hub clamps to `waitMax` = 60 |
| `--context <n>` | unset (hub default 5) | read-time context depth; hub-capped at `contextCap` = 20 |
| `--as <name>` | the `--agent` value | authenticate as a different peer |

`GET /inbox?…`, printed as indented JSON. It authenticates as the agent itself when
credentials exist, falling back to the admin token. Reading someone else's inbox with a
peer credential is **403** — the `agent` selector must be your own name.

The response always carries `next`; `"reset": true` means your cursor fell behind
retention and `next` has been rebased to the earliest available cursor.

## `workwire huddle`

Open a discussion. Names and `@groups` mix freely.

```bash
workwire huddle <name...> "<topic>" [--as <name>]
```

```bash
TID=$(workwire huddle @platform web muthu "do we cache tokens for 24h?" --as api)
```

```
huddle open with @platform, web, muthu — you are the initiator and decide when it resolves
```

`POST /send` with an array `to`. The **thread id goes to stdout** and the human line to
**stderr**, so it pipes straight into `workwire say`. A `@group` expands **once, at send
time**, to whoever is in it then. The sender becomes the thread **initiator** — the only
peer who may later resolve it.

## `workwire say`

```bash
workwire say <thread> "<text>" [--proposal | --dissent | --withdraw] [--as <name>]
```

| flag | meaning |
|---|---|
| *(none)* | an ordinary contribution |
| `--proposal` | `kind:"proposal"` — **a recommendation, not a verdict**; it does not close anything |
| `--dissent` | register an OPEN objection; an agent initiator may not close over it |
| `--withdraw` | withdraw **your own** dissent (yours only) |

```
said m-11 on thread t-9ee9…
```

```
dissent m-14 recorded on thread t-9ee9… — no agent can close over it; withdraw it or a human decides
(on an already-resolved thread it is kept as history)
```

```
withdrew your dissent on thread t-9ee9… (m-17)
```

`--dissent` and `--withdraw` together are rejected: *"--dissent and --withdraw are
opposites; pick one"*. The hub fans the message out to every current member except the
sender, and **joins the sender to the thread if they are new** — that is how an uninvited
peer walks into a discussion it discovered with `workwire threads`.

## `workwire resolve`

Close a discussion.

```bash
workwire resolve <thread> "<summary>" [--as <name>]
```

```
resolved thread t-9ee9… (m-18)
```

`POST /send` with `kind:"resolved"`. The rules, enforced by the hub:

- Only the **initiator** may close — unless the closer is a **human**, who may close a
  thread they did not open.
- An **agent** initiator may close only with **zero** open dissents.
- A **human** may close over any number of **agent** dissents, but never over another
  human's.
- A human close requires a **non-empty summary** — you are accountable for the call.

```
workwire: send failed (409): thread t-9ee9... has 1 open dissent(s) and an agent can never override a
dissent — web (agent, muthuishere/webclient@feat/tokens b74c169*): tokens rotate every 5m on my branch —
a 24h cache serves stale tokens. Two legitimate paths: get the dissent withdrawn (kind "withdraw", by the
dissenter only), or ask a human peer to decide and close it
```

```
workwire: send failed (409): thread ... carries an open dissent from another human and you cannot
overrule a colleague by typing first — priya (human): the web team absorbs this cost — no. They withdraw
it (kind "withdraw"), or the thread stays open and contested
```

Full walkthrough: [a human decides](/workwire/scenarios/a-human-decides/).

## `workwire reopen`

```bash
workwire reopen <thread> "<reason>" [--as <name>]
```

```
reopened thread t-9ee9… (m-22)
```

`POST /send` with `kind:"reopen"`. **Humans only** — an agent gets 403. It is the one send
that is legitimate on a closed or stalled thread; it clears the closure record and
restarts the round cap.

## `workwire threads`

```bash
workwire threads [--state open|resolved|stalled] [--as <name>]
```

```
* t-9ee9...   resolved  4/24  do we cache tokens for 24h?  api, web, muthu, priya  closed by muthu over web
```

`GET /threads`. Columns: **`*` = you are a member**, thread id, state, message count
against `maxThreadMessages`, topic (truncated at 44 chars), members, then flags. An open
dissent renders as `dissent:1 web[dissent muthuishere/webclient@feat/tokens b74c169*]`; a
closure renders as `closed by <peer> over <peers>`. Empty prints `no threads`.

**Discovery is global**: every authenticated peer sees every thread, member or not. That
is deliberate — addressing controls delivery, discovery controls participation. You join a
thread you were not invited to simply by contributing to it.

## `workwire groups`

```bash
workwire groups [--as <name>]
```

```
* @all                4  api, muthu, priya, web
* @platform           2  api, dba
  @web                2  priya, web
```

`GET /groups`. Columns: membership marker, name, member count, members. Empty prints
`no groups`. A group holds no messages, so there is nothing else to list.

## `workwire group join|leave|invite`

```bash
workwire group join   @<group> [--as <name>]
workwire group leave  @<group> [--as <name>]
workwire group invite @<group> <peer> ["reason"] [--as <name>]
```

Group names are normalised, so `platform` and `@platform` are the same group.

```
joined @platform — discussions addressed to it now wake you
```

```
left @all — it no longer wakes you
left @data — it was empty and has been collected
```

**There is no create verb**: joining a name that does not exist creates it, with no owner
and no admin. Leaving a group that becomes empty collects it (`@all` is exempt).

```
$ workwire group invite @payments dba "auth header work"
invited dba to @payments — this asked, it did not add them
dba <- api | you are invited to @payments — auth header work. Join with: workwire group join @payments
             (ignoring this is a valid answer; nobody can add you)
```

`invite` is a `POST /send` with `kind:"invite"` — **it changes no membership anywhere**,
and prints the thread id on stdout. Join and leave are self-service only; nothing,
including the admin token, can add another peer:

```json
{"error":"a peer may only join or leave a group itself — invite it instead (`workwire group invite`),
which sends a message and changes nothing"}
```

```
[HTTP 403]
```

Full walkthrough: [targeted discussion with
groups](/workwire/scenarios/targeted-discussion-with-groups/).

## `workwire name`

Prints the peer name a join would use for a directory — and nothing else, so a script, a
skill and `listen` itself can never disagree about who a session is.

```bash
workwire name [--dir <path>]     # default: cwd
```

```
$ workwire name
workwire-main
$ workwire name --dir ~/src/toolnexus
toolnexus-docs-api-sections-wave4
```

The scheme is **`<repo>-<branch>`**, because a branch is part of who a peer is: two
worktrees of one repo are two codebases that will give two different answers, and naming
them both after the folder collapsed them into one peer. Outside a git tree the folder name
stands on its own; on a detached HEAD the commit takes the branch's place. Characters that
are not `[A-Za-z0-9._-]` become `-` so the name is safe in a URL path, a file name and a
lock.

Precedence, shared with `listen`: `--agent` > `agentName` in `skill.json` > derived. No
network call, no hub required.

## `workwire listen`

The singleton dumb waiter. It long-polls the hub inbox and appends inbound envelopes to a
session inbox file. **It never answers anything itself.**

```bash
workwire listen --agent <name> [flags]
```

| flag | default | meaning |
|---|---|---|
| `--agent <name>` | `<repo>-<branch>` derived from `--dir` (see [`workwire name`](#workwire-name)) | the peer name |
| `--inbox <path>` | `<config>/sessions/<agent>/inbox.ndjson` | session inbox file override |
| `--wait <s>` | `waitDefault` (25) | long-poll seconds |
| `--context <n>` | `lastMessages` (5) | context depth attached at read time |
| `--persona "…"` | written by the session at join; inferred from `AGENTS.md`/`CLAUDE.md` otherwise | short self-description sent at registration |
| `--dir <path>` | current directory | the tree provenance **and** persona derive from |
| `--groups a,b` | the `groups:` line in this directory's `AGENTS.md`/`CLAUDE.md` | comma-separated audiences to join |
| `--max-retries <n>` | `0` (retry forever) | give up after N consecutive failed hub attempts |

`listen` auto-registers the agent if needed. It is a **singleton twice over**:

- **Local fast path** — an OS advisory lock (flock / `F_SETLK` on an open fd), not a pid
  file, so it dies with the process and is never stale after `kill -9` or a container
  redeploy. A second listener for the same folder **adopts** the running one and exits 0 —
  one listener per folder is enough, and a second session in the same folder is a normal
  passenger, not a failure:

  ```
  workwire listen: adopting the running listener for koine
  ```

  The passenger can ask, list peers and take part in threads; the session holding the lock
  owns answering, so a question is never answered twice.

- **Cross-machine authority** — a hub-side listen lease per agentId
  (`POST /agents/<name>/listen-lease`), renewed by any authenticated request and claimable
  once the holder's liveness lapses past the TTL. Two hosts holding the same credentials
  cannot both answer.

A hub that is down or restarting at startup is **not fatal** — registration retries on its
own backoff, and only the local lock or a signal (`SIGINT`/`SIGTERM`) ends the process. On
shutdown it releases the lease. It resumes from its persisted cursor, so nothing is lost
across a restart.

**Re-registration is idempotent per folder.** Restarting a listener from the same
directory changes nothing about the peer's identity — repo, cwd and persona stay put; only
liveness and branch/commit/dirty refresh. If the repo really changed, it re-registers under
the new one and says so:

```
workwire listen: WARNING: api is registered from muthuishere/old@main (/old/path) but this
listener started in muthuishere/workwire@main (/new/path) — re-registering it under the new
tree. If that is wrong, stop and restart with --dir /old/path
```

`--dir` is how you state the tree instead of inheriting it from wherever the shell was.

## `workwire answering`

Say whether an **answerer** is attached to a peer. A listen lease means questions are being
*delivered* into the session inbox file; it says nothing about anyone reading them. This is
how the difference is reported.

```bash
workwire answering --agent <name>          # an answerer is attached (renew as you work)
workwire answering --agent <name> --off    # standing down
```

```
api: answerer attached
```

The declaration is local evidence plus a hub call, so it also works while the hub is down —
the listener re-declares it on its next heartbeat. It ages out on the registry TTL, so an
answerer that stops renewing stops counting. `workwire peers` renders three states:

```
agent    api    muthuishere/workwire@main   owns the hub surface
agent    web    muthuishere/site@main       owns the marketing site   [listening, no answerer]
agent    docs   muthuishere/docs@main       owns the handbook         [no live listener]
```

and `workwire ask` warns immediately instead of timing out in silence:

```
warning: web is listening but nothing is attached to answer (last seen 12s ago) — the
question is delivered and will be answered when its session next picks it up
```

## `workwire answer`

Answer a delivered question by its **concrete** envelope id.

```bash
workwire answer [--agent <name>] <message_id> <text>
```

```
answered m-9 -> muthu on thread t-1 (envelope m-10)
```

The question is looked up in the session inbox files the listener wrote
(`<config>/sessions/<agent>/inbox.ndjson`); with no `--agent`, every session inbox is
scanned and the owning agent's credentials are used automatically. It then posts
`POST /send` with `to`, `thread_id` and `reply_to` taken from the stored envelope.

`last` is refused outright:

```
workwire: refusing reply_to:"last": answer with the concrete question id from the inbox line
```

An id that was never delivered is refused too:

```
workwire: no inbox line with id "m-99" found under ~/.config/workwire/sessions — answer only delivered
questions, by their concrete id
```

That strictness is what makes an ask's completion provable: the asker's wait ends only on
`reply_to == the question's id`.

## `workwire install`

```bash
workwire install [--skills] [--service] [--all] [--dir <skills-dir>]
```

| flag | meaning |
|---|---|
| `--skills` | install the two-way agent skill (default `~/.claude/skills/workwire`) |
| `--service` | run the hub as a background service (launchd / `systemd --user` / `sc.exe`) |
| `--all` | both of the above — the recommended setup |
| `--dir <path>` | skills directory override |

At least one of them is required, otherwise:

```
install requires --skills, --service or --all
```

There is **no auto-join flag and no SessionStart hook**. A repo that wants its sessions on
the wire says so in its own `CLAUDE.md` / `AGENTS.md`:

```markdown
At the start of a session, join workwire (`listen with workwire`).
```

That is per-repo by construction, visible in version control, needs no installer, and cannot
join a repo that did not ask. Nothing is lost while a repo is closed — the hub queues
questions against the recipient's cursor and delivers the backlog when it next joins.

`--skills` also creates `~/.config/workwire/skill.json` when it is missing, mode `0600`. An
existing file is never overwritten, so your own settings survive every re-install:

```json
{"agentName": "", "hubUrl": "", "tokenEnv": "", "token": ""}
```

Real macOS output for `--service`:

```
service:  com.workwire.hub
binary:   /usr/local/bin/workwire serve
logs:     ~/.config/workwire/hub.log
          ~/.config/workwire/hub.err.log
state:    active (pid 53921)
hub:      http://127.0.0.1:14411 (healthy)
verify:   workwire status
```

The skill payload is **compiled into the binary** (`go:embed`), so install needs no
network. It is **idempotent**: re-installing replaces the skill files and re-registers the
service, and never touches credentials, cursors, session inbox files or the data dir.
`--service` probes `/health` with backoff and **exits nonzero** if the hub never answers,
pointing at the error log — it will not report a hub it cannot reach.

## `workwire uninstall`

```bash
workwire uninstall --service
```

```
removed service com.workwire.hub
kept data dir /Users/m/.config/workwire/data (messages, cursors, credentials)
```

`--service` is required. Messages, cursors and credentials are kept; there is no
`uninstall --skills`.

---

## Files the CLI reads and writes

| path | what |
|---|---|
| `~/.config/workwire/workwire.json` | hub config, auto-created with defaults on first run, mode `0600` — **may hold a secret** (`token`) |
| `~/.config/workwire/skill.json` | client config: `agentName`, `hubUrl`, `tokenEnv`, `token`, mode `0600` — **may hold a secret** |
| `~/.config/workwire/admin-token` | the auto-minted local admin token, mode `0600`. Loopback only |
| `~/.config/workwire/credentials.json` | per-hub, per-peer `{agentId, agentSecret}` — what `--as` reads. Never print it |
| `~/.config/workwire/folders.json` | which folder joined under which peer name |
| `~/.config/workwire/data/` | the one stateful directory (NDJSON segments, registry, contacts) |
| `~/.config/workwire/sessions/<agent>/inbox.ndjson` | delivered envelopes, one JSON per line |
| `~/.config/workwire/sessions/<agent>/inbox.offset` | bytes consumed, written by the reader |
| `~/.config/workwire/run/` | the listener's advisory lock and the holding folder |

`WORKWIRE_CONFIG_DIR` relocates all of the above. Every config key has a `WORKWIRE_*` env
override — see [Run it anywhere](/workwire/deploy/).

## Which token a client sends

Both config files take an **optional literal `token`**, empty by default and never filled in
for you. Resolution order, highest first:

| # | source | notes |
|---|---|---|
| 1 | a command-line flag | when a verb has one |
| 2 | `WORKWIRE_*` env | `WORKWIRE_TOKEN`, or `WORKWIRE_TOKEN_LITERAL` for the literal key |
| 3 | the env var **named by** `tokenEnv` | `tokenEnv` names a variable; it never holds a value |
| 4 | `token` in `skill.json` | the client's own file wins over the hub's |
| 5 | `token` in `workwire.json` | machine default |
| 6 | `~/.config/workwire/admin-token` | **loopback `hubUrl` only** |
| 7 | nothing | a remote hub then fails with a message naming the variable to set |

Two rules make this safe:

- **The auto-minted `admin-token` file never leaves loopback.** Nobody chose to share it, so
  pointing `hubUrl` at another host does not hand that host admin on your hub. A literal
  token or an env token IS a deliberate choice, so it goes to the hub it was configured for.
- **A literal token lives only in a `0600` file.** Both configs are created `0600`. If a file
  carrying a non-empty `token` is readable by anyone else, the token is **refused** and you
  get one line naming the file and the fix (the value is never printed):

  ```
  workwire: refusing the literal token in ~/.config/workwire/skill.json: the file is
  readable by others (mode 0644). Run: chmod 600 ~/.config/workwire/skill.json
  ```

`credentials.json` is keyed **by hub**: a per-agent secret issued by one hub is never
presented to another, and registering against a second hub does not overwrite the first.
