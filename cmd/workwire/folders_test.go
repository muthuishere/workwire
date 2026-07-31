package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
)

// Two folders called `api` are two peers, not one. The second must be told —
// with the name to use instead — never quietly put on the wire under the
// first's identity, where `ask api "..."` is answered about a different
// codebase.
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

	// The first folder joined as `api` and bound the name to itself.
	saveFolderBinding(cfg, first, "api")

	// The second folder wanting the same name is a conflict, not an adoption.
	err := cmdListen(cfg, []string{"--agent", "api", "--dir", second})
	if err == nil {
		t.Fatal("listen from a colliding folder must not report success")
	}
	if !strings.Contains(err.Error(), "already the peer name") ||
		!strings.Contains(err.Error(), absOf(first)) ||
		!strings.Contains(err.Error(), absOf(second)) {
		t.Fatalf("the conflict must name both folders: %v", err)
	}
	// With no hub to ask, the local disambiguation is offered.
	if !strings.Contains(err.Error(), "b-api") {
		t.Fatalf("no suggested name offered: %v", err)
	}
	// A folder that did not join is never bound.
	if boundName(cfg, second) != "" {
		t.Fatal("the losing folder must not be bound to the name")
	}

	// A live lock whose holder is another folder is a conflict too, even
	// before anything was persisted.
	fresh := config.Config{ConfigDir: t.TempDir(), HubURL: cfg.HubURL}
	runDir := filepath.Join(fresh.ConfigDir, "run")
	if err := listen.WriteHolder(runDir, "api", absOf(first)); err != nil {
		t.Fatal(err)
	}
	if got := nameConflict(fresh, "api", second); got != absOf(first) {
		t.Fatalf("lock holder ignored: %q", got)
	}
	// ...and the SAME folder is not.
	if got := nameConflict(fresh, "api", first); got != "" {
		t.Fatalf("the owning folder must not conflict with itself: %q", got)
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
}

// Same folder, second session: still adoption, still exit 0.
func TestSecondListenerInTheSameFolderAdopts(t *testing.T) {
	cfgDir := t.TempDir()
	dir := t.TempDir()
	cfg := config.Config{ConfigDir: cfgDir, HubURL: "http://127.0.0.1:1"}
	runDir := filepath.Join(cfgDir, "run")
	lock, err := listen.AcquireLock(runDir, "peer")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := listen.WriteHolder(runDir, "peer", absOf(dir)); err != nil {
		t.Fatal(err)
	}
	saveFolderBinding(cfg, dir, "peer")
	if err := cmdListen(cfg, []string{"--agent", "peer", "--dir", dir}); err != nil {
		t.Fatalf("second listener in the same folder should adopt and exit 0, got: %v", err)
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
