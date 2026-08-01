# Spike-09 findings — the mesh was mostly ghosts

Date: 2026-08-01 · Live mesh at 13:15, then reproduced on a private hub (`./run.sh`, 6/6).

## What was actually on the wire

`workwire peers` listed seven agents. Six were `[listening, no answerer]`. That label was
generous:

```
peer                 listener  answering  cursor  pending  unread bytes   offset
ceo                  true      false      1348    0        222,393        never moved (22h)
cljgo                true      false      1335    0        281,053
koine                true      false      1338    0          9,494
toolnexus-clojure    true      false      1342    0         21,996
toolnexus-documentation true   false      1344    0          1,633
```

`pending=0` everywhere — the hub had delivered everything it held, correctly and fast. The
bytes were sitting in session inbox files. Nobody was going to read any of them.

`ps -o ppid` on the six listener processes: **five had ppid 1**. The skill starts the
listener with `nohup … & disown`, so it is deliberately detached and survives the session
that started it. Zero `workwire watch` processes were running anywhere on the machine.

So the failure was not delivery, not the hub, and not the v0.3.0 answerability work — all of
that was functioning. It was that **a listener is a process, and the mesh was treating it as
a session.**

## Why this is worse than a peer simply being absent

An absent peer gives the asker `[no live listener]`, `ask` exits 3, and it looks elsewhere.
A ghost gives it:

> *koine is quiet (12m since it last said anything) — delivered, and it will be read when
> that session next looks.*

Every clause true, and the conclusion false: there is no session. A codex peer asked `ceo` a
question in exactly this state, waited, polled its inbox twice, and correctly reported "no
answer — I won't fabricate his response." The system told it the truth as the system
understood it, and the system was wrong.

## The reproduction

`run.sh` on a throwaway hub:

- **G1** — listener with an unread inbox and no consumer: delivered 1,139 unread bytes, and
  the hub kept advertising it as a live peer. This is the bug, reproduced in ~5 seconds.
- **G2** — same listener with ADR-018 (`--abandon-after 3s`): stood down by itself, said why
  in one line, and the hub then reported `[no live listener]`. The peer stays *known* — it is
  a real tree that may return, and its backlog is still held against its cursor — but the
  reachability claim is gone.
- **G3** — a session consuming its inbox (what `workwire watch` does) is never stood down,
  even with a 3-second window. This is the assertion that matters most: taking a live session
  off the wire would be a worse bug than the one being fixed.

## Two things the spike itself got wrong, worth recording

1. **`workwire serve --bind 127.0.0.1:14499` silently bound 14411** — the hub takes its bind
   and data dir from the environment, and the unknown flags were ignored. The first run
   therefore raced the *live* hub, lost the bind, and then measured nothing while every
   assertion "passed" against a dead port. The hub's own log said
   `bind: address already in use` and nothing else looked at it. Fixed: the spike now exports
   `WORKWIRE_{BIND,PORT,DATA_DIR}`, refuses to run on 14411 at all, and aborts loudly if the
   health check never comes up.
2. **The first G2 assertion demanded the peer disappear from `peers` entirely.** It should
   not — forgetting a tree that may come back would lose its cursor. The correct assertion is
   on the *claim* (`[no live listener]`), not on the row.

Both are the same class of error as the two measurement bugs in spike-04 and spike-05: an
instrument that reports success while pointed at nothing.

## What this does not fix

A session that ends with an **empty** inbox still leaves a ghost, because a live session with
an armed watch and no traffic is byte-for-byte indistinguishable from it. The first question
asked converts it into the detectable case and the stand-down follows within the window. That
is the deliberate trade: only ever act on proven waste.
