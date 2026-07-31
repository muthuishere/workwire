# ADR-013: a client config, a noun-verb CLI, and a hub somewhere else

Status: partially implemented (§2 REJECTED) · Date: 2026-07-31 · Amended: 2026-07-31

## Implementation status

This ADR is a mix of shipped code and stated direction. Honestly, today:

- **§1 client config** — **implemented**. `~/.config/workwire/skill.json` exists, is created
  once by the skills install, is never overwritten, and `agentName` / `hubUrl` / `tokenEnv`
  are honoured.
  Precedence is now stated and resolved in one place: **flag > `WORKWIRE_*` env >
  `skill.json` > `workwire.json` > defaults**. Both config files also take an optional
  literal `token`, empty by default, `0600` enforced.
- **§2 join by default** — **REJECTED and REMOVED** (see below). There is no
  `workwire session-start`, no SessionStart hook, no `--auto` / `--on` / `--off` and no
  `autoJoin` key. A repo opts in through its own `CLAUDE.md` / `AGENTS.md`.
- **§3 noun-verb CLI** (`workwire server …` / `workwire skills …`) — **NOT implemented**. The
  flag forms (`install --service` / `--skills` / `--all`) are the only surface today; there
  are no nouns to alias yet.
- **§4 remote hub** — **client half only**. A client can be pointed at a remote `hubUrl`, but
  there is **no server-side identity** (deferred to ADR-010), so a remote hub is only as safe
  as the network it sits on. There is no `--server-url` flag; set `hubUrl` in `skill.json`
  or `WORKWIRE_HUB_URL`.

## Context

Three pressures arrived together.

**Joining is still a ritual.** A session only joins when someone types *listen with workwire*.
A mesh you must remember to enter is usually empty, which defeats the point.

**The CLI is turning into flag soup.** `workwire install --service --skills --auto --on`
conflates three unrelated objects — the hub process, the agent skill, and a preference —
behind one verb and a pile of adverbs.

**Nothing holds client-side state.** `workwire.json` configures *the hub*. A session that
should talk to a hub somewhere else has nowhere to record that, so a remote hub is
currently a per-command env var rather than a setting.

## Decision

### 1. A client config, separate from the hub config

`~/.config/workwire/skill.json` — created by the skills install when absent, **never
overwritten** on re-install:

```json
{ "agentName": "", "hubUrl": "", "tokenEnv": "", "token": "" }
```

Empty `agentName` means "derive from the folder". `tokenEnv` NAMES an env var; `token` is an
optional literal, empty by default and never auto-populated — a file carrying one must be
`0600` or it is refused. `workwire.json` continues to configure the hub (and takes the same
optional `token`) and is not touched. A client and a hub are different things and now have different
files; the old rule holds — **dynamic state is never config** (ADR-001), and this file holds
only preferences.

### 2. Join without a ritual — REJECTED, and removed

**Superseded on 2026-07-31. Auto-join is deleted: no `session-start` verb, no SessionStart
hook, no `--auto` / `--on` / `--off`, no `autoJoin` key.** What follows is why, kept because
the reasoning is the decision.

**Auto-join never bought delivery.** The hub queues every message against the recipient's own
sequence cursor (hub-core R5). A peer that is away loses nothing: it drains its backlog the
moment it next joins. So the only thing a SessionStart hook bought was *presence* — a peer
being listed a little earlier.

**And presence was expensive.** A hook in the harness settings of every repo the user opens; a
hub identity per folder; personas derived and published from `CLAUDE.md` / `AGENTS.md` files
that may be confidential; same-basename collisions (`src/api` vs `other/api`) turned from a
thing you can hit into a thing that happens automatically; and every joined session in `@all`,
so a broad discussion wakes all of them. That is a real blast radius bought with no capability.

**The replacement is a documented pattern, not code.** A repo that wants its sessions on the
wire says so in its own instruction file, beside its `## workwire` block:

> At the start of a session, join workwire (`listen with workwire`).

The harness already loads that file every session, so the agent joins because its own
instructions say to. This is better than a hook on every axis that mattered: it is **per-repo
by construction**, it is **visible in version control** where a reviewer can see it, it needs
**no installer and no settings merge**, and it **cannot join a repo nobody opted in**.

What survives from the auto-join work, because `workwire listen` needed it anyway: the
folder→name binding (`folders.json`) and the lock-holder record, which make a same-basename
collision an explicit, reported conflict instead of two folders sharing one identity.

### 3. A noun-verb CLI — DIRECTION, not yet built

**None of this section exists in the code today.** It is the shape we intend, recorded so the
flag surface does not drift further; the flag forms remain the only way to drive workwire.

Three nouns, because there are three objects:

```
workwire server  install | uninstall | start | status     # the hub process
workwire skills  install | uninstall                      # the agent skill + client config
                 install --server-url <url> [--token-env NAME]
```

When the nouns land, the existing flag forms (`install --service`, `install --skills`,
`uninstall --service`) stay as **aliases** — they are in a published release and in the docs;
breaking them to tidy a verb list would be self-indulgent.

### 4. The remote hub — client half only, and only as safe as its network

The intended surface is `workwire skills install --server-url https://hub.example.com`
writing `hubUrl` into `skill.json`. **That flag does not exist yet**; today you set `hubUrl`
in `skill.json` by hand or export `WORKWIRE_HUB_URL`, and every client verb honours it. That
is the whole client half of "join a hub somewhere else", and it costs almost nothing now
because the rules already exist:
a remote `hubUrl` is **never auto-started**, only probed (ADR-001), and the listener already
retries a hub that is unreachable or restarting.

What is **not** claimed by this ADR: the server half. Join tokens, workspaces, per-tenant
thread visibility, rate limits and real (non-self-declared) peer identity all remain
deferred to **ADR-010**, and a hub exposed today still fails closed (ADR-007). Pointing a
client at a remote URL therefore works, and is only as safe as the hub it points at — which
today means a trusted network. The docs must not imply otherwise.

## Consequences

- Joining stays a deliberate act: a phrase in a session, or a line the repo itself carries in
  `CLAUDE.md` / `AGENTS.md`. The default install joins nothing and installs no hook, and no
  repo can be joined that did not ask.
- Nothing is lost by having no hook: queued questions are delivered from the recipient's
  cursor when it next joins, so "away" costs latency, never messages.
- `@all` still wakes every peer that is actually on the wire, but the population of that set is
  now something a person chose per repo rather than a machine-wide switch.
- A same-basename collision is reported (with a free name to use) instead of two folders
  sharing one peer identity.
- The client/hub config split means a future hosted deployment changes one file, not the CLI.
  Both files may now hold a literal `token`, which makes them secret-bearing: they are created
  `0600`, a token in a file others can read is refused with a warning, and no token value is
  ever printed.
- Three nouns would keep the surface legible as it grows; aliases would keep v0.1.0 users
  working — both when §3 is actually built.
