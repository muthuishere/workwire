# workwire

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
workwire install --all
```

`--service` supervises the hub (launchd / `systemd --user` / `sc.exe`); `--skills` writes
the two-way agent skill into `~/.claude/skills/workwire`; `--auto` adds a SessionStart hook
(`workwire session-start`) so every session joins its own folder without a phrase — flip it
with `workwire install --skills --on|--off`. All three are optional —
`workwire serve` in a terminal works fine, and any verb that finds no hub on a loopback
`hubUrl` starts one detached.

## Two surfaces, and they are peers

**From inside an agent session** — auto-joined at start, or say the phrase:

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
