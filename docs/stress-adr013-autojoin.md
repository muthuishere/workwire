# ADR-013 stress-test — join-by-default and the remote-hub seam · 2026-07-31

> **Later note (same day):** auto-join was subsequently **deleted outright** — no
> `session-start`, no SessionStart hook, no `autoJoin` key. A repo now opts in with a line in
> its own `CLAUDE.md` / `AGENTS.md`. Findings A, C, E and F were fixed in code; B and C's
> machinery survive because `workwire listen` needs them; D, G and H are moot for the removed
> mechanism. This report is kept as the dated record that produced the decision.

Adversarial review of ADR-013 (`docs/adr/013-client-config-auto-join-and-the-cli-surface.md`,
accepted today, §1–§2 implemented in `c42310c` / `9db960e`) against the code at HEAD `9db960e`.
71 raw claims → 8 shortlisted → 8 verified against a built binary and a live hub. **All 8 came
back PARTIAL**: the mechanism is real in every one, and in every one at least one load-bearing
claim in the original finding was wrong and the severity was inflated. **Zero blockers survived
verification.** Nothing found here requires a wire-format, schema or migration change — every fix
is additive except one credential gate, which is deliberately breaking.

---

## 1. What we still need — ranked

Before join-by-default is on for every repo on a machine, and before anyone points `hubUrl` at a
hub they do not own.

1. **Stop sending the local hub's admin token to a non-loopback hub.** `[BREAKING]`
   `newClient` (`cmd/workwire/main.go:196-208`) falls back to the 0600 `admin-token` file whenever
   `$WORKWIRE_TOKEN` is unset, with no check on what `hubUrl` names — reproduced going out on an
   unauthenticated `GET /health`. This is the one item that actually gates the remote-hub seam:
   until it lands, "point at a team hub" means "hand that host admin on your own hub". Breaking,
   because anyone who today copies a shared admin-token file into their config dir for a LAN hub
   will start getting a hard failure telling them to set the token env var instead.

2. **Key `credentials.json` by hub, not by agent name alone.** `[ADDITIVE, with on-disk migration]`
   `internal/listen/listen.go:842`. Today pointing at a remote hub presents the *local* per-agent
   secret to it, and a successful remote registration overwrites the local one — so flipping
   `hubUrl` back breaks local auth silently.

3. **Split answerer-liveness from lease-liveness on the hub.** `[ADDITIVE]`
   `listener: true` (`internal/registry/registry.go:376` → `internal/server/agents.go:86`) means "a
   listen lease exists". `ask` and `peers` present it as "someone can answer right now". Auto-join
   makes the divergence the resting state of the machine because it takes the lease for every
   folder while only the trigger phrase recruits the session that answers.

4. **Make the derived agent name collision-aware per folder.** `[ADDITIVE]`
   `filepath.Base(dir)` (`cmd/workwire/autojoin.go:272`) mints a hub identity with no collision
   check, and both adopt paths treat "the name-keyed flock is held" as same-folder adoption without
   comparing the folder behind it. Two `api` folders collapse onto one identity and the second is
   told it joined while it is on the wire under no name at all.

5. **A local run-state file plus `workwire doctor` (or `status --local`).** `[ADDITIVE]`
   Nothing enumerates, explains or stops the detached listeners we spawn. This is the item that
   makes the other four self-diagnosing, and it is the only thing that catches a stale binary
   holding the lock — today `session-start` says "adopting the running listener" without reporting
   the holder's exe or version.

6. **Make `session-start` and its spawned child tolerate a corrupt `workwire.json`.** `[ADDITIVE]`
   ADR-013's own non-negotiable is "always exits 0"; it exits 1 today. Fixing only the parent buys
   a green exit code while auto-join stays dead, because the child re-reads the same file.

7. **A docs truth pass.** `[ADDITIVE]`
   Five places promise a hub auto-start no CLI verb implements; `help` and `cli.md` advertise a
   `skill.json` `hubUrl` that nothing reads. Both are one-line-each fixes and both are actively
   misleading today.

