# workwire — Competitive Landscape Research (mid-2026)

*Web research snapshot, 2026-07-30. Facts sourced from public web search; star counts and version claims are approximate and marked where sources are thin.*

## What workwire is (for framing)

A tiny, LLM-free, HTTP-only message hub — single Go binary, container-portable — where AI agents and terminal coding sessions are peers. Agents auto-register via an installed agent skill; a singleton listener loop waits for questions and delivers them into the *already-running* agent session, which answers from its own live context. Messages carry recent thread context. Channels (Telegram/WhatsApp/Teams) are external adapter peers; a contacts directory is harvested from traffic; the hub serves A2A agent cards + ask endpoints so external A2A clients interoperate.

Positioning: MCP/tools/skills are a solved problem — the unsolved core is the **work between workers**: agents discovering each other, asking, and answering with context.

---

## 1. Landscape map

Three concentric rings compete or overlap:

1. **Protocol / registry layer** — A2A (Linux Foundation), AGNTCY + SLIM, NANDA, Coral Protocol, A2A registries. These standardize the wire format and discovery, but ship no "deliver into a live interactive session" story.
2. **Broker / framework runtimes** — Solace Agent Mesh, Microsoft Agent Framework (AutoGen successor), LangGraph Platform, CrewAI, OpenAI Agents SDK handoffs. Agents live *inside* the framework's process model; communication is intra-framework.
3. **Coding-session multiplexers & phone bridges** — MCP Agent Mail, workwire-mcp, MCP Talk, OpenACP, ccgram, TMAI/Tmux-Orchestrator/claude-squad, OpenClaw. Closest in spirit; mostly MCP- or tmux-bound, mostly hub-and-human rather than peer-to-peer agent asks.

## 2. Comparison table

