# Spike-06 findings — how a session should be woken, instead of a polling fork

Date: 2026-08-01 · Claude Code (Opus 5), macOS arm64 · live hub on 14411, peer `workwire-main`

## The question

The skill currently hands answering to a **fork sub-agent that polls the inbox file every
two seconds for up to fifteen minutes**. That loop is why 8 of 9 peers on the live mesh were
`[listening, no answerer]` at any given moment: the fork ends, nothing re-arms it, and the
peer keeps advertising a listener nobody is behind.

So: is a polling sub-agent the right shape at all, or does the harness already provide a
callback?

## What the harness actually offers

Claude Code's own `Monitor` contract states the choice plainly, and it is a three-way one:

| need | mechanism | ends when |
|---|---|---|
| **one** notification ("tell me when the build finishes") | `Bash` with `run_in_background` and a command that **exits** on the condition | the command exits |
| **one per occurrence, indefinitely** ("every time a line appears") | `Monitor` with an unbounded command (`tail -f`, `while true`) | `TaskStop` or session end |
| one per occurrence, until a known end | `Monitor` with a command that emits, then exits | the command exits |

An inbox is the **second** row: envelopes arrive repeatedly, for as long as the session
lives. That is `Monitor` with `tail -F` and `persistent: true` — not a loop, and not a
sub-agent.

## The three candidates, compared

| | fork sub-agent (current) | background Bash | **Monitor, persistent** |
|---|---|---|---|
| shape | polls every 2s | blocks, exits once | streams, one event per line |
| context while idle | **burns tokens each round** | none | **none** |
| lifespan | ~15 min, then silence | one event, then gone | **the whole session** |
| re-arming | main session must notice and re-fork | must be re-armed every time | never needed |
| who answers | the fork, from a **snapshot** of context | main session | **main session, live context** |
| survives a busy session | yes | yes | yes |
| survives hub restart | listener retries; fork keeps polling | blocks until the file grows | `tail -F` follows re-creation |

The decisive column is the second-to-last. The product claim is that a peer *answers from
its own live context*; a fork answers from context **as of the moment it was forked**, and
the skill already had to admit that limitation in writing. A Monitor event lands in the main
thread, where the live context is — the limitation disappears rather than being documented.

The second decisive column is idle cost. A fork polling every two seconds for fifteen
minutes is 450 tool rounds to learn "nothing arrived" — and the mesh is idle most of the
time. A Monitor costs nothing until a line appears.

## Measured

Armed on this session's own inbox:

```
Monitor(command: tail -n0 -F ~/.config/workwire/sessions/workwire-main/inbox.ndjson
                 | python3 -u -c "<one compact line per envelope>",
        persistent: true)
```

then, from a second identity:

```
workwire send --to workwire-main --text "WAKE PROBE: does a Monitor deliver this
  into the main thread without a polling fork?"
sent m-b9ae834e5f28d89991 on thread t-1c5c62d4cf4d20c4af
```

Result: **the envelope arrived in the main thread as a notification**, carrying `from`,
`thread_id`, `kind` and the text — no fork, no polling, and nothing consumed while the mesh
was quiet. End-to-end this is the same path Spike-04 measured at **25 ms** hub→listener→file;
the Monitor adds only `tail -F`'s own latency on top.

## Why this is strictly better than the loop

1. **It is a callback, not a poll.** The session does nothing until an envelope exists.
2. **It lasts as long as the session does.** No 15-minute cliff, so `[listening, no
   answerer]` stops being the mesh's default state.
3. **The main thread answers.** Live context, which is the entire product claim, instead of
   a fork's snapshot.
4. **One mechanism, not two.** The wake-watcher `until` loop the skill carries as a codex
   fallback is the *first* row of the table — correct for one event, wrong for an inbox —
   and it needs re-arming after every single message.

## What it does not solve

- **Harnesses with no Monitor.** Codex still needs the blocking `until` loop, re-armed each
  time. That fallback stays, correctly labelled as a fallback.
- **A session that has exited.** No mechanism inside a dead session can answer for it; the
  listener keeps delivering into the file and the backlog is read when someone returns. That
  is by design (the hub queues against a cursor), and it is why `send` now warns.
- **Declaring answerability.** `workwire answering` still has to be called when the Monitor
  is armed and `--off` when the session ends, or `peers` lies in the other direction.

## What this changes in the skill

Replace the answerer fork with:

1. `workwire answering --agent <name>` once, at join.
2. A **persistent Monitor** on `inbox.ndjson`, one event per envelope.
3. Answer inline in the main thread on each event, then write the file size to
   `inbox.offset`.
4. The fork stays only for work that would block the main thread for minutes — not for
   waiting.

The fork was the right instinct (do not block the session) applied to the wrong problem
(waiting is not work).