8. **Group-declaration precedence and a per-folder opt-out.** `[ADDITIVE]`
   A cloned repo's `groups:` line currently beats the user's own `~/.claude/CLAUDE.md`, and
   `autoJoin` is a single global switch with no path filter.

9. **openspec scenarios for ADR-013, and an exec-the-binary test.** `[ADDITIVE]`
   `session-start` / `autoJoin` / `skill.json` have zero spec coverage, and
   `TestSessionStartAlwaysSucceeds` calls `cmdSessionStart` directly so it structurally cannot
   catch the config-load failure that breaks the ADR's own stated property.

10. **Either build §3/§4 or stop naming them.** `[ADDITIVE]`
    `workwire skills`, `workwire server` and `--server-url` do not exist; `skillConfig.HubURL` and
    `.TokenEnv` have zero readers. Decide and write the precedence order while doing it — neither
    the ADR nor the code states one.

---

## 2. Findings, most severe first

All PARTIAL. Corrected severity is given first; the originally claimed severity in brackets.

### A. The local hub's admin token is sent to whatever host `hubUrl` names — high `[claimed blocker]`

**Defect.** `cmd/workwire/main.go:196-208` reads `<configDir>/admin-token` whenever
`cfg.TokenFromEnv()` is empty and attaches it as `Authorization: Bearer` to `c.base+path`
(`main.go:245-247`), with no check on the resolved base. `cmd/workwire/cmd_listen.go:67-72` does the
same for the listener's registration bootstrap.

**Scenario (reproduced).** `WORKWIRE_CONFIG_DIR=$D WORKWIRE_HUB_URL=http://127.0.0.1:19999 workwire
status` against a capture listener put `Authorization: Bearer fake-admin-token-abc123` on the wire —
on `GET /health`, which `internal/server/server.go:118` documents as unauthenticated in every auth
mode. So the credential leaves on a bare reachability probe. That token is `KindAdmin` on the local
hub, which may read *any* agent's inbox (`internal/server/server.go:282-286`), is minted once and
never rotated (`internal/auth/auth.go` `EnsureAdminToken`), and is exactly what ADR-007 claims to
withhold ("co-tenants without file access are locked out"). Two aggravators, both verified: the
listener classifies 401/403 as retryable (`internal/listen/listen.go:162-166`), clears the agent
secret (`:763-765`) and re-enters fresh registration (`:350`) with `MaxRetries` defaulting to 0 =
forever (`cmd_listen.go:30`, `listen.go:770`) — so a host that has already refused the token is
handed it again every ≤30s indefinitely; and `credentials.json` is keyed by name, not hub
(`listen.go:842`), so the local per-agent secret leaks too and gets clobbered on the way back.

**Why PARTIAL.** The claim chained this to ADR-013 §4. Wrong: §4 is not implemented
(`autojoin.go:45,48` are dead fields), and `hubUrl` has been a persisted client setting since
ADR-006 (`internal/config/config.go:22`, documented in `site/src/content/docs/deploy.md`). This is a
present-tense defect in shipped v0.1.0, not a new one. Exploitability is also narrower than
"blocker": the token grants admin on a hub that binds 127.0.0.1 by default, so the receiver needs a
route to the victim's loopback — i.e. shared-box co-tenants and container/reverse-proxy
deployments. And the *documented* remote path does not leak: if `$WORKWIRE_TOKEN` is set,
`TokenFromEnv()` wins. The defect is the silent fallback when it is unset, which turns a
should-be-401 into credential disclosure.

**Fix.** One `isLoopbackBase(url)` helper — none exists anywhere in the repo, verified — called by
`main.go:196` and `cmd_listen.go:67`. Loopback ⇒ admin-token file as today. Non-loopback ⇒ env var
only, and hard-fail with "hubUrl is remote; set $<name> — the local admin token is never sent to a
remote hub". Test that no request with a non-loopback base carries the file's bytes. Make 401/403
terminal for a non-loopback base. Key `credentials.json` by hub base. **BREAKING** — say so in
`deploy.md`. **Drop from scope:** refusing non-loopback plain `http://` would break a working LAN
hub and belongs in ADR-010's deferred server half.

