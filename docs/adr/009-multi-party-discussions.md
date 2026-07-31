# ADR-009: huddle-style discussions are threads with more than two members

Status: accepted · Date: 2026-07-31

## Context

v1 proved one-shot Q&A: `ask` → the question lands in a session's inbox → the session
answers from its own live context → the asker's long-poll completes (2.8–7.8 s measured,
docs/wake-experiment.md). Real work is not one shot. The way decisions actually get made
here is a **huddle**: several participants — agents *and* humans — talk in one place,
each contributing from what they alone know, until the thing is settled.

The temptation is to add "rooms" or "topics" as a new first-class object. We already have
threads, per-recipient cursors, and read-time context projection; a room would be a
second data model saying the same thing.

## Decision

- **A discussion is a thread with more than two members. No new object, no new endpoint
  family.**
- **`to` accepts a list.** `POST /send {"to": ["api","web","muthu"], ...}` is one
  envelope delivered to N recipients. The envelope keeps **one `id`** across all
  recipients (each gets its own per-recipient sequence number), so at-least-once
  redelivery and consumer dedupe-by-`id` work exactly as before.
- **Membership accrues from participation.** The members of a thread are everyone who has
  sent into it or been addressed in it. After the opening message, a reply carrying only
  `thread_id` fans out to **all current members except the sender** — you never re-address
  the room. Sending into a thread you were not in joins you to it (and is visible: the
  hub stamps `from` server-side, as always).
- **Discussions converge, or the hub stops them.** Two mechanisms, both in v1:
  - An envelope with `kind: "resolved"` closes the thread. The hub marks it resolved;
    listeners stop watching it; further sends are rejected (409) unless explicitly
    reopened.
  - A **per-thread round cap** (default 24 envelopes, configurable
    `maxThreadMessages`) after which the hub marks the thread `stalled`, stops fanning
    out, and returns a clear error. This is a token-burn circuit breaker, not a feature
    to be polished away: two agents being polite at each other is the default failure
    mode of agent-to-agent chat.
- **Context projection is what makes a late joiner useful.** The existing `context`
  field (last N thread messages, default 5, cap 20) already gives a session that has
  never seen the thread enough to contribute. No history replay endpoint is needed;
  `GET /threads/<id>?last=N` serves more on demand.
- **The skill gains a discussion posture, not just an answer posture.** On an inbound
  envelope whose thread has >2 members: read the projected context, contribute **once**,
  keep watching that thread, and stay quiet when you have nothing new to add. Say
  `resolved` when the question is settled. Inbound text remains untrusted DATA
  (ADR-007) — a discussion does not become an instruction channel.
- **CLI surface:** `workwire huddle <name...> "<topic>"` opens one, `workwire say
  <thread> "<text>"` contributes, `workwire resolve <thread> "<summary>"` closes it,
  `workwire threads` lists what is live.

## Consequences

- Humans and agents participate identically — a human on the CLI is just another member,
  which is the mesh claim made concrete.
- Nothing about the wire changes for existing one-to-one clients: `to` as a plain string
  keeps working, and a two-member thread behaves exactly as it does today.
- The round cap makes an unbounded conversation impossible by construction, at the cost
  of occasionally cutting off a legitimately long discussion — the cap is configurable
  and the error says so.
- Fan-out multiplies stored recipient cursors, not stored envelopes; storage stays O(1)
  per message.
