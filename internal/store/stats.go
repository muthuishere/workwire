package store

import (
	"os"
	"path/filepath"
	"time"
)

// Stats is what the hub can say about its own storage without an
// archaeologist (registry-a2a R13). Everything here is derived from state the
// store already holds; nothing is counted twice on the hot path.
type Stats struct {
	Envelopes  int   `json:"envelopes"`
	Tombstones int   `json:"tombstones"`
	Threads    int   `json:"threads"`
	Bytes      int64 `json:"bytes"`
	Segments   int   `json:"segments"`
	LastSeq    int64 `json:"lastSeq"`
	// OldestTS and NewestTS bound retained history — "we have nothing from
	// before X" is the first thing you want when a message is missing.
	OldestTS string `json:"oldestTs,omitempty"`
	NewestTS string `json:"newestTs,omitempty"`
}

// Stats snapshots storage counters.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{
		Envelopes:  len(s.byID),
		Tombstones: len(s.tombs),
		Threads:    len(s.threads),
		LastSeq:    s.lastSeq,
	}
	for _, list := range s.threads {
		for _, e := range list {
			if st.OldestTS == "" || e.Env.TS < st.OldestTS {
				st.OldestTS = e.Env.TS
			}
			if e.Env.TS > st.NewestTS {
				st.NewestTS = e.Env.TS
			}
		}
	}
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
	return st
}

// AgentStats is the per-peer delivery picture: what has been written for this
// peer and what it has not yet collected. `pending > 0` with a live listener
// and no answerer is the exact shape of "questions are arriving and nobody is
// reading them" — the failure that cost a peer four rounds on 2026-08-01.
type AgentStats struct {
	Delivered     int    `json:"delivered"`
	Pending       int    `json:"pending"`
	Cursor        int64  `json:"cursor"`
	LastDelivered string `json:"lastDeliveredAt,omitempty"`
}

// AgentStats computes delivery facts for one peer from a known cursor.
func (s *Store) AgentStats(agent string, since int64) AgentStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := AgentStats{Cursor: since}
	for _, list := range s.threads {
		for _, st := range list {
			if !addressedTo(st, agent) {
				continue
			}
			out.Delivered++
			if st.Seq > since {
				out.Pending++
			}
			if st.Env.TS > out.LastDelivered {
				out.LastDelivered = st.Env.TS
			}
		}
	}
	return out
}

func addressedTo(st *Stored, agent string) bool {
	for _, to := range st.Env.To {
		if to == agent {
			return true
		}
	}
	return false
}

// Uptime is a helper for callers that report process age alongside storage.
func Uptime(start time.Time) string { return time.Since(start).Round(time.Second).String() }