### B. Lease-liveness is reported as answerability; auto-join makes that the default state — major `[claimed blocker]`

**Defect.** `cmdSessionStart` (`cmd/workwire/autojoin.go:261-292`) ends at `spawnListener`; nothing
forks an answerer. The answerer is forked only when the user says the trigger phrase
(`cmd/workwire/skills/workwire/SKILL.md:9-11, 50-56`). The listener takes the hub lease, which
`registry.go:376` reports as `listener`, which `main.go:446-452` uses to suppress the only honesty
warning `ask` has.

**Scenario (reproduced end-to-end).** Hub on :14477, `session-start` run from a folder named `api`:
it printed `auto-joined as api (listening in the background)`, exit 0, listener only. `workwire ask
api "…" --timeout 20s` printed **no** warning and failed with `no answer within 20s`. The envelope
sat unconsumed in `sessions/api/inbox.ndjson`; no `inbox.offset` was ever written. `workwire peers`
showed `api` with no `[no live listener]` mark.

**Why PARTIAL.** "This is a regression" is false, and that was the severity argument. `listener` has
never meant "an answerer is attached" — both `ListenerLive` and the `ask` warning arrived in
`558d03d`, before every ADR-013 commit — and the phrase path already decoupled the lifetimes: the
skill's `nohup … & disown` listener outlives the session that started it, and the answerer fork is
time-boxed by the skill to ~20 rounds / ~15 idle minutes (`SKILL.md:70-84`). "Structurally mute" also
overstates it: delivery is durable, the queue is consumed when the session next engages the skill,
and a human can `workwire answer <id>`. ADR-013 changed the *frequency*, not the semantics.

**Fix.** `[ADDITIVE]` An `answering` flag on `/agents`, renewed by whatever is advancing
`inbox.offset`, so `peers` renders three states (`[no listener]` / `[listening, no answerer — n
pending]` / live) and `ask` warns immediately. This alone also covers the three pre-existing cases.
Optionally have the hook recruit the session via Claude Code's `hookSpecificOutput.additionalContext`
— but note that fights ADR-013's own "silent when it has nothing to say" and the `"async": true`
hook entry, and costs tokens in every session in every repo. That is an ADR decision, not a patch.

### C. The derived name is a folder basename with no collision check — major `[claimed blocker]`

**Defect.** `cmd/workwire/autojoin.go:272` derives the hub identity from `filepath.Base(dir)`. Both
adopt paths (`autojoin.go:284`, `cmd/workwire/cmd_listen.go:60`) treat "the name-keyed flock is
held" as same-folder adoption without comparing the folder behind the lock.

**Scenario (reproduced).** `$D/a/api` and `$D/b/api`, one config dir, run back-to-back: **both**
printed `auto-joined as api`. Afterwards exactly one `run/api.lock`, one `sessions/api`, one live
listener (`--dir …/a/api`), and `auto-join.log` held the unseen `adopting the running listener for
api`. `b/api` is on the wire under no name at all while its user and its model were told it joined;
`workwire ask api "where is the retry logic?"` is then answered confidently about a different
codebase.

**Why PARTIAL, and one proposed fix rejected.** The claim framed name-keying of the flock as the
defect and proposed hashing the folder path. That would be **worse**: `credentials.json`
(`listen.go:843`), `sessions/<name>/inbox.ndjson`, the cursor and the hub lease
(`agents.go:196`) are all name-scoped, so two same-named listeners would share one credential, flap
provenance on every heartbeat, share one inbox file and deadlock on one lease. The acquire-then-
release TOCTOU (`autojoin.go:281-288`) is real but narrow — a 2s gap already produces the correct
"adopting" line — and closing it does not fix the cross-folder ghost at all. Its actual cost is a
*same-folder* one the claim missed: inside the window both sessions print "auto-joined" and may both
spawn answerers over the same inbox, and `workwire answer` has no idempotency guard — a direct
violation of ADR-013's "a question is never answered twice". Mitigants: no corruption, no lost
messages, the loser exits cleanly, and ADR-011 provenance is stamped correctly so `peers` shows the
wrong answerer's `project` path. The collision predates auto-join; what changed is that the adopt
line now goes somewhere nobody reads.

