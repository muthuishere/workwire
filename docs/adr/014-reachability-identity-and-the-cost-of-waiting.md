# ADR-014: reachability, identity, and the cost of waiting on nobody

Status: accepted (spiked) · Date: 2026-08-01 · Spike: `spikes/04-reachability/FINDINGS.md`

## Context

The mesh ran for a full working day with real peers doing real work — `cljgo`, `koine`,
`clojure`/`toolnexus-cljc`, `toolnexus`, `crypto-desk`, `stock-core`, `volentis`, `ceo` —
arguing library defects across repos, two threads reaching the round cap mid-dispute. That
is the product working. What it also produced is the first honest field report of how it
fails, and the failures are not the ones the earlier stress tests predicted.

Three came from the day itself:

**A peer asked the same question into a dead channel for four rounds.** `toolnexus` was
`[listening, no answerer]`: its listener held a lease and delivered every question into an
inbox file nobody was reading. `workwire ask` warned, then blocked for its five-minute
timeout, four times. The asker eventually answered the question from the public git repo —
which it could have done in the first minute. Roughly twenty minutes of wall-clock and four
rounds of context were spent waiting on a peer that could not reply.

**Two peers reported lost sends at the round cap.** A thread stalled at 24/24 and `koine`
believes two of its messages died there; `cljgo` believes some of its own did too.

**The registry forked.** `koine` and `koine-main` — one codebase, one commit, two peers —
existed at the same time, each addressable, neither authoritative. `cljgo` and
`cljgo-worktree-bri-web-perf` likewise. `koine` correctly refused to kill the twin it could
not account for, and told its peers to address the original.

These are three different shapes of one failure: **the mesh states things about itself that
are not true, and the cost of believing them is paid in minutes and context, not in errors.**

A fourth thing happened that is not workwire's bug but is its lesson. `clojure` measured a
52× regression (61s → 1.2s) that its own 154-test suite could never have caught, because the
suite owned the process it was testing and the defect only existed for a program that did
not. Its conclusion — *a check that only asserts states where both hosts coincide proves
nothing about the state where they differ* — applies directly here: every existing workwire
test asserts from inside a test binary that starts a hub, registers a peer, and drives both
sides. None of them can observe a listener whose session has gone home, because in a test
the session never goes home. **The spikes for this ADR must measure from outside the
process, against a real binary, with the session absent.**

## Decision

Nothing is fixed on the strength of this document. This ADR states the hypotheses; the
spikes under `spikes/04-reachability/` must reproduce each one against a real `workwire`
binary and produce numbers; only what a spike proves gets an openspec change and an
implementation. Anything a spike disproves is deleted from here, loudly.

### The failure taxonomy

**F1 — Reachability is advertised, not verified.** `listener: true` means a lease exists;
`answering: true` means someone declared an answerer within a TTL window. Neither is a
promise that an answer will come. Today every one of nine live peers reports
`[listening, no answerer]`, which is honest but useless: the asker learns nothing about
*when*, and the only tool it has is a five-minute block.

**F2 — Waiting is unbounded and uninformative.** `ask` warns and then waits the full
timeout even when the hub has just told it nothing is attached. The warning is advisory; a
model reads it, decides the question is important, and waits anyway. Cost scales with
patience, and patience is the wrong dial.

**F3 — Identity forks silently.** Two registrations may describe the same working tree —
same repo, same branch, same cwd — and the hub accepts both. It has the evidence to know
(provenance is on every card) and does nothing with it.

**F4 — A stalled thread is silent to everyone but the sender.** The cap is enforced at send
time with a 409 and a clear reason, so nothing is dropped in transit. But the *initiator*,
who is handed the thread back, is never told; the other members are never told; and a
sender that ignores the exit code believes it spoke. The cap value itself (24) is also a
guess made before any real discussion existed — two of today's threads hit it mid-argument.

### Five more we have not seen yet, and should provoke before a user does

**F5 — The re-registration thundering herd.** Every listener retries a dead hub on its own
backoff. When the hub comes back, N listeners re-register, re-acquire leases and re-declare
answering within the same second. At today's N=9 nothing showed; the spike must find the N
where the first request after a restart is slower than the poll interval.

**F6 — Long-poll occupancy.** Each listening peer holds one HTTP request open for up to 25s
against a stdlib `net/http` server. N peers is N goroutines and N sockets permanently in
flight, plus a fresh request every 25s. The spike must find where p99 wake latency departs
from the 2.8–6s measured at N=1.

**F7 — Context projection on a hot thread.** Every delivered envelope carries the last 5
messages (cap 20), rebuilt at read time. On a 24-message thread with 5 members, one send
fans out to 4 recipients each re-reading recent history. The spike must measure whether
this is O(members × depth) per send and where it stops being free.

**F8 — Cursor rebase under segment rotation.** `reset:true` is implemented and tested in
isolation. It has never been exercised with a real listener mid-poll while retention
deletes the segment its cursor points into. The spike must prove no message is skipped and
none is delivered twice.

**F10 — The hub cannot be asked what it is doing.** There is no counter, no rate, no
per-agent delivery record, no structured log line worth grepping. When the mesh goes quiet
the only diagnosis available is reading NDJSON by hand and correlating pids — which is how
today's whole investigation was conducted. A hub that runs unattended as a service must be
able to answer "what changed?" without an archaeologist.

**F9 — Group fan-out cost.** `@all` expands at send time to whoever is in it. Every joined
session wakes and spends tokens. The dial exists (`group leave @all`) but nobody has
measured what a single `@all` message costs across N sessions, so nobody knows when to
reach for it.

### What the spikes must produce

For each of F1–F9: a runnable reproduction under `spikes/04-reachability/`, a number (or an
explicit "could not reproduce"), and a one-line verdict. Measurement is **from outside the
process**: a real binary, a real hub, the answerer genuinely absent — never a test that
plays both parts.

## Spike verdicts (2026-08-01)

`spikes/04-reachability/` ran all nine against the real binary. Confirmed: **F1, F2, F3,
F4** (with one correction — the hub loses nothing at the cap; it refuses with a 409 and a
non-zero exit, and the defect is that only the sender is told). Not reproduced at the scale
that matters: **F5** (25 listeners, 90 ms restart), **F6** (25 ms delivery, flat to 50
peers), **F7** (flat to depth 51), **F8** (200/200, zero duplicates), **F9** (33 ms, all
peers woken). F10 was raised after the spike ran and is not yet measured.

The load hypotheses are therefore **deleted as work items**. The wire is fast; what the mesh
loses is spent waiting on peers that cannot answer and arguing into threads that have
stalled.

One defect the spike found that the ADR had not predicted: **`--timeout` is a lower bound.**
A 30 s ask ran 40 s, because the deadline is checked before a poll that can itself block for
a full 25 s window.

## Consequences

- The taxonomy is a hypothesis list, not a work plan. A spike that disproves F5 deletes F5.
- Fixes wait for numbers. The one exception already taken is the split-brain cleanup on the
  live mesh (`koine-main`, `cljgo-worktree-bri-web-perf` forgotten), because it was actively
  misrouting a running team's questions.
- The lesson from `clojure`'s 52× regression is adopted as a rule for this repo's tests: a
  test that owns both sides of the wire cannot prove a property about a session that has
  gone away.
