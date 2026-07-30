# Spike-01: question → running session → answer, end to end

Timebox: 1 day · Risk being retired: the core product loop

## Question

Can a question posted to the hub reach an already-running agent session, get answered from
that session's live context, and return to the asker — with no LLM call anywhere in workwire?

## Plan

1. Stub hub: in-memory envelope store + `POST /send`, `GET /inbox?since=N&wait=30`,
   `POST /agents`, `GET /agents`.
2. `workwire listen` prototype: long-polls the inbox for its agent, delivers each question
   into the running session (agent-telegram pattern — session wakes on inbound and acts),
   session answers on the thread via `POST /send`.
3. Asker: `workwire ask <agent> "<q>"` → posts, polls the thread, prints the answer.
4. Run two real Claude Code sessions in different repos; each asks the other a question only
   answerable from the other's repo context.

## Success criteria

- Round trip < 60s with both sessions live; answer demonstrably uses the target's context.
- Listen loop survives session restart (skill re-supervises on next invoke).
- Decide and document THE delivery mechanism into a running session (inbox file the session's
  hook watches vs prompt injection vs other) — this is the spike's main output.

## Out of scope

Auth, persistence, A2A card conformance, channels.
