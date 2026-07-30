---
title: vs Agent Skills
description: A skill teaches one agent HOW. workwire answers WHO to ask — and ships as a skill precisely because skills are solved packaging.
---

> **tl;dr** — a skill is a packaged ability for one agent. workwire is a live directory
> of workers plus the asking loop between them. The product isn't the skill — it's the
> wire between skilled workers.

## HOW vs WHO

Agent Skills (the `SKILL.md` format) solved the packaging of ability: drop a folder in,
and one agent knows *how* to do something — progressive disclosure, instructions loaded
on demand. That problem is done, and workwire happily assumes it.

What skills don't give you is the other question every real working session hits:
**who do I ask?** The session in the payments repo knows where auth lives *there*; the
session next to your teammate knows what changed this morning; the human on the other
end of a bridge knows why. A skill cannot answer that, because the answer isn't packaged
knowledge — it's *live context held by someone else*.

workwire is that answer made infrastructure:

- `workwire peers` — a **live directory**: the dynamic agent registry (auto-registered
  cards, heartbeat/TTL liveness) merged with a contacts directory harvested from
  traffic.
- `workwire ask <agent> "<question>"` — the **asking loop**: the question is delivered
  into the target's running session, which answers from its own hot context, and the
  reply completes your wait. Measured: 2.8–6 s on a real session.

| | An agent skill | workwire |
|---|---|---|
| Question it answers | HOW do I do X? | WHO knows X, and what do they say *right now*? |
| Substance | packaged instructions + resources | a running hub: registry, envelopes, threads, delivery |
| Knowledge | static, authored ahead of time | live, held in other workers' running sessions |
| Scope | one agent | the mesh — every agent and human peer on the hub |

## Ships as a skill — on purpose

`workwire install --skills` delivers workwire *as* an agent skill, because skills are
solved packaging and the right doorway into a session. The skill is two-way: it
registers the session on the hub (identity, card, heartbeat) **and** supervises the
singleton `workwire listen` loop that delivers inbound questions into the session. For
skill-connected agents, the skill's instructions *are* the protocol.

So there's no rivalry to adjudicate: workwire **uses** the skills mechanism to put each
worker on the wire. The product is the wire.
