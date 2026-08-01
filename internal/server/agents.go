package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/envelope"
	"github.com/muthuishere/workwire/internal/registry"
)

// POST /agents (registry-a2a R1/R2, auth R3): 201 mints {agentId,
// agentSecret}; re-register with the correct secret is 200; a taken name is
// 409 {"error":"name taken","name","suggestion"}.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	var card registry.Card
	if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if card.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// The presented per-agent secret (if any) is the bearer token when the
	// caller authenticated as an agent.
	presented := ""
	if id.Kind == auth.KindAgent {
		presented = auth.BearerToken(r)
	}
	res := s.registry.Register(card, presented)
	switch {
	case res.Created:
		writeJSON(w, http.StatusCreated, map[string]string{
			"agentId": res.Agent.AgentID, "agentSecret": res.Secret,
		})
	case res.Updated:
		writeJSON(w, http.StatusOK, map[string]string{
			"agentId": res.Agent.AgentID, "name": res.Agent.Name,
		})
	case res.KindConflict:
		// Right credential, wrong ask: the card tried to change an established
		// kind. Nothing is updated — a silent flip would move decision
		// precedence (ADR-011 §3).
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "kind is fixed",
			"name":  card.Name,
			"kind":  res.KindWas,
			"detail": fmt.Sprintf(
				"%s is already registered as a %s and a peer's kind cannot change on re-registration — re-register without a \"kind\", or join under a different name",
				card.Name, res.KindWas),
		})
	case res.TreeConflict:
		// One working tree, one peer (registry-a2a R12). Registering a second
		// name for a tree that already has a live peer is how `koine` and
		// `koine-main` both existed on 2026-08-01: every question to that
		// codebase then became a coin flip between a half with an answerer
		// and a half without.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "tree already has a peer",
			"name":  card.Name,
			"peer":  res.TreeHolder,
			"detail": fmt.Sprintf(
				"this working tree is already on the wire as %q — one tree, one peer. Use that name, or stop its listener and `workwire forget %s` first if it is a leftover",
				res.TreeHolder, res.TreeHolder),
		})
	default: // conflict — existing registration untouched, no takeover
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "name taken", "name": card.Name, "suggestion": res.Suggestion,
		})
	}
}

