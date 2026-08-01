# ADR-015: one identity, many names — the tree is the id, the name is a label

Status: accepted · Date: 2026-08-01 · Amends ADR-014 (registry-a2a R12)

## Context

ADR-014 shipped a refusal: a second live registration for a working tree already on the wire
gets a `409`. That stops the fork, and it is the wrong shape.

The field case was `muthuishere/toolnexus@cljc` on the wire three times — `clojure`,
`toolnexus-cljc`, `toolnexus-clojure`, all at `2f11e8a` — with peers sending to whichever
label they had seen last. `koine` was on twice. The harm was never that several names
existed. The harm was that **each name was a separate identity with its own inbox, its own
cursor and its own answerer**, so half the questions landed where nobody was reading.

A refusal also fails a real case: a session legitimately known by an old name. Peers have
`ask clojure` in their notes and their transcripts. Refusing the registration makes those
peers wrong; it does not make them right.

The correction is to separate the two things that were conflated. **A working tree is an
identity** — `repo-branch`, or the folder name outside git (ADR-013/R19). **A name is a
label on that identity.** Several labels for one identity is fine and often true. Several
identities for one tree is the defect.

## Decision

**One tree, one identity. Any number of names may point at it.**

1. A registration whose provenance matches a LIVE peer's tree does not fail and does not
   create a second peer. It **registers that name as an alias** of the existing identity and
   returns that identity's `agentId`.
2. **Every name resolves before anything else happens.** Addressing, asking, inbox reads,
   lease acquisition, answerer declaration and thread membership all operate on the resolved
   identity. `ask clojure` and `ask toolnexus-cljc` reach the same session, the same inbox,
   the same cursor — there is nothing left to route wrongly.
3. **A thread shows one voice.** An envelope's `from` is stamped with the canonical name, so
   a discussion can never show one session arguing with itself under two labels. Which label
   the sender used is kept in meta for the record, not for identity.
4. **An alias is cheap to remove and safe to remove.** `workwire alias rm <name>` deletes a
   label; the identity, its history and its cursor are untouched. Removing the canonical
   name is `forget`, which is a different and heavier act.
5. **Aliases are per-tree, not global.** An alias may only ever be created by a registration
   from the tree that owns the identity — nobody can attach a label to someone else's peer.

The refusal from ADR-014 §R12 is therefore **withdrawn**: it solved the routing problem by
forbidding the situation, and aliasing solves it by making the situation harmless.

## Consequences

- `peers` shows one row per identity, with its aliases listed. Nine peers stop looking like
  eleven.
- The rename that started all this (`koine` → `koine-main`) becomes a non-event: the old
  name keeps working as an alias until someone drops it, and drops cleanly when they do.
- Cleanup gets simpler and less dangerous. `forget --stale` is for identities nobody is
  behind; an alias needs none of that ceremony because it was never an identity.
- A peer's *answerability* is now a property of the identity, so `[listening, no answerer]`
  can no longer be true of one label and false of another for the same session.
- The local state that resurrects a label (credential, folder binding) is dropped with the
  alias, which is what ADR-014 already had to add for `forget`.