| Project | What it is | Transport | Broker/lock-in | Discovery | License | Maturity (approx) | Key difference vs workwire |
|---|---|---|---|---|---|---|---|
| **A2A protocol** (Google → Linux Foundation) | Agent interop standard: Agent Cards, Tasks | HTTP / SSE / JSON-RPC | None (protocol only) | Agent Cards at well-known URLs | Apache 2.0 | ~22k★, 150+ org backers, 5 SDK languages | A spec, not a running hub; no delivery into live sessions. workwire *speaks* A2A rather than competing with it |
| **A2A registries** (prassanna-ravishankar/a2a-registry, awslabs/a2a-agent-registry-on-aws, agentoperations/agent-registry) | Directories of hosted A2A agents | HTTP REST | Standalone services (AWS one is serverless-stack-bound) | Manual/registered AgentCards, semantic search | OSS (mostly Apache/MIT) | Early, small communities | Registries of *deployed* service agents; nothing about interactive terminal sessions or auto-harvested contacts |
| **AGNTCY + SLIM** (Cisco → Linux Foundation) | "Internet of Agents" infra: directories, observability SDKs, SLIM transport | SLIM (gRPC-based secure low-latency messaging) | SLIM message nodes; SDK adoption | AGNTCY directories of A2A/MCP endpoints | Apache 2.0 | LF project since 2025; enterprise-oriented | Heavyweight infra layer with its own transport; workwire is one plain-HTTP binary, zero new protocol |
| **Solace Agent Mesh** | Event-driven multi-agent framework on Solace broker + Google ADK | Solace event broker (pub/sub), A2A over the mesh | Yes — Solace broker + ADK agent model | Orchestrator delegates via registry of mesh agents | Apache 2.0 | ~4.7k★, active | Needs a broker and agents written *for* the mesh; workwire is broker-free and framework-agnostic |
| **NANDA (MIT)** | Decentralized index/registry for an internet of agents: identity, trust, federation | Builds on MCP + A2A; crypto-signed metadata | Federated registries (15 institutions) | NANDA Index → registry → verified agent facts | OSS (projnanda) | Research-stage, academic | Global-scale trust fabric; workwire is a local/team-scale working hub, not identity infrastructure |
| **Coral Protocol** | MCP-native runtime + registry; threaded, mention-addressed agent messaging; payments angle | MCP; Coral Server runtime | Yes — Coral Server ("Kubernetes for agents") | Public Coral registry | OSS (Apache, per repos) | v1 shipped Sep 2025; venture/crypto-adjacent | Closest conceptual overlap (threads, mentions, registry) but MCP-bound, runtime-owning, and payment-oriented |
| **ACP (IBM/BeeAI)** | Former agent comms protocol | REST | — | — | Apache 2.0 | **Wound down Aug 2025, merged into A2A** | No longer a competitor; validates A2A as the one standard to speak |
| **Microsoft Agent Framework / AutoGen runtime** | Framework: agent runtime, handoffs, group chat | In-process / gRPC runtime | Yes — write agents in the framework | Framework-internal registration | MIT | Very mature, large community | Agents must be authored in it; can't make an existing Claude Code session a peer |
| **LangGraph Platform / CrewAI / OpenAI Agents SDK (handoffs) / CAMEL** | Orchestration frameworks; handoffs = intra-app delegation | In-process; platform APIs | Yes — framework + (for platforms) hosted runtime | Framework-internal | MIT/Apache (SDKs); platforms commercial | Very mature | Same story: orchestration *inside one app*, not messaging between independent live sessions |
| **MCP Agent Mail** | Mailbox for AI coding agents: identities, threaded messages, file reservations, audit; SQLite+Git | MCP server (stdio/HTTP) | MCP-capable agents only | Contact handshake protocol between projects | OSS | Active, niche but the most direct adjacent | Very close in intent. But MCP tool-call model = agent must *poll/check mail* inside a turn; no listener loop delivering questions into an idle live session; no channel/human peers; no A2A |
| **workwire-mcp (gilbarbara), MCP Talk, swarm-mcp** | Small MCP servers for inter-agent messaging / spawning | MCP | MCP only | Static/manual | MIT-ish | Small (<1k★ each, uncertain) | Same MCP-turn limitation; also — note the name collision (see §5) |
| **OpenACP** | Self-hosted bridge: Telegram/Discord/Slack ↔ coding agents via Agent Client Protocol | ACP (editor-agent protocol) over messaging platforms | Agent must speak ACP | Manual config | OSS | Early | Human→agent remote control, not agent↔agent asks; no registry/contacts |
| **ccgram** | Telegram ↔ tmux bridge for Claude Code/Codex/Gemini; parallel session mgmt | Telegram bot + tmux inject | tmux; single-user | Manual session list | OSS | Small, active | Phone remote-control of sessions; no peer messaging between agents, no context-carrying threads |
| **TMAI / Tmux-Orchestrator / claude-squad / dmux / thurbox** | tmux multiplexers/orchestrators for many coding agent sessions, some with two-way messaging | tmux keystroke injection; some local IPC | tmux + same machine | Manual/spawned | OSS | Fast-moving hobby→serious tier | Machine-local, injection-based (fragile), orchestrator-centric. workwire is network HTTP, cross-machine, peer model |
| **OpenClaw** (ex-Warelay/Moltbot) | Personal AI assistant gateway: local agent reachable over WhatsApp/Telegram/Signal; native mobile apps | Gateway process + channel adapters | Its own gateway/agent runtime | Single-assistant model | MIT | ~347k★ (most-starred repo on GitHub, Apr 2026) — the gorilla | Human↔one-assistant, not many-agents-as-peers; but it proves the channel-bridge demand and could subsume the channel-adapter half of workwire |
| **Claude Code Channels** (Anthropic, native) | Built-in Telegram/Discord plugins forwarding messages into a Claude Code session | MCP-based channel plugins | Claude Code only | n/a | Proprietary feature | Shipping 2026 | Vendor-native human→session bridge; validates "deliver into the running session" but is single-vendor, human-only, no agent↔agent |

## 3. Per-competitor notes

### A2A protocol + registries
The de-facto standard (Linux Foundation, Apache 2.0, 150+ orgs, SDKs in Python/JS/Java/Go/.NET; ACP folded into it Aug 2025). All registries found (community a2a-registry, AWS Labs, agentoperations) index *deployed service agents* with hand-registered cards. None address: agents that are interactive terminal sessions, auto-registration from a skill, or context-carrying replies. workwire's move — serve A2A cards + ask endpoints from the hub — makes it an A2A *participant*, which is the right posture; competing with the protocol would be fatal.

### AGNTCY / SLIM (Cisco, Linux Foundation since July 2025)
Enterprise "internet of agents" plumbing: directories for A2A/MCP endpoints, observability SDKs, and SLIM as a new secure low-latency transport. Standards-body pace, heavy SDK adoption cost. Opposite philosophy to workwire's single-binary plain HTTP.

### Solace Agent Mesh (~4.7k★, Apache 2.0)
Real open-source "message bus for agents" — but the bus is a Solace event broker and agents are Google-ADK-style components written for the mesh. Orchestrator-centric delegation, not peer asks. Strong enterprise story; zero story for "my three Claude Code terminals should be able to ask each other."

### NANDA (MIT Media Lab)
Federated index + identity/trust/reputation for a global agent web. Research-stage; complements rather than competes at team scale. If it matures, workwire hubs could be leaf registries.

