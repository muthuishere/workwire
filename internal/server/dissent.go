package server

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/registry"
	"github.com/muthuishere/workwire/internal/store"
)

// describeDissent renders one objection with its provenance, e.g.
// `web (agent, muthuishere/webclient@feat/tokens f9e0d1*): tokens rotate`.
func describeDissent(d store.Dissent) string {
	who := d.Peer
	bits := []string{d.Kind}
	if s := d.Origin.String(); s != "" {
		bits = append(bits, s)
	}
	out := fmt.Sprintf("%s (%s)", who, strings.Join(bits, ", "))
	if strings.TrimSpace(d.Text) != "" {
		out += ": " + d.Text
	}
	return out
}

// abandonWindow is how long an objection must go undefended before it stops
// blocking. Long on purpose: wrongly discarding a dissent is the failure this
// whole design exists to prevent, so the bar is "nobody could plausibly still
// be behind this", not "we would like to close".
const abandonWindow = time.Hour

// splitAbandoned divides open dissents into those still defensible and those
// whose author has gone (ADR-017, Dung reinstatement in its narrow form).
//
// Every condition here is checkable from the envelope log and the registry —
// ADR-017's rule that no hub rule may rest on something we cannot verify. The
// first draft of this used "no live listener" alone and was dangerous: a human
// at a terminal, or any CLI-only peer, never holds a listen lease, so it would
// have classified EVERY human objection as abandoned. Three conditions now,
// all required.
func (s *Server) splitAbandoned(all []store.Dissent) (live, abandoned []store.Dissent) {
	_, per := s.store.Snapshot(nil)
	now := time.Now()
	for _, d := range all {
		// 1. A HUMAN's dissent is never abandoned by a timer. A person who
		//    objects and walks away has still objected; ADR-011 says not even
		//    another human may close over it, and a clock certainly may not.
		if d.Kind == registry.KindHuman {
			live = append(live, d)
			continue
		}
		if a, ok := s.registry.Get(d.Peer); ok && a.IsHuman() {
			live = append(live, d)
			continue
		}
		// 2. A peer with a live listener is present, whatever else is true.
		if s.registry.ListenerLive(d.Peer) {
			live = append(live, d)
			continue
		}
		// 3. Anything authored after the objection means they are still in it.
		spoke := per[d.Peer].LastSpoke
		if spoke > d.TS {
			live = append(live, d)
			continue
		}
		// 4. And it must have been undefended for a long time — measured from
		//    the dissent itself, so a fresh objection from a peer that has not
		//    yet taken a lease is never swept away.
		since := d.TS
		if spoke > since {
			since = spoke
		}
		t, err := time.Parse(time.RFC3339Nano, since)
		if err != nil || now.Sub(t) < abandonWindow {
			live = append(live, d)
			continue
		}
		abandoned = append(abandoned, d)
	}
	return live, abandoned
}

func describeDissents(list []store.Dissent) string {
	parts := make([]string, 0, len(list))
	for _, d := range list {
		parts = append(parts, describeDissent(d))
	}
	return strings.Join(parts, "; ")
}