**Fix.** `[ADDITIVE]` Persist `abs-dir → {name, agentId}` (a `folders.json`) so a folder's name is
stable across restarts; on a lock held by a *different* abs dir, do not claim adoption —
disambiguate locally (`api` → `acme-api`) or let the listener's 409 path assign `api-2` (note this
requires registering under a fresh name; sharing the name-keyed credential returns 200 Updated, so
the 409 path is unreachable today). Record the holder's abs dir in the lock file. Separately, drop
the pre-check and have `workwire listen` emit `role=driver` / `role=passenger holder=<dir> pid=<n>`
as its first line, so `session-start`'s claim is honest by construction. Keep the flock name-keyed.

### D. Nothing tears down, enumerates, stops or explains a listener — minor-to-moderate `[claimed major]`

**Defect.** `autojoin.go:315-321` starts the listener detached and never waits; `internal/listen`'s
`Run()` exits only on a signal. Zero hits for `SessionEnd` anywhere in the repo. `main.go:38-83` has
no `stop` / `listeners` / `doctor`. `cmd_install.go:99-108` `--on/--off` flip one key in
`skill.json`; `uninstall` (`:219-261`) takes only `--service` and `--auto` and touches no listener.
`cmdStatus` (`main.go:270-282`) is a three-line `/health` ping that knows nothing about the client
half.

**Scenario (observed live on this machine).** An orphan `~/.local/bin/workwire listen --agent koine`
from a closed session is running, and `workwire peers` lists it with no `[no live listener]` mark.
Three stale `run/*.lock` files sit in `~/.config/workwire/run/`. `session-start`'s adopt path
(`autojoin.go:283-289`) does no exe or version comparison, so a developer with a stale binary on
PATH concludes his new code is live.

**Why PARTIAL.** "No verb lists them" is false — `workwire peers` enumerates peers with a live-lease
flag and provenance, which is exactly the name list needed for `group leave`. "`pkill` appears in no
doc" is false — `SKILL.md:94` ships `pgrep -f "workwire listen --agent $N"`. Orphan listeners belong
to *closed* sessions, so `@all` traffic wakes nobody and burns zero tokens. And a false-positive
`listener` costs the asker one warning line, not five minutes: `cmdAsk` blocks for the full timeout
in **both** branches (`main.go:445-485`). Listener survival is ADR-013's deliberate adopt-and-outlive
design, so a naive SessionEnd teardown would break adoption and the queue-for-next-session
behaviour.

**Fix.** `[ADDITIVE]` In value order: (1) run-state file per listener (`run/<key>.json`: name, abs
dir, pid, exe, version, started, hubUrl, registered-as) plus `workwire doctor` / `status --local`;
(2) `workwire listeners` and `workwire off` as ergonomic wrappers; (3) an **idle exit** in the
listener (no session has consumed the inbox for N hours ⇒ release the lease and exit) in preference
to a SessionEnd hook, since it reaps orphans without breaking adoption. Cheap now: print exe+version
in the "adopting" line, sweep stale `run/*.lock`.

### E. `skill.json` declares two keys nothing reads; §3/§4 are prose — minor-to-moderate `[claimed major]`