### Coral Protocol (v1, Sep 2025)
The closest protocol-layer analogue: threaded, mention-addressed agent-to-agent messaging, a runtime (Coral Server), CLI/Studio, and a public registry. Differences: MCP-native (agents join via MCP tools, so answering is still a tool-call inside a managed turn), it *owns the runtime* ("Kubernetes for AI agents"), and it carries a payments/crypto agenda. workwire deliberately owns nothing about the agent's runtime.

### Framework runtimes (Microsoft Agent Framework/AutoGen, LangGraph, CrewAI, OpenAI Agents SDK handoffs, CAMEL)
All mature, all MIT/Apache, all enormous — and all solve *orchestration inside one application you write*. Handoffs (OpenAI SDK) and group chats (AutoGen) are in-process control transfer. None can enroll an already-running third-party coding session as a peer. They are competitors mainly for mindshare ("multi-agent = use a framework").

### MCP Agent Mail (mcpagentmail.com)
The most direct adjacent project: identities, threaded messaging, file reservations, search, audit trails for multi-agent *coding*, SQLite+Git backed, contact handshakes across projects. Gaps vs workwire: MCP-server model means the agent must actively check mail within a turn (no push into an idle live session); no human channels as peers; no A2A interop; per-machine rather than plain networked HTTP. Maturity/stars: modest (uncertain — verify on GitHub before quoting).

### Session multiplexers (TMAI, Tmux-Orchestrator, claude-squad, MatchaOnMuffins/orchestrator, dmux, thurbox)
A booming 2025–2026 category. Two-way messaging exists (Tmux-Orchestrator advertises it; thurbox has inter-session messaging), but the mechanism is tmux keystroke injection on one machine, and the model is a boss-orchestrator steering workers — not peers with a directory. Fragile, local, no channels, no protocol interop. TMAI's v3 pivot (become the "exoskeleton" a producer-agent drives, not the orchestrator) shows the category converging on workwire's "the agent is the brain, infra is dumb" philosophy.

### Phone/channel bridges (OpenACP, ccgram, Claude Code Channels, OpenClaw)
All human→agent remote control. OpenClaw (ex-Warelay/Moltbot, ~347k★ by Apr 2026, native mobile apps since Jun 2026) has completely won "reach your personal AI over WhatsApp/Telegram" — but it is one-assistant-per-human, with agents as its plugins, not a peer hub. Anthropic's native Channels feature validates "deliver messages into the running Claude Code session" as a first-class need — and is the strongest signal that vendors will build this in.

## 4. Gaps and differentiation

