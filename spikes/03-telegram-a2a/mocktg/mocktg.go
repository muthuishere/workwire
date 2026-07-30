// Package mocktg is a minimal mock of the Telegram Bot API for offline testing
// (Spike-03). It implements getUpdates (long-poll) and sendMessage under
// /bot<token>/..., plus test control endpoints:
//
//	POST /control/inject  {"chat_id":N,"from":"name","text":"..."}  -> queue an update
//	GET  /control/sent                                              -> sendMessage calls seen
package mocktg

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type SentMessage struct {
	ChatID           int64  `json:"chat_id"`
	Text             string `json:"text"`
	ReplyToMessageID int64  `json:"reply_to_message_id,omitempty"`
	MessageID        int64  `json:"message_id"`
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		MessageID int64 `json:"message_id"`
		From      struct {
			Username string `json:"username"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

type Server struct {
	Token string // expected token; requests to other /bot<x>/ paths get 401

	mu      sync.Mutex
	cond    *sync.Cond
	updates []update
	sent    []SentMessage
	nextUpd int64
	nextMsg int64
}

func New(token string) *Server {
	s := &Server{Token: token, nextUpd: 100, nextMsg: 1000}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Inject queues an inbound "human typed in the chat" update; returns its message_id.
func (s *Server) Inject(chatID int64, from, text string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var u update
	u.UpdateID = s.nextUpd
	s.nextUpd++
	u.Message.MessageID = s.nextMsg
	s.nextMsg++
	u.Message.From.Username = from
	u.Message.Chat.ID = chatID
	u.Message.Text = text
	s.updates = append(s.updates, u)
	s.cond.Broadcast()
	return u.Message.MessageID
}

func (s *Server) Sent() []SentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SentMessage{}, s.sent...)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/control/inject", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ChatID int64  `json:"chat_id"`
			From   string `json:"from"`
			Text   string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		id := s.Inject(req.ChatID, req.From, req.Text)
		writeJSON(w, map[string]any{"ok": true, "message_id": id})
	})
	mux.HandleFunc("/control/sent", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"sent": s.Sent()})
	})
	mux.HandleFunc("/", s.handleBot)
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleBot(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	if !ok {
		http.NotFound(w, r)
		return
	}
	token, method, ok := strings.Cut(rest, "/")
	if !ok || token != s.Token {
		w.WriteHeader(401)
		writeJSON(w, map[string]any{"ok": false, "description": "Unauthorized"})
		return
	}
	switch method {
	case "getUpdates":
		s.getUpdates(w, r)
	case "sendMessage":
		s.sendMessage(w, r)
	default:
		writeJSON(w, map[string]any{"ok": false, "description": "unknown method " + method})
	}
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	offset, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
	timeout, _ := strconv.Atoi(q.Get("timeout"))
	if r.Method == http.MethodPost { // Bot API also accepts params as a JSON body
		var body struct {
			Offset  *int64 `json:"offset"`
			Timeout *int   `json:"timeout"`
		}
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			if body.Offset != nil {
				offset = *body.Offset
			}
			if body.Timeout != nil {
				timeout = *body.Timeout
			}
		}
	}
	if timeout > 5 {
		timeout = 5 // keep tests snappy
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	t := time.AfterFunc(time.Until(deadline), func() { s.cond.Broadcast() })
	defer t.Stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		var out []update
		for _, u := range s.updates {
			if u.UpdateID >= offset {
				out = append(out, u)
			}
		}
		if len(out) > 0 || time.Now().After(deadline) {
			if out == nil {
				out = []update{}
			}
			writeJSON(w, map[string]any{"ok": true, "result": out})
			return
		}
		s.cond.Wait()
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChatID           int64  `json:"chat_id"`
		Text             string `json:"text"`
		ReplyToMessageID int64  `json:"reply_to_message_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	m := SentMessage{ChatID: req.ChatID, Text: req.Text,
		ReplyToMessageID: req.ReplyToMessageID, MessageID: s.nextMsg}
	s.nextMsg++
	s.sent = append(s.sent, m)
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "result": map[string]any{
		"message_id": m.MessageID, "chat": map[string]any{"id": m.ChatID}, "text": m.Text,
	}})
}
