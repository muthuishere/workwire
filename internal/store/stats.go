package store

import (
	"os"
	"path/filepath"
)

// Stats is what the hub can say about its own storage without an
// archaeologist (registry-a2a R13).
//
// Every number here comes from ONE pass over the RETAINED set. The first draft
// derived oldest/newest by walking the thread index instead, which still
// references envelopes retention has evicted: after a retention pass it
// reported 2000 messages and an unchanged oldest timestamp while 109 remained
// (spikes/05-observability S6). A metrics payload that lies about history is a
// second bug to debug during an incident — `oldestTs` is the one field that
// explains "my message vanished".
type Stats struct {
	Envelopes  int   `json:"envelopes"`
	Tombstones int   `json:"tombstones"`
	Threads    int   `json:"threads"`
	Bytes      int64 `json:"bytes"`
	Segments   int   `json:"segments"`
	LastSeq    int64 `json:"lastSeq"`
	MinSeq     int64 `json:"minSeq"`
	// OldestTS and NewestTS bound RETAINED history — "we have nothing before
	// X" is the first thing worth knowing when a message is missing.
	OldestTS string `json:"oldestTs,omitempty"`
	NewestTS string `json:"newestTs,omitempty"`
}

// AgentStats is the per-peer delivery picture. `pending > 0` with a live
// listener and no answerer is the exact shape of "questions are arriving and
// nobody is reading them" — the state that cost a peer four rounds.
type AgentStats struct {
	Delivered     int    `json:"delivered"`
	Pending       int    `json:"pending"`
	Cursor        int64  `json:"cursor"`
	LastDelivered string `json:"lastDeliveredAt,omitempty"`
}

// Snapshot is storage stats plus every peer's delivery facts, computed in a
// SINGLE pass.
//
// Per-peer stats computed one peer at a time walk the whole store per peer:
// 0.95 ms at 5 peers, 13.8 ms at 100, against 20k envelopes
// (spikes/05-observability S3). Linear in peers × envelopes looks free at nine
// peers and is not the shape to ship, so cursors go in and every peer comes
// out of one scan — the same information for the cost of the storage scan
// alone (S2: 1.43 ms at 50k envelopes).
func (s *Store) Snapshot(cursors map[string]int64) (Stats, map[string]AgentStats) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := Stats{
		Tombstones: len(s.tombs),
		LastSeq:    s.lastSeq,
		MinSeq:     s.minSeq,
	}
	per := map[string]AgentStats{}
	// Seed from the cursor map so a peer with nothing pending still appears —
	// "zero delivered" is an answer, and an absent row reads as a bug.
	for name, cur := range cursors {
		per[name] = AgentStats{Cursor: cur}
	}
	threads := map[string]bool{}

	// s.msgs IS the retained set, ordered by seq. Nothing evicted is in it,
	// which is exactly why the numbers stay true after retention.
	for _, m := range s.msgs {
		st.Envelopes++
		if m.Env.ThreadID != "" {
			threads[m.Env.ThreadID] = true
		}
		if st.OldestTS == "" || m.Env.TS < st.OldestTS {
			st.OldestTS = m.Env.TS
		}
		if m.Env.TS > st.NewestTS {
			st.NewestTS = m.Env.TS
		}
		for _, to := range m.Env.To {
			a := per[to]
			a.Delivered++
			if m.Seq > a.Cursor {
				a.Pending++
			}
			if m.Env.TS > a.LastDelivered {
				a.LastDelivered = m.Env.TS
			}
			per[to] = a
		}
	}
	st.Threads = len(threads)

	// Bytes and segments are a filesystem fact, not an index fact: retention
	// removes whole segments, and the draft's directory sizing reported the
	// pre-retention total. Count what is on disk right now.
	if entries, err := os.ReadDir(s.dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".ndjson" {
				continue
			}
			st.Segments++
			if fi, err := e.Info(); err == nil {
				st.Bytes += fi.Size()
			}
		}
	}
	return st, per
}

// Stats snapshots storage counters alone.
func (s *Store) Stats() Stats {
	st, _ := s.Snapshot(nil)
	return st
}
