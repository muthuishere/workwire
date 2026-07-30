// Package hub is a minimal stub hub for Spike-03: an in-memory envelope store
// with a dynamic agent registry (ADR-002) and A2A plain serving.
// Endpoints: POST /send, GET /inbox, POST /agents, GET /agents,
// GET /threads/{id}, GET /agents/{name}/card, POST /agents/{name}/ask.
package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Envelope struct {
	ID       int64             `json:"id"`
	ThreadID string            `json:"thread_id"`
	From     string            `json:"from"`
	To       string            `json:"to"`
	Text     string            `json:"text"`
	Meta     map[string]string `json:"meta,omitempty"`
	TS       time.Time         `json:"ts"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

type Agent struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Skills      []Skill   `json:"skills,omitempty"`
	LastSeen    time.Time `json:"last_seen"`
}

type Hub struct {
	mu       sync.Mutex
	cond     *sync.Cond
	messages []Envelope
	agents   map[string]*Agent
	nextID   int64
	nextTID  int64
}

func New() *Hub {
	h := &Hub{agents: map[string]*Agent{}, nextID: 1, nextTID: 1}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", h.handleSend)
	mux.HandleFunc("GET /inbox", h.handleInbox)
	mux.HandleFunc("POST /agents", h.handleRegister)
	mux.HandleFunc("GET /agents", h.handleListAgents)
	mux.HandleFunc("GET /threads/{id}", h.handleThread)
	mux.HandleFunc("GET /agents/{name}/card", h.handleCard)
	mux.HandleFunc("POST /agents/{name}/ask", h.handleAsk)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// append stores an envelope (assigning id + thread) and wakes long-pollers.
func (h *Hub) append(e Envelope) Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	e.ID = h.nextID
	h.nextID++
	if e.ThreadID == "" {
		e.ThreadID = fmt.Sprintf("t-%d", h.nextTID)
		h.nextTID++
	}
	e.TS = time.Now().UTC()
	h.messages = append(h.messages, e)
	h.cond.Broadcast()
	return e
}

func (h *Hub) handleSend(w http.ResponseWriter, r *http.Request) {
	var e Envelope
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil || e.To == "" {
		http.Error(w, `{"error":"bad envelope: need to"}`, 400)
		return
	}
	e = h.append(e)
	writeJSON(w, map[string]any{"id": e.ID, "thread_id": e.ThreadID})
}

// collect returns matching envelopes with ID > since, optionally waiting.
func (h *Hub) collect(since int64, wait time.Duration, match func(Envelope) bool) []Envelope {
	deadline := time.Now().Add(wait)
	if wait > 0 {
		// wake sleepers at the deadline so cond.Wait can't block past it
		t := time.AfterFunc(wait, func() { h.cond.Broadcast() })
		defer t.Stop()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		var out []Envelope
		for _, e := range h.messages {
			if e.ID > since && match(e) {
				out = append(out, e)
			}
		}
		if len(out) > 0 || time.Now().After(deadline) {
			if out == nil {
				out = []Envelope{}
			}
			return out
		}
		h.cond.Wait()
	}
}

func parseWait(r *http.Request) time.Duration {
	s, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if s < 0 {
		s = 0
	}
	if s > 30 {
		s = 30
	}
	return time.Duration(s) * time.Second
}

func (h *Hub) handleInbox(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("for")
	if name == "" {
		http.Error(w, `{"error":"need for=<agent>"}`, 400)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	msgs := h.collect(since, parseWait(r), func(e Envelope) bool {
		return e.To == name || strings.HasPrefix(e.To, name+"/")
	})
	writeJSON(w, map[string]any{"messages": msgs})
}

func (h *Hub) handleThread(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("id")
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	msgs := h.collect(since, parseWait(r), func(e Envelope) bool {
		return e.ThreadID == tid
	})
	writeJSON(w, map[string]any{"thread_id": tid, "messages": msgs})
}

func (h *Hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	var a Agent
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil || a.Name == "" {
		http.Error(w, `{"error":"bad agent: need name"}`, 400)
		return
	}
	a.LastSeen = time.Now().UTC()
	h.mu.Lock()
	h.agents[a.Name] = &a
	h.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "name": a.Name})
}

func (h *Hub) handleListAgents(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	list := []*Agent{}
	for _, a := range h.agents {
		list = append(list, a)
	}
	h.mu.Unlock()
	writeJSON(w, map[string]any{"agents": list})
}

// handleCard serves an A2A v0.3.0 AgentCard on behalf of a registered agent.
// Spec: https://a2a-protocol.org/v0.3.0/specification/ (schema vendored in
// schema/a2a-v0.3.0.json, definitions/AgentCard).
func (h *Hub) handleCard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h.mu.Lock()
	a, ok := h.agents[name]
	h.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"unknown agent"}`, 404)
		return
	}
	skills := []map[string]any{}
	for _, s := range a.Skills {
		tags := s.Tags
		if tags == nil {
			tags = []string{}
		}
		skills = append(skills, map[string]any{
			"id": s.ID, "name": s.Name, "description": s.Description, "tags": tags,
		})
	}
	if len(skills) == 0 {
		skills = append(skills, map[string]any{
			"id":          name + "-ask",
			"name":        "ask",
			"description": "Ask " + name + " a question; answer arrives on the returned thread.",
			"tags":        []string{"chat"},
		})
	}
	desc := a.Description
	if desc == "" {
		desc = "Agent '" + name + "' registered on the spike03 hub."
	}
	base := "http://" + r.Host
	card := map[string]any{
		"protocolVersion":    "0.3.0",
		"name":               a.Name,
		"description":        desc,
		"url":                base + "/agents/" + name + "/ask",
		"preferredTransport": "HTTP+JSON",
		"version":            "0.1.0-spike03",
		"capabilities":       map[string]any{"streaming": false, "pushNotifications": false},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             skills,
	}
	writeJSON(w, card)
}

// handleAsk is A2A plain serving (ADR-002): write an envelope addressed to the
// agent, return {thread_id}; the asker reads the answer off GET /threads/{id}.
func (h *Hub) handleAsk(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	h.mu.Lock()
	_, ok := h.agents[name]
	h.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"unknown agent"}`, 404)
		return
	}
	var req struct {
		Text string `json:"text"`
		From string `json:"from"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		http.Error(w, `{"error":"need text"}`, 400)
		return
	}
	if req.From == "" {
		req.From = "a2a-client"
	}
	e := h.append(Envelope{From: req.From, To: name, Text: req.Text})
	writeJSON(w, map[string]any{"thread_id": e.ThreadID})
}
