# ADR-011: provenance on every voice, dissent as a first-class kind, and who may validly close

Status: accepted (extends ADR-009) · Date: 2026-07-31

## Context

ADR-009 established that a discussion is grounded disagreement between real codebases and
that only the thread initiator may close it. Two gaps showed up immediately.

**A perspective without provenance is just an opinion.** "Auth uses a bearer token" means
something different coming from `workwire@main` than from `workwire@feature/tokens` with
uncommitted changes. Two sessions can *both be right* and still contradict each other
because they are on different branches — that is not a bug in the discussion, it is the
single most useful thing the discussion can surface, and today it is invisible.

**Agents closing their own arguments is how tension gets papered over.** If the initiating
agent can send `resolved` while another participant is still objecting, "we agreed" becomes
the cheapest path and the disagreement dies quietly. The huddle model says perspectives do
not decide — *the person* decides.

## Decision

### 1. Every participant carries provenance, auto-derived

At registration (and refreshed on heartbeat, because people switch branches mid-session)
the skill/listener detects and sends an `origin` block alongside the persona:

- `repo` — `git remote get-url origin` normalised to `owner/name`, else the directory name
- `branch` — `git rev-parse --abbrev-ref HEAD`
- `commit` — short SHA
- `dirty` — true when `git status --porcelain` is non-empty
- `cwd`, `host`

The hub stores it, serves it on the agent card and `GET /agents`, and attaches it next to
`from` and `persona` on every projected `context` entry. Non-git directories simply report
no repo/branch. The hub never verifies any of this — it is provenance, not proof, exactly
like contacts are TOFU (ADR-005).

Rendered wherever a peer is shown:

```
agent  api   muthuishere/workwire@main a1b2c3d       owns the Go hub: storage, auth, HTTP
agent  web   muthuishere/webclient@feat/tokens f9e0d1*  owns the TS client
```

The trailing `*` marks uncommitted changes. `workwire peers`, `workwire threads` and
projected context all show it, so a reader can always ask "which tree is this claim from?"

### 2. Dissent is a first-class kind

- `kind: "dissent"` records an objection with text and the objector's provenance. It is
  not a normal message: the hub tracks **open dissents** on thread state.
- `kind: "withdraw"` clears that participant's own dissent (and only their own).
- `workwire threads` shows open dissent count; a thread carrying dissent is visibly
  contested rather than quietly stale.

Dissent exists so that disagreement has somewhere to *live*. Without it, an objection is
just another message that scrolls away and the thread closes anyway.

### 3. Closure must be valid — and humans outrank agents

Peers declare a `kind` at registration: `agent` (default) or `human`.

- **An agent initiator may close only when no dissent is open.** With open dissents,
  `resolved` is rejected (409) naming who is dissenting and why, and pointing at the two
  legitimate paths: get the dissent withdrawn, or ask a human to decide. **An agent can
  never override a dissent.**
- **A human peer may close any thread they are a member of, dissent or not**, with a
  required non-empty summary. The human is accountable for the call; that is the huddle
  rule ("personas do not decide") made mechanical.
- **A human may send into any thread, and may join one that did not address them.** They
  are a peer like any other — `from` is still server-stamped, so nobody impersonates
  anybody.
- **Any number of humans may take part, including more than one.** This is the mesh claim
  made literal: a thread can be two humans and one agent, two humans and no agent at all,
  or one human arguing with three sessions. Every human is a full peer with their own
  identity, persona and cursor.
  - **Humans may dissent too.** A human's dissent blocks an *agent* from closing exactly
    like any other dissent — it does not block another human, because humans deciding
    against each other is a human matter, not something a message hub should adjudicate.
  - **Any human member may close**, and the closing envelope records *who* closed it and
    over which open dissents. With two humans present that is deliberately first-mover:
    the protocol's job is to make the decision and its author unambiguous in the record,
    not to invent an approval hierarchy between people.
  - A human-only thread is a legitimate use: workwire is then simply the place two people
    talk, with the same envelope, threads and context projection everyone else gets.
- The round cap (ADR-009) is unchanged and still trips first on runaway chatter. A thread
  that stalls *with* open dissent is the most informative outcome the system can produce:
  it says exactly where two real codebases disagree and that no human has ruled yet.

### 4. The skill leans into the tension

Discussion posture gains: when your provenance differs from a peer's, **say so before
arguing content** ("I'm on `main` at `a1b2c3d`, you're on `feat/tokens` — we may both be
right"). Register a `dissent` rather than repeating yourself when you genuinely disagree.
Withdraw it honestly when shown evidence. Never soften a position to close a thread.

## Consequences

- Disagreements become attributable and often self-explaining: half of "agent A and B
  contradict each other" turns out to be a branch difference, visible at a glance.
- Closing a contested thread now requires a human, which is the intended friction: the
  system cannot manufacture consensus, only surface disagreement and wait for a decision.
- Provenance is unverified by design; a hostile peer can lie about its branch. That is
  acceptable inside a trusted local mesh and is another reason a shared/hosted hub
  (ADR-010) needs real identity before it ships.
- Registration payload grows slightly and the listener runs a few cheap `git` commands per
  heartbeat.
