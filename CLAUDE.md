# workwire — agent instructions

Open-source, HTTP-only message hub for the **work between workers**: agents (and the humans
working with them) discover each other, ask questions, and answer from their own live
context. Repo: github.com/muthuishere/workwire. Local dir name (`local-agent-messenger`) is
historical — the product is **workwire**, CLI `workwire`.

## What we ARE building

- **hub-core**: one dumb Go static binary (`workwire serve`). Envelope store (NDJSON
  segments), `GET /inbox?agent=<name>&since=<cursor>&wait=25&context=X` long-poll,
  hub-assigned per-recipient sequence cursors (+ `reset:true` rebase), threads,
  read-time context projection (`lastMessages=5`, cap 20), dynamic agent registry
  (`POST /agents` + heartbeat), contacts harvested from traffic, plain-served A2A v0.3.0
  (card + `message/send` shim + `/ask`). Container-portable (`FROM scratch`, env-only,
  `bind` vs `hubUrl`, explicit `authMode`). Config auto-created at
  `~/.config/workwire/workwire.json`, `WORKWIRE_*` env overrides.
- **The two-way agent skill** (`workwire install --skills`): auto-registers the session on
  the hub AND runs a singleton `workwire listen` (flock + hub lease) that delivers inbound
  questions into the already-running session via a session inbox file; the session answers
  from its live context. The skill is the guide for skill-connected agents; external A2A
  clients get the plain served surface.
- **The mesh**: humans and agents are the same kind of node — many-to-many
  (agent1↔human1, agent2↔human1, agent3↔human2), one registry, one envelope.
- Identity: hub-issued agentId+secret, server-stamped `from` (ADR-007). Retention +
  tombstone deletion (ADR-008).

## What we are NOT building (do not reintroduce these)

- **No channel code. None.** No telegram/whatsapp/teams adapters — not even optional ones.
  Channel bridging is someone else's peer process (the old messenger product does that).
- **No LLM calls anywhere in workwire.** The hub is plumbing; the answerer is the agent
  session itself. Never bake in a model, an API key, or an "answer command".
- **No toolnexus (or any SDK) dependency.** A2A is implemented natively from the spec;
  toolnexus is at most a compatible external client.
- **No WebSocket/SSE in the contract.** Only HTTP. Long-poll is the receive mechanism.
- **No push subscriptions / consumer HTTP servers.** Consumers poll with cursors.
- **No daemon install ceremony, no owner/agent role split, no config-holds-runtime-state.**
  Agents/adapters are dynamic registry state, never config entries.
- **No brokers, no frameworks.** One binary, plain HTTP, curl-grade clients.

## Process (in order — don't skip ahead)

ADR → spikes → openspec → implementation.
- ADRs: `docs/adr/001…008`. Stress-test log: `docs/spec-stress-test.md`,
  team report: `docs/stress-team-report.md`. Competitors: `docs/research.md`.
- Spikes (all green, code under `spikes/`): 01 question→session→answer (file-inbox delivery
  won), 02 long-poll/context/container (numbers in FINDINGS), 03 external-peer +
  A2A card conformance (telegram there was disposable evidence only).
- Open items gating the v1 promise: real interactive-session wake latency, cross-harness
  delivery matrix (beyond Claude Code), context-manifest shape. Prove during implementation.

## Working rules for this repo

- Go, minimal deps, single static binary. Port default 14411.
- Secrets by env NAME only; never a value in code, config, tests, or output.
- Never use the agent-telegram skill or improvise remote-control bridges for this project.
- Commit style: plain `type: subject`, no Co-authored-by.
