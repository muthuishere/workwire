package listen

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHub is a controllable hub: it can forget leases, reject credentials,
// claim the lease is held elsewhere, and — crucially — be stopped and
// restarted on the SAME address, which is the outage this suite is about.
type fakeHub struct {
	mu sync.Mutex

	srv  *httptest.Server
	addr string

	// leaseMode: "ok" | "forget" (restart lost the lease) | "notfound" |
	// "contended" (another host really holds it).
	leaseMode  string
	rejectCred bool

	pending   []string // envelopes served on the next /inbox poll
	next      int64
	since     []int64  // every cursor the listener asked with, in order
	leaseIDs  []string // leases issued
	registers int
	leaseReqs int
	released  bool
	secret    string
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{leaseMode: "ok", secret: "s3cret-1"}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = l.Addr().String()
	h.start(l)
	t.Cleanup(h.stop)
	return h
}

func (h *fakeHub) start(l net.Listener) {
	srv := httptest.NewUnstartedServer(h.handler())
	_ = srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	h.mu.Lock()
	h.srv = srv
	h.mu.Unlock()
}

// restart brings the hub back on the same address, as a hub service restart
// does. Everything the hub held in memory (leases) is gone.
func (h *fakeHub) restart(t *testing.T) {
	t.Helper()
	h.stop()
	var l net.Listener
	var err error
	for i := 0; i < 50; i++ {
		if l, err = net.Listen("tcp", h.addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("re-listen on %s: %v", h.addr, err)
	}
	h.start(l)
}

func (h *fakeHub) stop() {
	h.mu.Lock()
	srv := h.srv
	h.srv = nil
	h.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
}

func (h *fakeHub) url() string { return "http://" + h.addr }

func (h *fakeHub) set(fn func(*fakeHub)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *fakeHub) snap() fakeHub {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fakeHub{since: append([]int64(nil), h.since...), registers: h.registers,
		leaseReqs: h.leaseReqs, released: h.released,
		leaseIDs: append([]string(nil), h.leaseIDs...)}
}

func (h *fakeHub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.rejectCred && r.Header.Get("Authorization") != "" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"unknown credential"}`)
			return
		}
		h.registers++
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"agentId":"ag_1","agentSecret":%q}`, h.secret)
	})
	mux.HandleFunc("POST /agents/{name}/listen-lease", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			LeaseID string `json:"leaseId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.leaseReqs++
		conflict := func(code int) {
			w.WriteHeader(code)
			fmt.Fprintf(w, `{"holder":"other","expiresAt":%q}`,
				time.Now().Add(30*time.Second).UTC().Format(time.RFC3339Nano))
		}
		switch h.leaseMode {
		case "contended":
			conflict(409)
			return
		case "forget":
			if req.LeaseID != "" {
				conflict(409) // the hub no longer knows this leaseId
				return
			}
		case "notfound":
			if req.LeaseID != "" {
				w.WriteHeader(404)
				fmt.Fprint(w, `{"error":"agent not found"}`)
				return
			}
		}
		id := fmt.Sprintf("l_%d", len(h.leaseIDs)+1)
		h.leaseIDs = append(h.leaseIDs, id)
		w.WriteHeader(200)
		fmt.Fprintf(w, `{"leaseId":%q,"ttl":30}`, id)
	})
	mux.HandleFunc("DELETE /agents/{name}/listen-lease", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.released = true
		h.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		var since int64
		fmt.Sscan(r.URL.Query().Get("since"), &since)
		h.since = append(h.since, since)
		if h.rejectCred && r.Header.Get("Authorization") == "Bearer "+h.secret {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":"unknown credential"}`)
			return
		}
		msgs := h.pending
		h.pending = nil
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"messages":[%s],"next":%d}`, joinRaw(msgs), h.next)
	})
	return mux
}

func joinRaw(msgs []string) string {
	out := ""
	for i, m := range msgs {
		if i > 0 {
			out += ","
		}
		out += m
	}
	return out
}

// queue makes one envelope available on the next poll and advances the hub's
// cursor, as a real ask would.
func (h *fakeHub) queue(id, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	h.pending = append(h.pending, env(id, text))
}

// runRunner starts a Runner in the background with test-speed backoff and
// returns it plus a stop func that waits for a clean exit.
func runRunner(t *testing.T, h *fakeHub, opts Options) (*Runner, func(), chan error) {
	t.Helper()
	if opts.Agent == "" {
		opts.Agent = "repoA"
	}
	opts.HubURL = h.url()
	if opts.ConfigDir == "" {
		opts.ConfigDir = t.TempDir()
	}
	opts.Wait = 0
	if opts.Heartbeat == 0 {
		opts.Heartbeat = 20 * time.Millisecond
	}
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = 10 * time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 50 * time.Millisecond
	}
	if opts.ContendedBackoff == 0 {
		opts.ContendedBackoff = 200 * time.Millisecond
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	r, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	shutdown := func() { once.Do(func() { close(stop) }) }
	finished := make(chan struct{})
	go func() { done <- r.Run(stop); close(finished) }()
	t.Cleanup(func() {
		shutdown()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("listener did not stop")
		}
	})
	return r, shutdown, done
}

// waitLines blocks until the inbox file holds n lines, or fails.
func waitLines(t *testing.T, path string, n int, why string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		b, err := readFileQuiet(path)
		if err == nil {
			lines = splitNonEmpty(string(b))
			if len(lines) >= n {
				return lines
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: wanted %d inbox lines, got %d", why, n, len(lines))
	return nil
}

// TestListenerSurvivesHubRestart is the outage that started this: the hub
// went away, the listener died, and every agent silently fell off the mesh.
func TestListenerSurvivesHubRestart(t *testing.T) {
	h := newFakeHub(t)
	r, _, done := runRunner(t, h, Options{})
	h.queue("q-1", "before")
	waitLines(t, r.InboxPath(), 1, "delivery before the outage")

	h.stop()
	time.Sleep(150 * time.Millisecond) // listener is now failing every poll
	select {
	case err := <-done:
		t.Fatalf("listener exited while the hub was down: %v", err)
	default:
	}

	h.restart(t)
	h.queue("q-2", "after")
	lines := waitLines(t, r.InboxPath(), 2, "delivery after the hub came back")
	if len(lines) != 2 {
		t.Fatalf("expected exactly 2 deliveries, got %d", len(lines))
	}

	// The cursor must never go backwards across the outage.
	prev := int64(-1)
	for _, s := range h.snap().since {
		if s < prev {
			t.Fatalf("cursor regressed across the restart: %v", h.snap().since)
		}
		prev = s
	}
	if prev < 1 {
		t.Fatalf("cursor never advanced: %v", h.snap().since)
	}
}

// TestListenerRecoversFromHubSideFailures covers every recoverable condition:
// none of them may end the process.
func TestListenerRecoversFromHubSideFailures(t *testing.T) {
	tests := []struct {
		name   string
		mode   string // lease mode applied after the first successful connect
		reject bool   // hub starts rejecting our credential
		check  func(t *testing.T, h *fakeHub, r *Runner)
	}{
		{
			name: "hub forgot the lease (409 on renew)",
			mode: "forget",
			check: func(t *testing.T, h *fakeHub, r *Runner) {
				// Re-acquired from scratch: more than one lease was issued.
				if got := len(h.snap().leaseIDs); got < 2 {
					t.Fatalf("expected a fresh lease after the hub forgot it, issued=%d", got)
				}
			},
		},
		{
			name: "hub does not know the agent (404 on renew)",
			mode: "notfound",
			check: func(t *testing.T, h *fakeHub, r *Runner) {
				if got := len(h.snap().leaseIDs); got < 2 {
					t.Fatalf("expected a fresh lease after 404, issued=%d", got)
				}
			},
		},
		{
			name:   "credential rejected (401) re-registers and keeps the cursor",
			reject: true,
			check: func(t *testing.T, h *fakeHub, r *Runner) {
				if got := h.snap().registers; got < 2 {
					t.Fatalf("expected a re-registration, registers=%d", got)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newFakeHub(t)
			r, _, done := runRunner(t, h, Options{})
			h.queue("q-1", "first")
			waitLines(t, r.InboxPath(), 1, "first delivery")
			cursorBefore := r.state.Next

			h.set(func(h *fakeHub) {
				if tc.mode != "" {
					h.leaseMode = tc.mode
				}
				h.rejectCred = tc.reject
			})
			time.Sleep(200 * time.Millisecond)
			select {
			case err := <-done:
				t.Fatalf("listener exited on a recoverable condition: %v", err)
			default:
			}

			// Heal, then prove delivery resumes on the SAME cursor.
			h.set(func(h *fakeHub) {
				h.leaseMode = "ok"
				h.rejectCred = false
				if tc.reject {
					h.secret = "s3cret-2"
				}
			})
			h.queue("q-2", "second")
			waitLines(t, r.InboxPath(), 2, "delivery after recovery")
			if r.state.Next < cursorBefore {
				t.Fatalf("cursor regressed: %d -> %d", cursorBefore, r.state.Next)
			}
			tc.check(t, h, r)
		})
	}
}

// A lease genuinely held by another host is NOT an error to hammer: this
// listener waits its turn on a slow cadence and takes over when the other
// host dies.
func TestContendedLeaseBacksOffSlowlyAndTakesOver(t *testing.T) {
	h := newFakeHub(t)
	h.set(func(h *fakeHub) { h.leaseMode = "contended" })
	r, _, done := runRunner(t, h, Options{ContendedBackoff: 300 * time.Millisecond})

	time.Sleep(700 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("listener exited on contention instead of waiting: %v", err)
	default:
	}
	// Slow cadence: a handful of attempts in 700ms, not hundreds.
	if got := h.snap().leaseReqs; got > 8 {
		t.Fatalf("hammering a live peer: %d lease attempts in 700ms", got)
	}

	// The other host dies: this listener must take over on its own.
	h.set(func(h *fakeHub) { h.leaseMode = "ok" })
	h.queue("q-1", "took over")
	waitLines(t, r.InboxPath(), 1, "takeover after the other host died")
}

func TestGracefulShutdownReleasesLease(t *testing.T) {
	h := newFakeHub(t)
	r, stop, done := runRunner(t, h, Options{})
	h.queue("q-1", "hello")
	waitLines(t, r.InboxPath(), 1, "delivery before shutdown")

	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown returned an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not shut down")
	}
	if !h.snap().released {
		t.Fatal("graceful shutdown must release the lease")
	}
}

// The escape hatch is opt-in only; the default is infinite.
func TestMaxRetriesGivesUp(t *testing.T) {
	h := newFakeHub(t)
	h.stop() // hub never comes up
	_, _, done := runRunner(t, h, Options{MaxRetries: 3})
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the exhausted-retries error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("--max-retries did not stop the listener")
	}
}

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:0", "repoA")
	r.opts.BaseBackoff = time.Second
	r.opts.MaxBackoff = 30 * time.Second
	for _, tc := range []struct {
		attempt int
		lo, hi  time.Duration
	}{
		{1, 750 * time.Millisecond, 1250 * time.Millisecond},
		{4, 6 * time.Second, 10 * time.Second},
		{50, 22 * time.Second, 38 * time.Second}, // capped
	} {
		d := r.backoffFor(condUnreachable, tc.attempt)
		if d < tc.lo || d > tc.hi {
			t.Fatalf("attempt %d: backoff %v outside [%v,%v]", tc.attempt, d, tc.lo, tc.hi)
		}
	}
}

func readFileQuiet(path string) ([]byte, error) { return os.ReadFile(path) }

func splitNonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
