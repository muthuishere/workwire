# ADR-017: deliberation — what forty years of prior art says we got wrong

Status: proposed · Date: 2026-08-01 · Research: FIPA, Hamblin/Mackenzie/Prakken, Dung, Singh/Wooldridge, and the 2024–2026 LLM-debate literature

## Context

We designed the discussion rules (ADR-009, ADR-011) from first principles and field
observation. A survey of the prior art says several of those principles are already
standardised, one of our rules is unverifiable on its own terms, one deadlocks, and one of
our marketing claims is contradicted by compute-matched benchmarks.

This ADR records what to change and, equally important, what claim to stop making.

## What the literature says we got wrong

### 1. `answering` was a claim, not an observation — and that is fatal, not fixable

Singh's critique of mentalistic agent-communication semantics (1998) lands exactly on it:
compliance "depends on neither the agent's behavior nor its design, but on how the design is
documented… any designer who cares to insert a comment saying 'This program is correct' is
freed from establishing its compliance." Wooldridge (ICMAS-98): "if there is no way of
determining whether a system that claims to conform does conform, the value of the standard
itself must be questioned."

Our `answering` marker broke twice because it is a claim. Moving it into one long-lived
process (`workwire watch`) makes it a claim *about a process*, which is still not a claim
about attention.

**Already acted on** (ADR-016): attention is now derived from evidence the peer authored
something. This ADR records *why* that was right, and makes the rule general:

> **Never write a hub rule you cannot check from the envelope log.**

### 2. Blocking closure on ANY open dissent deadlocks on dead sessions — a live bug

Today an agent initiator may not close a thread with an open dissent, and only the dissenter
may withdraw it. If that session dies, the thread is unclosable forever. We have already hit
the shape of this once: two threads sat permanently stalled because the cap refused the very
send that would have ended them.

Dung's abstract argumentation framework (AIJ 77, 1995) solves it with **reinstatement**: an
argument attacked by a defeated attacker is defended. If `agent3` attacks `agent2`'s dissent
and nothing attacks `agent3`, the proposal is defended and closure is legal — *with the
dissent still on record*. The **grounded extension** is the right semantics for us: it always
exists, is unique, and computes by monotone fixpoint (P-complete), which suits a stdlib-only
Go binary.

It also distinguishes three cases our boolean renders identically as "blocked": an answered
dissent, a two-cycle standoff, and an odd cycle with no stable extension — the last being a
human's problem, not a waiting problem.

### 3. The round cap is a symptom mask

Prakken: termination is obtainable only if premises come from a bounded context — "Without
this condition, endless challenging is often possible: W: claim p, B: why p, W: p since q,
B: why q, and so on." An LLM is an unbounded premise generator, so a fixed cap of 24 is a
timer over a structural problem. The literature's actual device is a **relevance rule** —
reject a move that cannot change the dialectical status of the thread's opening proposal —
plus no-repetition, which are the only rules with proved termination bounds
(Parsons/Wooldridge/Amgoud, JLC 13(3), 2003).

### 4. Dissent will not happen because we asked for it

"Only the Devil's Advocate Works" (1,920 agent responses): strong role framing (61.7%) and
explicit dissent instructions (55.0%) are **statistically indistinguishable from baseline
(48.3%)**. Only an *assigned* devil's-advocate role reaches 99.2%.

Our skill instructs peers to disagree. That is the 55.0% condition. Making `dissent` a
first-class envelope kind does not make dissent happen.

### 5. We were not worried enough about correlated error

Kim et al. (ICML 2025), 349 models × 12,032 questions: **conditional on both models being
wrong, mean agreement is 0.60 on HELM against a 0.33 chance baseline**; same-family
agreement reaches 0.97; and after controlling for provider, architecture and size, *more
accurate* models are *more* correlated. "Agreement between models is not evidence" was
right, and understated.

### 6. Stop claiming debate produces better answers

Compute-matched, it does not. Huang et al. (ICLR 2024), full GSM8K: self-consistency@3 =
82.5%, debate (3 agents × 2 rounds = 6 generations) = 83.2%, **self-consistency@6 = 85.3%**.
Intrinsic self-correction *degrades* accuracy. "Stop Overvaluing MAD" (9 benchmarks × 4
models) finds every multi-agent-debate method loses to plain CoT and self-consistency —
**except with heterogeneous models**.

That exception is the whole point, and it is our actual claim: **workwire's peers are not
three samples of one model, they are sessions holding different repositories, different
branches and different working state.** The defensible claim is *different context*, never
*more voices*. Any doc that implies otherwise should be corrected.

## Decisions

1. **Rule of verifiability.** No hub rule may depend on a predicate that cannot be checked
   from the envelope log. `answering` is derived (ADR-016); anything similar must be too.
2. **Dissent gets an attack edge and grounded-extension closure.** `dissent` may carry
   `attacks: <messageId>`; closure legality is computed as a grounded extension rather than
   "zero open dissents". A dissent whose author is `gone` and which nothing has defended for
   a configured period no longer blocks — it stays on the record, and the closure names it as
   overridden. *(Implemented in the narrow form below; the full framework stays proposed.)*
3. **Closure returns three states**, not two: `close-allowed`, `blocked` (with what blocks
   it), `escalate` (odd cycle — unresolvable by argument, hand to a human).
4. **The round cap becomes a backstop, not the mechanism.** Add a relevance rule and
   no-repetition; keep the cap as the outer bound.
5. **Assign the devil's advocate.** For a thread above N members, the hub names one peer as
   the assigned dissenter for that thread, rotating across threads. Asking nicely does not
   work; assignment does.
6. **Correct the claim.** Docs say "different context, not more voices", and cite the
   heterogeneity exception rather than implying debate improves accuracy.

## Deferred, with reasons

- **FIPA-Request's `agree|refuse` first turn and per-turn `reply-by`** (SC00026/SC00061G):
  right shape, and it changes the ask contract — worth a spike first.
- **Full Dung framework** with preferred/stable semantics: grounded is enough for now.
- **A2A v1.0** (Jan 2026, protobuf core): we serve v0.3.0. Either re-target or drop the
  claim; a stale shim is worse than none.

## Consequences

- We stop defending a rule we cannot check, and start deriving it.
- A dead dissenter can no longer hold a thread hostage, without erasing what they said.
- The product claim gets narrower and true: different context, heterogeneous by
  construction, which is the one multi-agent result that survives skeptical replication.
