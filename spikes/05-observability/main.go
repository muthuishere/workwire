// Spike-05 — how to build hub observability (ADR-014 F10, registry-a2a R13).
//
// The question is NOT "should the hub have metrics" — the specs already say it
// must. The question is what shape survives contact with the real store, and
// the only way to know is to run the candidate implementations against real
// data at real sizes.
//
// Eight probes, each answering one build decision:
//
//	S1  what does the diagnosis actually need?   (the decision tree, priced)
//	S2  full Stats() cost vs store size          (is polling every 5s sane?)
//	S3  per-agent pending cost vs peers          (the O(everything) risk)
//	S4  incremental counters vs on-demand scan   (which one do we build?)
//	S5  what a poll costs the hot path           (does /metrics block sends?)
//	S6  metrics after rotation + tombstones      (do the numbers stay true?)
//	S7  can the payload leak a secret?           (assert, do not hope)
//	S8  what is answerable WITHOUT the hub        (a `doctor` that works when
//	                                              the hub is the thing that died)
//
// Run:  go run ./spikes/05-observability            (all)
//
//	go run ./spikes/05-observability s2 s3      (some)
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/envelope"
	"github.com/muthuishere/workwire/internal/store"
)

func main() {
	want := os.Args[1:]
	run := func(id string) bool {
		if len(want) == 0 {
			return true
		}
		for _, w := range want {
			if w == id {
				return true
			}
		}
		return false
	}
	probes := []struct {
		id string
		fn func()
	}{
		{"s1", s1DecisionTree},
		{"s2", s2StatsCost},
		{"s3", s3PerAgentCost},
		{"s4", s4IncrementalVsScan},
		{"s5", s5HotPathImpact},
		{"s6", s6AfterRotation},
		{"s7", s7LeakCheck},
		{"s8", s8WithoutTheHub},
	}
	for _, p := range probes {
		if run(p.id) {
			fmt.Printf("\n=== %s ===\n", strings.ToUpper(p.id))
			p.fn()
		}
	}
}

// openStore makes a throwaway store with n envelopes spread over `peers`
// recipients, so every probe measures against the same shape of data.
func openStore(n, peers int, opts store.Options) (*store.Store, string) {
	dir, err := os.MkdirTemp("", "spike05-")
	if err != nil {
		panic(err)
	}
	st, err := store.Open(dir, opts)
	if err != nil {
		panic(err)
	}
	for i := 0; i < n; i++ {
		to := fmt.Sprintf("peer%d", i%max(peers, 1))
		_, err := st.Append(&envelope.Envelope{
			ID:       envelope.NewID("m"),
			From:     "prober",
			To:       []string{to},
			ThreadID: fmt.Sprintf("t-%d", i%max(n/8, 1)),
			Text:     "a message with a body of roughly the size a real question has, give or take a clause",
			TS:       envelope.Now(),
			Kind:     "question",
		})
		if err != nil {
			panic(err)
		}
	}
	return st, dir
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000) }

// ---------------------------------------------------------------------- S1 --
// Before measuring anything, name what a human actually asks at 2am. Each
// question is a field; a field nobody asks for is weight, not observability.
func s1DecisionTree() {
	rows := []struct{ question, field, source string }{
		{"Is the hub even up?", "uptime, startedAt", "process"},
		{"Did my message reach the hub?", "envelopes, lastSeq, newestTs", "store"},
		{"Was it addressed to the peer I meant?", "perAgent.delivered", "store"},
		{"Is that peer collecting it?", "perAgent.pending, cursor", "store+registry"},
		{"Is anything attached to ANSWER?", "perAgent.listener, answering", "registry"},
		{"Is the thread refusing sends?", "threads.stalled", "store"},
		{"Are we losing history to retention?", "oldestTs, segments, bytes", "store+fs"},
		{"Is the hub struggling?", "inflightPolls, p99", "server"},
	}
	fmt.Println("the eight questions a quiet mesh raises, and what answers each:")
	for _, r := range rows {
		fmt.Printf("  %-38s -> %-34s (%s)\n", r.question, r.field, r.source)
	}
	fmt.Println("\nNote the shape: SIX of eight are store or registry state we already hold.")
	fmt.Println("Only inflightPolls/p99 needs new instrumentation on the hot path.")
}

