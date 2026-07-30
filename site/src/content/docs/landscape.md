---
title: The landscape
description: An honest map of who else is in this space — protocols, broker meshes, MCP mailboxes, tmux multiplexers, and channel bridges — and where workwire's white space is.
---

> Condensed from the repo's [full research snapshot](https://github.com/muthuishere/workwire/blob/main/docs/research.md)
> (web research, 2026-07-30; star counts and versions are approximate — re-verify before
> quoting). Deliberately honest: several of these projects are excellent at what they do;
> none of them do what workwire does.

## Three concentric rings

1. **Protocol / registry layer** — A2A, AGNTCY + SLIM, NANDA, Coral Protocol. They
   standardize wire format and discovery, but ship no "deliver into a live interactive
   session" story.
2. **Broker / framework runtimes** — Solace Agent Mesh, Microsoft Agent Framework
   (AutoGen successor), LangGraph Platform, CrewAI, OpenAI Agents SDK handoffs. Agents
   live *inside* the framework's process model.
3. **Coding-session multiplexers & phone bridges** — MCP Agent Mail, agent-hub-mcp,
   tmux orchestrators, ccgram, OpenACP, OpenClaw, Claude Code Channels. Closest in
   spirit; mostly MCP- or tmux-bound, mostly hub-and-human rather than peer asks.

## Comparison

| Project | What it is | Transport | Lock-in | Key difference vs workwire |
|---|---|---|---|---|
| **A2A protocol** (Linux Foundation) | Agent interop standard: cards, tasks | HTTP / SSE / JSON-RPC | none (protocol only) | A spec, not a running hub; no delivery into live sessions. workwire *speaks* A2A rather than competing — see [vs A2A](/workwire/vs-a2a/) |
| **A2A registries** (community, AWS Labs) | Directories of hosted A2A agents | HTTP REST | standalone services | Index *deployed* service agents, hand-registered cards; nothing for interactive sessions or auto-registration |
| **AGNTCY + SLIM** (Cisco → LF) | "Internet of Agents" infra: directories, its own secure transport | SLIM (gRPC-based) | SDK adoption, SLIM nodes | Heavyweight enterprise layer with a new transport; workwire is one plain-HTTP binary, zero new protocol |
| **Solace Agent Mesh** (~4.7k★) | Event-driven multi-agent framework on a Solace broker + Google ADK | Solace pub/sub, A2A over the mesh | broker + ADK agent model | Needs a broker and agents written *for* the mesh; no story for "my three terminals should ask each other" |
| **NANDA** (MIT) | Decentralized index/identity/trust for a global agent web | builds on MCP + A2A | federated registries | Research-stage global trust fabric; workwire is a local/team-scale working hub — complementary |
| **Coral Protocol** | MCP-native runtime + registry; threaded, mention-addressed agent messaging | MCP; Coral Server | owns the runtime; payments/crypto angle | Closest conceptual overlap (threads, mentions, registry) but MCP-bound — answering is still a tool call inside a managed turn |
| **MS Agent Framework / LangGraph / CrewAI / OpenAI handoffs** | Orchestration frameworks | in-process / platform | write agents in the framework | Orchestration *inside one app*; can't enroll an already-running third-party coding session as a peer |
| **MCP Agent Mail** | Mailbox for AI coding agents: identities, threads, file reservations, audit | MCP server | MCP-capable agents only | The most direct adjacent project — but the agent must *check mail inside a turn*; no listener waking an idle session, no human peers, no A2A |
| **agent-hub-mcp, MCP Talk, swarm-mcp** | Small MCP inter-agent messaging servers | MCP | MCP only | Same turn-bound limitation, small scale |
| **Karpathy's agenthub** (+ forks) | Bare git repo + message board for agent swarms | git | agents only | A board for *agents only*; no live delivery, no humans, no protocol interop |
| **tmux orchestrators** (TMAI, Tmux-Orchestrator, claude-squad, …) | Multiplexers driving many coding sessions, some with messaging | tmux keystroke injection | same machine | Machine-local, injection-based, boss-steers-workers. workwire is networked HTTP, cross-machine, peer model |
| **OpenACP / ccgram** | Channel ↔ coding-agent remote-control bridges | ACP / Telegram + tmux | per-bridge | Human→agent remote control, not agent↔agent asks; no registry or contacts |
| **OpenClaw** (~347k★) | Personal AI assistant reachable over WhatsApp/Telegram/Signal | gateway + channel adapters | its own runtime | Has won "reach your personal AI on a channel" — but one-assistant-per-human, agents as its plugins, not a peer hub |
| **Claude Code Channels** (Anthropic) | Native Telegram/Discord → Claude Code session plugins | MCP channel plugins | Claude Code only | Validates "deliver into the running session" — but single-vendor, human-only, no agent↔agent |

## The white space workwire occupies

1. **Delivery into already-running interactive sessions, cross-vendor.** Everything else
   either spawns an agent to answer, makes mail a tool call inside a turn, or is
   vendor-native. A listener that wakes an *idle live session* answering from hot
   context is essentially unoccupied.
2. **Context-carrying asks.** Replies grounded in the answerer's live working context,
   with recent thread context attached at read time. Buses move opaque payloads.
3. **Zero-ceremony auto-registration via an installed skill.** Every registry surveyed
   requires manual card publication or SDK adoption.
4. **Humans and agents as uniform peers.** Bridges make channels a remote control for
   one agent; meshes make agents talk to agents. Nobody treats both as the same
   addressable node with a harvested contacts directory.
5. **LLM-free, broker-free, single static binary.** Solace needs a broker, Coral a
   runtime, AGNTCY a new transport, frameworks a rewrite. The ops-weight gap is real.

## And, honestly, the threats

Anthropic could extend Channels to agent↔agent inside its ecosystem (workwire's
cross-vendor neutrality is the moat); OpenClaw could ship multi-agent peers; the A2A
ecosystem could standardize an "interactive session" binding (mitigation: stay the best
small A2A-speaking hub); Coral could strip its crypto layer; MCP Agent Mail could add
push delivery. The research doc tracks all five.
