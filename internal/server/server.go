// Package server wires the workwire HTTP surface: /health, /send, /inbox
// long-poll with context projection, /threads, /agents + A2A, /contacts,
// /media, and tombstone excision endpoints (hub-core, registry-a2a, auth,
// contacts specs).
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/contacts"
	"github.com/muthuishere/workwire/internal/envelope"
	"github.com/muthuishere/workwire/internal/origin"
	"github.com/muthuishere/workwire/internal/registry"
	"github.com/muthuishere/workwire/internal/store"
)

// APIVersion is the hub surface version reported by /health.
const APIVersion = 1

// Server holds the hub state.
type Server struct {
	cfg      config.Config
	store    *store.Store
	registry *registry.Registry
	contacts *contacts.Directory
	auth     *auth.Authenticator
	media    *mediaStore
	mux      *http.ServeMux
}

// New assembles the hub. adminToken may be empty in open mode.
func New(cfg config.Config, st *store.Store, reg *registry.Registry, dir *contacts.Directory, adminToken string) (*Server, error) {
	ms, err := newMediaStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		store:    st,
		registry: reg,
		contacts: dir,
		media:    ms,
		auth:     &auth.Authenticator{Mode: cfg.AuthMode, AdminToken: adminToken, Registry: reg},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /send", s.handleSend)
	mux.HandleFunc("GET /inbox", s.handleInbox)
	mux.HandleFunc("GET /threads", s.handleListThreads)
	mux.HandleFunc("GET /threads/{id}", s.handleThread)
	mux.HandleFunc("DELETE /threads/{id}", s.handleDeleteThread)
	mux.HandleFunc("DELETE /messages/{id}", s.handleDeleteMessage)
	mux.HandleFunc("GET /agents", s.handleListAgents)
	mux.HandleFunc("POST /agents", s.handleRegister)
	mux.HandleFunc("DELETE /agents/{name}", s.handleForget)
	mux.HandleFunc("GET /agents/{name}/card", s.handleCard)
	mux.HandleFunc("POST /agents/{name}/ask", s.handleAsk)
	mux.HandleFunc("POST /agents/{name}/rpc", s.handleRPC)
	mux.HandleFunc("POST /agents/{name}/listen-lease", s.handleLeaseAcquire)
	mux.HandleFunc("DELETE /agents/{name}/listen-lease", s.handleLeaseRelease)
	mux.HandleFunc("POST /agents/{name}/answering", s.handleAnswering)
	mux.HandleFunc("GET /groups", s.handleListGroups)
	mux.HandleFunc("POST /groups/{name}/join", s.handleGroupJoin)
	mux.HandleFunc("POST /groups/{name}/leave", s.handleGroupLeave)
	mux.HandleFunc("GET /contacts", s.handleListContacts)
	mux.HandleFunc("POST /contacts", s.handleAddContact)
	mux.HandleFunc("POST /contacts/{id}/verify", s.handleVerifyContact)
	mux.HandleFunc("DELETE /contacts/{id}", s.handleDeleteContact)
	mux.HandleFunc("POST /media", s.handleUploadMedia)
	mux.HandleFunc("GET /media/{id}", s.handleGetMedia)
	s.mux = mux
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.mux }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// identify resolves the actor or writes 401 and returns ok=false.
func (s *Server) identify(w http.ResponseWriter, r *http.Request) (auth.Identity, bool) {
	id, err := s.auth.Identify(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return auth.Identity{}, false
	}
	return id, true
}

// clampWait parses ?wait= seconds: default WaitDefault, clamped to WaitMax.
func (s *Server) clampWait(r *http.Request) time.Duration {
	w := s.cfg.WaitDefault
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			w = n
		}
	}
	if w < 0 {
		w = 0
	}
	if w > s.cfg.WaitMax {
		w = s.cfg.WaitMax
	}
	return time.Duration(w) * time.Second
}

// GET /health — unauthenticated in every auth mode; identity/version only
// (hub-core R9, auth R7).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":       "workwire",
		"schemaVersion": envelope.SchemaVersion,
		"apiVersion":    APIVersion,
	})
}

type sendRequest struct {
	To          envelope.Recipients   `json:"to"`
	Text        string                `json:"text"`
	ThreadID    string                `json:"thread_id"`
	ReplyTo     string                `json:"reply_to"`
	Kind        string                `json:"kind"`
	Meta        map[string]any        `json:"meta"`
	Attachments []envelope.Attachment `json:"attachments"`
}

// POST /send (hub-core R1–R3): hub stamps id/ts/from; resolves
// reply_to:"last" once at ingest, thread-scoped, 409 when no inbound exists.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	env, status, errMsg := s.ingest(id, req)
	if errMsg != "" {
		writeErr(w, status, errMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id": env.ID, "thread_id": env.ThreadID, "ts": env.TS,
	})
}

