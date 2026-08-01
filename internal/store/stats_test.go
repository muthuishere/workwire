package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/workwire/internal/envelope"
)

func seed(t *testing.T, st *Store, n, peers int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := st.Append(&envelope.Envelope{
			ID:       envelope.NewID("m"),
			From:     "prober",
			To:       []string{fmt.Sprintf("peer%d", i%peers)},
			ThreadID: fmt.Sprintf("t-%d", i%4),
			Text:     strings.Repeat("x", 120),
			TS:       envelope.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// The whole point of the payload is that it stays true after retention. The
// first implementation derived oldest/newest from the thread index, which
// still references evicted envelopes — so it reported full history while most
// of it was gone (spikes/05-observability S6).
func TestSnapshotStaysTrueAfterRetention(t *testing.T) {
	st, err := Open(t.TempDir(), Options{SegmentMaxBytes: 4 << 10, RetentionMaxBytes: 32 << 10})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, st, 2000, 5)

	before, _ := st.Snapshot(nil)
	if err := st.EnforceRetention(time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Snapshot(nil)

	if after.Envelopes >= before.Envelopes {
		t.Fatalf("retention evicted nothing: %d -> %d", before.Envelopes, after.Envelopes)
	}
	if after.MinSeq <= before.MinSeq {
		t.Fatalf("minSeq must advance with eviction: %d -> %d", before.MinSeq, after.MinSeq)
	}
	if !(after.OldestTS > before.OldestTS) {
		t.Fatalf("oldestTs must move forward after eviction: %q -> %q", before.OldestTS, after.OldestTS)
	}
	if after.NewestTS != before.NewestTS {
		t.Fatalf("newestTs must not change: %q -> %q", before.NewestTS, after.NewestTS)
	}
}

// Every peer comes out of ONE pass, and a peer with nothing pending still
// appears — an absent row reads as a bug during an incident.
func TestSnapshotReportsEveryPeerInOnePass(t *testing.T) {
	st, err := Open(t.TempDir(), Options{SegmentMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, st, 40, 4)

	cursors := map[string]int64{"peer0": 0, "peer1": 0, "peer2": 0, "peer3": 0, "quiet": 0}
	_, per := st.Snapshot(cursors)
	if len(per) != 5 {
		t.Fatalf("want 5 rows including the quiet peer, got %d: %v", len(per), per)
	}
	if per["quiet"].Delivered != 0 || per["quiet"].Pending != 0 {
		t.Fatalf("a peer with no traffic must report zeroes, got %+v", per["quiet"])
	}
	if per["peer0"].Delivered != 10 || per["peer0"].Pending != 10 {
		t.Fatalf("peer0 = %+v, want 10 delivered / 10 pending", per["peer0"])
	}
	if per["peer0"].LastDelivered == "" {
		t.Fatal("lastDeliveredAt must be set — it is how you tell 'nothing sent' from 'nothing read'")
	}
}

// A cursor that has caught up means nothing pending. `pending > 0` with a live
// listener and no answerer is the exact shape of "arriving, nobody reading".
func TestPendingFollowsTheCursor(t *testing.T) {
	st, err := Open(t.TempDir(), Options{SegmentMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, st, 10, 1)

	_, behind := st.Snapshot(map[string]int64{"peer0": 0})
	if behind["peer0"].Pending != 10 {
		t.Fatalf("pending = %d, want 10", behind["peer0"].Pending)
	}
	_, caught := st.Snapshot(map[string]int64{"peer0": st.LastSeq()})
	if caught["peer0"].Pending != 0 {
		t.Fatalf("a caught-up cursor must have nothing pending, got %d", caught["peer0"].Pending)
	}
	if caught["peer0"].Delivered != 10 {
		t.Fatalf("delivered is history and must not shrink: %d", caught["peer0"].Delivered)
	}
}

// Assert the absence of secrets rather than hoping for it (S7).
func TestSnapshotCarriesNoCredentialShapedField(t *testing.T) {
	st, err := Open(t.TempDir(), Options{SegmentMaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seed(t, st, 20, 2)

	stats, per := st.Snapshot(map[string]int64{"peer0": 0})
	b, err := json.Marshal(map[string]any{"storage": stats, "agents": per})
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(string(b))
	for _, needle := range []string{"secret", "token", "bearer", "password", "hash"} {
		if strings.Contains(low, needle) {
			t.Fatalf("metrics payload contains %q: %s", needle, b)
		}
	}
}
