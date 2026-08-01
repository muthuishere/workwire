# ADR-016: presence is not a boolean, and "answering" is not a state we should maintain

Status: proposed · Date: 2026-08-01 · Research: XMPP, MQTT, Kubernetes, Erlang/OTP, Phoenix, Akka, IRC, A2A

## Context

We have now failed the same test three times in three different costumes.

1. A 15-minute answerer **fork** expired and nothing re-armed it. 8 of 9 live peers sat at
   `[listening, no answerer]`.
2. A persistent **Monitor** replaced it — and 21 minutes later a live session with its watch
   armed still reported `answering: false`, because the declaration is a marker file that
   ages out and nothing renewed it.
3. `workwire watch` fixed that by making renewal a property of the process that *is* the
   watch. It passes the 21-minute test (3.3s answer after idle).

Fix 3 works, and it works by **deleting the attention layer**: the process asserts "someone
is here" for as long as it runs. That holds until a session is wedged mid-tool-call behind a
perfectly healthy watch — which is the original failure wearing a third costume, and we will
find it the first time a peer spends six minutes on a build.

So before adding a fourth mechanism, we went and read what mature systems do. The summary is
unkind and useful: **almost every part of this is a solved problem, and we solved it wrong
by modelling attention as one boolean owned by one process.**

## What the prior art says

**XMPP separates four things we conflate into one** (RFC 6121 §4, XEP-0085, XEP-0352,
XEP-0012). Transport liveness is *inferred by the server*. Availability is *declared by the
client*, and connecting does not make you available. Per-conversation attention is a
separate declaration with decay timers — `active` / `composing` / `paused` / `inactive`
(~2 min) / `gone` (~10 min) — described in XEP-0085 as "session-level state rather than a
per-message event", which is verbatim our problem. And idle duration is a **pull**
(XEP-0012): ask a peer how long since it did anything, rather than making it continuously
assert that it is awake.

**MQTT publishes death instead of leaving silence** (MQTT 5.0 §3.1.2.5, §3.1.3.2.2). A Will
is registered at connect and published when the session ends — authored by the dying peer
*while it was still healthy*, which is the only moment it can describe itself accurately.
Will Delay debounces it, so a 3-second reconnect does not page everyone. Our lease expiry is
pure silence: every observer polls and infers.

**Kubernetes keeps liveness and readiness split, and gives unreadiness a consequence.** A
failed readiness probe removes the pod's address from the Service, so senders stop
addressing it. We keep filling the inbox of a peer that will not read it. Their docs also
warn that a badly-tuned liveness probe causes cascading restarts — our `answering` going
stale is exactly what a threshold of 1 with no re-entry path feels like.

**Erlang gives you `noproc` immediately.** `erlang:monitor` returns `DOWN` with no race and
no timeout, and `nodedown` is ordered *after* all in-flight messages. Our sender waits 25
seconds on a long-poll and learns nothing about a peer that died a minute ago.

**Phoenix Presence has two-stage death** — `down_period` ≈ 30s (temporarily gone, state
retained) versus `permdown_period` = 20 minutes (permanently gone, state discarded) — and
tracks **a list of metas per identity**, one per connection, because one identity legitimately
has several. We have neither, and we will need the second the moment two agent windows are
open on one repo.

**Akka names our actual bug.** A mailbox is enqueueing, not processing; Akka is blunt that
ordering "applies to the order in which messages are enqueued into the recipient's mailbox"
and that knowing a message was *handled* requires a business-level ack from the actor.
Our session inbox file is a mailbox. **`answering` is us trying to infer processing from
transport, which Akka says outright cannot be done.**

**Phi accrual failure detection** (Hayashibara et al., SRDS 2004) replaces the boolean
entirely: a sliding window of heartbeat inter-arrival times yields a continuous suspicion
level φ, and each observer picks its own threshold. Akka defaults to φ=8. The rise rate
adapts to the peer's own variance — a punctual peer is suspected quickly, a jittery one gets
slack automatically. An LLM session's cadence is *wildly* variable; one global timeout for
all peers is the worst possible fit.

**IRC does the cheapest useful thing in the whole survey** (RFC 2812): `AWAY` text is
returned **to the sender, at send time**, in the peer's own words.

**And nothing models this for LLM sessions.** A2A models *task* state (`submitted`,
`working`, `input-required`…), not agent attention — its Agent Card advertises capabilities,
never current availability, so "delivered but nobody is looking" is indistinguishable from
`working`. LiveKit's `AgentSession` has a 15-second `user_away_timeout` and publishes both
sides' state, which is the closest shipping analogue and is about voice, not work. There is
no standard to conform to here.

## Decision

**Stop maintaining `answering` as a boolean asserted by a process. Derive attention from
evidence, report it as a graded state, and give the sender the peer's own words.**

1. **Presence becomes three states, not two, with hysteresis.** `attentive` (evidence of
   processing within the recent window), `quiet` (watch alive, nothing processed lately),
   `gone` (no watch). Transitions debounce in both directions — Phoenix's two-stage
   `down_period` / `permdown_period`, k8s's `failureThreshold`/`successThreshold`. "Quiet"
   is a first-class state, not a failure.

2. **The evidence is an ACK, not a heartbeat.** A session advancing `inbox.offset` or
   posting an answer is proof of processing; a watch process running is not. This is Akka's
   rule and it is the one that ends the whole class of bug: we stop trying to infer
   processing from transport.

3. **Idle time is exposed and pulled, not asserted.** Every peer carries
   `idleSeconds` — time since its last evidence of processing — and the asker decides what
   is acceptable. One number, no policy baked in (XEP-0012).

4. **The sender is told at send time, in the peer's own words.** A send or ask to a `quiet`
   or `gone` peer returns its state, its idle time, and a stand-down reason it authored
   (IRC `RPL_AWAY`). We already warn; what we lack is *why* and *for how long*.

5. **Standing down is a published event, not an absence.** A watch shutting down posts its
   own stand-down — an MQTT Will authored while healthy — with a delay so a restart does not
   announce a death that did not happen.

6. **Presence is per-connection, aggregated per identity** (Phoenix metas): two windows on
   one repo are two watches, one peer, and the peer is attentive if any watch is.

Deferred, deliberately: **phi accrual**. It is the right long-term answer and it needs a
distribution of real inter-activity times to calibrate against — `spikes/08-stress` is now
producing exactly that data. Revisit once we have a few hundred samples per peer.

## Consequences

- `peers`, `/metrics` and the dashboard gain a graded state and an idle number; `answering`
  as a bare boolean is deprecated but kept until callers migrate.
- The stress harness gains a new axis worth measuring: does `attentive` predict an answer
  better than `answering` did? If it does not, this ADR is wrong and should be reverted.
- We are not conforming to a standard here because none exists — which means the burden is
  on us to borrow the *mechanisms* faithfully rather than invent our own vocabulary.
