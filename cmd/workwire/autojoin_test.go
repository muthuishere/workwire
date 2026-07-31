package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
)

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

// The hook is merged into whatever the user already configured — a settings
// file is somebody's own setup, not ours to rewrite.
func TestInstallAutoJoinMergesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	existing := `{
  "model": "opus",
  "hooks": {
    "SessionStart": [{"hooks":[{"type":"command","command":"echo mine"}]}],
    "PostToolUse": [{"matcher":"Write","hooks":[{"type":"command","command":"prettier"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installAutoJoin(path); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := readJSON(t, path)
	if s["model"] != "opus" {
		t.Fatal("unrelated settings were dropped")
	}
	hooks := s["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("another event's hooks were dropped")
	}
	list := hooks["SessionStart"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected the user's entry plus ours, got %d", len(list))
	}
	if !strings.Contains(string(first), autoJoinCommand) {
		t.Fatalf("hook command not written: %s", first)
	}

	// Re-running install must not stack a second copy.
	if err := installAutoJoin(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("install is not idempotent:\n%s\n---\n%s", first, second)
	}
}

// Uninstall removes exactly our entry — nothing else, ever.
func TestUninstallAutoJoinRemovesOnlyOurs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{
  "hooks": {
    "SessionStart": [{"hooks":[{"type":"command","command":"echo mine"}]}]
  }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installAutoJoin(path); err != nil {
		t.Fatal(err)
	}
	removed, err := uninstallAutoJoin(path)
	if err != nil || !removed {
		t.Fatalf("uninstall: removed=%v err=%v", removed, err)
	}
	s := readJSON(t, path)
	list := s["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(list) != 1 {
		t.Fatalf("user hook not preserved: %+v", list)
	}
	if strings.Contains(string(mustRead(t, path)), autoJoinCommand) {
		t.Fatal("our hook survived uninstall")
	}
	// Removing twice is a no-op, not an error.
	if removed, err := uninstallAutoJoin(path); err != nil || removed {
		t.Fatalf("second uninstall: removed=%v err=%v", removed, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// skill.json is created once, auto-join ON, and never overwritten: a
// deliberate `--off` must survive a re-install.
func TestSkillConfigCreatedOnceAndTogglesOneKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skill.json")

	created, err := ensureSkillConfig(path)
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	if !loadSkillConfig(path).AutoJoin {
		t.Fatal("auto-join should default to on")
	}

	// A user edit plus an unknown key, then a re-install.
	if err := os.WriteFile(path, []byte(`{"autoJoin":false,"agentName":"api","future":"keep me"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = ensureSkillConfig(path)
	if err != nil || created {
		t.Fatalf("re-install must not recreate: created=%v err=%v", created, err)
	}
	if sc := loadSkillConfig(path); sc.AutoJoin || sc.AgentName != "api" {
		t.Fatalf("re-install clobbered the config: %+v", sc)
	}

	// --on flips exactly one key.
	if err := setAutoJoin(path, true); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	if m["autoJoin"] != true || m["agentName"] != "api" || m["future"] != "keep me" {
		t.Fatalf("toggle rewrote more than autoJoin: %+v", m)
	}
	if err := setAutoJoin(path, false); err != nil {
		t.Fatal(err)
	}
	if loadSkillConfig(path).AutoJoin {
		t.Fatal("--off did not stick")
	}
}

// The hook entrypoint must always exit 0 and fast: a session the user did not
// ask to join must never be blocked or broken by us.
func TestSessionStartAlwaysSucceeds(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := config.Config{ConfigDir: cfgDir, HubURL: "http://127.0.0.1:1"} // nothing listening

	t.Run("off does nothing", func(t *testing.T) {
		if err := setAutoJoin(skillConfigPath(cfg), false); err != nil {
			t.Fatal(err)
		}
		if err := cmdSessionStart(cfg, nil); err != nil {
			t.Fatalf("session-start failed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(cfgDir, "run")); err == nil {
			t.Fatal("auto-join off still touched the run dir")
		}
	})

	t.Run("adopts quietly when the folder already has a listener", func(t *testing.T) {
		if err := setAutoJoin(skillConfigPath(cfg), true); err != nil {
			t.Fatal(err)
		}
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(wd)
		lock, err := listen.AcquireLock(filepath.Join(cfgDir, "run"), name)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		if err := cmdSessionStart(cfg, nil); err != nil {
			t.Fatalf("adopting must not be an error: %v", err)
		}
	})

	t.Run("hub unreachable is still exit 0", func(t *testing.T) {
		// The lock is free here, so this reaches the spawn: the hook does not
		// probe the hub at all, and the listener it starts retries by itself.
		if err := setAutoJoin(skillConfigPath(cfg), true); err != nil {
			t.Fatal(err)
		}
		spawned := 0
		orig := spawnListener
		spawnListener = func(config.Config, string, string) { spawned++ }
		defer func() { spawnListener = orig }()
		if err := cmdSessionStart(cfg, nil); err != nil {
			t.Fatalf("session-start failed with no hub: %v", err)
		}
		if spawned != 1 {
			t.Fatalf("expected one detached listener, got %d", spawned)
		}
	})
}

// "One guy is enough": a second `workwire listen` for the same folder adopts
// the running one and exits 0. Auto-join fires in every session, so this must
// read as normal, not as a failure.
func TestSecondListenerAdoptsInsteadOfFailing(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := config.Config{ConfigDir: cfgDir, HubURL: "http://127.0.0.1:1"}
	lock, err := listen.AcquireLock(filepath.Join(cfgDir, "run"), "peer")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := cmdListen(cfg, []string{"--agent", "peer"}); err != nil {
		t.Fatalf("second listener should adopt and exit 0, got: %v", err)
	}
}
