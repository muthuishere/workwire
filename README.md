# agent-hub

An open-source, HTTP-only message hub for agents. Terminals, agents, and channel adapters are all peers: they auto-register, exchange context-carrying messages, and answer each other — no daemon ceremony, no broker.

**Positioning:** giving an agent tools (MCP) and skills is a solved problem. What isn't solved is the **work between workers** — agents discovering each other, asking questions, and answering from their own live context. That collaboration loop is the core of this platform; everything else (channels, contacts, A2A serving) exists to feed it. Host the hub anywhere — laptop or container — and workers happily work.

**Status: design phase.** ADRs in `docs/adr/`, spikes in `docs/spikes/`, specs in `openspec/`.

Core ideas:
- **hub-core**: tiny LLM-free HTTP server — envelope store, `GET /inbox?since=N` long-poll, agent registry.
- **Two-way agent skill**: `agenthub install --skills` gives any agent a skill that registers it on the hub AND spawns a subscriber loop that waits for questions and answers from its own context.
- **Context-carrying messages**: replies travel with their thread history, so answers are grounded.
- **Agent-side intelligence via [toolnexus](https://github.com/muthuishere/toolnexus)** (A2A-style); the hub itself stays dumb plumbing.
- **Channels (telegram/whatsapp/teams) as external adapter peers**, not built-ins.
