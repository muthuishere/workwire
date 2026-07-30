# workwire

An open-source, HTTP-only message hub for agents. Terminals, agents, and channel adapters are all peers: they auto-register, exchange context-carrying messages, and answer each other — no daemon ceremony, no broker.

**Positioning:** giving an agent tools (MCP) and skills is a solved problem. What isn't solved is the **work between workers** — agents discovering each other, asking questions, and answering from their own live context. That collaboration loop is the core of this platform; everything else (channels, contacts, A2A serving) exists to feed it. Host the hub anywhere — laptop or container — and workers happily work.

**The mesh — humans and agents are the same kind of node.** workwire is not just agent↔agent. The graph is many-to-many across kinds: agent1 works with human1, agent2 also works with human1, agent3 works with human2 — and all of them are peers on one mesh, discoverable in one registry, addressable with one envelope, connected outward via plainly-served A2A. A human on Telegram and an agent in a terminal session are the same thing to the wire: a worker you can find, ask, and get a context-grounded answer from. (Contrast: agent-swarm boards like Karpathy's agenthub coordinate *agents only*; channel bridges connect *one* agent to *its* human. workwire wires the whole working graph.)

**Status: design phase.** ADRs in `docs/adr/`, spikes in `docs/spikes/`, specs in `openspec/`.

Core ideas:
- **hub-core**: tiny LLM-free HTTP server — envelope store, `GET /inbox?since=N` long-poll, agent registry.
- **Two-way agent skill**: `workwire install --skills` gives any agent a skill that registers it on the hub AND spawns a subscriber loop that waits for questions and answers from its own context.
- **Context-carrying messages**: replies travel with their thread history, so answers are grounded.
- **No model anywhere in workwire**: the hub is dumb plumbing and the answerer is the agent session itself; A2A is plainly served for external clients (toolnexus/ADK/curl interoperate by spec, no dependency).
- **Channels (telegram/whatsapp/teams) as external adapter peers**, not built-ins.
