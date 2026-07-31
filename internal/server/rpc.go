package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/muthuishere/workwire/internal/store"
)

// JSON-RPC 2.0 shim at the card url (registry-a2a R9): message/send maps to
// the ask path and returns an A2A v0.3.0 Task; tasks/get reports
// submitted/working vs completed by the reply_to == message_id rule. A task
// is a threaded envelope with a completion semantic — never a second data
// model (ADR-002).

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
}

func rpcResult(w http.ResponseWriter, id json.RawMessage, result any) {
	if id == nil {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
}

type a2aPart struct {
	Kind string `json:"kind"`
	Type string `json:"type"` // tolerated legacy field name
	Text string `json:"text"`
}

type a2aMessage struct {
	Role      string    `json:"role"`
	Parts     []a2aPart `json:"parts"`
	MessageID string    `json:"messageId"`
	TaskID    string    `json:"taskId"`
	ContextID string    `json:"contextId"`
}

func textOfParts(parts []a2aPart) string {
	for _, p := range parts {
		if (p.Kind == "text" || p.Type == "text" || (p.Kind == "" && p.Type == "")) && p.Text != "" {
			return p.Text
		}
	}
	return ""
}

// taskFor builds the A2A Task for a thread: completed once an envelope with
// reply_to == the newest question's id exists, with the answer text in
// artifacts; submitted/working otherwise.
func (s *Server) taskFor(threadID string) (map[string]any, bool) {
	list, ok := s.store.Thread(threadID, 0)
	if !ok {
		return nil, false
	}
	var question *store.Stored
	for _, st := range list {
		if st.Env.Kind == "question" {
			question = st
		}
	}
	state := "submitted"
	var artifacts []map[string]any
	history := make([]map[string]any, 0, len(list))
	for _, st := range list {
		e := s.store.Render(st.Env)
		role := "user"
		if question != nil && question.Env.To.Has(e.From) {
			role = "agent"
		}
		history = append(history, map[string]any{
			"kind":      "message",
			"role":      role,
			"messageId": e.ID,
			"taskId":    threadID,
			"contextId": threadID,
			"parts":     []map[string]any{{"kind": "text", "text": e.Text}},
		})
		if question != nil && e.ReplyTo == question.Env.ID {
			state = "completed"
			artifacts = []map[string]any{{
				"artifactId": "answer-" + e.ID,
				"parts":      []map[string]any{{"kind": "text", "text": e.Text}},
			}}
		}
	}
	task := map[string]any{
		"kind":      "task",
		"id":        threadID,
		"contextId": threadID,
		"status":    map[string]any{"state": state},
		"history":   history,
	}
	if artifacts != nil {
		task["artifacts"] = artifacts
	}
	return task, true
}

// POST /agents/{name}/rpc — the card url.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	id, ok := s.identify(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		rpcError(w, nil, -32700, "parse error")
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		rpcError(w, nil, -32700, "parse error")
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		rpcError(w, req.ID, -32600, "invalid request")
		return
	}
	name := r.PathValue("name")
	switch req.Method {
	case "message/send":
		var params struct {
			Message a2aMessage `json:"message"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			rpcError(w, req.ID, -32602, "invalid params")
			return
		}
		text := textOfParts(params.Message.Parts)
		if text == "" {
			rpcError(w, req.ID, -32602, "message has no text part")
			return
		}
		threadID := params.Message.TaskID
		if threadID == "" {
			threadID = params.Message.ContextID
		}
		env, status, errMsg := s.askCore(id, name, text, threadID)
		if errMsg != "" {
			code := -32602
			if status == http.StatusForbidden || status == http.StatusUnauthorized {
				code = -32000
			}
			rpcError(w, req.ID, code, errMsg)
			return
		}
		task, _ := s.taskFor(env.ThreadID)
		rpcResult(w, req.ID, task)
	case "tasks/get":
		var params struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
			rpcError(w, req.ID, -32602, "invalid params: id required")
			return
		}
		task, ok := s.taskFor(params.ID)
		if !ok {
			rpcError(w, req.ID, -32001, "task not found")
			return
		}
		rpcResult(w, req.ID, task)
	default:
		rpcError(w, req.ID, -32601, "method not found")
	}
}
