package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

const (
	skillLiteral = "SKILL-LITERAL-TOKEN"
	hubLiteral   = "HUBFILE-LITERAL-TOKEN"
	envLiteral   = "ENV-TOKEN"
)

// writeSkillJSON writes a skill.json with the given token and mode.
func writeSkillJSON(t *testing.T, dir, token string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, skillConfigName)
	b, err := json.MarshalIndent(skillConfig{Token: token}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // defeat umask
		t.Fatal(err)
	}
	return path
}

// The whole chain, top to bottom.
func TestTokenPrecedence(t *testing.T) {
	dir := t.TempDir()
	seedAdminToken(t, dir)
	writeSkillJSON(t, dir, skillLiteral, 0o600)

	base := func() config.Config {
		cfg := config.Defaults()
		cfg.ConfigDir = dir
		cfg.HubURL = "http://127.0.0.1:14411"
		cfg.Token = hubLiteral // as read from a 0600 workwire.json
		return cfg
	}
	withSkill := func(cfg config.Config) config.Config {
		sc, warn := loadSkillConfigWarn(skillConfigPath(cfg))
		if warn != "" {
			t.Fatalf("unexpected warning: %s", warn)
		}
		applySkillConfig(&cfg, sc)
		return cfg
	}

	t.Run("env beats every file", func(t *testing.T) {
		t.Setenv("WORKWIRE_TOKEN", envLiteral)
		if got := newClient(withSkill(base())).token; got != envLiteral {
			t.Fatalf("token = %q, want the env value", got)
		}
	})

	t.Run("the tokenEnv-named var beats the files", func(t *testing.T) {
		t.Setenv("TEAM_HUB_TOKEN", "NAMED-ENV-TOKEN")
		cfg := base()
		cfg.TokenEnv = "TEAM_HUB_TOKEN"
		if got := newClient(withSkill(cfg)).token; got != "NAMED-ENV-TOKEN" {
			t.Fatalf("token = %q, want the tokenEnv-named value", got)
		}
	})

	t.Run("skill.json literal beats workwire.json literal", func(t *testing.T) {
		if got := newClient(withSkill(base())).token; got != skillLiteral {
			t.Fatalf("token = %q, want the skill.json literal", got)
		}
	})

	t.Run("workwire.json literal beats the admin-token file", func(t *testing.T) {
		cfg := base() // no skill.json applied
		if got := newClient(cfg).token; got != hubLiteral {
			t.Fatalf("token = %q, want the workwire.json literal", got)
		}
	})

	t.Run("no literal falls back to the minted file on loopback", func(t *testing.T) {
		cfg := base()
		cfg.Token = ""
		if got := newClient(cfg).token; got != fakeAdmin {
			t.Fatalf("token = %q, want the minted admin token", got)
		}
	})

	t.Run("a literal token may go to a remote hub; the minted file may not", func(t *testing.T) {
		cfg := base()
		cfg.HubURL = "https://team-hub.example.com"
		if c := newClient(cfg); c.token != hubLiteral || c.authErr != nil {
			t.Fatalf("a deliberately configured token must reach its hub: %q %v", c.token, c.authErr)
		}
		cfg.Token = ""
		c := newClient(cfg)
		if c.token != "" || c.authErr == nil {
			t.Fatalf("the minted admin token leaked to a remote hub: %q", c.token)
		}
		if strings.Contains(c.authErr.Error(), fakeAdmin) {
			t.Fatal("the error text contains a token value")
		}
	})
}

// A secret in a file anyone can read is not a secret. Refuse it, say so, and
// never print the value.
func TestLiteralTokenRequires0600(t *testing.T) {
	t.Run("group/world-readable file with a token is refused", func(t *testing.T) {
		dir := t.TempDir()
		path := writeSkillJSON(t, dir, skillLiteral, 0o644)
		sc, warn := loadSkillConfigWarn(path)
		if sc.Token != "" {
			t.Fatalf("a world-readable token was used: %q", sc.Token)
		}
		if warn == "" || !strings.Contains(warn, path) || !strings.Contains(warn, "chmod 600") {
			t.Fatalf("warning must name the file and the fix: %q", warn)
		}
		if strings.Contains(warn, skillLiteral) {
			t.Fatal("the warning printed the token value")
		}
	})

	t.Run("0600 file with a token is used", func(t *testing.T) {
		dir := t.TempDir()
		path := writeSkillJSON(t, dir, skillLiteral, 0o600)
		sc, warn := loadSkillConfigWarn(path)
		if sc.Token != skillLiteral || warn != "" {
			t.Fatalf("a 0600 token must be used: %q warn=%q", sc.Token, warn)
		}
	})

	t.Run("a readable file WITHOUT a token still works", func(t *testing.T) {
		dir := t.TempDir()
		path := writeSkillJSON(t, dir, "", 0o644)
		sc, warn := loadSkillConfigWarn(path)
		if warn != "" {
			t.Fatalf("a fresh install must never warn: %q", warn)
		}
		if sc.Token != "" {
			t.Fatalf("token should be empty: %q", sc.Token)
		}
	})

	t.Run("the same rule guards workwire.json", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "workwire.json")
		if err := os.WriteFile(path, []byte(`{"token":"`+hubLiteral+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WORKWIRE_CONFIG_DIR", dir)
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Token != "" {
			t.Fatalf("a world-readable hub token was used: %q", cfg.Token)
		}
		if !strings.Contains(cfg.TokenWarning, "chmod 600") || strings.Contains(cfg.TokenWarning, hubLiteral) {
			t.Fatalf("bad warning: %q", cfg.TokenWarning)
		}
	})
}

// Both config files are created 0600 and with an EMPTY token: nothing ever
// copies a secret into them.
func TestConfigFilesAreCreated0600AndEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WORKWIRE_CONFIG_DIR", dir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	hubPath := filepath.Join(dir, "workwire.json")
	fi, err := os.Stat(hubPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("workwire.json mode %04o, want 0600", fi.Mode().Perm())
	}
	b, err := os.ReadFile(hubPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"token": ""`) {
		t.Fatalf("auto-created config must carry an EMPTY token key:\n%s", b)
	}

	created, err := ensureSkillConfig(skillConfigPath(cfg))
	if err != nil || !created {
		t.Fatalf("ensure skill.json: created=%v err=%v", created, err)
	}
	fi, err = os.Stat(skillConfigPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("skill.json mode %04o, want 0600", fi.Mode().Perm())
	}
	sb, err := os.ReadFile(skillConfigPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sb), `"token": ""`) {
		t.Fatalf("skill.json must carry an EMPTY token key:\n%s", sb)
	}
	// Nothing may ever copy the minted admin token into a config file.
	seedAdminToken(t, dir)
	for _, p := range []string{hubPath, skillConfigPath(cfg)} {
		c, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(c), fakeAdmin) {
			t.Fatalf("%s was auto-populated with a secret", p)
		}
	}
}
