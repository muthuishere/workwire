# ADR-003: the two-way agent skill (auto-register + subscriber answer loop)

Status: accepted · Date: 2026-07-30

## Context

The product promise: `workwire install --skills` and any agent session is on the network —
no owner, no wiring. The skill must be **two-way**: it registers the agent (outbound identity
+ send verbs) AND runs a subscriber loop (inbound questions → answers from the agent's own
context) — one skill, both directions.

## Decision

- `workwire install --skills` writes the embedded skill (go:embed) into the agent's skills
  directory. On first invocation in a session the skill:
  1. **Registers**: ensures a hub is reachable (starts one if not — see first-run flow
     below), then `POST /agents` with an agent card derived from the session (name,
     cwd/project, capabilities). Heartbeat keeps it live.
  2. **Spawns the subscriber**: `workwire listen --agent <name>` as a separate background
     process that long-polls the agent's inbox and answers questions **from that agent's
     context** (its repo, docs, memory). A "context manifest" captured at register time is
     **proposed, shape TBD pending the real-session spike** — it is NOT a decided mechanism;
     the openspec defines only the spike-proven registration fields.
  3. Exposes the outbound verbs in-session: `workwire peers` (find people), `workwire send`,
     `workwire ask <agent> "<question>"` (A2A ask, waits for the answer).
- **The answerer is the agent session itself.** workwire never owns a model and never makes
  an LLM call — the agents on the network are the intelligence. `workwire listen` is a dumb
  waiter: it long-polls the hub and, when a question arrives, delivers it into the
  already-running agent session (the session that installed the skill), which answers from
  its own live context and sends the reply back through the hub. This is the agent-telegram
  pattern: an inbound message wakes the session and it acts.
- **The skill is the guide.** For any agent connected via the skill, the skill's instructions
  ARE the protocol: how to register, how to run/supervise the listen loop, how to answer
  inbound questions, how to find peers and ask. External A2A clients get the plain served
  surface (ADR-002); skill-connected agents get guided behavior.
- **Safety default:** the answerer is read/answer-only — no shell or write tools on
  inbound-triggered turns — unless the registration explicitly opts in. Inbound question
  text is untrusted DATA, never instructions; envelopes carry authenticated provenance
  (ADR-007) and the skill treats the body as a quoted external question.
- **First-run flow:** the skill probes `/health`; if absent AND `hubUrl` is loopback, it
  starts `workwire serve` detached (own process group — the hub survives the session's
  exit). The auto-start race between two simultaneous sessions resolves by
  **bind-first-then-health-check**: both try to bind; the loser of the bind race re-probes
  `/health` and becomes a client.

## Consequences

- Any agent (Claude Code, Codex, a plain script) joins with one install + one invoke.
- Answer quality depends on the context manifest → its shape is part of Spike-01.
- **`workwire listen` is a singleton per agent** — enforced two ways:
  - **Local fast path:** an OS-held advisory lock (`flock`/`F_SETLK` on an open fd under the
    config/run dir) — NOT a pid lockfile. The lock dies with the process, so it is correct
    across container restarts, kill -9, and stale volumes; pid liveness is invalid across
    PID namespaces and reboots.
  - **Cross-machine authority:** a hub-side **listen LEASE per agentId** — acquired on
    listen start, renewed with the heartbeat; an expired lease is claimable. Two hosts
    listening as the same agent cannot both hold the lease, so double answers are
    impossible even across machines. The local flock is just the fast path.
  Re-invoking the skill adopts the running listener instead of spawning a second.
- The listen process is per-agent, cheap, and supervised by the skill (restarted on next
  invoke if dead).
- **Proven so far:** file-inbox delivery is proven with simulated sessions (Spike-01).
  Real interactive-session wake latency and cross-harness portability (Codex, Cursor,
  plain scripts) are OPEN items gating the v1 promise; a per-harness delivery matrix
  (harness, wake mechanism, measured latency) will live in the openspec.
