// Package spike02: NDJSON envelope store with integer line cursors.
package spike02

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Envelope is the one wire value (ADR-001).
type Envelope struct {
	ID       string            `json:"id"`
	From     string            `json:"from"`
	To       string            `json:"to,omitempty"`
	ThreadID string            `json:"thread_id,omitempty"`
	ReplyTo  string            `json:"reply_to,omitempty"`
	Text     string            `json:"text"`
	TS       string            `json:"ts"`
	Kind     string            `json:"kind,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
	// Context is attached at read time only, never stored.
	Context []Envelope `json:"context,omitempty"`
}

// Store is an append-only NDJSON file. The cursor is the 1-based line number
// of the last line the client has seen (0 = from the beginning).
type Store struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	all     []Envelope // in-memory mirror; line i (1-based) == all[i-1]
	seq     int64
	changed chan struct{} // closed+replaced on every append (long-poll wakeup)
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "messages.ndjson")
	s := &Store{path: path, changed: make(chan struct{})}
	// Load existing lines (cursor survival across restart).
	if b, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(b)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			var e Envelope
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				s.all = append(s.all, e)
			}
		}
		b.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.f = f
	return s, nil
}

// Append assigns id/ts (hub-generated, UTC), resolves reply_to:"last",
// writes one NDJSON line, and returns the stored envelope plus its cursor.
func (s *Store) Append(e Envelope) (Envelope, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.ID = fmt.Sprintf("m-%d-%d", time.Now().UnixNano(), s.seq)
	e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	e.Context = nil // never store context
	if e.ReplyTo == "last" {
		e.ReplyTo = s.lastIDLocked(e.ThreadID, e.From)
	}
	if e.ThreadID == "" {
		e.ThreadID = e.ID // new message starts its own thread
	}
	b, err := json.Marshal(e)
	if err != nil {
		return e, 0, err
	}
	if _, err := s.f.Write(append(b, '\n')); err != nil {
		return e, 0, err
	}
	s.all = append(s.all, e)
	old := s.changed
	s.changed = make(chan struct{})
	close(old)
	return e, len(s.all), nil
}

// lastIDLocked finds the newest message on the thread (or from/to the peer)
// NOT authored by `from` — "the newest inbound".
func (s *Store) lastIDLocked(threadID, from string) string {
	for i := len(s.all) - 1; i >= 0; i-- {
		m := s.all[i]
		if m.From == from {
			continue
		}
		if threadID == "" || m.ThreadID == threadID {
			return m.ID
		}
	}
	return ""
}

// Since returns messages after line cursor n, the new cursor, and whether the
// cursor was ahead of the file (truncated/replaced store → client must reset).
func (s *Store) Since(n int) (msgs []Envelope, cursor int, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.all)
	if n > total {
		return nil, total, true // cursor older than (i.e. beyond) the file
	}
	out := make([]Envelope, total-n)
	copy(out, s.all[n:])
	return out, total, false
}

// Changed returns a channel closed on the next append (for long-poll).
func (s *Store) Changed() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// Last returns the newest message on a thread or involving a peer.
func (s *Store) Last(threadID, peer string) (Envelope, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.all) - 1; i >= 0; i-- {
		m := s.all[i]
		if threadID != "" && m.ThreadID != threadID {
			continue
		}
		if peer != "" && m.From != peer && m.To != peer {
			continue
		}
		return m, true
	}
	return Envelope{}, false
}

// Thread returns the last `last` messages of a thread (all if last<=0).
func (s *Store) Thread(threadID string, last int) []Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Envelope
	for _, m := range s.all {
		if m.ThreadID == threadID {
			out = append(out, m)
		}
	}
	if last > 0 && len(out) > last {
		out = out[len(out)-last:]
	}
	return out
}

// Project attaches read-time context: the last `lastMessages` messages of the
// envelope's thread that precede it (excluding the message itself).
func (s *Store) Project(e Envelope, lastMessages int) Envelope {
	if lastMessages <= 0 || e.ThreadID == "" {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ctx []Envelope
	for i := len(s.all) - 1; i >= 0 && len(ctx) < lastMessages; i-- {
		m := s.all[i]
		if m.ThreadID != e.ThreadID || m.ID == e.ID {
			continue
		}
		if m.TS > e.TS {
			continue // only history, not the future
		}
		ctx = append([]Envelope{m}, ctx...)
	}
	e.Context = ctx
	return e
}

// Lines reports the current cursor (total line count).
func (s *Store) Lines() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.all)
}
