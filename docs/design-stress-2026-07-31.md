# workwire design stress-test — 2026-07-31

Adversarial review of the accepted design (ADR 001–012, `openspec/specs/`) against the
implementation in `internal/` and `cmd/`. 75 raw claims were generated; 8 survived triage; all 8
were then verified against the code — several by building the hub and running the scenario. All 8
came back **PARTIAL**: the mechanism is real in each case, but in every one of them at least one
load-bearing claim in the original finding was wrong, and in every one the severity was inflated.
Zero blockers survived verification.

Where a finding names something the ADRs already accept as a tradeoff, that is said plainly below
rather than dressed up as a discovery.

---

## 1. Verdict — can auditability, accountability, security and scale be added later?

**Short answer: yes on all four. Nothing found in this review requires a design change now to keep
a later change additive.** The one thing that is genuinely lossy-if-deferred is cheap enough to do
this week (a `meta["audience"]` stamp), and the one thing that degrades silently over time
(retention rewriting thread state) is bounded by a sidecar pattern the repo already uses for
tombstones.

### Auditability — ADDITIVE, with one cheap item worth doing now

The audit record is *derived*, not stored: `threadStateLocked` (`internal/store/store.go:466`)
recomputes members, initiator, dissents, closure and provenance from the persisted envelopes on
every read, and its own comment says "nothing extra to keep in sync". That is the right call — it
means the record cannot drift from the log, and it means re-keying or enriching the derivation
later is a code change, not a migration. Provenance is already stamped immutably per envelope at
ingest (`internal/server/server.go:211` writes `meta["peerRole"]`, `peerKind`, `origin`), so a past
closure stays interpretable even if the peer's card changes afterwards. `meta` is an open map and
`SchemaVersion` is still 1 after five hub-stamped keys, so new audit fields cost nothing on the wire.

Two caveats. First, the derivation is computed over a log that ADR-008 is obliged to delete, so
thread state has a retention half-life (Finding 1). That is fixable with a retention-immune
`threads.ndjson` checkpoint — exactly the pattern `tombstones.ndjson` already establishes
(`store.go:121`). Second, group expansion throws away the addressing intent (Finding 6): the roster
survives forever in the envelope's `to`, but "did muthu address a role or hand-pick four names" does
not. That is a three-line `meta["audience"]` stamp in `ingest()`, and it is only worth doing *now*
because it is cheap and unrecoverable for messages already sent — of which there are effectively
none, since groups landed four commits ago.

### Accountability — ADDITIVE

ADR-011's machinery survived the attack better than anything else in the design. The rules that
matter — an agent can never override a dissent, a human may reopen anything, closure records
`closed_by` / `closed_by_kind` / `closed_over` — are enforced from the *persisted* envelope's
stamped role, not from the live registry card. I attempted the obvious escalation (an agent flips
its own `kind` to `human` and re-closes) and history did not budge: past closures and dissents keep
the role as of the act.

What is missing is the *authorized* half of the lifecycle, not the model: there is no name release,
no secret rotation, no revoke, and the auto-minted admin token has no peer identity — so the owner's
very first contested `workwire resolve` 409s at him with a message that doesn't name the remedy
(`workwire join muthu --human`, which exists and works). Every one of those is a new verb or a new
error string. No wire change, no migration, no stored-state rewrite.

