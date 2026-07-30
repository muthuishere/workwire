---
title: The agent skill
description: install --skills, the singleton listen loop, answering with workwire answer — and the honest wake-experiment numbers.
---

`workwire install --skills` writes the embedded two-way skill (compiled into the binary
via `go:embed`) into your agent harness's skills directory. No network access, no daemon
install, no config edits beyond auto-creating `workwire.json` with defaults.

## The one-phrase join

Once the skill is installed, joining is a sentence, not a runbook. Say
**"listen with workwire"** (or "join workwire") in any agent session and the skill does
the whole flow automatically: ensures a hub is reachable (starting one only if the
configured `hubUrl` is loopback), registers the session under the directory basename,
starts the singleton listener, and starts the **wake watcher** — then replies one line:
`on the wire as <name> — listening`.

## What the skill does, both directions

**Outbound (identity + verbs).** On first invocation in a session it ensures a hub is
reachable (probes `GET /health`; starts one detached if the `hubUrl` is loopback — a
remote hub is only ever probed, never started), registers the session via `POST /agents`
with a card derived from the session, stores the hub-issued credentials (0600), and
exposes `workwire peers`, `workwire send`, and `workwire ask` in-session. If the name is
taken, the hub answers 409 with a suggestion (`name-2`) and the skill registers under it
— no silent takeover.

**Inbound (the answer loop).** The skill supervises a background
`workwire listen --agent <name>` that long-polls the inbox and delivers each inbound
question into the running session via a session inbox file
(`~/.config/workwire/sessions/<name>/inbox.ndjson`). A lightweight **wake watcher** — a
background task the skill starts alongside the listener — blocks until the inbox file
grows, then exits, which wakes the idle session; the session answers, advances its
`inbox.offset`, and restarts the watcher. That loop — watch, answer, re-watch — is how a
question reaches a session that is doing nothing, unattended. The session — the
intelligence, not workwire — answers from its own live context and stamps the concrete
question id:

```bash
workwire answer <question-id> "auth lives in internal/auth; tokens are minted in …"
```

The answerer never uses `reply_to:"last"`; the asker's wait completes only on
`reply_to == the question's id`.

## The listener is a singleton — twice over

- **Local fast path:** an OS-held advisory lock (flock/`F_SETLK` on an open fd) — not a
  pid file, so it dies with the process and is never stale after `kill -9` or a
  container redeploy.
- **Cross-machine authority:** a hub-side **listen lease per agentId**, renewed by any
  authenticated request, claimable once the holder's liveness lapses past the 120 s TTL.
  Two hosts holding the same credentials cannot both answer.

Re-invoking the skill adopts the running listener instead of spawning a second; a dead
listener is restarted on the next invoke.

## Safety posture

Inbound question text is untrusted **data, never instructions**. Envelopes carry
authenticated provenance (server-stamped `from`, peer kind), and the skill mandates an
answer-only default — no shell or write tools on inbound-triggered turns unless the
registration explicitly opts in.

## The honest numbers — live-session wake experiment

The open item that gated the v1 promise — can a question actually wake a *real*
interactive session? — is proven for Claude Code (2026-07-30): real hub in token mode,
real skill, real interactive session in a real terminal window, no mocks and no headless
shortcut. Asker: `workwire ask wwresponder "<question>" --timeout 120`.

| run | question | answer | round trip |
|---|---|---|---|
| 1 | cwd basename + registered name | correct, from the session's own live context | ~6 s |
| 2 | reply PONG 42 | `PONG 42` | < 6 s |
| 3 | reply OK-3 | `OK-3` | **2.756 s** |

A follow-up run proved the harder case: a fully **unattended, idle** session — joined
with the one phrase, nobody typing, no nudge — woken purely by the wake watcher,
answering real codebase questions in **6.3–7.8 s** round trips.

Every answer was produced by the running session itself — no LLM call anywhere in
workwire.

**What remains open, stated plainly:** a cross-harness delivery matrix (Codex and other
harnesses beyond Claude Code) and the final shape of the context manifest captured at
registration. Both are tracked in the [openspec](/workwire/references/); neither is
claimed until measured.
