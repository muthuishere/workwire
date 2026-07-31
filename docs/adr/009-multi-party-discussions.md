# ADR-009: huddle-style discussions — grounded disagreement between real codebases

Status: accepted · Date: 2026-07-31

## Context

v1 proved one-shot Q&A: `ask` → the question lands in a session's inbox → the session
answers from its own live context → the asker's long-poll completes (2.8–7.8 s measured,
docs/wake-experiment.md). Real work is not one shot, and — more importantly — **it is not
agreement**.

The model to copy is the `huddle` skill (github.com/muthuishere, installed locally):
several *named perspectives* give short, opinionated takes grounded in the repo, a
**counterweight is deliberately included when it sharpens the outcome**, and the personas
**do not decide — the user decides**. The value is not that participants converge; it is
that a real disagreement is surfaced cheaply, in front of the person who has to choose.

workwire can do something the huddle skill cannot: its perspectives are not simulated.
Each participant is a *real session sitting in a real codebase*, with that repo's
`CLAUDE.md` / `AGENTS.md` as its persona and the code itself as its evidence. When the API
session says "that's not what the auth code does", it is not playing a role — it has the
file open.

So the failure mode to design against is **not** only runaway chatter. It is
**sycophancy**: two agents politely agreeing produce a worse answer than one agent alone,
because agreement between models is not evidence.

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
- **Each participant speaks as its own repo.** At registration the skill derives a short
  **persona** from the session's own `CLAUDE.md` / `AGENTS.md` (plus cwd/project) — who
  this worker is, what it owns, what it will not speak for — and the hub carries it on the
  agent card and alongside `from` in projected context. Peers therefore always know
  *which vantage point* is talking. The hub neither invents nor validates personas; it is
  still dumb plumbing.
- **Participants surface perspectives; the initiator decides.** Copying the huddle rule
  that personas do not decide: only the **thread initiator** (a human on the CLI, or the
  agent that opened it) may send `kind: "resolved"`. A participant that thinks the matter
  is settled sends `kind: "proposal"` — a recommendation, not a verdict. This keeps the
  decision with whoever owns the problem and stops two agents from closing a question
  neither of them owns.
- **Disagreement is the point, and the skill says so.** The discussion posture is
  explicitly anti-sycophantic: answer from your own repo's ground truth, contradict a
  claim about your domain when the code says otherwise, say "I don't know / that's not
  mine to answer" instead of guessing, and never agree merely because a peer asserted
  something. Agreement between models is not evidence. When participants do agree, they
  say what evidence they agreed on.
- **Discussions converge, or the hub stops them.** Two mechanisms, both in v1:
  - `kind: "resolved"` from the initiator closes the thread; listeners stop watching it;
    further sends are rejected (409).
  - A **per-thread round cap** (default 24 envelopes, configurable `maxThreadMessages`)
    after which the hub marks the thread `stalled`, stops fanning out, and returns a
    clear error naming the cap. This is a token-burn circuit breaker: unresolved
    disagreement is a fine outcome — an infinite one is not. A stalled thread is handed
    back to the initiator with the disagreement intact, which is more useful than a
    manufactured consensus.
- **Context projection is what makes a late joiner useful.** The existing `context`
  field (last N thread messages, default 5, cap 20) already gives a session that has
  never seen the thread enough to contribute, and now carries each speaker's persona so a
  newcomer can weigh who said what. `GET /threads/<id>?last=N` serves more on demand.
- **Inbound text remains untrusted DATA** (ADR-007). A discussion is a place to be
  argued with, never an instruction channel: a peer's message cannot direct tool use.
- **CLI surface:** `workwire huddle <name...> "<topic>"` opens one, `workwire say
  <thread> "<text>"` contributes, `workwire resolve <thread> "<summary>"` closes it,
  `workwire threads` lists what is live.

## Consequences

- **This is the differentiator, stated plainly:** other agent meshes pass tasks or seek
  consensus. workwire convenes *grounded disagreement* — perspectives that are each
  anchored in a different real codebase — and hands the decision to the person who owns
  the problem. That is the huddle model with real evidence behind every voice.
- Humans and agents participate identically — a human on the CLI is just another member,
  which is the mesh claim made concrete.
- Nothing about the wire changes for existing one-to-one clients: `to` as a plain string
  keeps working, and a two-member thread behaves exactly as it does today.
- The round cap makes an unbounded conversation impossible by construction, at the cost
  of occasionally cutting off a legitimately long discussion — the cap is configurable
  and the error says so.
- Fan-out multiplies stored recipient cursors, not stored envelopes; storage stays O(1)
  per message.