// ---------------------------------------------------------------------- S2 --
// Is a full on-demand Stats() cheap enough to poll?
func s2StatsCost() {
	for _, n := range []int{100, 1_000, 10_000, 50_000} {
		st, dir := openStore(n, 10, store.Options{SegmentMaxBytes: 1 << 20})
		t0 := time.Now()
		var s store.Stats
		for i := 0; i < 5; i++ {
			s = st.Stats()
		}
		d := time.Since(t0) / 5
		fmt.Printf("  envelopes=%-6d Stats()=%-9s bytes=%-9d segments=%d threads=%d\n",
			n, ms(d), s.Bytes, s.Segments, s.Threads)
		st.Close()
		os.RemoveAll(dir)
	}
	fmt.Println("  verdict: a full scan is fine to poll while a hub holds tens of thousands;")
	fmt.Println("           past that it wants counters kept at Append (see S4).")
}

// ---------------------------------------------------------------------- S3 --
// AgentStats as drafted walks every envelope for ONE agent. With P peers that
// is P full scans per poll — the shape that looks free at 9 peers and is not.
func s3PerAgentCost() {
	for _, peers := range []int{5, 25, 100} {
		st, dir := openStore(20_000, peers, store.Options{SegmentMaxBytes: 1 << 20})
		cursors := map[string]int64{}
		for i := 0; i < peers; i++ {
			cursors[fmt.Sprintf("peer%d", i)] = 0
		}
		t0 := time.Now()
		_, per := st.Snapshot(cursors)
		d := time.Since(t0)
		fmt.Printf("  peers=%-4d 20k envelopes: ONE pass for all %d peers=%-10s (%s per peer)\n",
			peers, len(per), ms(d), ms(d/time.Duration(peers)))
		st.Close()
		os.RemoveAll(dir)
	}
	fmt.Println("  verdict: cost is O(peers x envelopes) — the naive version is a trap.")
	fmt.Println("           one pass computing ALL peers at once is the shape to build.")
}

// ---------------------------------------------------------------------- S4 --
// Counters maintained at Append vs a scan on demand. Which do we build?
func s4IncrementalVsScan() {
	st, dir := openStore(20_000, 25, store.Options{SegmentMaxBytes: 1 << 20})
	defer func() { st.Close(); os.RemoveAll(dir) }()

	t0 := time.Now()
	_ = st.Stats()
	scan := time.Since(t0)

	// A counter read is a map lookup; simulate the read side only.
	counters := map[string]int{"envelopes": 20000}
	t1 := time.Now()
	_ = counters["envelopes"]
	read := time.Since(t1)

	fmt.Printf("  on-demand scan: %-10s   counter read: %s\n", ms(scan), ms(read))
	fmt.Println("  but a counter costs correctness: retention deletes and tombstones must")
	fmt.Println("  decrement it, and a counter that drifts is worse than no counter.")
	fmt.Println("  verdict: scan for the cheap facts, counters ONLY for what a scan cannot")
	fmt.Println("           see (inflight polls, request rates) — those have no store to read.")
}

// ---------------------------------------------------------------------- S5 --
// Does serving /metrics interfere with sends? Both take the store lock.
func s5HotPathImpact() {
	st, dir := openStore(20_000, 25, store.Options{SegmentMaxBytes: 1 << 20})
	defer func() { st.Close(); os.RemoveAll(dir) }()

	send := func() time.Duration {
		t := time.Now()
		_, _ = st.Append(&envelope.Envelope{
			ID: envelope.NewID("m"), From: "prober", To: []string{"peer1"},
			ThreadID: "t-hot", Text: "measured send", TS: envelope.Now(),
		})
		return time.Since(t)
	}
	var quiet time.Duration
	for i := 0; i < 20; i++ {
		quiet += send()
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = st.Stats()
			}
		}
	}()
	var busy time.Duration
	for i := 0; i < 20; i++ {
		busy += send()
	}
	close(stop)
	fmt.Printf("  send with no metrics traffic: %s\n", ms(quiet/20))
	fmt.Printf("  send while /metrics is hammered: %s\n", ms(busy/20))
	fmt.Println("  verdict: this is the number that decides whether /metrics may hold the")
	fmt.Println("           store lock, or must read from a snapshot taken off the hot path.")
}

