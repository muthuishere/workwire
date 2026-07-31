# ADR-012: groups are durable audiences; threads remain the only discussion

Status: accepted (extends ADR-009, ADR-011) · Date: 2026-07-31

## Context

Once the mesh holds a web UI, a web API, three dependent APIs, a DB admin, an
observability owner, testers and business owners — each present as an agent, a human, or
both — addressing discussions to individual names stops working. You do not want to type
seven names, and you certainly do not want to *remember* which seven.

There also needs to be a **default place to talk**. A peer that has just joined should be
able to say something and have the right people hear it, without first knowing who exists.

ADR-009 rejected "rooms/topics as a first-class object". That rejection stands and this is
not a reversal — it is a different concept:

- A **thread** is one discussion: a question, an argument, a resolution. Ephemeral.
- A **group** is an **audience**: a named, durable set of peers you can address. It holds
  no messages, has no state to converge, and is never a place where a discussion "lives".

Threads remain the only discussion model. Groups are addressing plus subscription.

## Decision

- **A group is a named, dynamic set of peers.** Runtime registry state, exactly like
  agents — never config (ADR-001). Referenced with an `@` prefix (`@platform`) so a group
  can never be confused with a peer name; the hub rejects a group whose name collides with
  a registered peer, and vice versa.
- **`@all` is the default group.** Every peer joins it at registration — `listen with
  workwire` puts you in the lobby without asking — and may leave. It guarantees there is
  always somewhere a newcomer can speak and be heard by the people who care.
- **Addressing a group expands at ingest, once.** `to: "@platform"` resolves to a snapshot
  of that group's current members, and from there it is an ordinary fan-out and an ordinary
  thread (ADR-009). The snapshot is deliberate: a discussion should not silently acquire
  new participants days later because someone joined a group. Peers who join the group
  afterwards can still find the thread and walk in (ADR-011 discovery), which is the
  honest version of the same thing.
- **Group membership is what wakes you.** Being in a group means discussions addressed to
  that group are delivered to your inbox and wake your session. This is the cost dial from
  the addressing decision, made durable: leaving `@all` for `@platform` is how a peer says
  "stop waking me for everything".
- **The room narrows itself, and that is the point.** A huddle addressed to `@platform`
  invites everyone in it, but thread membership accrues from *participation* (ADR-009) —
  so the effective room becomes whoever actually had something to say. Invitation is
  broad; the discussion evolves down to the people holding evidence. Nobody has to curate
  the guest list up front, and nobody is stuck in an argument that turned out not to be
  theirs.
- **Groups are declared, joined and left like anything else here:**

  ```bash
  workwire groups                        # what exists, who is in them, am I a member
  workwire group join @platform          # opt in (start being woken by its discussions)
  workwire group leave @all              # opt out of the lobby
  workwire huddle @platform "should /send take a recipient array?"
  workwire huddle @platform db-admin "…" # groups and individuals mix freely
  ```

  A peer's `AGENTS.md` / `CLAUDE.md` declaration (ADR-011) may list the groups it wants to
  join, so onboarding stays "write the file, say the phrase".

## Consequences

- Addressing scales to an organisation without a directory to maintain: you address a
  role, not a roster.
- There is exactly one place where discussions happen (threads) and one place where
  audiences live (groups). No second data model, no message ever stored "in" a group.
- Group membership is a genuine cost control — the difference between ten sessions waking
  and two.
- Snapshot-at-ingest means group membership changes never rewrite history, and cursors are
  untouched.
- Groups are unauthenticated-by-design inside a local trusted mesh: anyone on the hub may
  join any group and therefore see its future discussions. Access control belongs with
  workspaces on a shared hub (ADR-010), not here.
