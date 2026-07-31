package listen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/origin"
)

// identityHub is a hub that remembers one peer's card, so a test can see
// exactly what a re-registration would rewrite.
type identityHub struct {
	stored  map[string]any // the card the hub currently holds
	posts   []map[string]any
	secret  string
	baseURL string
}

func newIdentityHub(t *testing.T, stored map[string]any) *identityHub {
	t.Helper()
	h := &identityHub{stored: stored, secret: "s3cret-test"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agents/{name}/card", func(w http.ResponseWriter, r *http.Request) {
		if h.stored == nil {
			http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, 200, h.stored)
	})
	mux.HandleFunc("POST /agents", func(w http.ResponseWriter, r *http.Request) {
		var card map[string]any
		_ = json.NewDecoder(r.Body).Decode(&card)
		h.posts = append(h.posts, card)
		if h.stored == nil {
			h.stored = card
			writeJSON(w, 201, map[string]string{"agentId": "ag_1", "agentSecret": h.secret})
			return
		}
		// The registry rewrites the stored card wholesale on re-registration —
		// which is exactly why the client must not send a changed one.
		h.stored = card
		writeJSON(w, 200, map[string]string{"agentId": "ag_1", "name": "peer"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	h.baseURL = ts.URL
	return h
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *identityHub) lastPost(t *testing.T) map[string]any {
	t.Helper()
	if len(h.posts) == 0 {
		t.Fatal("no registration was posted")
	}
	return h.posts[len(h.posts)-1]
}

func postedOrigin(t *testing.T, card map[string]any) map[string]any {
	t.Helper()
	o, ok := card["origin"].(map[string]any)
	if !ok {
		t.Fatalf("card carried no origin: %+v", card)
	}
	return o
}

// gitRepo makes a real one-commit repo so provenance is genuine.
func gitRepo(t *testing.T, remote string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "first")
	return dir
}

// seedCredential makes the runner believe it has registered before.
func seedCredential(t *testing.T, configDir, hubURL, name, secret string) {
	t.Helper()
	if err := SaveCredential(configDir, hubURL, name, Credential{AgentID: "ag_1", AgentSecret: secret}); err != nil {
		t.Fatal(err)
	}
}

// --dir states which tree the listener speaks for, instead of inheriting
// whatever directory the shell happened to be in.
func TestDirOverridesCwdForOriginAndPersona(t *testing.T) {
	dir := gitRepo(t, "git@github.com:acme/widget.git")
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"),
		[]byte("# widget\n\n## workwire\n- owns the widget service and its schema\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newIdentityHub(t, nil)
	r, err := New(Options{
		Agent: "peer", HubURL: h.baseURL, ConfigDir: t.TempDir(),
		OriginDir: dir,
		Persona:   "owns the widget service and its schema",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureRegistered(); err != nil {
		t.Fatal(err)
	}
	card := h.lastPost(t)
	o := postedOrigin(t, card)
	if o["repo"] != "acme/widget" {
		t.Fatalf("origin not taken from --dir: %+v", o)
	}
	if o["cwd"] != dir || card["project"] != dir {
		t.Fatalf("cwd/project not taken from --dir: %+v %v", o, card["project"])
	}
	if card["persona"] != "owns the widget service and its schema" {
		t.Fatalf("persona not carried: %v", card["persona"])
	}
}

// Restarting a listener from the same folder must change nothing about the
// peer's identity: same repo, same cwd, same persona — only liveness and the
// fields that legitimately move mid-session (branch, commit, dirty).
func TestReRegisterFromSameDirIsIdempotent(t *testing.T) {
	dir := gitRepo(t, "git@github.com:acme/widget.git")
	stored := map[string]any{
		"name":    "peer",
		"persona": "the hand-written persona nobody may clobber",
		"origin": map[string]any{
			"repo": "acme/widget", "branch": "old-branch",
			"commit": "deadbee", "cwd": dir, "host": "box",
		},
	}
	h := newIdentityHub(t, stored)
	cfgDir := t.TempDir()
	seedCredential(t, cfgDir, h.baseURL, "peer", h.secret)
	r, err := New(Options{
		Agent: "peer", HubURL: h.baseURL, ConfigDir: cfgDir,
		OriginDir: dir,
		Persona:   "an inferred line that must not win", // inferred, not stated
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureRegistered(); err != nil {
		t.Fatal(err)
	}
	card := h.lastPost(t)
	if card["persona"] != "the hand-written persona nobody may clobber" {
		t.Fatalf("stored persona was clobbered: %v", card["persona"])
	}
	o := postedOrigin(t, card)
	if o["repo"] != "acme/widget" || o["cwd"] != dir || card["project"] != dir {
		t.Fatalf("identity fields moved: %+v project=%v", o, card["project"])
	}
	// Branch and commit DO refresh — people switch branches mid-session.
	live := origin.Detect(dir)
	if o["branch"] != live.Branch || o["commit"] != live.Commit {
		t.Fatalf("provenance did not refresh: %+v want %s/%s", o, live.Branch, live.Commit)
	}
}

// Switching branches is normal and must be silent; moving to another repo is
// the thing that made a peer misrepresent its codebase, so it must be loud.
func TestRepoMismatchWarnsButBranchChangeDoesNot(t *testing.T) {
	dir := gitRepo(t, "git@github.com:acme/widget.git")
	cases := []struct {
		name     string
		storedIn map[string]any
		wantWarn bool
	}{
		{
			name: "changed branch is expected",
			storedIn: map[string]any{
				"repo": "acme/widget", "branch": "feature/x", "commit": "0000000", "cwd": dir,
			},
			wantWarn: false,
		},
		{
			name: "changed repo is a silent lie unless we shout",
			storedIn: map[string]any{
				"repo": "acme/other", "branch": "main", "commit": "0000000", "cwd": "/elsewhere",
			},
			wantWarn: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newIdentityHub(t, map[string]any{
				"name": "peer", "persona": "stored persona", "origin": c.storedIn,
			})
			cfgDir := t.TempDir()
			seedCredential(t, cfgDir, h.baseURL, "peer", h.secret)
			var logs []string
			r, err := New(Options{
				Agent: "peer", HubURL: h.baseURL, ConfigDir: cfgDir, OriginDir: dir,
				Persona: "inferred",
				Logf:    func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := r.EnsureRegistered(); err != nil {
				t.Fatal(err)
			}
			warned := ""
			for _, l := range logs {
				if strings.Contains(l, "WARNING") {
					warned = l
				}
			}
			if c.wantWarn {
				if warned == "" {
					t.Fatalf("no warning for a changed repo; logs=%v", logs)
				}
				for _, want := range []string{"acme/other", "acme/widget", "--dir"} {
					if !strings.Contains(warned, want) {
						t.Fatalf("warning does not name %q: %s", want, warned)
					}
				}
				// A genuine move must still go through.
				if postedOrigin(t, h.lastPost(t))["repo"] != "acme/widget" {
					t.Fatal("the move was refused instead of applied")
				}
			} else if warned != "" {
				t.Fatalf("branch change should be silent, got: %s", warned)
			}
		})
	}
}

// An explicit --persona is a deliberate act: it overwrites, same folder or not.
func TestExplicitPersonaStillWins(t *testing.T) {
	dir := gitRepo(t, "git@github.com:acme/widget.git")
	h := newIdentityHub(t, map[string]any{
		"name": "peer", "persona": "the old stored persona",
		"origin": map[string]any{"repo": "acme/widget", "branch": "main", "cwd": dir},
	})
	cfgDir := t.TempDir()
	seedCredential(t, cfgDir, h.baseURL, "peer", h.secret)
	r, err := New(Options{
		Agent: "peer", HubURL: h.baseURL, ConfigDir: cfgDir, OriginDir: dir,
		Persona: "stated on the command line", PersonaExplicit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureRegistered(); err != nil {
		t.Fatal(err)
	}
	if got := h.lastPost(t)["persona"]; got != "stated on the command line" {
		t.Fatalf("explicit persona lost: %v", got)
	}
}
