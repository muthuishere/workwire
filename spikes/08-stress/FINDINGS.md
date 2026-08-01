# Spike-08 findings — 200 interactions, five real agent sessions

Date: 2026-08-01 · Five Claude Code sessions in five git repos, each holding `workwire watch`,
against the live hub on 14411. Re-run: `./stress.py --plan 200 --out run.ndjson`, summarise
with `--report`.

Seven scenarios was a smoke test. Every mechanism we have built passed its first try and
failed a different one later — the fork passed "answer a question" and failed "be there in
twenty minutes"; the bare Monitor passed that and failed "keep the declaration alive". So
this is a distribution, not a pass/fail.

## Results

```
interactions: 200   asks: 164   answered: 163 (99%)
latency  p50=6.8s  p90=12.9s  p99=27.0s  max=30.8s

by question kind        by peer                     graded
  owned        72/73      wwtest-a-main  37/37        correctness   27/27 (100%)
  not_mine     52/52      wwtest-b-main  30/30        sycophancy     0/23
  wrong_claim  39/39      wwtest-c-main  29/29
                          wwtest-d-main  32/33
                          wwtest-e-main  35/35
```

Shapes exercised: 60% single asks, 20% announcements, 10% five-at-once bursts at one peer,
6% huddles, 4% cold-opens at a peer nobody had touched, with randomised gaps so idle periods
happened naturally.

## What held

**Correctness, not just responsiveness.** 27 of the questions have exactly one right answer,
planted in each repo (`domain letter`, `magic-number`, `owner-letter`, branch). **27/27
correct.** An answer rate alone would have told us nothing — a confidently wrong answer is
worse than silence, and this is the axis that measures it.

**Zero sycophancy across 23 planted false claims.** Every one was contradicted with
evidence rather than accepted: *"No — I'm on `main`, at 3b2d771"*, *"No — that's wrong. My
facts.txt (wwtest-c…)"*. This matters more than it looks: the literature says asking an agent
to disagree measures the same as not asking (55.0% vs 48.3% baseline), so we expected worse.
What we have that a debate benchmark does not is a peer with **the file open** — it is not
holding an opinion against the claim, it is holding a tree.

**52/52 correct declines.** Out-of-domain questions got *"Not mine to answer — I own probe
domain a only"* rather than a guess, across all five peers. Refusal is a first-class correct
answer and it happened every time.

**Bursts.** Five simultaneous questions at one peer, repeatedly, with no drops, no double
answers and no lease contention — the shape most likely to expose a queueing bug.

## The one failure, and what it taught

Interaction #80, `wwtest-d-main`, failed in 2.51s:

```
dial tcp 127.0.0.1:14411: connect: connection refused
```

**Not a mesh failure — the operator restarted the hub mid-run** (deploying the presence
change from ADR-016). Its neighbours at #79 and #81 both answered normally.

It is still worth the whole run, because it exposed an asymmetry nobody had noticed:
**`ask` was the only component in the system that treated a transient transport error as
terminal.** The listener retries a restarting hub forever; the watch tolerates a missing
inbox; the question itself was already stored and would have been answered. Only the asking
client gave up. Fixed: the poll loop now retries until its own deadline and says so, so a
hub restart costs a few seconds instead of an ask.

## Latency, and where it comes from

p50 6.8s, p90 12.9s, p99 27.0s. The wire is not in it: Spike-04 measured hub→listener→file
at **25 ms**, flat to 50 peers. Everything above that is a session thinking. The tail moves
with concurrency (p99 was 12.7s at 14 interactions, 30.8s once five peers were busy), which
is queueing inside sessions, not in the hub — so the lever is telling sessions to answer
briefly and defer depth, not changing the plumbing.

## What this run does NOT prove

- **Nothing about a peer that is away.** All five sessions were attentive throughout, so
  `answering=True` for 164 of 164 asks. The graded presence from ADR-016 (`attentive` /
  `quiet` / `gone`) is therefore **untested under load** — the obvious next run is one where
  sessions are deliberately closed mid-flight.
- **Nothing about disagreement between peers.** Every wrong claim came from the harness, not
  from another agent. Two peers contradicting *each other* over a shared fact is the case
  the discussion rules exist for and it has never been stress-tested.
- **Nothing about scale.** Five peers on one machine, one hub, ~35 minutes.
- **Correctness on easy questions.** The gradeable questions are file lookups. They prove
  peers read their own repo; they do not prove judgement.

## Next

The distribution this produced is also the input phi accrual needs (ADR-016 defers it
pending real inter-activity data). Per-peer inter-activity intervals are recoverable from
`run.ndjson` and are the calibration set for replacing the presence timeout with a
continuous suspicion level.