**What nobody in the surveyed field does well (workwire's white space):**

1. **Delivery into already-running interactive sessions, cross-vendor.** Everything is either (a) spawn-an-agent-to-answer (A2A services, frameworks), (b) tool-call-inside-a-turn (MCP Agent Mail, Coral, MCP Talk), or (c) vendor-native single-tool (Claude Code Channels). A singleton listener loop that wakes an *idle live session* which answers from its own hot context is essentially unoccupied.
2. **Context-carrying asks.** Replies grounded in the answering session's live working context, with recent thread context attached to messages, is not a feature of any registry or bus found. Buses move opaque payloads; frameworks share state only internally.
3. **Zero-ceremony auto-registration via an installed skill.** All registries surveyed require manual card publication or SDK adoption. "Install a skill → session becomes a discoverable peer" has no direct equivalent.
4. **Humans-on-channels and agents as uniform peers.** Bridges make channels a remote control for one agent; meshes make agents talk to agents. Nobody treats a Telegram human and a Claude Code session as the same kind of addressable peer with a harvested contacts directory.
5. **LLM-free, broker-free, single static binary.** Solace needs a broker, Coral a server-runtime, AGNTCY a new transport, frameworks a rewrite. The ops-weight gap is real and durable.

**Threats (who could subsume this fast):**

- **Anthropic** — Claude Code Channels already delivers messages into live sessions; extending it to agent↔agent within the Claude ecosystem is a small step. Highest-probability subsumer, but vendor-locked by construction — workwire's cross-vendor neutrality is the moat.
- **OpenClaw** — massive community, already owns channel adapters and a gateway process; a "multi-agent peers" release would overlap heavily. Watch its roadmap.
- **A2A ecosystem** — if A2A adds a standard "interactive session agent" binding or a reference hub, registries + SDKs could commoditize the hub. Mitigation: stay the best small A2A-speaking hub rather than a rival protocol.
- **Coral Protocol** — funded, conceptually closest (threads/mentions/registry); could strip its crypto layer and ship a lightweight mode.
- **MCP Agent Mail** — a push-delivery + channels release would collide directly; it already owns coding-agent-coordination mindshare in the MCP world.

## 5. Naming collision check: "workwire" / "workwire"

The name is **heavily contested** on GitHub — at least eight active or notable projects:

- `mudler/workwire` — LocalAI/LocalAGI agent-configuration hub (mudler is LocalAI's author; visible project).
- `DjangoPeng/workwire` — Chinese-language hub of agent apps (GitHub Sentinel, ChatPPT); sizable following.
- `gilbarbara/workwire-mcp` — **an MCP server for communication/coordination between multiple AI agents** — a near-identical name *in the same niche*.
- `ottogin/workwire` — fork of a Karpathy "workwire" (bare git repo + message board for agent swarms) — Karpathy association gives this name-form high visibility.
- `Stanshy/AgentHub` — Electron app managing 47 Claude Code agents ("Harness Engineering").
- `nathangtg/workwire`, `k0msenapati/workwire`, org `AI-Agent-Hub` — smaller MCP/data-agent platforms.

Also: Hugging Face "Hub" + agents, and various commercial "Agent Hub" products (GreyNoise, Salesforce-adjacent naming) crowd search results. **Verdict:** "workwire" is effectively unownable for search, npm/brew discoverability, and disambiguation — a rename (or a distinctive qualified name) is strongly advisable before public launch. The gilbarbara MCP project and the Karpathy-fork are the most damaging collisions because they sit in the same conceptual space.

## 6. Sources

- A2A: https://developers.googleblog.com/en/a2a-a-new-era-of-agent-interoperability/ · https://en.wikipedia.org/wiki/Agent2Agent · https://www.programming-helper.com/tech/agent-to-agent-protocol-2026-google-a2a-standard · https://atlan.com/know/google-a2a-protocol/
- ACP→A2A merger: https://lfaidata.foundation/communityblog/2025/08/29/acp-joins-forces-with-a2a-under-the-linux-foundations-lf-ai-data/ · https://zuplo.com/blog/agent-protocol-stack-mcp-a2a-acp-2026
- A2A registries: https://github.com/prassanna-ravishankar/a2a-registry · https://github.com/awslabs/a2a-agent-registry-on-aws · https://github.com/agentoperations/agent-registry · https://github.com/sing1ee/a2a-directory
- AGNTCY/SLIM: https://www.prnewswire.com/news-releases/linux-foundation-welcomes-the-agntcy-project-to-standardize-open-multi-agent-system-infrastructure-and-break-down-ai-agent-silos-302515443.html · https://github.com/agntcy/slim
- Solace Agent Mesh: https://github.com/SolaceLabs/solace-agent-mesh · https://solace.com/products/agent-mesh/
- NANDA: https://thenewstack.io/how-mits-project-nanda-aims-to-decentralize-ai-agents/ · https://github.com/projnanda · https://arxiv.org/pdf/2507.14263
- Coral: https://github.com/Coral-Protocol/coral-server · https://www.marktechpost.com/2025/09/20/an-internet-of-ai-agents-coral-protocol-introduces-coral-v1-an-mcp-native-runtime-and-registry-for-cross-framework-ai-agents/ · https://arxiv.org/pdf/2505.00749
- Frameworks/gateways: https://www.truefoundry.com/blog/multi-agent-orchestration-tools · https://www.mintmcp.com/blog/agent-gateways-multi-agent-workflows
- MCP inter-agent messaging: https://mcpagentmail.com/ · https://github.com/gilbarbara/workwire-mcp · https://stiege.github.io/swarm-mcp/ · https://glama.ai/mcp/servers/@devinvenable/mcp-talk
- Bridges/multiplexers: https://github.com/Open-ACP/OpenACP · https://github.com/alexei-led/ccgram · https://github.com/trust-delta/tmai · https://github.com/absmartly/Tmux-Orchestrator · https://github.com/MatchaOnMuffins/orchestrator · https://shipyard.build/blog/claude-code-multi-agent/ · https://pub.towardsai.net/claude-code-channels-message-your-ai-coding-agent-from-telegram-and-discord-2026-5f263ccc4b9c
- OpenClaw: https://www.jitendrazaa.com/blog/ai/clawdbot-complete-guide-open-source-ai-assistant-2026/ · https://openclaw.ai/
- Name collisions: https://github.com/mudler/workwire · https://github.com/DjangoPeng/workwire · https://github.com/ottogin/workwire · https://github.com/Stanshy/AgentHub · https://github.com/nathangtg/workwire · https://github.com/k0msenapati/workwire