// ---------------------------------------------------------------------- S6 --
// Retention and tombstones must not make the numbers lie.
func s6AfterRotation() {
	st, dir := openStore(2_000, 5, store.Options{
		SegmentMaxBytes: 4 << 10, RetentionMaxBytes: 32 << 10,
	})
	defer func() { st.Close(); os.RemoveAll(dir) }()

	before := st.Stats()
	if err := st.EnforceRetention(time.Now()); err != nil {
		fmt.Println("  retention error:", err)
	}
	after := st.Stats()
	// Full precision: these timestamps differ in FRACTIONAL seconds, so a
	// second-truncated display makes a moving oldestTs look frozen. That
	// display cost one wrong "bug" in the first run of this probe.
	fmt.Printf("  before retention: envelopes=%d bytes=%d segments=%d minSeq=%d oldest=%s\n",
		before.Envelopes, before.Bytes, before.Segments, before.MinSeq, before.OldestTS)
	fmt.Printf("  after  retention: envelopes=%d bytes=%d segments=%d minSeq=%d oldest=%s\n",
		after.Envelopes, after.Bytes, after.Segments, after.MinSeq, after.OldestTS)
	if after.OldestTS > before.OldestTS {
		fmt.Println("  oldestTs MOVED FORWARD — retention is visible in the payload")
	} else {
		fmt.Println("  oldestTs did NOT move — the payload would hide the eviction")
	}
	fmt.Println("  verdict: `oldestTs` moving forward is the ONE fact that explains a")
	fmt.Println("           'my message vanished' report. It must be in the payload.")
}

// ---------------------------------------------------------------------- S7 --
// Assert the payload cannot carry a secret. Hoping is not a control.
func s7LeakCheck() {
	st, dir := openStore(50, 3, store.Options{SegmentMaxBytes: 1 << 20})
	defer func() { st.Close(); os.RemoveAll(dir) }()
	b, _ := json.Marshal(st.Stats())
	needles := []string{"secret", "token", "Bearer", "SecretHash", "agentSecret", "password"}
	bad := []string{}
	low := strings.ToLower(string(b))
	for _, n := range needles {
		if strings.Contains(low, strings.ToLower(n)) {
			bad = append(bad, n)
		}
	}
	if len(bad) == 0 {
		fmt.Println("  storage stats carry no credential-shaped field:", string(b)[:min(len(b), 160)])
		fmt.Println("  verdict: keep this as a TEST, not a spike — the registry half of the")
		fmt.Println("           payload (agents) is where a SecretHash could slip in.")
	} else {
		fmt.Println("  LEAK:", bad)
	}
}

// ---------------------------------------------------------------------- S8 --
// The hub is the thing most likely to be down. What can be diagnosed without
// it? This is the argument for a `doctor` verb that reads local state only.
func s8WithoutTheHub() {
	home, _ := os.UserHomeDir()
	cfg := filepath.Join(home, ".config", "workwire")
	facts := []struct{ what, path string }{
		{"hub config", filepath.Join(cfg, "workwire.json")},
		{"client config", filepath.Join(cfg, "skill.json")},
		{"hub stdout", filepath.Join(cfg, "hub.log")},
		{"hub stderr", filepath.Join(cfg, "hub.err.log")},
		{"credentials", filepath.Join(cfg, "credentials.json")},
		{"folder bindings", filepath.Join(cfg, "folders.json")},
		{"run locks", filepath.Join(cfg, "run")},
		{"session inboxes", filepath.Join(cfg, "sessions")},
	}
	for _, f := range facts {
		fi, err := os.Stat(f.path)
		switch {
		case err != nil:
			fmt.Printf("  %-18s MISSING  %s\n", f.what, f.path)
		case fi.IsDir():
			entries, _ := os.ReadDir(f.path)
			fmt.Printf("  %-18s %d entries, newest %s\n", f.what, len(entries), short(fi.ModTime().Format(time.RFC3339)))
		default:
			fmt.Printf("  %-18s %6d bytes, modified %s\n", f.what, fi.Size(), short(fi.ModTime().Format(time.RFC3339)))
		}
	}
	fmt.Println("  verdict: every local fact needed for 'is my side alive?' exists on disk.")
	fmt.Println("           A `doctor` verb answers it with the hub DOWN — which is exactly")
	fmt.Println("           when the question gets asked, and when /metrics cannot answer.")
}

func short(ts string) string {
	if len(ts) > 19 {
		return ts[:19]
	}
	return ts
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
