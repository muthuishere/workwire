package server

import (
	"fmt"
	"net/http"
	"strings"

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

	// The round cap is unchanged and still trips first on runaway chatter.
	if s.cfg.MaxThreadMessages > 0 && ts.Count >= s.cfg.MaxThreadMessages {
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
		if len(ts.Dissents) > 0 {
			return http.StatusConflict, fmt.Sprintf(
				"thread %s has %d open dissent(s) and an agent can never override a dissent — %s. Two legitimate paths: get the dissent withdrawn (kind \"withdraw\", by the dissenter only), or ask a human peer to decide and close it",
				ts.ThreadID, len(ts.Dissents), describeDissents(ts.Dissents))
		}
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