// GET /agents — live agents only, no secret material (registry-a2a R4).
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	live := s.registry.Live()
	out := make([]map[string]any, 0, len(live))
	for _, a := range live {
		out = append(out, map[string]any{
			"name":         a.Name,
			"description":  a.Description,
			"project":      a.Project,
			"persona":      a.Persona,
			"kind":         registry.NormalizeKind(a.Kind),
			"origin":       a.Origin,
			"capabilities": a.Capabilities,
			"skills":       a.Skills,
			// Registered is not the same as reachable, and reachable is not the
			// same as answerable. `listener` is the delivery fact: a live listen
			// lease exists, so questions are being written into a session inbox.
			// `answering` is the answerability fact: something has declared
			// itself attached to READ that inbox. A listener outlives the
			// session that started it, so the two legitimately differ and are
			// reported separately.
			"listener":  s.registry.ListenerLive(a.Name),
			"answering": s.registry.AnswererLive(a.Name),
			"lastSeen":  a.LastSeen.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

// GET /agents/{name}/card — A2A v0.3.0 agent card (registry-a2a R6).
func (s *Server) handleCard(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	a, ok := s.registry.Get(name)
	if !ok {
		writeErr(w, http.StatusNotFound, "agent not found")
		return
	}
	skills := make([]map[string]any, 0, len(a.Skills))
	for _, sk := range a.Skills {
		tags := sk.Tags
		if tags == nil {
			tags = []string{}
		}
		skills = append(skills, map[string]any{
			"id": sk.ID, "name": sk.Name, "description": sk.Description, "tags": tags,
		})
	}
	if len(skills) == 0 {
		// Synthesize the default ask skill when registration carried none.
		skills = append(skills, map[string]any{
			"id":          "ask",
			"name":        "ask",
			"description": fmt.Sprintf("Ask %s a question; it answers from its own live context.", a.Name),
			"tags":        []string{"ask"},
		})
	}
	base := strings.TrimSuffix(s.cfg.HubURL, "/")
	card := map[string]any{
		"protocolVersion":    "0.3.0",
		"name":               a.Name,
		"description":        a.Description,
		"persona":            a.Persona,
		"kind":               registry.NormalizeKind(a.Kind),
		"origin":             a.Origin,
		"url":                fmt.Sprintf("%s/agents/%s/rpc", base, a.Name),
		"preferredTransport": "HTTP+JSON",
		"version":            "1",
		"capabilities":       map[string]any{"streaming": false, "pushNotifications": false},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             skills,
	}
	writeJSON(w, http.StatusOK, card)
}

// askCore enforces policy and queues a kind:"question" envelope. The
// registry is discovery-only: an aged-out agent still receives asks
// (registry-a2a R7); a never-registered name is 404.
func (s *Server) askCore(id auth.Identity, name, text, threadID string) (env *envelope.Envelope, status int, errMsg string) {
	target, ok := s.registry.Get(name)
	if !ok {
		return nil, http.StatusNotFound, "agent not found"
	}
	if text == "" {
		return nil, http.StatusBadRequest, "text is required"
	}
	if !registry.AskAllowed(target, id.Name()) {
		return nil, http.StatusForbidden, "ask_denied"
	}
	env, st, msg := s.ingest(id, sendRequest{To: envelope.Recipients{name}, Text: text, ThreadID: threadID, Kind: "question"})
	if msg != "" {
		return nil, st, msg
	}
	return env, 0, ""
}

// POST /agents/{name}/ask (registry-a2a R7): 202 {thread_id, message_id}.
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	var req struct {
		Text     string `json:"text"`
		ThreadID string `json:"thread_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	name := r.PathValue("name")
	env, status, errMsg := s.askCore(id, name, req.Text, req.ThreadID)
	if errMsg != "" {
		writeErr(w, status, errMsg)
		return
	}
	// The question is queued either way (an offline agent still receives
	// asks), but the asker deserves to know nobody is listening rather than
	// staring at a silent timeout.
	out := map[string]any{"thread_id": env.ThreadID, "message_id": env.ID,
		"listener":  s.registry.ListenerLive(name),
		"answering": s.registry.AnswererLive(name)}
	if a, ok := s.registry.Get(name); ok {
		out["last_seen"] = a.LastSeen.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusAccepted, out)
}

// POST /agents/{name}/listen-lease (registry-a2a R10). Requires the agent's
// own credentials (or admin).
func (s *Server) handleLeaseAcquire(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if id.Kind == auth.KindAgent && id.Agent.Name != name {
		writeErr(w, http.StatusForbidden, "forbidden: credential does not correspond to agent")
		return
	}
	if _, exists := s.registry.Get(name); !exists {
		writeErr(w, http.StatusNotFound, "agent not found")
		return
	}
	var req struct {
		LeaseID string `json:"leaseId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	res := s.registry.AcquireLease(name, req.LeaseID)
	if !res.OK {
		writeJSON(w, http.StatusConflict, map[string]string{
			"holder":    res.Holder,
			"expiresAt": res.ExpiresAt.UTC().Format(time.RFC3339Nano),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"leaseId": res.LeaseID, "ttl": res.TTL})
}

// POST /agents/{name}/answering (registry-a2a R11): a peer declares that
// something is ATTACHED TO ANSWER for it — `{"attached":true}` to declare or
// renew, `{"attached":false}` to stand down. Requires the agent's own
// credentials (or admin): nobody may declare answerability on another peer's
// behalf. The declaration ages out on the registry TTL, so an answerer that
// stops renewing stops counting.
func (s *Server) handleAnswering(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if id.Kind == auth.KindAgent && id.Agent.Name != name {
		writeErr(w, http.StatusForbidden, "forbidden: credential does not correspond to agent")
		return
	}
	var req struct {
		Attached *bool `json:"attached"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	attached := true
	if req.Attached != nil {
		attached = *req.Attached
	}
	if !s.registry.DeclareAnswering(name, attached) {
		writeErr(w, http.StatusNotFound, "agent not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"answering": s.registry.AnswererLive(name)})
}

// DELETE /agents/{name} drops a registration: the identity, its credential,
// its lease and its group memberships. Messages are NOT touched — history is
// append-only (ADR-008). Admin, or the agent itself standing down for good.
//
// The case this exists for is a RENAME: a session that joined as `api` now
// joins as `api-main`, and the old name would otherwise linger forever as a
// peer that `peers` lists and `ask` can address but nobody will ever answer.
func (s *Server) handleForget(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if id.Kind == auth.KindAgent && id.Agent.Name != name {
		writeErr(w, http.StatusForbidden, "forbidden: credential does not correspond to agent")
		return
	}
	if !s.registry.Forget(name) {
		writeErr(w, http.StatusNotFound, "agent not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /agents/{name}/listen-lease with the current leaseId → 204.
func (s *Server) handleLeaseRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if id.Kind == auth.KindAgent && id.Agent.Name != name {
		writeErr(w, http.StatusForbidden, "forbidden: credential does not correspond to agent")
		return
	}
	var req struct {
		LeaseID string `json:"leaseId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.LeaseID == "" {
		req.LeaseID = r.URL.Query().Get("leaseId")
	}
	if !s.registry.ReleaseLease(name, req.LeaseID) {
		writeErr(w, http.StatusConflict, "leaseId does not match the current lease")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