**Defect.** `skillConfig.HubURL` and `.TokenEnv` (`cmd/workwire/autojoin.go:44-48`) are unmarshalled
and never consulted — grep over `cmd/` + `internal/` returns only the struct declaration. The only
consumers of the loaded struct are `autojoin.go:266` (AutoJoin, AgentName) and `cmd_install.go:110`
(AutoJoin). Yet `workwire help` (`main.go:125`) prints `{"autoJoin":true,"agentName":"","hubUrl":""}`
and `site/src/content/docs/cli.md:576,626` list `hubUrl` as a skill.json key. Set it and you get no
hub change, no warning, and no mention of the file anywhere — the spawned listener re-resolves from
`cfg.HubURL` at `cmd_listen.go:78`.

**Why PARTIAL.** The capability is not missing. `hubUrl` / `tokenEnv` in `workwire.json`
(`config.go:22,24,47,49`) and `WORKWIRE_HUB_URL` / `WORKWIRE_TOKEN_ENV` (`:129,131`) already point
every client verb and the listener at a remote hub, with openspec coverage at
`openspec/specs/agent-skill/spec.md:47` and `openspec/specs/auth/spec.md:48`. The problem is a
duplicate key *name*, not an absent feature. Also: `workwire skills install --server-url` is
documented nowhere shipped (it exists only inside ADR-013 §3), and `tokenEnv` in `skill.json` is
advertised nowhere at all — it was added one commit ago as an inert placeholder. An accepted ADR
with unbuilt sections is backlog, not a defect.

**Fix.** `[ADDITIVE]` Now: drop `hubUrl` from `main.go:125` and `cli.md:576/626`, or have
`loadSkillConfig` print one stderr line when either key is non-empty; disambiguate `deploy.md:22`.
When §3/§4 are built: write the precedence order into the ADR (flag > `WORKWIRE_*` env > skill.json >
workwire.json > defaults — the ADR is silent), resolve it in one place, pass the resolved hub
explicitly to the listener at `autojoin.go:306`, and rename skill.json's token key (e.g.
`clientTokenEnv`) so it stops shadowing the hub's `tokenEnv` — today one `WORKWIRE_TOKEN` serves both
the hub admin token (`main.go:145`) and the client's outbound token (`main.go:197`).

### F. `session-start` exits 1 on a corrupt `workwire.json` — medium `[claimed blocker]`

**Defect.** `cmd/workwire/main.go:33-36` runs `config.Load()` and `fatal()`s before the verb switch
reaches `case "session-start"` at `:71`; `internal/config/config.go:80-82` errors on malformed JSON.
ADR-013 lines 43-45 say verbatim: "It **always exits 0, immediately**. A missing config, a **corrupt
config**, or a dead hub can never make a session fail to start."

**Scenario (reproduced).** `printf '{ "lastMessages": 8,, }' > $CFG/workwire.json; workwire
session-start` → `workwire: parse …/workwire.json: …`, EXIT=1. With the file absent, exit 0.
`cmd/workwire/autojoin_test.go`'s `TestSessionStartAlwaysSucceeds` builds a `config.Config` by hand
and calls `cmdSessionStart` directly, so it can never traverse main's loader; there is no
exec-the-binary test anywhere in the repo.

**Why PARTIAL.** "Dead machine-wide with no other symptom" is false and was the severity argument.
`config.Load()` has one call site, so a corrupt file fails *every* verb identically — `listen`,
`status`, `peers` and `serve` all exit 1 naming the exact file. The hub will not start. The failure
is loud everywhere except at the hook. And the hook is installed `"async": true`, so a non-zero,
non-2 SessionStart hook does not abort a Claude Code session: the ADR's *intent* holds; the
mechanism it chose to guarantee it does not.

**Fix.** `[ADDITIVE]` Tolerant loader for `session-start` falling back to `config.Defaults()` +
ConfigDir with one line into `auto-join.log` — **and the same tolerance, or `WORKWIRE_*` env
propagation, in the spawned `workwire listen` child**, or the fix buys a green exit code while
auto-join stays dead. Add the exec-based test. Whether the *other* client verbs should tolerate a
garbage hub config is a separate call (failing loud is arguably right for `serve`).

