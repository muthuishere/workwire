# Spike-05 findings — how to build hub observability

Date: 2026-08-01 · macOS arm64, Go 1.26.3 · `go run ./spikes/05-observability [s1…s8]`
Measured against the real `internal/store`, not a model of it.

## Summary

| probe | question | answer |
|---|---|---|
| S1 | what does a quiet mesh actually make you ask? | 8 questions; **6 are state we already hold** |
| S2 | is an on-demand scan cheap enough to poll? | **yes** — 0.02 ms at 100, 1.43 ms at 50 000 envelopes |
| S3 | what does per-agent `pending` cost? | draft was **O(peers × envelopes)** — 13.8 ms at 100 peers; one pass is **0.86 ms** |
| S4 | counters at `Append`, or scan on demand? | **scan** for store facts; counters only for what has no store |
| S5 | does `/metrics` disturb sends? | 0.03 ms → 0.10 ms while hammered. Holding the lock is fine |
| S6 | do the numbers survive retention? | mostly — one real gap (`bytes`), and one wrong alarm of my own |
| S7 | can the payload carry a secret? | storage half clean; **the registry half is the risk** |
| S8 | what is diagnosable with the hub DOWN? | everything about your own side — argues for a `doctor` verb |

## S1 — the eight questions, and where each is answered

```
Is the hub even up?                    -> uptime, startedAt              (process)
Did my message reach the hub?          -> envelopes, lastSeq, newestTs   (store)
Was it addressed to the peer I meant?  -> perAgent.delivered             (store)
Is that peer collecting it?            -> perAgent.pending, cursor       (store+registry)
Is anything attached to ANSWER?        -> perAgent.listener, answering   (registry)
Is the thread refusing sends?          -> threads.stalled                (store)
Are we losing history to retention?    -> oldestTs, segments, bytes      (store+fs)
Is the hub struggling?                 -> inflightPolls, p99             (server)
```

Six of eight are already in the store or the registry. Only in-flight polls and latency
need new instrumentation. **Observability here is mostly an exposure problem, not a
collection problem** — which is why it is worth building now rather than deferring.

## S2 — a full scan is pollable

| envelopes | `Stats()` |
|---|---|
| 100 | 0.02 ms |
| 1 000 | 0.03 ms |
| 10 000 | 0.15 ms |
| 50 000 | 1.43 ms |

Sub-millisecond to ten thousand, and 1.4 ms at fifty thousand. A 5-second poll costs
nothing measurable. **Build the scan; do not build a counter cache.**

## S3 — the per-agent shape is where it goes wrong

Computing `AgentStats` per peer, as first drafted, walks every envelope once **per peer**:

| peers | 20 000 envelopes |
|---|---|
| 5 | 0.95 ms |
| 25 | 2.98 ms |
| 100 | 13.8 ms |

Linear in peers × envelopes. It looks free at today's nine peers and is not the shape to
ship. **One pass computing every peer at once** is the same information for the cost of the
single scan in S2 — and the rebuilt `Snapshot(cursors)` measures flat:

| peers | one pass, 20 000 envelopes |
|---|---|
| 5 | 1.03 ms |
| 25 | 0.87 ms |
| 100 | **0.86 ms** (was 13.8 ms) |

Sixteen times cheaper at 100 peers, and no longer growing with them.

## S4 — scan, except where there is nothing to scan

A counter read is free, but a counter is a second source of truth that retention and
tombstones must both remember to decrement, and a drifting counter is worse than no counter
— it is a confident wrong answer at 2am. Given S2, the scan is cheap enough that the
correctness risk buys nothing.

**Counters only for facts with no store behind them**: in-flight long-polls, request counts,
latency percentiles. Those cannot be derived, so they must be recorded.

## S5 — `/metrics` may hold the store lock

Send latency with no metrics traffic: **0.03 ms**. Send latency while `/metrics` is polled
in a tight loop: **0.10 ms**. Three times worse and still two orders of magnitude below
anything a human or a long-poll notices. No snapshot machinery needed.

## S6 — one real gap, and one alarm I raised wrongly

First run, reported as a bug:

```
before retention: envelopes=2000 bytes=69148 segments=2 oldest=2026-08-01T03:00:12
after  retention: envelopes=109  bytes=69148 segments=2 oldest=2026-08-01T03:00:12
```

**The frozen `oldestTs` was my instrument, not the code.** The probe printed timestamps
truncated to the second, and all 2000 envelopes were written inside the same second, so a
field that *was* moving looked frozen. At full precision, against the rebuilt `Snapshot`:

```
before: envelopes=2000 minSeq=1    oldest=2026-08-01T03:01:52.025416Z
after:  envelopes=109  minSeq=1892 oldest=2026-08-01T03:01:52.095773Z
oldestTs MOVED FORWARD — retention is visible in the payload
```

That is the second measurement error these spikes have produced (the first was the 23.7 s
delivery figure in Spike-04), and both had the same cause: an instrument that could not see
the thing it was asked about, reporting confidently anyway.

**What IS real**: `bytes` and `segments` did not move, because they are a filesystem fact and
retention had evicted from memory without yet removing a segment file. Disk legitimately
lags the index — but it must be documented in the payload's meaning, or an operator reads
"69 KB" as "history intact" while most of it is unreachable. `minSeq` makes the eviction
unambiguous, so it is now a field.

The rewrite stands on its own merits regardless: every storage number now comes from ONE
authoritative retained set in a single pass, rather than the thread index, which holds
references to envelopes retention has already dropped.

## S7 — assert the absence of secrets, do not hope for it

The storage half of the payload has no credential-shaped field. The **registry** half is the
real exposure: `Agent` carries `SecretHash`, and any `map[string]any` built by ranging over
agent structs can pick it up by accident. This belongs in a test that greps the marshalled
payload for `secret`/`token`/`hash`, not in a spike that runs once.

## S8 — the hub is the most likely thing to be down

Every local fact needed to answer "is my side alive?" is already on disk: both configs, both
hub logs, credentials, folder bindings, run locks, session inboxes. On this machine the
probe read all eight without a hub.

`/metrics` cannot answer a question about a dead hub. **A `doctor` verb reading local state
only** — is the service loaded, when did the hub log last move, which listeners hold locks,
which session inboxes have unread bytes — answers exactly the case where the endpoint
cannot, and it is the first thing anyone will reach for.

## What this hands to the implementation

1. One-pass `Stats()` + all-peer stats from a single authoritative retained set (S3, S6).
2. No counter cache for store facts; counters only for in-flight polls and rates (S2, S4).
3. `/metrics` may take the store lock (S5).
4. `oldestTs` and `minSeq` are mandatory and must be proven correct **after** retention (S6);
   `bytes`/`segments` are documented as a disk fact that lags eviction.
5. A payload-greps-clean test, aimed at the registry half (S7).
6. A `doctor` verb that works with the hub down (S8).
