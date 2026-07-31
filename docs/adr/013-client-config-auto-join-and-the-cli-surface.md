# ADR-013: a client config, join-by-default, and a noun-verb CLI that reaches a remote hub

Status: partially implemented · Date: 2026-07-31

## Implementation status

This ADR is a mix of shipped code and stated direction. Honestly, today:

- **§1 client config** — **implemented**. `~/.config/workwire/skill.json` exists, is created
  once by the skills install, is never overwritten, and `agentName` / `hubUrl` / `tokenEnv`
  are honoured.
- **§2 join by default** — the **machinery is implemented** (`workwire session-start`, the
  SessionStart hook install/uninstall, `--on` / `--off`), but the **default is OFF**, pending
  the blast-radius review (`docs/stress-adr013-autojoin.md`): a peer name per folder,
  personas derived from possibly-confidential `CLAUDE.md` files, no per-repo opt-out, and
  same-basename collisions. Auto-join is enabled deliberately with `workwire install --auto`.
- **§3 noun-verb CLI** (`workwire server …` / `workwire skills …`) — **NOT implemented**. The
  flag forms (`install --service` / `--skills` / `--auto` / `--on` / `--off`) are the only
  surface today; there are no nouns to alias yet.
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
{ "autoJoin": false, "agentName": "", "hubUrl": "", "tokenEnv": "" }
```

Empty `agentName` means "derive from the folder". `workwire.json` continues to configure
the hub and is not touched. A client and a hub are different things and now have different
files; the old rule holds — **dynamic state is never config** (ADR-001), and this file holds
only preferences.

### 2. Join without a ritual — machinery shipped, default off

A skill cannot start itself — it waits for a trigger phrase — so joining is a **SessionStart
hook**, and the hook is one word: `workwire session-start`. That verb reads `skill.json`,
does nothing if `autoJoin` is false, and otherwise starts the detached listener for the
current folder.

The **machinery ships; the default is off.** The original decision here was "on out of the
box"; the blast radius (§ Implementation status) has not cleared review, so joining every
session in every folder is opted into deliberately with `workwire install --auto`, which
installs the hook, flips the key on, and prints what it costs. `workwire install --skills
--off` turns it back off instantly without removing the hook.

Non-negotiable properties, because when it is on this runs on **every** session the user
opens — all of these are implemented:
- It **always exits 0, immediately**. A missing config, a corrupt config, or a dead hub can
  never make a session fail to start.
- It is **silent when it has nothing to say**. Auto-join must be invisible when it works.
- **One listener per folder.** The flock already guarantees this; a second session in the
  same folder adopts quietly instead of erroring. That session is a **passenger** — it can
  ask, list peers and take part in threads, but the lock-holder owns answering, so a
  question is never answered twice.
- Toggling is instant and does not touch the hook or the skill file: `--on` / `--off` flip
  one key.

The cost is stated rather than hidden: an auto-joined session is in `@all` (ADR-012), so
broad discussions wake every joined session on the machine. `workwire group leave @all` is
the dial, and the docs say so.

### 3. A noun-verb CLI — DIRECTION, not yet built

**None of this section exists in the code today.** It is the shape we intend, recorded so the
flag surface does not drift further; the flag forms remain the only way to drive workwire.

Three nouns, because there are three objects:

```
workwire server  install | uninstall | start | status     # the hub process
workwire skills  install | uninstall | --on | --off       # the agent skill + client config
                 install --server-url <url> [--token-env NAME]
workwire session-start                                     # the hook entrypoint
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

- Presence is one command away (`workwire install --auto`) rather than automatic. The mesh is
  populated by people who chose it, and the default install joins nothing.
- The blast radius of a bad auto-join grows with the number of repos a person opens, which is
  why `session-start` is a first-class, tested CLI verb rather than shell embedded in JSON —
  and why the default stayed off until the review closes.
- Once auto-join is on, every session on the machine can be woken by `@all` traffic. This is a
  real token cost and the reason group membership is a dial rather than decoration.
- The client/hub config split means a future hosted deployment changes one file, not the CLI.
- Three nouns would keep the surface legible as it grows; aliases would keep v0.1.0 users
  working — both when §3 is actually built.