The one accountability property that is *not* enforceable today and is not meant to be: a peer's
self-declared `kind`, persona and branch are unverified. ADR-011's Consequences already say so
("Provenance is unverified by design… another reason a shared/hosted hub needs real identity before
it ships") and ADR-010 owns the fix. That is an accepted tradeoff, not a finding.

### Security — ADDITIVE, because the trust boundary is explicit and narrow

The hub is single-node, loopback/LAN, one admin token at 0600, co-tenants without file access locked
out (ADR-007, auth R2/R7). Inside that boundary every authenticated peer can already read every
thread in full (hub-core R22, stated inline: "Local-trust assumption; a shared hub needs
per-workspace scoping — ADR-010"). Given that, most "no authorization on X" observations are not
holes — they are the declared model. `DELETE` being open to any authenticated peer buys an attacker
nothing they could not already do, and restricting it while R22 hands everyone everything would be
incoherent.

Two things do need doing and both are additive: enforce `askPolicy` on `/send` and fan-out, not only
`/ask` (Finding 5 — currently a trap for the first person who sets it, though nothing shipped can
set it), and stop `GET /threads` serving tombstoned text (Finding 3 — a conformance bug against a
requirement the spec already states correctly). Rate limits, quotas and workspace scoping are
pre-declared shared-hub work in ADR-010. A future `429` joins a contract that already returns
400/401/403/409.

### Scalability — ADDITIVE, with two internal fixes worth taking early

Measured, not argued. `Inbox` is a full O(N) scan under the store-wide mutex with a single broadcast
wake channel: 1.05 ms per scan at N=100k, 7.4 ms at N=400k, so a 50-waiter herd costs 55 ms / 385 ms
of wall time. That is worse than it should be but still push-like against a 25 s long-poll, and
ingest is *not* starved behind it (concurrent `Append` measured 1.3 ms) because Go's mutex starvation
mode hands off after 1 ms. Both fixes are internal: binary-search `since` (`s.msgs` is seq-ordered)
and per-agent notify channels. No client sees a difference.

Separately, age-based retention does not run: rotation happens only on size in `Append`
(`store.go:309`), `EnforceRetention` skips the active segment (`store.go:720`), and `RotateNow`
(`store.go:752`) has no production caller despite a doc comment claiming the retention loop uses it.
At the measured 232 bytes/record a 12-peer team needs ~2.5 years to trip the 64 MB segment, so a
configured `retentionDays: 30` effectively never engages. The 1 GB size bound *does* work (verified:
946 KB across 15 segments trimmed to 225 KB under a 256 KB budget), so R4's no-unbounded-growth
promise holds. This is a config-says-30-days / code-says-never divergence, and it is a one-line fix
in the hourly loop.

ADR-010's claim that "nothing in the current design has to be undone" holds on the points checked.
ADR-010 commits to a store *interface*; a Postgres implementation is free to store one row per
(envelope, recipient) and fan the existing `Seqs` map out at import.

**The one scale gap that is a design question, not a code question**, is the round cap (Finding 4):
it counts envelopes while ADR-012 exists to scale members, so `workwire huddle @all` cannot complete
a single round at ADR-012's own stated org size. The fix is permissive (raise the effective cap with
membership), so even that is non-breaking.

---

## 2. Findings

All PARTIAL — mechanism verified, framing corrected, severity reduced. Ordered by corrected severity.

### F1 — Retention silently rewrites thread state on partially-truncated threads
**Corrected severity: major** (claimed blocker) · dimensions: auditability, accountability, coherence

**What it is.** Thread state is derived by scanning surviving envelopes. When retention drops *some*
of a thread's segments but not all, `threadStateLocked` (`internal/store/store.go:494-497`) takes
`Initiator` and `Topic` from `list[0]` — now the wrong envelope — and `Resolved` / `ClosedBy` /
`ClosedOver` vanish with the dropped `resolved` record. Reproduced with a throwaway test
(`SegmentMaxBytes:400`, `RetentionAge:1h`):

```
BEFORE initiator="muthu" state="resolved" closedBy="muthu" closedOver=1 count=3
AFTER  initiator="api"   state="open"     closedBy=""      closedOver=0 dissents=1 count=1
```

The closed thread also starts accepting sends again, because `checkThreadRules`
(`internal/server/dissent.go`) reads `Resolved` from the same derivation. Fully-truncated threads
disappear cleanly; the defect is specific to *partial* truncation.

**Spec lines.** hub-core R15 ("the first sender is recorded as its INITIATOR… SHALL survive a hub
restart"), R19 ("Open dissents SHALL be derived from the persisted envelopes"), R4 ("those segments
are removed"). Strictly, no requirement is *violated* — R15/R19 scope durability to restart, which
the code honours exactly. This is an unspecified interaction between two accepted ADRs (008 and 011).

**Corrections to the original claim.** Restart does *not* rewrite anything (`load()` replays every
surviving segment; derived state is identical). The `maxThreadMessages` variant is not a defect —
`stalled` is a live gate and R16 plus the 409 text both say raising the cap is the remedy, so
un-stalling on a raise is the intended behaviour, not retroactive falsification. And the derived
initiator cannot close in one move: `dissent.go` blocks an agent closer while any dissent is open,
so the escalation needs `withdraw` then `resolved`.

**Reachability.** Needs a segment boundary inside one thread's lifetime. Segments are 64 MB and
rotate on size only (the hourly loop never rotates — see F7), so on a low-traffic hub everything
lives in the never-dropped active segment. You need ~220k envelopes of unrelated traffic plus a
capped thread straddling that boundary plus 30 days. Not reachable on v1 local-mesh targets;
reachable on a busy or shared hub, or with the tunables turned down.

**Fix: additive.** `tombstones.ndjson` (`store.go:121, 176-177`) already establishes the pattern — a
sidecar outside the segment set, immune to rotation and retention. A `threads.ndjson` checkpoint
written only on state-changing kinds (initiator, topic, resolved, closed_by, closed_over) is the same
shape, a few hundred bytes per thread, invisible to ADR-008's 1 GB bound. ADR-009 refused a *room* as
a new first-class wire object, not an internal durability sidecar. Cheapest partial fix: set
`truncated:true` on a rebuilt `ThreadState` whose head was dropped — ~10 lines, and it is exactly the
`reset:true`-style honesty signal ADR-001 gave cursors and thread state never got.

---

### F2 — Tombstoned text leaks through `GET /threads`
**Corrected severity: major** (claimed blocker) · dimensions: security, auditability

**What it is.** A conformance bug in one read path. `DELETE /messages/<id>` correctly excises from
inbox replay, `GET /threads/{id}` and context projection — all of which route through `store.Render()`.
`handleListThreads` (`internal/server/server.go:447`) marshals `ThreadState` straight to the wire,
and `threadStateLocked` builds `Topic` (`store.go:496`) and `Dissent{Text: e.Text}` (`store.go:512`)
from raw envelopes with no tombstone consultation. Verified at runtime: after DELETE, `GET /threads`
still returned `topic:"TOPIC-SECRET-sk-123"` and `dissents:[{peer:"priya", text:"DISSENT-SECRET-sk-456"}]`
verbatim — served to *every* authenticated peer under R22. A fourth leak: `describeDissent`
(`internal/server/dissent.go:14`) embeds raw dissent text in the 409 close-rejection body. R22's
"every NON-DELETED thread" is also unimplemented — a fully tombstoned thread is still listed.

The exposure is concentrated in the worst two fields: the thread's opening line (the most likely place
a secret gets pasted) and dissent text. The operator is told the secret is gone; it is still being
served to the whole mesh.

**Spec line.** hub-core R13 — excision "from ALL reads: inbox replay, GET /threads, and read-time
context projection". The spec is right; the code does not do it.

**Corrections.** The original claim led with "override by deletion". Refuted at runtime: after a
non-member deleted a human's dissent, the close was still rejected 409 with the dissent quoted in
full. Dissent state is derived from raw envelopes, which tombstoning does not touch — deleting a
dissent is close to a no-op. The claim's other sub-scenario ("converts a readable block into an
unreadable one") contradicts this one: the text is precisely what leaks. And the ADR-011 accountability
record — dissents, `ClosedBy`, `ClosedOver`, membership, initiator, provenance — is entirely derived
and survives deletion intact.

**Fix: additive, no spec change.** Consult `s.tombs` in `threadStateLocked` when populating `Topic`
and `Dissent.Text`, and in `describeDissent`; skip fully-tombstoned threads in `Threads()`. ~6 lines.
Worth taking in the same pass: add `deletedBy` / `deletedAt` to the tombstone record
(`store.go:627 appendTombLocked` writes `record{Type:"tomb", ID:id}` and nothing else) — a
backward-compatible NDJSON field addition, pre-v1, no released corpus.

**Not part of this fix:** authorization on DELETE. That belongs to ADR-010 and would be incoherent to
add while R22 hands every peer every thread's full contents.

---

### F3 — No credential lifecycle; the admin token has no peer identity
**Corrected severity: major** (claimed blocker) · dimensions: accountability, failure-modes

**What it is.** Two related gaps, both fixable with new verbs.

*(a) The owner's first contested close is a dead end.* Reproduced on a fresh build: `join api`,
`join web`, `huddle api web "…"` as admin, `say --as web --dissent`, then `resolve` →
`409 … an agent can never override a dissent … or ask a human peer to decide and close it`.
`Identity.Role()` (`internal/auth/auth.go`) returns `registry.KindAgent` for `KindAdmin`, so the
person at the terminal takes the agent branch in `dissent.go:41`. The remedy exists and works —
`workwire join muthu --human`, then `--as muthu`; I verified it resolves and records
`closed by muthu over web`. The defect is confined to two error strings (`dissent.go:88`, `:47`) not
naming the verb, plus admin-authored envelopes carrying `from:"admin"` with nil origin.

*(b) A lost or leaked secret has no remedy inside the API.* No `DELETE /agents` route
(`internal/server/server.go:51-73`); `Register` returns 409 for a taken name even to the admin token
(curl-verified). registry-a2a R1 says re-registration "does NOT rotate the secret" and no rotate verb
exists. If the hub's data dir outlives a client's `credentials.json`, that name is permanently
unusable and recovery means stopping the hub and hand-editing `registry.json`.

**Corrections.** The claim said nothing in CLI help or the error bodies mentions `--human` —
`cmd/workwire/main.go:101` is literally `workwire join <name> [--human]`, and `SKILL.md:145-148` has
a dedicated block for it. The reimage scenario is not reachable on the default layout (data dir
defaults to `~/.config/workwire/data`, inside the config dir, so a reimage takes the registry too).
`api`, `api-2`, `api-3` are not indistinguishable: `GET /agents` serves `registry.Live()` with a 120 s
TTL, so dead duplicates drop off in two minutes. Attribution is *not* lost: because names are
permanently reserved — the very property the claim calls a defect — name→agentId is a permanent
bijection, so every historical `from` is back-attributable via the registry. And re-keying thread
state by agentId needs no migration, because thread state is derived at read time. The claim wanted
names to be both reusable and unique-forever and called each the other's bug.

The admin token is also not an "unscoped superuser": its only extra power over an agent is reading
any inbox (`server.go:282`). It cannot take a name and cannot override a dissent.

**Fix: additive.** `DELETE /agents/{name}` (admin-token only, tombstoning the binding); rotate on
re-register with the current secret; two error strings that name `workwire join <name> --human`.
Optionally refuse `resolved`/`reopen` from `KindAdmin` with "join as yourself first". New endpoints,
no envelope change, no cursor change.

---

### F4 — The round cap counts envelopes while ADR-012 scales members, and blocks the two converging verbs
**Corrected severity: major** (claimed blocker) · dimensions: scalability, failure-modes, coherence

**What it is.** `ts.Count = len(list) - capBase` (`internal/store/store.go:547`) is raw envelopes since
the last reopen, against a default of 24 (`internal/config/config.go:57`), and one fan-out is one
stored envelope (R14/R24). One round of an N-member group therefore costs N. At ADR-012's own stated
scene — web UI, web API, three dependent APIs, DBA, obs owner, testers, business owners, all
auto-joined to `@all` — `workwire huddle @all "…"` spends the whole cap on round one: initiator plus
23 responders exactly fills it. The flagship group flow cannot complete a single round at the scale
ADR-012 exists to enable. ADR-009 accepts "occasionally cutting off a legitimately long discussion" —
a tradeoff about *time*. No ADR notices the cap is a function of *member count*.

Separately, `dissent.go:66` places the cap check before `if req.Kind != "resolved"` at :72, exempting
only `reopen`. So on a stalled thread a dissenter shown evidence cannot `withdraw` (one envelope,
strictly reduces open dissents) and an initiator with zero dissents cannot `resolved` (one envelope,
ends the thread). Both are rejected by a breaker whose purpose is to stop token burn. No test covers
either case (`huddle_test.go:285-311` tests only a plain send at the cap), which suggests fall-through
ordering rather than a decision.

**Corrections.** Not a deadlock. ADR-011 §3a's human `reopen` is the designed escape, and
`workwire threads --state stalled` (`cmd/workwire/cmd_huddle.go:169`) surfaces it — `state` is on the
wire under R22 and filterable. And "26 sessions each burn a turn that is then 409'd" is off by an
order of magnitude: fan-out happens at ingest, envelopes 2–24 are accepted, only post-cap posters get
409 — 3 wasted turns at 26 members, not 26.

**Spec rot, separately.** R16 still says "there is no reopen path" and "only the thread INITIATOR may
send it", both superseded by R20/R21 in the same file. And `skills/` never mentions `stalled` or
`reopen`, so sessions have no posture for either.

**Fix: additive and permissive.** Hoist `withdraw` and `resolved` above the cap check alongside the
existing `reopen` exemption; derive the effective cap from membership
(`max(MaxThreadMessages, k*len(ts.Members))`). Neither touches the envelope shape, the `count` field,
the `stalled` literal, the 409 body, or the `maxThreadMessages` key. Note that `state` is *already*
re-derived from live config on every read, so "raising the cap un-stalls existing threads" is an
existing property, not something the fix introduces.

---

### F5 — Re-registration silently rewrites a peer's `kind`
**Corrected severity: minor** (claimed blocker) · dimensions: accountability, coherence

**What it is.** `registry.Register()` (`internal/registry/registry.go`, re-registration branch:
`existing.Kind = NormalizeKind(card.Kind)`) rewrites `kind` from whatever the card carries, with no
audit line — and `cmd_join.go:51-53` *always* puts a `kind` in the card, so omitting `--human` means
"agent", not "leave it alone". Reproduced: `workwire join muthu --human` → `('muthu','human')`; the
same person later re-runs `workwire join muthu` for a new persona or after a hub restart → 200 and
`('muthu','agent')`. They have silently lost the two powers ADR-011 gave them — they can no longer
close over agent dissent (409) and can no longer `reopen` (403). The CLI prints
"rejoined as muthu (agent)" and nothing about the loss.

**Corrections — and why this is minor.** The claim framed this as agent self-promotion to human. It
is not an escalation on this hub: I verified that an agent holding only its own secret can already
`POST /agents {"name":"ghost","kind":"human"}` and get **201 with a fresh secret**, then close over
another peer's dissent — so pinning `kind` at mint fixes nothing, because the mint itself is
client-declared. And every same-UID agent session already holds the operator credential
(`newClient`, `cmd/workwire/main.go:188-194`, auto-reads the 0600 admin token); ADR-007 draws the
boundary at *file access*, not process identity.

Crucially, the claim's auditability consequence is false. Authorization is not read back from the
live card: `server.go:211` stamps `meta["peerRole"]` into the immutable envelope at ingest, and
`store.go:440 roleOf()` / `:524 ts.ClosedByKind = roleOf(e)` derive the closure record from the
persisted envelope. Flipping `kind` afterwards cannot rewrite any past closure or dissent.

This is a **documented accepted tradeoff**, not a discovery: ADR-011 §3 says outright "Peers declare
a `kind` at registration", its Consequences accept unverified self-description "inside a trusted local
mesh", and hub-core R22 carries the same note. ADR-010 owns the real fix (scoped join tokens,
workspaces).

