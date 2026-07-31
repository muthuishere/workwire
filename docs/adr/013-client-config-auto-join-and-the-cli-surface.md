# ADR-013: a client config, join-by-default, and a noun-verb CLI that reaches a remote hub

Status: accepted · Date: 2026-07-31

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
{ "autoJoin": true, "agentName": "", "hubUrl": "", "tokenEnv": "" }
```

Empty `agentName` means "derive from the folder". `workwire.json` continues to configure
the hub and is not touched. A client and a hub are different things and now have different
files; the old rule holds — **dynamic state is never config** (ADR-001), and this file holds
only preferences.

### 2. Join by default

Auto-join is **on** out of the box. A skill cannot start itself — it waits for a trigger
phrase — so joining is a **SessionStart hook**, and the hook is one word: `workwire
session-start`. That verb reads `skill.json`, does nothing if `autoJoin` is false, and
otherwise starts the detached listener for the current folder.

Non-negotiable properties, because this now runs on **every** session the user opens:
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

### 3. A noun-verb CLI

Three nouns, because there are three objects:

```
workwire server  install | uninstall | start | status     # the hub process
workwire skills  install | uninstall | --on | --off       # the agent skill + client config
                 install --server-url <url> [--token-env NAME]
workwire session-start                                     # the hook entrypoint
```

Existing flag forms (`install --service`, `install --skills`, `uninstall --service`) remain
as **aliases** — they are in a published release and in the docs; breaking them to tidy a
verb list would be self-indulgent.

### 4. The remote hub, client side only

`workwire skills install --server-url https://hub.example.com` writes `hubUrl` into
`skill.json`, and every client verb honours it. That is the whole client half of "join a
hub somewhere else", and it costs almost nothing now because the rules already exist:
a remote `hubUrl` is **never auto-started**, only probed (ADR-001), and the listener already
retries a hub that is unreachable or restarting.

What is **not** claimed by this ADR: the server half. Join tokens, workspaces, per-tenant
thread visibility, rate limits and real (non-self-declared) peer identity all remain
deferred to **ADR-010**, and a hub exposed today still fails closed (ADR-007). Pointing a
client at a remote URL therefore works, and is only as safe as the hub it points at — which
today means a trusted network. The docs must not imply otherwise.

## Consequences

- Presence becomes the default and the mesh is populated without anyone remembering to join.
- The blast radius of a bad auto-join grows with the number of repos a person opens, which is
  why `session-start` is a first-class, tested CLI verb rather than shell embedded in JSON.
- Every session on the machine can be woken by `@all` traffic. This is a real token cost and
  the reason group membership is a dial rather than decoration.
- The client/hub config split means a future hosted deployment changes one file, not the CLI.
- Three nouns keep the surface legible as it grows; aliases keep v0.1.0 users working.
