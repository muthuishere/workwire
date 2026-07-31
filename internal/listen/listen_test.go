package listen

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireLockSingleton(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name  string
		setup func(t *testing.T) *Lock // returns a held lock or nil
		agent string
		want  bool // second acquire succeeds?
	}{
		{"exclusive while held", func(t *testing.T) *Lock {
			l, err := AcquireLock(dir, "a")
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			return l
		}, "a", false},
		{"different agent unaffected", func(t *testing.T) *Lock {
			l, err := AcquireLock(dir, "a")
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			return l
		}, "b", true},
		{"free after release", func(t *testing.T) *Lock {
			l, err := AcquireLock(dir, "a")
			if err != nil {
				t.Fatalf("first acquire: %v", err)
			}
			l.Release()
			return nil
		}, "a", true},
		{"stale lock file never blocks", func(t *testing.T) *Lock {
			// A leftover file with no live holder (kill -9 case).
			if err := os.WriteFile(filepath.Join(dir, "stale.lock"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}, "stale", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			held := tc.setup(t)
			l2, err := AcquireLock(dir, tc.agent)
			if tc.want {
				if err != nil {
					t.Fatalf("expected acquire to succeed, got %v", err)
				}
				l2.Release()
			} else {
				if err == nil {
					l2.Release()
					t.Fatal("expected ErrLocked, second acquire succeeded")
				}
				if _, ok := err.(ErrLocked); !ok {
					t.Fatalf("expected ErrLocked, got %T: %v", err, err)
				}
			}
			if held != nil {
				held.Release()
			}
		})
	}
}

// stubHub is a minimal /inbox + /agents server driven by canned responses.
type stubHub struct {
	mu        []string // recorded request lines
	responses []string // JSON bodies served by /inbox in order
	i         int
}