**Fix: additive.** On re-registration keep the stored `kind` unless the card explicitly carries one,
require an explicit `--human`/`--agent` to transition, and log it. Optionally have `join` default the
card's `kind` to the stored value. One conditional and a log line.

---

### F6 — Group expansion discards addressing intent; membership has no timeline
**Corrected severity: minor** (claimed major) · dimensions: auditability

**What it is.** `ingest()` (`internal/server/server.go:177`) calls `ExpandRecipients` and stores only
the expanded list; the literal `@payments` appears nowhere in the envelope.
`internal/registry/groups.go` persists membership as an overwritten map with no join/leave log, and
`LeaveGroup` deletes an emptied group (except `@all`). So a reader on day 200 can answer "who was
addressed" but not "did muthu address the role or type four names" and not "when did
contractor-session join that role".

**Corrections — and why this is minor, not major.** The claim's headline question ("who was in
`@payments` on day 3? the store answers none of them") is false: R24 mandates expansion to a snapshot
of current members, and that snapshot is exactly what lands in the envelope's `to`, immutably, per
message. The claim that a silent addressee "never appears anywhere a reader would look" is
contradicted by hub-core R15 — members are everyone who sent into a thread *or was addressed in it* —
so a group-expanded silent peer is a thread member by construction and appears in R22's `members`.
And the leak-investigation framing mislocates the state: `handleThread`
(`internal/server/server.go:375`) does `identify` and then serves the thread with **no membership
check**, per R22. Group membership is a wake-cost dial (ADR-012: "Group membership is what wakes
you"), not an access boundary — so `contractor-session` could read that huddle whether or not it ever
joined.

Also false: that this needs a schema-version bump. `meta` is an open map and `ingest()` already
stamps five hub-generated keys with `envelope.SchemaVersion` still 1. And the "free now, impossible
later" urgency does not hold — groups landed four commits ago, nothing is deployed.

**Fix: additive.** `meta["audience"] = ["@payments"]` (tokens as typed) in `ingest()` alongside the
existing stamps, reported by `GET /threads`; optionally an append-only NDJSON group log of
create/join/leave/GC. Do it now because it is three lines, not because it becomes impossible.

---

### F7 — Age-based retention never fires; `Inbox` is an unindexed scan behind a broadcast wake
**Corrected severity: minor** (claimed major) · dimensions: scalability, failure-modes

**(a) Retention.** Verified empirically: with `RetentionAge=1ns` and now advanced a year, all 200
appended envelopes survive. Rotation happens only in `Append` at `segSize >= SegmentMaxBytes`
(`store.go:309`), `EnforceRetention` skips the active segment (`store.go:720`), and `RotateNow`
(`store.go:752`) has no production caller — `cmd/workwire/main.go:172` calls only `EnforceRetention`,
despite `RotateNow`'s doc comment claiming the retention loop uses it. At the measured 232 bytes per
record a 12-peer team at 300 msg/day needs ~2.5 years to trip 64 MB, so `retentionDays: 30` in the
auto-created config effectively never engages.

What is *not* broken: the size bound works (measured — 946 KB across 15 segments trimmed to 225 KB
under a 256 KB budget), so hub-core R4's "no unbounded growth" holds; and ADR-008's actual privacy
mechanism is the tombstone path, which ADR-008 names in so many words as "the pasted-secret excision
path". The retention window was never the privacy mechanism. This is a config-says-30-days /
code-says-never divergence, not a data-retention incident.

**(b) Inbox scan.** `store.go:349` walks all of `s.msgs` from index 0 regardless of `since`, under the
mutex that also serialises `Append`; `wakeLocked` (`store.go:326`) closes one channel for all waiters.
Measured on M-series: 1.05 ms/scan at N=100k, 7.4 ms at N=400k; a 50-waiter herd is 55 ms / 385 ms
wall. That is the recipient's worst-case delivery latency — still push-like against a 25 s long-poll,
not the claimed two-orders-of-magnitude violation of R6. Ingest is not starved: concurrent `Append`
measured 1.3 ms / 0.15 ms, because Go's mutex starvation mode hands off after 1 ms. At the ~10-peer
scale workwire actually declares today, the herd is ~10 ms.

**Corrections.** `/threads` is not polled — agent-skill R15 and `SKILL.md` make `workwire threads` an
on-demand verb and `internal/listen/listen.go` polls `/inbox` only, so `Threads()`' O(N) cost (14 ms
at N=100k) is off the hot path. The `EnforceRetention` re-parse (~7 s extrapolated at 1 GB from a
measured 723 ms for 93 MB) is real but fires at most hourly and only when a segment is actually
dropped. The ADR-010 Postgres critique does not stand: ADR-010 commits to a store *interface*, and a
Postgres implementation can store one row per (envelope, recipient), fanning the existing `Seqs` map
out at import.