### G. A hub auto-start is promised in five places and implemented nowhere — minor `[claimed major]`

**Defect.** `README.md:39`, `site/src/content/docs/cli.md:90`, `deploy.md:48`, `main.go:121-122` and
`cmd_install.go:30-31` all say a hub auto-starts when any verb finds no hub on a loopback `hubUrl`.
Reproduced: with nothing on the port, `workwire peers` printed `connection refused`, exit 1, and
started nothing. Separately `autojoin.go:316-320` prints `auto-joined … (listening in the
background)` unconditionally after `cmd.Start()`, and sends listener output to the shared
`auto-join.log` while `SKILL.md:94`'s watcher tails `sessions/<name>/listen.log`.

**Why PARTIAL.** The normative spec never asked a CLI verb for this:
`openspec/specs/agent-skill/spec.md:31` R2 and ADR-003 put the loopback auto-start on **the skill**,
which implements it at `SKILL.md:20`. So the implementation matches the spec and the prose
over-generalises. The claimed log flood is measurably false: `Runner.note`
(`internal/listen/listen.go:700-712`) logs once per condition *transition* — measured at exactly 2
lines / 357 bytes per listener, unchanged after 90s against a dead hub. The forever-retrying
listener with no hub is a decided, tested tradeoff (`autojoin_test.go:195` "hub unreachable is still
exit 0") and self-heals the moment any hub appears.

**Fix.** `[ADDITIVE]` Fix the prose to say the *skill* starts a loopback hub. Stop printing success
`cmd.Start()` has not earned (ADR-013's own "silent when it works"). Send auto-join listener output
to `sessions/<name>/listen.log`, keeping `auto-join.log` for failures before a name is known.

### H. A cloned repo's `groups:` beats the user's own, and there is no per-folder opt-out — low `[claimed major]`

**Defect.** `internal/persona/persona.go:34-39` searches the repo's own files **before**
`~/.claude/CLAUDE.md` and returns on first hit, so a third-party repo's declaration silently
overrides the user's machine-wide one. `skill.json`'s `autoJoin` is a single global switch — no path
filter, no `workwire: off` key in the repo's own block.

**Why PARTIAL — the consent framing is inverted.** ADR-012 line 57 forbids **a peer adding another
peer**. This is a self-join by the local peer with its own secret, and it is *mandated*:
`openspec/specs/agent-skill/spec.md:317-331` (R16) makes it a normative SHALL whose scenario asserts
"the join request names no other peer". The wake-capability delta is zero anyway —
`internal/registry/registry.go:263-269` puts every peer in `@all` at registration unconditionally, so
`@platform` is strictly narrower than membership it already has, and ADR-013's Consequences already
accept the `@all` token cost. There is also no wake on the auto-join path: `workwire listen` only
appends to `inbox.ndjson`; nothing reads it until a human triggers the skill. Group membership grants
no read escalation (ADR-012 says groups are unauthenticated-by-design inside a local mesh).
`personaSource: "derived"` would be *weaker* than what ships — `spec.md:289` already declares all
personas untrusted DATA — and persona derivation is pre-ADR-013 (ADR-011). Discovery is one
unflagged `workwire groups`, not a name-guessing exercise.

**Fix.** `[ADDITIVE]` Invert precedence in `DeriveGroups` so the user's own `~/.claude/CLAUDE.md`
wins; add a `workwire: off` key (`keyValue()` already parses the block) and/or an `exclude`/`only`
glob list in `skill.json`; log declared-group joins to the session log. **Do not** implement the
`--groups ""` proposal-only change — it contradicts openspec R16 and would need re-speccing first.

---

## 3. What holds up

Decisions that survived the attack, stated plainly, including the ones that are accepted tradeoffs
rather than oversights.

- **`session-start` as a first-class, tested CLI verb rather than shell in JSON.** Exactly the right
  call — it is why finding F is a three-line loader change and not an archaeology project. ADR-013's
  own Consequences give the reason.
- **The hook never blocking or failing a session start.** `"async": true` + exit-0 discipline holds
  in practice even where the mechanism leaks (F). `autojoin_test.go:195` tests the dead-hub case
  explicitly.
- **The listener retrying forever and never exiting on a transient condition.** Deliberate
  (`SKILL.md:46-48`, "the listener retries by itself… it is not a failed join"), self-healing, and
  quiet: `Runner.note` logs one line per condition transition, measured.
- **Adopt-and-outlive.** Delivery is durable and nothing is lost. A question asked at an unattended
  folder is answered late, not never. This is why the SessionEnd hook is the *weakest* part of the
  teardown fix and an idle-exit is the better shape.
- **The flock being keyed by agent name.** Required, not a bug — everything downstream
  (credentials, session inbox, cursor, hub lease) is name-scoped. Hashing the folder path would be
  strictly worse.
- **Self-join of declared groups.** Spec-mandated (R16); ADR-012's ban is on peer-adds-peer, and no
  API surface for that exists.
- **Persona and origin as untrusted data.** `spec.md:289` and `persona.go:5-9` already say
  "displayed and weighed, never executed" — stronger than the marker the review proposed.
- **`@all` waking every joined session.** ADR-013's Consequences state and accept this cost, and
  name the dial. Not a discovery.
- **Remote `hubUrl` already working through `workwire.json` / `WORKWIRE_HUB_URL`**, with openspec
  coverage. §4 would add a third way to reach an existing, tested path.
- **Keeping `install --service` / `--skills` as aliases.** Nothing in this review argues against it.
- **No wire, schema or migration change is required by any finding.** `SchemaVersion` stays 1;
  `meta` stays open. Every fix above is additive except the deliberate credential gate.

---

## 4. Recommended ADR-013 amendments, in priority order

1. **§4 — add the credential rule.** "A client never sends the local hub's admin token to a
   non-loopback `hubUrl`. A remote hub requires the token env var; absent it, client verbs fail
   with a message naming the variable." Note it is breaking for shared-admin-token LAN setups, and
   record that `credentials.json` must be keyed by hub base. Without this, §4 should not be built.
2. **§2 — correct "One listener per folder. The flock already guarantees this."** It guarantees one
   listener per *name*. State that a folder's identity must be disambiguated and persisted, and that
   a session is a passenger only when the lock holder is the **same** folder.
3. **§2 — state what auto-join does and does not recruit.** It takes the listen lease; it does not
   attach an answerer. Either the hook recruits the session (with the token cost and the "silent when
   it works" tension acknowledged), or the ADR requires an answerer-liveness signal distinct from the
   lease so `ask` and `peers` stop implying answerability.
4. **§2 — restate the exit-0 property as an outcome, not a mechanism.** "The hook can never block or
   fail a session start" — and extend the requirement explicitly to the process it spawns, which
   re-reads the same config.
5. **§1 — write the precedence order.** flag > `WORKWIRE_*` env > `skill.json` > `workwire.json` >
   defaults, resolved in one place. Rename `skill.json`'s token key so it does not shadow the hub's
   `tokenEnv`.
6. **New section — teardown and observability.** Run-state per listener, `doctor` / `listeners` /
   `off`, listener idle-exit, exe+version in the "adopting" line. Today the ADR ships a spawner with
   no reaper and no local view.
7. **§2 — a per-folder dial.** `autoJoin` is currently all-or-nothing; add path filters and a repo-side
   `workwire: off`, and state that the user's own global declaration outranks a cloned repo's.
8. **Consequences — say §3 and §4 are unbuilt** and that docs must not name `workwire skills` /
   `workwire server` / `--server-url` until they are. Add the openspec scenarios for §1–§2, which do
   not exist.

---

*Method: 71 raw claims, 8 shortlisted, 8 verified against HEAD `9db960e` with a built binary, a live
hub on a scratch config dir, and a capture listener. 8/8 PARTIAL, 0 CONFIRMED-as-stated, 0 blockers.*
