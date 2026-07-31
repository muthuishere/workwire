package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/hubaddr"
	"github.com/muthuishere/workwire/internal/listen"
)

const fakeAdmin = "fake-admin-token-abc123"

// seedAdminToken writes the locally minted 0600 admin token.
func seedAdminToken(t *testing.T, configDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(configDir, auth.TokenFileName), []byte(fakeAdmin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureHub records the Authorization header of every request it sees.
func captureHub(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"workwire","schemaVersion":1,"apiVersion":1}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &seen
}

// The locally minted admin token is a credential for THIS machine's hub. It
// goes to loopback and nowhere else — not even on an unauthenticated probe.
func TestAdminTokenOnlyGoesToLoopback(t *testing.T) {
	dir := t.TempDir()
	seedAdminToken(t, dir)

	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		t.Run("loopback "+host, func(t *testing.T) {
			cfg := config.Config{ConfigDir: dir, HubURL: "http://" + host + ":14411", TokenEnv: "WORKWIRE_TOKEN"}
			c := newClient(cfg)
			if c.token != fakeAdmin {
				t.Fatalf("loopback hub should carry the local admin token, got %q", c.token)
			}
			if c.authErr != nil {
				t.Fatalf("loopback hub should not be blocked: %v", c.authErr)
			}
		})
	}

	t.Run("remote host gets nothing, on the wire too", func(t *testing.T) {
		ts, seen := captureHub(t)
		remote := strings.Replace(ts.URL, "127.0.0.1", "example.invalid", 1)
		cfg := config.Config{ConfigDir: dir, HubURL: remote, TokenEnv: "WORKWIRE_TOKEN"}
		c := newClient(cfg)
		if c.token != "" {
			t.Fatalf("remote hub was handed a token: %q", c.token)
		}
		// Even the reachability probe must fail closed rather than leak.
		c.base = ts.URL // point the socket at the capture server, keep the remote judgement
		if _, err := c.do("GET", "/health", nil, nil); err == nil {
			t.Fatal("a remote hub with no credential must fail, not probe")
		} else if !strings.Contains(err.Error(), "WORKWIRE_TOKEN") {
			t.Fatalf("error does not name the env var to set: %v", err)
		}
		for _, h := range *seen {
			if strings.Contains(h, fakeAdmin) {
				t.Fatalf("the local admin token went to a remote host: %q", h)
			}
		}
		if len(*seen) != 0 {
			t.Fatalf("no request should have been made at all, got %d", len(*seen))
		}
	})

	t.Run("the env-named token is what a remote hub gets", func(t *testing.T) {
		t.Setenv("WORKWIRE_TOKEN", "issued-by-the-remote-hub")
		cfg := config.Config{ConfigDir: dir, HubURL: "https://hub.example.com", TokenEnv: "WORKWIRE_TOKEN"}
		c := newClient(cfg)
		if c.token != "issued-by-the-remote-hub" || c.authErr != nil {
			t.Fatalf("env credential must win for a remote hub: token=%q err=%v", c.token, c.authErr)
		}
	})
}

// A per-agent secret is issued BY a hub: it is selected by hub, never by name
// alone, so pointing hubUrl elsewhere presents nothing.
func TestPerHubCredentialSelection(t *testing.T) {
	dir := t.TempDir()
	local := "http://127.0.0.1:14411"
	remote := "https://hub.example.com"
	if err := listen.SaveCredential(dir, local, "api", listen.Credential{AgentID: "a_local", AgentSecret: "s_local"}); err != nil {
		t.Fatal(err)
	}
	if err := listen.SaveCredential(dir, remote, "api", listen.Credential{AgentID: "a_remote", AgentSecret: "s_remote"}); err != nil {
		t.Fatal(err)
	}
	// Storing the remote one must not have clobbered the local one.
	c := newClient(config.Config{ConfigDir: dir, HubURL: local, TokenEnv: "WORKWIRE_TOKEN"})
	if err := c.asAgent(config.Config{ConfigDir: dir, HubURL: local}, "api"); err != nil {
		t.Fatal(err)
	}
	if c.token != "s_local" {
		t.Fatalf("local hub got the wrong secret: %q", c.token)
	}
	c = newClient(config.Config{ConfigDir: dir, HubURL: remote, TokenEnv: "WORKWIRE_TOKEN"})
	if err := c.asAgent(config.Config{ConfigDir: dir, HubURL: remote}, "api"); err != nil {
		t.Fatal(err)
	}
	if c.token != "s_remote" {
		t.Fatalf("remote hub got the wrong secret: %q", c.token)
	}
	// A hub we hold nothing for gets nothing — not somebody else's secret.
	other := config.Config{ConfigDir: dir, HubURL: "https://other.example.com", TokenEnv: "WORKWIRE_TOKEN"}
	c = newClient(other)
	if err := c.asAgent(other, "api"); err == nil {
		t.Fatalf("a hub with no stored credential must not borrow one: %q", c.token)
	}
	// Trailing slash / case must not split a hub in two.
	c = newClient(config.Config{ConfigDir: dir, HubURL: "http://127.0.0.1:14411/", TokenEnv: "WORKWIRE_TOKEN"})
	if err := c.asAgent(config.Config{ConfigDir: dir, HubURL: "http://127.0.0.1:14411/"}, "api"); err != nil {
		t.Fatalf("hub key is not normalised: %v", err)
	}
}

// Nobody loses a credential on upgrade: a legacy name-keyed file is adopted
// by the local loopback hub — the only hub that could have issued it — and
// rewritten in the new shape, silently.
func TestLegacyCredentialsMigrateInPlace(t *testing.T) {
	dir := t.TempDir()
	legacy := `{
  "api": {"agentId": "a_1", "agentSecret": "s_1"},
  "docs": {"agentId": "a_2", "agentSecret": "s_2"}
}`
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	// Read through a REMOTE hub: the legacy entries are local, so this must
	// see nothing — and must not have thrown them away either.
	remote, err := listen.LoadCredentials(dir, "https://hub.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := remote["api"]; ok {
		t.Fatal("legacy local credentials were offered to a remote hub")
	}

	creds, err := listen.LoadCredentials(dir, "http://127.0.0.1:14411")
	if err != nil {
		t.Fatal(err)
	}
	if creds["api"].AgentSecret != "s_1" || creds["docs"].AgentSecret != "s_2" {
		t.Fatalf("migration lost credentials: %+v", creds)
	}

	// The file is upgraded in place, still 0600, and stays readable.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("migrated credentials.json mode %v, want 0600", fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Version int                                     `json:"version"`
		Hubs    map[string]map[string]listen.Credential `json:"hubs"`
	}
	if err := json.Unmarshal(b, &onDisk); err != nil {
		t.Fatalf("migrated file is not valid JSON: %v\n%s", err, b)
	}
	if onDisk.Version != 2 {
		t.Fatalf("expected version 2 on disk, got %d", onDisk.Version)
	}
	key := hubaddr.Key("http://127.0.0.1:14411")
	if onDisk.Hubs[key]["api"].AgentSecret != "s_1" {
		t.Fatalf("legacy entries not homed under the local hub: %+v", onDisk.Hubs)
	}
	// Re-reading the migrated file is stable.
	again, err := listen.LoadCredentials(dir, "http://localhost:14411/")
	if err != nil || again["docs"].AgentSecret != "s_2" {
		t.Fatalf("second read after migration: %+v err=%v", again, err)
	}
}
