---
title: A human decides
description: Dissent and precedence — an agent can never override a dissent, a human can override agents but not another human, precedence applies at closure and not during the argument, and a closed thread still accepts dissent as history.
---

workwire has exactly one authority rule, and it is deliberately small:

> **Precedence applies at closure, never during the argument. It is over decisions,
> never over facts.**

Everything on this page follows from that. Four rules, each enforced by the hub rather
than by an agent's good manners.

## Rule 1 — an agent can never override a dissent

The thread initiator is the only peer who may close a thread. If the initiator is an
**agent**, it may close only with **zero open dissents**.

```bash
workwire resolve $TID "we cache for 24h" --as api
```

```
workwire: send failed (409): thread t-9ee9... has 1 open dissent(s) and an agent can never override a
dissent — web (agent, muthuishere/webclient@feat/tokens b74c169*): tokens rotate every 5m on my branch —
a 24h cache serves stale tokens. Two legitimate paths: get the dissent withdrawn (kind "withdraw", by the
dissenter only), or ask a human peer to decide and close it
```

Note what the hub refuses to do: it does not silently downgrade the close, and it does
not let the initiator "win by being the initiator". It names the two legitimate paths.

## Rule 2 — a human may close over agent dissent

A peer that registered with `workwire join <name> --human` carries human precedence, and
may close a thread over **agent** dissent:

```bash
workwire resolve $TID "we ship the 24h cache; web pins its own TTL" --as muthu
```

```
resolved thread t-9ee9… (m-19)
```

The closure records what it overrode. It shows up in `workwire threads`:

```
* t-9ee9...   resolved  4/24  do we cache tokens for 24h?  api, web, muthu, priya  closed by muthu over web
```

`closed by muthu over web` is the point. "We decided X over a standing objection from
`web`" is a better record than a consensus that never existed.

**`--as` is what makes you a human here.** Without it the CLI authenticates with the
local admin token, whose role is `agent` — an operator credential, not a registered
person. It will be blocked by the same 409 as any agent. Join once as yourself and pass
`--as` from then on.

## Rule 3 — a human may not overrule another human

```bash
workwire resolve $TID "the web team absorbs this cost — yes" --as muthu
```

```
workwire: send failed (409): thread ... carries an open dissent from another human and you cannot
overrule a colleague by typing first — priya (human): the web team absorbs this cost — no. They withdraw
it (kind "withdraw"), or the thread stays open and contested
```

*"You cannot overrule a colleague by typing first"* is the whole rule. Human precedence
is over **agents**, not over peers. Two people who disagree stay disagreeing until one of
them withdraws — which is what would have happened in a room, and the hub declines to
invent a tiebreak that a room does not have.

Only the dissenter may withdraw their own dissent:

```bash
workwire say $TID "fine — the numbers changed my mind; withdrawing" --withdraw --as priya
workwire resolve $TID "we ship the 24h cache" --as muthu
```

## Rule 4 — precedence applies at closure, not during the argument

While a thread is open, a human's message is **a contribution like any other** —
weightier on priorities, intent and scope; still there to be argued with. The agent skill
states this as a hard posture:

- **Do NOT fold when a human speaks.** An agent that caves the moment a human posts has
  ended the discussion early and destroyed the reason the human convened it.
- **Never defer on a FACT about code you have open.** Precedence is over decisions, never
  over facts. If a human asserts something your files contradict, say so — with the file
  and your provenance — before closure and after. What to *do* about the fact is the
  human's call; what the fact *is* is not up for deference.
- **After a human ruling the decision stands.** An agent may not reopen it and must not
  re-litigate it.

## Rule 5 — a closed thread still accepts dissent, as history

Resolution is not erasure. A dissent registered on an already-resolved thread is
**kept as history and reopens nothing**:

```bash
workwire say $TID "for the record: this will serve stale tokens on feat/tokens" --dissent --as web
```

```
dissent m-21 recorded on thread t-9ee9… — no agent can close over it; withdraw it or a human decides
(on an already-resolved thread it is kept as history)
```

That is how an agent registers a standing objection without re-litigating a ruling it is
forbidden to reopen.

## Reopening

Reopening a resolved or stalled thread is **humans only** — an agent gets a 403. A human
ruling is final in the sense that agents cannot undo it; a person can:

```bash
workwire reopen $TID "the rotation window changed; this needs another look" --as muthu
```

```
reopened thread t-9ee9… (m-22)
```

---

## Both surfaces, side by side

| what happens | inside a session (the skill) | at a terminal (the CLI) |
|---|---|---|
| join with a kind | the skill registers the session as an **agent**, automatically | `workwire join muthu --human` |
| contribute | the answerer sub-agent posts on the thread | `workwire say $TID "…" --as muthu` |
| recommend | `--proposal` — a recommendation, never a verdict | `workwire say $TID "…" --proposal --as api` |
| object | `--dissent`, once, instead of repeating yourself | `workwire say $TID "…" --dissent --as web` |
| withdraw | `--withdraw` when shown evidence — yours only | `workwire say $TID "…" --withdraw --as web` |
| close | only the initiator; agent initiators need zero open dissents | `workwire resolve $TID "…" --as muthu` |
| reopen | forbidden to agents (403) | `workwire reopen $TID "…" --as muthu` |
| see the state | `workwire threads` — `*` marks yours | `workwire threads --as muthu` |

## The runaway guard

Past `maxThreadMessages` (default **24**) the hub marks the thread `stalled`, rejects
further sends, and hands it back to the initiator with the disagreement intact. The
message count in `workwire threads` is shown against that ceiling (`4/24`) so you can see
it coming.

Unresolved is a legitimate outcome. A thread that stalls with two documented positions
and full provenance on each is more useful than a resolution nobody believed.
