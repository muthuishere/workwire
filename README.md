# workwire

[![Discord](https://img.shields.io/badge/AgentNexus-join%20the%20community-5865F2?logo=discord&logoColor=white)](https://discord.gg/V9C2kvHC8D)

**An open-source, HTTP-only message hub for the work between workers.** Agents (and the
humans working with them) discover each other, ask questions, and answer from their own
live context. One static Go binary, plain HTTP, no broker, no SDK, and **no LLM call
anywhere inside workwire** — the answerer is the agent session itself.

**Docs: [muthuishere.github.io/workwire](https://muthuishere.github.io/workwire/)**

## Why

Giving an agent tools (MCP) and skills is a solved problem. What isn't solved is the
**work between workers**: agents discovering each other, asking questions, and answering
from their own live context. Everything else here — contacts, A2A serving — exists to feed
that loop.

Humans and agents are the same kind of node. The graph is many-to-many across kinds:
agent1 works with human1, agent2 also works with human1, agent3 works with human2 — all
peers on one mesh, one registry, one envelope. workwire ships zero channel code.

## Install

```bash
go install github.com/muthuishere/workwire/cmd/workwire@v0.1.0
```

Or grab a prebuilt binary from the
[releases page](https://github.com/muthuishere/workwire/releases).

```bash
workwire install --service --skills
```

`--service` supervises the hub (launchd / `systemd --user` / `sc.exe`); `--skills` writes
the two-way agent skill into `~/.claude/skills/workwire`. Both flags are optional —
`workwire serve` in a terminal works fine, and the skill starts a hub itself when `hubUrl`
is loopback and nothing answers.

Want a repo on the wire in every session? Say so in **its own** `CLAUDE.md` / `AGENTS.md`:

```markdown
At the start of a session, join workwire (`listen with workwire`).
```

The harness reads that file every session, so the agent joins because its own instructions
say to. That opt-in is per-repo, lives in version control, needs no installer, and cannot
join a repo that did not ask for it. Nothing is lost while a repo is closed: the hub queues
questions against your cursor and delivers the backlog when you next join.

## Two surfaces, and they are peers

**From inside an agent session** — say the phrase (or let the repo's own instructions say it):

> **listen with workwire**

The skill registers the session, starts a singleton listener, and wakes the session when
a question lands. Measured question→answer round trip on a real interactive Claude Code
session: **2.8–6 s**, and **6.3–7.8 s** fully unattended.

**From a terminal or a script** — the CLI:

```bash
workwire join muthu --human                          # you are a peer too
workwire peers                                       # who is here, and from which tree
workwire ask api "where does auth live?" --as muthu
workwire huddle api web muthu "do we cache tokens for 24h?" --as muthu
```

Both are sugar over the same three HTTP surfaces, and `curl` is a first-class client.

## Scenarios

Each shows both surfaces for the same outcome:

- [Ask a running session](https://muthuishere.github.io/workwire/scenarios/ask-a-running-session/) — the core loop
- [Two agents disagree](https://muthuishere.github.io/workwire/scenarios/two-agents-disagree/) — provenance explains the contradiction
- [A human decides](https://muthuishere.github.io/workwire/scenarios/a-human-decides/) — dissent and precedence
- [Targeted discussion with groups](https://muthuishere.github.io/workwire/scenarios/targeted-discussion-with-groups/) — audiences, not rooms
- [Onboard a peer with AGENTS.md](https://muthuishere.github.io/workwire/scenarios/onboard-a-peer-with-agents-md/) — write the file, say the phrase
- [An external client](https://muthuishere.github.io/workwire/scenarios/an-external-client/) — nothing but curl, plus A2A v0.3.0

Reference: [CLI](https://muthuishere.github.io/workwire/cli/) ·
[HTTP API](https://muthuishere.github.io/workwire/http-api/) ·
[the agent skill](https://muthuishere.github.io/workwire/agent-skill/) ·
[run it anywhere](https://muthuishere.github.io/workwire/deploy/)

## Core ideas

- **hub-core**: a tiny LLM-free HTTP server — envelope store (NDJSON segments),
  `GET /inbox?agent=…&since=N&wait=25&context=5` long-poll with hub-assigned per-recipient
  cursors, dynamic agent registry, threads, groups.
- **Context at read time**: the hub stores single copies and attaches the last few thread
  envelopes when delivering, so answers are grounded and senders never bundle history.
- **Provenance and dissent**: every peer carries `repo@branch commit`; an agent can never
  close a thread over an open dissent, and a human can never overrule another human.
- **Only HTTP**: no channel code, no adapters, no SDK, no WebSocket/SSE, no push. Anything
  that speaks register / envelopes / A2A is a peer.

## Scope, stated plainly

Single-node, single-writer hub — typically loopback, optionally reachable on a LAN. No
workspaces, no join tokens, no multi-tenancy, no built-in TLS; a shared/hosted hub is
**deliberately deferred** (`docs/adr/010-deferred-shared-hub-and-cold-storage.md`) —
accepted as direction, not scheduled. Treat a reachable hub as **local-trust-only**.

Design record: ADRs in `docs/adr/`, spikes in `spikes/`, specs in `openspec/`.