// checkThreadRules enforces convergence, the round cap, dissent and valid
// closure on an existing thread (ADR-009 + ADR-011 §3). It returns an HTTP
// status and message when the send must be rejected, and otherwise fills
// closedOver with the dissents a successful closure overrides.
func (s *Server) checkThreadRules(id auth.Identity, ts store.ThreadState, req sendRequest, closedOver *[]store.Dissent) (int, string) {
	from := id.Name()
	human := id.IsHuman()
	// The admin token is an OPERATOR credential — the person running the hub,
	// doing maintenance. It may close a thread it did not open (consolidating
	// duplicates, retiring dead announcements) without pretending to be a
	// human peer whose ruling carries decision precedence (ADR-011 §3).
	//
	// Closing 29 duplicate threads on 2026-08-01 required registering a human
	// peer purely to have the authority, which put the heaviest voice on the
	// mesh behind pure housekeeping. Operator work should sound like operator
	// work.
	operator := id.Kind == auth.KindAdmin

	// Reopen first: it is the one send that is legitimate on a closed or
	// stalled thread, and only a human may do it (ADR-011 §3a).
	if req.Kind == "reopen" {
		if !human {
			return http.StatusForbidden, fmt.Sprintf(
				"only a human peer may reopen thread %s — a human ruling is final and agents may not reopen anything (ADR-011); record a kind \"dissent\" for the history instead",
				ts.ThreadID)
		}
		return 0, ""
	}

	if ts.Resolved {
		// A closed thread ends the decision, not the disagreement: a dissent
		// is still recorded as history and does not reopen it (ADR-011 §3a).
		if req.Kind == "dissent" {
			return 0, ""
		}
		return http.StatusConflict, fmt.Sprintf(
			"thread %s is resolved: it was closed by %s and only kind \"dissent\" (recorded as history, does not reopen it) is still accepted — a human peer may \"reopen\" it, otherwise start a new thread",
			ts.ThreadID, ts.ClosedBy)
	}

	// The round cap stops CHATTER. It must not stop the three kinds that end a
	// thread or record its state rather than continue it — otherwise a stalled
	// thread can never be closed by anyone, which is exactly what happened on
	// 2026-08-01: two threads sat at 24/24, and every attempt to retire them
	// was refused by the cap before the closure rules were even reached. A
	// stall is handed back to its initiator; it must be possible to accept it.
	endsOrRecords := req.Kind == "resolved" || req.Kind == "dissent" || req.Kind == "withdraw"
	if s.cfg.MaxThreadMessages > 0 && ts.Count >= s.cfg.MaxThreadMessages && !endsOrRecords {
		return http.StatusConflict, fmt.Sprintf(
			"thread %s is stalled: it reached the per-thread cap of %d messages and is handed back to its initiator (%s) with the disagreement intact — raise maxThreadMessages in workwire.json (or WORKWIRE_MAX_THREAD_MESSAGES) to allow more rounds",
			ts.ThreadID, s.cfg.MaxThreadMessages, ts.Initiator)
	}

	if req.Kind != "resolved" {
		return 0, ""
	}
	if operator {
		// An operator closes over agent dissent like a human, but never over a
		// HUMAN's open dissent: maintenance may tidy a mesh, not overrule a
		// person.
		for _, d := range ts.Dissents {
			if d.Kind == registry.KindHuman && d.Peer != from {
				return http.StatusConflict, fmt.Sprintf(
					"thread %s has an open dissent from %s (a human) — an operator may not close over a person's objection: %s",
					ts.ThreadID, d.Peer, describeDissent(d))
			}
		}
		*closedOver = append(*closedOver, ts.Dissents...)
		return 0, ""
	}

	// --- closure validity (ADR-011 §3) ---
	if !human {
		// Only the initiator decides (ADR-009); a participant recommends
		// with kind:"proposal", which never closes the thread.
		if ts.Initiator != "" && ts.Initiator != from {
			return http.StatusForbidden, fmt.Sprintf(
				"only the initiator of thread %s (%s) may send kind \"resolved\" — send kind \"proposal\" to recommend a resolution instead",
				ts.ThreadID, ts.Initiator)
		}
		// A dissent from a session that has GONE cannot be withdrawn by anyone
		// — only its author may withdraw it — so the old rule left the thread
		// unclosable forever. That is a deadlock, not a principle.
		//
		// Dung (AIJ 77, 1995) calls the repair reinstatement: an objection its
		// author can no longer defend stops blocking, while staying on the
		// record. We take the narrow, checkable form of it — abandonment is
		// observable from the envelope log (the peer has no live listener and
		// has authored nothing since it dissented), which satisfies ADR-017's
		// rule that no hub rule may rest on something we cannot verify.
		live, abandoned := s.splitAbandoned(ts.Dissents)
		if len(live) > 0 {
			return http.StatusConflict, fmt.Sprintf(
				"thread %s has %d open dissent(s) and an agent can never override a dissent — %s. Two legitimate paths: get the dissent withdrawn (kind \"withdraw\", by the dissenter only), or ask a human peer to decide and close it",
				ts.ThreadID, len(live), describeDissents(live))
		}
		// Nothing live blocks it. Anything abandoned is recorded as overridden
		// rather than erased: "we closed over a standing objection from a peer
		// that went away" is a better record than a consensus that never was.
		*closedOver = append(*closedOver, abandoned...)
		return 0, ""
	}

	// A human is accountable for the call, so the summary is required.
	if strings.TrimSpace(req.Text) == "" {
		return http.StatusBadRequest, fmt.Sprintf(
			"closing thread %s as a human requires a non-empty summary: you are accountable for the call", ts.ThreadID)
	}
	// A human may close over any number of AGENT dissents — that is what
	// precedence means — but never over another HUMAN's open dissent.
	if blocking := ts.OpenHumanDissents(from); len(blocking) > 0 {
		return http.StatusConflict, fmt.Sprintf(
			"thread %s carries an open dissent from another human and you cannot overrule a colleague by typing first — %s. They withdraw it (kind \"withdraw\"), or the thread stays open and contested",
			ts.ThreadID, describeDissents(blocking))
	}
	*closedOver = ts.OpenDissentsBy(from)
	return 0, ""
}
