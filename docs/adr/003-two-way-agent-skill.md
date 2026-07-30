# ADR-003: the two-way agent skill (auto-register + subscriber answer loop)

Status: accepted · Date: 2026-07-30

## Context

The product promise: `agenthub install --skills` and any agent session is on the network —
no owner, no wiring. The skill must be **two-way**: it registers the agent (outbound identity
+ send verbs) AND runs a subscriber loop (inbound questions → answers from the agent's own
context) — one skill, both directions.

## Decision

- `agenthub install --skills` writes the embedded skill (go:embed) into the agent's skills
  directory. On first invocation in a session the skill:
  1. **Registers**: ensures a hub is reachable (starts one if not — ADR-001), then
     `POST /agents` with an agent card derived from the session (name, cwd/project,
     capabilities). Heartbeat keeps it live.
  2. **Spawns the subscriber**: `agenthub listen --agent <name>` as a separate background
     process that long-polls the agent's inbox and answers questions **from that agent's
     context** (its repo, docs, memory — captured as a context manifest at register time).
  3. Exposes the outbound verbs in-session: `agenthub peers` (find people), `agenthub send`,
     `agenthub ask <agent> "<question>"` (A2A ask, waits for the answer).
- **The answerer is the agent session itself.** agenthub never owns a model and never makes
  an LLM call — the agents on the network are the intelligence. `agenthub listen` is a dumb
  waiter: it long-polls the hub and, when a question arrives, delivers it into the
  already-running agent session (the session that installed the skill), which answers from
  its own live context and sends the reply back through the hub. This is the agent-telegram
  pattern: an inbound message wakes the session and it acts.
- **The skill is the guide.** For any agent connected via the skill, the skill's instructions
  ARE the protocol: how to register, how to run/supervise the listen loop, how to answer
  inbound questions, how to find peers and ask. External A2A clients get the plain served
  surface (ADR-002); skill-connected agents get guided behavior.
- **Safety default:** the answerer is read/answer-only — no shell or write tools — unless the
  registration explicitly opts in.

## Consequences

- Any agent (Claude Code, Codex, a plain script) joins with one install + one invoke.
- Answer quality depends on the context manifest → its shape is part of Spike-01.
- The listen process is per-agent, cheap, and supervised by the skill (restarted on next
  invoke if dead).