**Fix: additive, internal.** Call `RotateNow` from the hourly loop when the active segment is
non-empty and older than a fraction of `RetentionAge`; fix the false doc comment. Binary-search
`since` in `Inbox` (`s.msgs` is seq-ordered and every per-recipient seq ≥ `st.Seq`); optionally
per-agent notify channels. No client contract changes.

---

### F8 — `askPolicy` is enforced on `/ask` only
**Corrected severity: minor** (claimed blocker) · dimensions: security, failure-modes

**What it is.** `AskAllowed` has exactly one call site — `askCore`
(`internal/server/agents.go:138`), serving `POST /agents/{name}/ask` and the A2A `message/send` RPC.
`POST /send` → `ingest()` (`internal/server/server.go:163`) never consults it. Verified empirically:

```
ask   /agents/guarded/ask         from `stranger` → 403 {"error":"ask_denied"}
send  /send {"to":["guarded",…]}  from `stranger` → 200, lands in guarded's inbox
send  /send {"to":"@all"}         from `stranger` → 200, lands in guarded's inbox
```

A security-shaped control that returns 403 on one path and delivers on every other path into the same
inbox is a trap for the first person who sets it.

**Why minor.** It is not currently reachable: there is no CLI flag (`grep -rn askPolicy cmd/
internal/listen/` → zero hits) and the skill never mentions it, so only a raw `POST /agents` can set
it. And auth R8, ADR-007 and `site/src/content/docs/http-api.md:108` all describe it strictly as an
ask-path gate — the implementation conforms to its spec. The claim's premise ("the documented way to
say only my human may wake me") is not what the docs say; the documented wake/cost dial is group
membership (ADR-012: "Group membership is what wakes you… the cost dial"; SKILL.md: "Leaving `@all`
is how you go quiet").

**Corrections on the rest.** "The round cap is the only cost control" is false — ADR-012 states group
membership as "a genuine cost control" and ships `group leave @all`. "No rate limit is an unnoticed
hole" is false — ADR-010 opens by enumerating rate limits as deferred shared-hub work and hub-core R22
carries the local-trust caveat inline. "Auto-joining `@all` is the silent add ADR-012 bans" is false —
R25 forbids any endpoint adding a peer *other than the authenticated caller*; self-join-by-default is
not a third party force-joining you. And the wake-storm multiplier is overstated: the skill's watcher
fires once per burst and the session drains batched inbox lines, so 9,000 deliveries are ~9 session
wakes, not 9,000.

**Fix: additive.** Enforce the target's policy per-recipient at `ingest()`. One design decision
needed: the policy must gate *unsolicited first contact*, not replies — enforcing it only when the
recipient is not already a thread member handles that, otherwise an allowlisted target's own
conversation partners get 403'd on the reply fan-out. Nothing shipped depends on today's behaviour.

---

## 3. What holds up

**Derived thread state was the right call.** `threadStateLocked` recomputing members, initiator,
dissents and closure from the log means the audit record cannot drift from the evidence, and it means
re-keying (say, by agentId) later is a code change, not a migration. Three separate findings assumed
this would force a stored-state rewrite; none did.

**Provenance is stamped at ingest, not read from the live card.** `server.go:211` writes
`meta["peerRole"]` / `peerKind` / `origin` into the immutable envelope, and `roleOf()` derives
`ClosedByKind` from it. This single decision defeats the entire class of "flip your card, rewrite
history" attacks — I tried it and history did not move. It is the strongest thing in the design.

**ADR-011's closure rules enforce correctly.** An agent genuinely cannot override a dissent, including
a non-member agent that has deleted the dissent envelope — the 409 still fires, quoting the dissent.
Human reopen genuinely is the escape hatch, and `--human` genuinely works end to end.

**Permanent name reservation is coherent, not an oversight.** It is what makes name-keyed attribution
sound: name→agentId is a permanent bijection, so every historical `from` is back-attributable. The
review's own "names should be reclaimable" complaint was in direct tension with its "attribution is
unsound" complaint.

**`meta` as an open map with a stable `SchemaVersion`.** Five hub-stamped keys already ride in it at
version 1. Every audit enrichment this review wants — `audience`, `truncated`, `deletedBy` — fits
without a wire break. That is why so much of this report says "additive".

**The tombstone sidecar pattern.** `tombstones.ndjson` lives outside the segment set and survives
rotation and retention by construction. It is the template for the one durability fix that matters
(F1's thread checkpoint), which is why that fix is cheap rather than architectural.

**The size-based retention bound works.** Verified by measurement, not by reading. R4's promise of
indefinite operation on one volume holds today, even with the age half unimplemented.

**Long-poll latency is fine at declared scale.** Measured, not assumed: ~10 ms herd cost at the ~10
peers workwire targets. R6's "push-like, ~ms after ingest" survives.

**The trust boundary is stated in the right places.** hub-core R22, ADR-007, ADR-010 and ADR-012's
Consequences all say the same thing inline — single-node, local-trust, real identity deferred. That
consistency is why the "no authorization on X" findings collapsed on inspection: they were describing
the declared model, not breaking it. The failure mode to watch is the opposite one — that boundary
must be re-checked the moment ADR-010 is picked up, because roughly half of this review's refutations
lean on it.

---

## 4. Recommended ADR amendments, in priority order

1. **ADR-008 (retention) + ADR-011 (thread state)** — state that derived thread state is
   retention-bounded, and specify the remedy: a retention-immune `threads.ndjson` checkpoint written
   on state-changing kinds, plus `truncated:true` / `earliestRetained` on `GET /threads` when a
   thread's head has been dropped. Add a hub-core R4 scenario asserting initiator, state and dissents
   are unchanged after retention drops a thread's earliest segment. *(F1)*
2. **hub-core R13 — no ADR change, a conformance fix.** The requirement already says "ALL reads".
   Make `threadStateLocked` and `describeDissent` consult `s.tombs`, and make `Threads()` honour
   R22's "non-deleted". Add the scenario. Take `deletedBy` / `deletedAt` on the tombstone record in
   the same pass. *(F2)*
3. **ADR-009 (round cap)** — redefine the cap so it means what the ADR says: rounds, or
   `max(base, k × members)`. Exempt `withdraw` and `resolved` from the cap alongside `reopen`. Fix
   R16's stale text ("there is no reopen path", "only the initiator may send it") with a forward
   reference to R20/R21. Add `stalled` and `reopen` to the agent skill. *(F4)*
4. **ADR-007 (identity) + registry-a2a R1** — add the authorized half of the lifecycle:
   admin-gated `DELETE /agents/{name}`, rotate-on-re-register-with-current-secret, and pin `kind`
   across re-registration unless explicitly transitioned. State that the admin token is an operator
   credential with no peer identity, and make the two decision-verb error bodies name
   `workwire join <name> --human`. *(F3, F5)*
5. **auth R8** — restate `askPolicy` as a delivery-admission rule enforced on every path that
   enqueues into a peer's inbox (`/ask`, `/send`, group expansion, thread fan-out), scoped to
   unsolicited first contact so replies are not blocked. Consider renaming it `wakePolicy`. *(F8)*
6. **ADR-012 (groups)** — stamp `meta["audience"]` with the group tokens as typed, and note that
   group membership is a wake-cost dial and explicitly *not* an access boundary (since
   `GET /threads/{id}` serves any thread to any authenticated peer). Optional: an append-only group
   join/leave log. *(F6)*
7. **ADR-008 / hub-core R4 — implementation, not spec.** Call `RotateNow` from the hourly loop when
   the active segment is older than a fraction of the retention window, and fix its false doc comment.
   Separately, binary-search `since` in `Inbox` and give each agent its own wake channel. *(F7)*
8. **ADR-010** — when it is picked up, re-check every "local-trust, deferred" caveat this review
   leaned on: DELETE authorization, per-workspace thread visibility, rate limits and quotas (a `429`
   the contract does not yet mention), and real peer identity replacing self-declared `kind`. Also
   record that cursors are opaque strings, so a watermark or LSN can be substituted without breaking
   clients.