// ingest builds, validates and stores an envelope. `from` is stamped
// server-side from the authenticated identity — any client-supplied from is
// ignored by construction (the request shape has no from field; auth R5).
func (s *Server) ingest(id auth.Identity, req sendRequest) (*envelope.Envelope, int, string) {
	from := id.Name()
	threadID := req.ThreadID
	if threadID == "" {
		threadID = envelope.NewID("t")
	}
	// Convergence + fan-out (ADR-009). Membership accrues from participation:
	// a send carrying only thread_id goes to every current member but the
	// sender, and sending into a thread joins you to it.
	// A group is an AUDIENCE, not a room (ADR-012): `to:"@platform"` expands
	// ONCE, here at ingest, to a snapshot of the group's current members.
	// From there it is an ordinary fan-out and an ordinary thread — a peer
	// who joins the group tomorrow does not retroactively enter it; they
	// discover the thread and walk in (ADR-011).
	to, err := s.registry.ExpandRecipients(req.To, from)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error()
	}
	var closedOver []store.Dissent
	if ts, exists := s.store.ThreadState(threadID, s.cfg.MaxThreadMessages); exists {
		if status, msg := s.checkThreadRules(id, ts, req, &closedOver); msg != "" {
			return nil, status, msg
		}
		if len(to) == 0 {
			for _, m := range ts.Members {
				if m != from {
					to = append(to, m)
				}
			}
		}
	}
	if len(to) == 0 {
		return nil, http.StatusBadRequest, "to is required (a name or an array of names), or a thread_id of a thread with other members"
	}
	replyTo := req.ReplyTo
	if replyTo == "last" {
		// Resolve exactly once at ingest, thread-scoped (hub-core R3).
		st, found := s.store.LastInbound(threadID, from)
		if !found {
			return nil, http.StatusConflict, "reply_to \"last\": thread has no inbound message to reply to"
		}
		replyTo = st.Env.ID
	}
	meta := req.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	meta["peerKind"] = id.PeerKind() // authenticated provenance (auth R9)
	meta["peerRole"] = id.Role()     // "human" | "agent" precedence (ADR-011 §3)
	if o := id.Origin().Map(); o != nil {
		meta["origin"] = o // which tree said it (ADR-011 §1)
	}
	if req.Kind == "resolved" {
		// The closing envelope records who closed it and which open dissents
		// the closure overrode (ADR-011 §3).
		meta["closedBy"] = from
		if len(closedOver) > 0 {
			names := make([]string, 0, len(closedOver))
			for _, d := range closedOver {
				names = append(names, d.Peer)
			}
			meta["closedOver"] = names
		}
	}
	env := &envelope.Envelope{
		ID:          envelope.NewID("m"),
		From:        from,
		To:          to,
		ThreadID:    threadID,
		ReplyTo:     replyTo,
		Text:        req.Text,
		TS:          envelope.Now(),
		Kind:        req.Kind,
		Meta:        meta,
		Attachments: req.Attachments,
	}
	if _, err := s.store.Append(env); err != nil {
		return nil, http.StatusInternalServerError, "store append failed"
	}
	// TOFU harvest from every accepted envelope's sender (contacts R1).
	s.contacts.Harvest(from, id.PeerKind(), from, env.TS)
	return env, 0, ""
}

// delivered is one inbox entry: the envelope plus its read-time context
// projection (hub-core R7).
type delivered struct {
	envelope.Envelope
	Context []contextEntry `json:"context,omitempty"`
}

// contextEntry is a projected thread message plus the speaker's registered
// persona, so a participant can weigh who said what (ADR-009).
type contextEntry struct {
	envelope.Envelope
	Persona string `json:"persona,omitempty"`
	// Origin is the speaker's provenance — which tree the claim came from
	// (ADR-011 §1). Taken from what the speaker stamped at send time, so
	// history stays true after they switch branches.
	Origin *origin.Info `json:"origin,omitempty"`
	// Kind is "agent" or "human": who is talking, not just from where.
	Kind_ string `json:"peer_kind,omitempty"`
}

