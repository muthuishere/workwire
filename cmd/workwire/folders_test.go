package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

// captureStdout runs fn and returns whatever it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	out := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		out <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	s := <-out
	r.Close()
	return s
}

// chdir moves into dir for the rest of the test (the module targets go1.22,
// so testing.Chdir is not available).
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// Two folders called `api` are two peers, not one. The second must be told —
// with the name to use instead — never silently merged onto the first's
// identity while being told it joined.
func TestTwoFoldersWithTheSameBasenameConflict(t *testing.T) {
	cfgDir := t.TempDir()
	root := t.TempDir()
	first := filepath.Join(root, "a", "api")
	second := filepath.Join(root, "b", "api")
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{ConfigDir: cfgDir, HubURL: "http://127.0.0.1:1"} // no hub
	if err := setAutoJoin(skillConfigPath(cfg), true); err != nil {
		t.Fatal(err)
	}

	var spawns []string
	orig := spawnListener
	spawnListener = func(_ config.Config, name, dir string) { spawns = append(spawns, name+" @ "+dir) }
	defer func() { spawnListener = orig }()

	chdir(t, first)
	if err := cmdSessionStart(cfg, nil); err != nil {
		t.Fatalf("first folder: %v", err)
	}
	if len(spawns) != 1 || !strings.HasPrefix(spawns[0], "api @ ") {
		t.Fatalf("first folder should have joined as api: %v", spawns)
	}

	chdir(t, second)
	out := captureStdout(t, func() {
		if err := cmdSessionStart(cfg, nil); err != nil {
			t.Fatalf("second folder must still exit 0: %v", err)
		}
	})
	if len(spawns) != 1 {
		t.Fatalf("the second folder joined under a shared identity: %v", spawns)
	}
	if !strings.Contains(out, "already the peer name") || !strings.Contains(out, "has NOT joined") {
		t.Fatalf("the conflict was not reported: %q", out)
	}
	// With no hub to ask, the local disambiguation is offered.
	if !strings.Contains(out, "b-api") {
		t.Fatalf("no suggested name offered: %q", out)
	}
	// And it is on the record, not only on a stdout nobody reads.
	logged, err := os.ReadFile(filepath.Join(cfgDir, "auto-join.log"))
	if err != nil || !strings.Contains(string(logged), "already the peer name") {
		t.Fatalf("conflict not logged: %v %s", err, logged)
	}

	// An explicit `workwire listen` from the second folder fails loudly rather
	// than quietly adopting somebody else's identity.
	err = cmdListen(cfg, []string{"--agent", "api", "--dir", second})
	if err == nil {
		t.Fatal("listen from a colliding folder must not report success")
	}
	if !strings.Contains(err.Error(), "already the peer name") {
		t.Fatalf("unhelpful conflict error: %v", err)
	}

	// The first folder is unaffected: same name, same folder, still adopts.
	chdir(t, first)
	if err := cmdSessionStart(cfg, nil); err != nil {
		t.Fatalf("re-running in the owning folder: %v", err)
	}
	if len(spawns) != 2 {
		t.Fatalf("the owning folder must keep joining as api: %v", spawns)
	}

	// The binding is persisted, so the answer survives a restart.
	b, err := os.ReadFile(filepath.Join(cfgDir, foldersFileName))
	if err != nil {
		t.Fatal(err)
	}
	var ff foldersFile
	if err := json.Unmarshal(b, &ff); err != nil {
		t.Fatal(err)
	}
	if ff.Folders[absOf(first)].Name != "api" {
		t.Fatalf("binding not persisted: %s", b)
	}
	if _, ok := ff.Folders[absOf(second)]; ok {
		t.Fatalf("a folder that did not join must not be bound: %s", b)
	}
}

// The hub already owns the `<name>-N` suggestion for a taken name; the CLI
// asks it rather than inventing a second scheme — and asking must never
// create the peer it is asking about.
func TestSuggestionComesFromTheHubWithoutRegistering(t *testing.T) {
	var posts int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agents/{name}/card", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "api" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api"}`))
	})
	mux.HandleFunc("POST /agents", func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"name taken","name":"api","suggestion":"api-2"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := config.Config{ConfigDir: t.TempDir(), HubURL: ts.URL}
	if got := suggestFreeName(cfg, "api", "/tmp/b/api"); got != "api-2" {
		t.Fatalf("hub suggestion not used: %q", got)
	}
	// A name the hub does not know is never POSTed — asking must not register.
	if got := suggestFreeName(cfg, "unknown", "/tmp/b/unknown"); got != "b-unknown" {
		t.Fatalf("expected the local fallback, got %q", got)
	}
	if posts != 1 {
		t.Fatalf("expected exactly one POST /agents, got %d", posts)
	}
}