func newStubServer(t *testing.T, hub *stubHub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		hub.mu = append(hub.mu, "since="+r.URL.Query().Get("since"))
		body := `{"messages":[],"next":0}`
		if hub.i < len(hub.responses) {
			body = hub.responses[hub.i]
			hub.i++
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	})
	mux.HandleFunc("POST /agents", func(w http.ResponseWriter, r *http.Request) {
		var card struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&card)
		if card.Name == "taken" {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":"name taken","name":"taken","suggestion":"taken-2"}`)
			return
		}
		w.WriteHeader(201)
		fmt.Fprint(w, `{"agentId":"ag_1","agentSecret":"s3cret-test"}`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func env(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"from":"asker","to":"repoA","thread_id":"t-1","text":%q,"ts":"2026-07-30T00:00:00Z","kind":"question","context":[{"id":"c1","text":"prior"}]}`, id, text)
}

func newRunner(t *testing.T, hubURL, agent string) *Runner {
	t.Helper()
	r, err := New(Options{Agent: agent, HubURL: hubURL, ConfigDir: t.TempDir(), Wait: 0})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPollOnceAppendsCursorAndDedupes(t *testing.T) {
	hub := &stubHub{responses: []string{
		fmt.Sprintf(`{"messages":[%s,%s],"next":2}`, env("q-1", "one"), env("q-2", "two")),
		fmt.Sprintf(`{"messages":[%s,%s],"next":3}`, env("q-2", "two"), env("q-3", "three")), // q-2 redelivered
	}}
	ts := newStubServer(t, hub)
	r := newRunner(t, ts.URL, "repoA")

	if n, err := r.PollOnce(); err != nil || n != 2 {
		t.Fatalf("first poll: n=%d err=%v", n, err)
	}
	if n, err := r.PollOnce(); err != nil || n != 1 {
		t.Fatalf("second poll (dup q-2 must be skipped): n=%d err=%v", n, err)
	}

	// Cursor persisted and advanced between polls.
	if got := hub.mu; len(got) != 2 || got[0] != "since=0" || got[1] != "since=2" {
		t.Fatalf("cursor progression wrong: %v", got)
	}
	var st State
	b, err := os.ReadFile(r.statePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Next != 3 {
		t.Fatalf("persisted cursor: %+v err=%v", st, err)
	}

	// Inbox file: exactly one NDJSON line per unique envelope, full envelope
	// (context included) preserved verbatim-parseable.
	lines := readLines(t, r.InboxPath())
	if len(lines) != 3 {
		t.Fatalf("expected 3 inbox lines, got %d: %v", len(lines), lines)
	}
	for i, want := range []string{"q-1", "q-2", "q-3"} {
		var e struct {
			ID      string            `json:"id"`
			Text    string            `json:"text"`
			Context []json.RawMessage `json:"context"`
		}
		if err := json.Unmarshal([]byte(lines[i]), &e); err != nil {
			t.Fatalf("line %d not valid JSON: %v", i, err)
		}
		if e.ID != want || len(e.Context) != 1 {
			t.Fatalf("line %d: id=%s context=%d", i, e.ID, len(e.Context))
		}
	}
}

func TestDedupeSurvivesRestart(t *testing.T) {
	hub := &stubHub{responses: []string{
		fmt.Sprintf(`{"messages":[%s],"next":1}`, env("q-1", "one")),
		fmt.Sprintf(`{"messages":[%s],"next":1}`, env("q-1", "one")), // same envelope again after restart
	}}
	ts := newStubServer(t, hub)
	cfgDir := t.TempDir()
	r1, err := New(Options{Agent: "repoA", HubURL: ts.URL, ConfigDir: cfgDir, Wait: 0})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r1.PollOnce(); n != 1 {
		t.Fatalf("first delivery n=%d", n)
	}
	// New Runner = process restart: state reloaded from disk.
	r2, err := New(Options{Agent: "repoA", HubURL: ts.URL, ConfigDir: cfgDir, Wait: 0})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := r2.PollOnce(); n != 0 {
		t.Fatalf("duplicate after restart must not append, n=%d", n)
	}
	if lines := readLines(t, r2.InboxPath()); len(lines) != 1 {
		t.Fatalf("inbox lines after restart: %d", len(lines))
	}
}

func TestResetAdoptsHubCursor(t *testing.T) {
	hub := &stubHub{responses: []string{
		`{"messages":[],"next":42,"reset":true}`,
		`{"messages":[],"next":42}`,
	}}
	ts := newStubServer(t, hub)
	r := newRunner(t, ts.URL, "repoA")
	r.state.Next = 7
	if _, err := r.PollOnce(); err != nil {
		t.Fatal(err)
	}
	if r.state.Next != 42 {
		t.Fatalf("reset must adopt hub cursor, got %d", r.state.Next)
	}
	if _, err := r.PollOnce(); err != nil {
		t.Fatal(err)
	}
	if hub.mu[1] != "since=42" {
		t.Fatalf("next poll must use adopted cursor: %v", hub.mu)
	}
}

func TestEnsureRegistered(t *testing.T) {
	t.Run("first run stores 0600 credentials", func(t *testing.T) {
		ts := newStubServer(t, &stubHub{})
		r := newRunner(t, ts.URL, "repoA")
		if err := r.EnsureRegistered(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(r.opts.ConfigDir, "credentials.json")
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("credentials.json mode %v, want 0600", fi.Mode().Perm())
		}
		creds, _ := LoadCredentials(r.opts.ConfigDir, r.opts.HubURL)
		if creds["repoA"].AgentID != "ag_1" || creds["repoA"].AgentSecret == "" {
			t.Fatalf("stored credential wrong: %+v", creds["repoA"])
		}
	})
	t.Run("name conflict adopts suggestion", func(t *testing.T) {
		ts := newStubServer(t, &stubHub{})
		r := newRunner(t, ts.URL, "taken")
		if err := r.EnsureRegistered(); err != nil {
			t.Fatal(err)
		}
		if r.AgentName() != "taken-2" {
			t.Fatalf("expected suggested name taken-2, got %s", r.AgentName())
		}
		creds, _ := LoadCredentials(r.opts.ConfigDir, r.opts.HubURL)
		if _, ok := creds["taken-2"]; !ok {
			t.Fatal("credentials not stored under suggested name")
		}
	})
}

func TestRotation(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:0", "repoA")
	r.opts.RotateMaxBytes = 10
	big := strings.Repeat("x", 32) + "\n"
	if err := os.WriteFile(r.InboxPath(), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("unconsumed tail never rotates", func(t *testing.T) {
		os.WriteFile(r.offsetPath(), []byte("5\n"), 0o644)
		r.maybeRotate()
		if fi, _ := os.Stat(r.InboxPath()); fi.Size() != int64(len(big)) {
			t.Fatal("rotated under the consumer")
		}
	})
	t.Run("fully consumed rotates and rebases offset", func(t *testing.T) {
		os.WriteFile(r.offsetPath(), []byte(fmt.Sprintf("%d\n", len(big))), 0o644)
		r.maybeRotate()
		if fi, _ := os.Stat(r.InboxPath()); fi.Size() != 0 {
			t.Fatalf("expected truncation, size=%d", fi.Size())
		}
		b, _ := os.ReadFile(r.offsetPath())
		if strings.TrimSpace(string(b)) != "0" {
			t.Fatalf("offset not rebased: %q", b)
		}
	})
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// The listener joins the audiences declared in the peer's own file
// (ADR-012) — and never joins anybody else to anything.
func TestJoinDeclaredGroups(t *testing.T) {
	var joined []string
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/join") {
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			joined = append(joined, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/groups/"), "/join"))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	r, err := New(Options{
		Agent:     "web",
		HubURL:    ts.URL,
		ConfigDir: t.TempDir(),
		// bare names are normalised; @all comes from the hub, not here
		Groups: []string{"@platform", "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.JoinDeclaredGroups()
	if got := strings.Join(joined, ","); got != "@platform,@data" {
		t.Fatalf("joined %q", got)
	}
	for _, b := range bodies {
		if strings.Contains(b, "peer") {
			t.Fatalf("join body must not name a peer: %s", b)
		}
	}
}