// GET /inbox?agent=&since=&wait=&context= — the single receive shape
// (hub-core R5–R7; auth R4 scoping).
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	agent := q.Get("agent")
	if agent == "" {
		writeErr(w, http.StatusBadRequest, "agent parameter is required")
		return
	}
	// Inbox reads are scoped to the authenticated agent (auth R4): a valid
	// credential acting as a different agent gets 403. Admin may read any.
	if id.Kind == auth.KindAgent && id.Agent.Name != agent {
		writeErr(w, http.StatusForbidden, "forbidden: credential does not correspond to agent")
		return
	}
	since := int64(0)
	if v := q.Get("since"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid since cursor")
			return
		}
		since = n
	}
	ctxDepth := s.cfg.LastMessages
	if v := q.Get("context"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid context depth")
			return
		}
		ctxDepth = n
	}
	if ctxDepth > s.cfg.ContextCap {
		ctxDepth = s.cfg.ContextCap
	}
	wait := s.clampWait(r)
	deadline := time.Now().Add(wait)
	for {
		watch := s.store.Watch()
		msgs, next, reset := s.store.Inbox(agent, since)
		if len(msgs) > 0 || reset || time.Now().After(deadline) || wait == 0 {
			out := make([]delivered, 0, len(msgs))
			for _, m := range msgs {
				d := delivered{Envelope: s.store.Render(m.Env)}
				if ctxDepth > 0 {
					d.Context = s.projectContext(m.Env.ThreadID, ctxDepth)
				}
				out = append(out, d)
			}
			resp := map[string]any{"messages": out, "next": next}
			if reset {
				resp["reset"] = true
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		remain := time.Until(deadline)
		timer := time.NewTimer(remain)
		select {
		case <-watch:
			timer.Stop()
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			return
		}
	}
}

// projectContext returns the last n thread envelopes stamped kind:"context"
// — background entries that never advance the cursor (hub-core R7).
func (s *Server) projectContext(threadID string, n int) []contextEntry {
	list, ok := s.store.Thread(threadID, n)
	if !ok {
		return nil
	}
	out := make([]contextEntry, 0, len(list))
	for _, st := range list {
		e := s.store.Render(st.Env)
		e.Kind = "context"
		entry := contextEntry{Envelope: e}
		if a, ok := s.registry.Get(e.From); ok {
			entry.Persona = a.Persona
			entry.Kind_ = registry.NormalizeKind(a.Kind)
			entry.Origin = a.Origin
		}
		if m, ok := e.Meta["origin"].(map[string]any); ok {
			if oi := origin.FromMap(m); oi != nil {
				entry.Origin = oi
			}
		}
		if r, ok := e.Meta["peerRole"].(string); ok && r != "" {
			entry.Kind_ = r
		}
		out = append(out, entry)
	}
	return out
}

// GET /threads/{id}?last=N&wait=S&answer_to=<qid> (hub-core R8,
// registry-a2a R8). With wait + answer_to, the request completes when an
// envelope with reply_to == answer_to exists; otherwise wait completes on
// any new thread envelope; on expiry it returns the thread state.
func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	threadID := r.PathValue("id")
	q := r.URL.Query()
	last := 0
	if v := q.Get("last"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid last")
			return
		}
		last = n
	}
	answerTo := q.Get("answer_to")
	wait := time.Duration(0)
	if q.Get("wait") != "" {
		wait = s.clampWait(r)
	}
	deadline := time.Now().Add(wait)
	if _, exists := s.store.Thread(threadID, 0); !exists {
		writeErr(w, http.StatusNotFound, "thread not found")
		return
	}
	initial, _ := s.store.Thread(threadID, 0)
	initialLen := len(initial)
	for {
		watch := s.store.Watch()
		list, _ := s.store.Thread(threadID, 0)
		done := wait == 0 || time.Now().After(deadline)
		if answerTo != "" {
			for _, st := range list {
				if st.Env.ReplyTo == answerTo {
					done = true
				}
			}
		} else if len(list) > initialLen {
			done = true
		}
		if done {
			if last > 0 && len(list) > last {
				list = list[len(list)-last:]
			}
			msgs := make([]envelope.Envelope, 0, len(list))
			for _, st := range list {
				msgs = append(msgs, s.store.Render(st.Env))
			}
			writeJSON(w, http.StatusOK, map[string]any{"thread_id": threadID, "messages": msgs})
			return
		}
		timer := time.NewTimer(time.Until(deadline))
		select {
		case <-watch:
			timer.Stop()
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			return
		}
	}
}

// GET /threads — list live discussions: id, topic, accrued members, message
// count, open dissents and convergence state (ADR-009, ADR-011).
//
// Discovery is deliberately broad: EVERY authenticated peer sees every
// thread, not only the ones it was addressed in — addressing controls
// delivery (who wakes up), discovery controls participation (who may walk
// in). Each entry is marked with whether the caller is already a member.
// This is a local-trust assumption; a shared hub needs per-workspace scoping
// (ADR-010).
func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	me := id.Name()
	all := s.store.Threads(s.cfg.MaxThreadMessages)
	out := make([]map[string]any, 0, len(all))
	for _, ts := range all {
		if r.URL.Query().Get("state") != "" && r.URL.Query().Get("state") != ts.State {
			continue
		}
		b, err := json.Marshal(ts)
		if err != nil {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(b, &entry); err != nil {
			continue
		}
		entry["member"] = ts.HasMember(me)
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"threads":           out,
		"maxThreadMessages": s.cfg.MaxThreadMessages,
	})
}

// DELETE /messages/{id} — tombstone one envelope (hub-core R13).
func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	msgID := r.PathValue("id")
	found, err := s.store.TombstoneMessage(msgID, id.Name())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tombstone write failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "message not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": msgID, "tombstoned": true})
}

// DELETE /threads/{id} — tombstone every envelope on the thread.
func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	threadID := r.PathValue("id")
	found, err := s.store.TombstoneThread(threadID, id.Name())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tombstone write failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "thread not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread_id": threadID, "tombstoned": true})
}
