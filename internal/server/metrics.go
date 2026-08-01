package server

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/muthuishere/workwire/internal/registry"
)

// metrics holds the few facts a scan cannot derive (spikes/05-observability
// S4): what is in flight right now, and how much has gone through. Everything
// else is read from the store and the registry on demand, because a counter is
// a second source of truth that retention and tombstones must both remember to
// decrement — and a drifting counter is a confident wrong answer at 2am.
type metrics struct {
	started       time.Time
	inflightPolls atomic.Int64
	polls         atomic.Int64
	sends         atomic.Int64
	asks          atomic.Int64
	refusals      atomic.Int64 // 4xx/5xx: the shape of "peers are being told no"

	mu sync.Mutex
	// cursors is the last `since` each peer presented on /inbox. The hub does
	// not own the cursor — the listener persists it and sends it — so this is
	// the hub's best knowledge of how far behind a peer is. That is exactly
	// the question "is anything actually collecting these?".
	cursors map[string]int64
}

func newMetrics() *metrics {
	return &metrics{started: time.Now(), cursors: map[string]int64{}}
}

func (m *metrics) noteCursor(agent string, since int64) {
	if agent == "" {
		return
	}
	m.mu.Lock()
	if since > m.cursors[agent] {
		m.cursors[agent] = since
	}
	m.mu.Unlock()
}

func (m *metrics) cursorSnapshot(names []string) map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int64, len(names)+len(m.cursors))
	// Seed every known peer so a peer with no traffic still gets a row: an
	// absent row reads as a bug during an incident.
	for _, n := range names {
		out[n] = m.cursors[n]
	}
	for n, c := range m.cursors {
		out[n] = c
	}
	return out
}

// GET /metrics — the hub answering "what is going on?" without an
// archaeologist (registry-a2a R13). Authenticated, no secret material, and
// cheap enough to poll: one pass over the retained set covers storage and
// every peer at once (S2, S3).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.identify(w, r); !ok {
		return
	}
	live := s.registry.Live()
	names := make([]string, 0, len(live))
	for _, a := range live {
		names = append(names, a.Name)
	}
	storage, per := s.store.Snapshot(s.metrics.cursorSnapshot(names))

	agents := make([]map[string]any, 0, len(live))
	for _, a := range live {
		st := per[a.Name]
		agents = append(agents, map[string]any{
			"name":    a.Name,
			"aliases": a.Aliases,
			"kind":    registry.NormalizeKind(a.Kind),
			// listener is a DELIVERY fact, answering an ANSWERABILITY fact.
			// pending>0 with listener=true and answering=false is the exact
			// shape of "questions are arriving and nobody is reading them".
			"listener":        s.registry.ListenerLive(a.Name),
			"answering":       s.registry.AnswererLive(a.Name),
			"delivered":       st.Delivered,
			"pending":         st.Pending,
			"cursor":          st.Cursor,
			"lastDeliveredAt": st.LastDelivered,
			"lastSeen":        a.LastSeen.UTC().Format(time.RFC3339Nano),
			"origin":          a.Origin,
		})
	}

	threads := map[string]int{"open": 0, "resolved": 0, "stalled": 0}
	dissenting := 0
	for _, t := range s.store.Threads(s.cfg.MaxThreadMessages) {
		threads[t.State]++
		if len(t.Dissents) > 0 {
			dissenting++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hub": map[string]any{
			"startedAt":     s.metrics.started.UTC().Format(time.RFC3339),
			"uptimeSeconds": int(time.Since(s.metrics.started).Seconds()),
			"apiVersion":    APIVersion,
			"authMode":      s.cfg.AuthMode,
		},
		// `bytes` and `segments` are a FILESYSTEM fact and lag in-memory
		// eviction; `minSeq`/`oldestTs` are what make eviction unambiguous
		// (spikes/05-observability S6).
		"storage": storage,
		"traffic": map[string]any{
			"sends":         s.metrics.sends.Load(),
			"asks":          s.metrics.asks.Load(),
			"polls":         s.metrics.polls.Load(),
			"refusals":      s.metrics.refusals.Load(),
			"inflightPolls": s.metrics.inflightPolls.Load(),
		},
		"threads": map[string]any{
			"open":       threads["open"],
			"resolved":   threads["resolved"],
			"stalled":    threads["stalled"],
			"dissenting": dissenting,
		},
		"agents": agents,
	})
}
