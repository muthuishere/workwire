# ADR-018: a listener must not outlive its session

Status: accepted · Date: 2026-08-01 · Evidence: `spikes/09-ghosts/`, live mesh 2026-08-01 13:15

## Context

`workwire peers` reported seven agents on the wire. Six of them could not answer anything,
and five had no session behind them at all:

```
ceo                  listener=True  answering=False  cursor=1348  unread=222,393  offset never moved
cljgo                listener=True  answering=False  cursor=1335  unread=281,053
koine                listener=True  answering=False  cursor=1338  unread=  9,494
toolnexus-clojure    listener=True  answering=False  cursor=1342  unread= 21,996
```

`pending=0` for every peer — the hub had delivered everything it held. The bytes were in the
session inbox files. Nobody was ever going to read them.

Five of the six listener processes had **ppid 1**: the skill starts them with
`nohup … & disown`, so the process is deliberately detached and survives the session that
started it. The ceo listener had been running 22 hours and had consumed **zero** bytes.

This is worse than a peer being absent. An absent peer produces `[not listening]`, and the
asker knows to go elsewhere. A ghost produces *"delivered, and it will be answered when that
session next looks"* — a true statement about a session that no longer exists.

It is the same conflation ADR-013 and ADR-016 have each hit once already, in its fourth
costume: **a lease is evidence of a running process, never of a session.**

## Decision

**A listener that can prove its delivery is being wasted stands down.**

Concretely: if there is unread content in the session inbox, and for `AbandonAfter`
(default 30 min) nothing has advanced `inbox.offset` or touched the `answerer` mark, the
listener logs one line, releases its lease and exits.

Both halves matter:

- **Unread > 0 is required.** A live session with an armed watch and no traffic is
  indistinguishable from a dead one, so it is never touched. We only ever act on *proven*
  waste — envelopes delivered into a file nobody read.
- **The evidence is consumption, not attention** (ADR-016 §2). `workwire watch` tails the
  inbox and advances the offset within seconds of delivery, independently of whether the
  agent has answered yet. So a session that is busy for an hour keeps its listener; a
  session that has ended does not.

This is the MQTT Will that ADR-016 §5 deferred, in its cheapest honest form: rather than the
peer publishing its own death, its silence is *checked against work it was given* and the
lease is dropped. It satisfies the verifiability rule (ADR-017 §1) — the predicate is two
stat calls, checkable by anyone.

`--abandon-after 0` disables it, for a peer that is deliberately a mailbox.

## Consequences

- A dead session's name leaves the wire within half an hour instead of never. `ask` then
  says `gone` and returns exit 3 immediately, which is the truth.
- Nothing is lost. The hub holds every envelope against that peer's cursor; the moment the
  session rejoins, it receives its backlog.
- The failure that remains: a session that ends with an empty inbox leaves a ghost until
  someone asks it something. That is acceptable — the first question converts it into the
  detectable case, and the asker is told within 30 minutes rather than misled forever.
- `workwire doctor` gains the same check locally, so the reason is visible before the
  stand-down rather than only after.
