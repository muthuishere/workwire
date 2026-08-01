# Spike-04 findings — reachability, identity, and the cost of waiting

Date: 2026-08-01 · Platform: macOS arm64 (M-series), Go 1.26.3 · Hub: the real
`workwire` binary at `d6cffb8`, spare port 14491, own config/data dirs. The live hub on
14411 was never touched. Reproduce: `./run.sh [f1 … f9]`.

Every measurement is taken **from outside the process**: a real hub, a real listener, and —
for F1/F2 — no session behind the listener at all. Nothing here is a test that plays both
parts.

## Summary

| id | hypothesis (ADR-014) | verdict | number |
|---|---|---|---|
| F1/F2 | ask waits on a peer that cannot answer | **CONFIRMED**, worse than stated | waited 40s on a `--timeout 30s`; exit 124 |
| F3 | identity forks silently | **CONFIRMED** | 2 peers, one tree, both accepted |
| F4 | a stalled thread is silent to everyone but the sender | **CONFIRMED** (send loss: DISPROVED) | 409 + exit 1 on every send; 0 notices to the initiator |
| F5 | re-registration herd after a hub restart | **not reproduced** | 25 listeners: `/health` 90 ms, `/agents` 26 ms |
| F6 | long-poll occupancy degrades wake latency | **not reproduced** | 25–27 ms delivery, flat from 1 to 50 peers |
| F7 | context projection cost grows with thread depth | **not reproduced** | 20–24 ms, flat from depth 2 to 51 |
| F8 | cursor rebase loses or duplicates under rotation | **not reproduced** | 200 sent → 200 delivered, 0 duplicates |
| F9 | `@all` fan-out is expensive | **not on the wire** | 33 ms send, 25/25 peers woken |

**The wire is not the problem. Attachment is.** Delivery is ~25 ms and stays there at 50
peers. Every minute the mesh actually loses is spent waiting on a peer with no answerer
attached, or arguing into a thread that has stalled.

## F1/F2 — the cost of waiting on nobody · CONFIRMED

A listener with no session behind it — the state 9 of 9 live peers were in this morning:

```
agent lonely  muthuishere/workwire@main d6cffb8*  [listening, no answerer]
warning: lonely is listening but nothing is attached to answer (last seen 2s ago)
asked lonely (thread t-eb70191f4f19f0e5d9); waiting for the answer...
F1: exit=124 waited_ms=40034
F1: the question IS delivered — 1 line in sessions/lonely/inbox.ndjson
```

Two distinct defects, one of them new:

1. **The hub tells the truth and the client ignores it.** `answering:false` is in the ask
   response. The CLI prints a warning and then blocks anyway. In the field this cost four
   rounds × 5 min default = ~20 minutes for one answer that was in a public git repo.
2. **`--timeout` is a lower bound, not an upper bound.** A 30 s timeout ran 40 s: the wait
   loop checks the deadline *before* each poll, and each poll can block for `waitDefault`
   (25 s). Worst case overshoot is one full poll window. `timeout 40` killed it — the
   command would not have returned on its own until 50 s.

The question is genuinely delivered, so failing fast loses nothing.

## F3 — split-brain · CONFIRMED

Two listeners, one directory, two names. Both cards carry identical provenance
(`muthuishere/workwire@main d6cffb8*`, same cwd) and the hub accepts both:

```
agent koine       muthuishere/workwire@main d6cffb8*  [listening, no answerer]
agent koine-main  muthuishere/workwire@main d6cffb8*  [listening, no answerer]
F3: both accepted? 2 registration(s) for one dir
```

This is exactly what happened to the real `koine` today, and it is detectable with data the
hub already holds — `origin.cwd` plus `repo@branch` are on every card. Local `folders.json`
catches it only when the same *name* is wanted twice; it cannot see one folder under two
names.

## F4 — the round cap · CONFIRMED, but nothing is lost in transit

With `maxThreadMessages=4`:

```
send 1..3 -> exit=0
send 4    -> exit=1  send failed (409): thread … is stalled: it reached the per-thread cap …
send 5,6  -> exit=1  (same)
F4: stall notices in the initiator's inbox: 0
```

So the reported "lost sends" were **not** dropped by the hub — every one was refused with a
409, a clear reason, and a non-zero exit. What is true and is the real defect:

- The **initiator is never told** the thread it owns has stalled, even though the error text
  says it is "handed back to its initiator". Nothing is handed to anyone.
- **No other member is told.** They keep composing into a thread that will refuse them.
- A sender that does not read stderr or check the exit code believes it spoke. Two peers
  independently reached that conclusion today, which makes it a design problem, not
  operator error.
- The cap value (24) was picked before any real discussion existed. Two of today's threads
  hit it mid-technical-argument; one is still `stalled` at 24/24.

## F5–F9 — the load hypotheses, all disproved at this scale

- **F5 herd**: 25 listeners, hub killed and restarted. `/health` answered 90 ms after
  restart, `GET /agents` 26 ms, all 25 peers back. The exponential backoff in the listener
  is doing its job.
- **F6 occupancy**: mean delivery 25 / 26 / 27 / 25 ms at 1 / 10 / 25 / 50 long-polling
  peers. Flat. `store.Watch()` wakes pending polls immediately — verified independently
  with a raw `curl` long-poll that returned 2.02 s after a send issued at t+2 s.
- **F7 projection**: 24 / 24 / 20 / 20 ms at thread depth 2 / 11 / 26 / 51. Flat.
- **F8 rotation**: 2 KB segments, 4 KB retention, 200 messages sent while a listener
  polls — 200 delivered, 200 unique ids, 0 duplicates, 0 gaps.
- **F9 `@all`**: 33 ms send, 25/25 peers woken. The hub cost is nil; the cost of `@all` is
  entirely the tokens each woken session spends, which is a skill-side dial, not a hub one.

## A measurement error worth recording

The first F6 run reported **23.7 s** delivery, flat across peer counts — and it was wrong.
The script captured the inbox file's baseline size *after* the send, so a delivery that had
already happened could never register as growth, and every run simply timed out its own
20 s polling loop. The fix is one line; the lesson is the one `clojure` paid 52× for today:
**an instrument that can only observe one side of an event will happily report a number for
the side it cannot see.** Corrected figure: 25 ms.

## What this hands to openspec

1. `ask` must not block on a peer with no answerer, and `--timeout` must be an upper bound.
2. The hub must refuse (or flag) a second live registration for a working tree it already
   has, using the provenance it already stores.
3. A thread that stalls must notify its initiator and its members; the cap needs a default
   informed by real discussions rather than a guess.
4. No scaling work is justified by evidence. Anything proposed for F5–F9 is speculation
   until a spike reproduces it.
5. Not covered here and needed next (raised while this ran): **the hub cannot currently be
   asked what it is doing.** There is no counter, no rate, no per-agent delivery record — so
   "why did nothing arrive" is answered by reading NDJSON by hand. See ADR-014 F10.
