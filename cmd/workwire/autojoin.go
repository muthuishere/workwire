package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
)

// Auto-join: nobody should have to say a phrase to be on the wire. A skill
// cannot fire itself — it waits for a trigger phrase — so the session joins
// its own folder from a harness **SessionStart hook** instead.
//
// The hook is one verb, `workwire session-start`: no shell logic in
// settings.json, so the behaviour lives here where it can be tested and
// changed. It always exits 0 and fast — a session the user did not ask to
// join must never look broken, or slow, because of us.
const (
	// autoJoinMarker identifies OUR hook entry, so uninstall removes exactly
	// it and re-install replaces it instead of stacking duplicates.
	autoJoinMarker = "workwire session-start"

	// autoJoinCommand is the whole hook.
	autoJoinCommand = autoJoinMarker

	// skillConfigName is the CLIENT-side config. The hub's config
	// (workwire.json) is a different thing with a different lifecycle; mixing
	// them would tie a session's join preference to how the hub is served.
	skillConfigName = "skill.json"
)

// skillConfig is ~/.config/workwire/skill.json.
type skillConfig struct {
	// AutoJoin is on by default: the point is that a session is reachable
	// without anyone doing anything.
	AutoJoin bool `json:"autoJoin"`
	// AgentName pins the peer name; empty means "derive from the folder".
	AgentName string `json:"agentName"`
	// HubURL overrides the hub for this client; empty means the hub config.
	HubURL string `json:"hubUrl"`
	// TokenEnv names (never holds) the env var carrying a bearer token for a
	// remote hub (ADR-013). A secret value never lives in a config file.
	TokenEnv string `json:"tokenEnv"`
}

func skillConfigPath(cfg config.Config) string {
	return filepath.Join(cfg.ConfigDir, skillConfigName)
}

// loadSkillConfig reads skill.json. A missing OR corrupt file is not an error
// anywhere this is used: the caller is a session-start hook, and the honest
// default is "on".
func loadSkillConfig(path string) skillConfig {
	sc := skillConfig{AutoJoin: true}
	b, err := os.ReadFile(path)
	if err != nil {
		return sc
	}
	_ = json.Unmarshal(b, &sc)
	return sc
}

// ensureSkillConfig creates skill.json with auto-join ON when it does not
// exist. An existing file is NEVER overwritten — a re-install must not undo
// somebody's `--off`.
func ensureSkillConfig(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	b, _ := json.MarshalIndent(skillConfig{AutoJoin: true}, "", "  ")
	return true, os.WriteFile(path, append(b, '\n'), 0o644)
}

// setAutoJoin flips exactly one key, preserving every other key in the file —
// including ones this version does not know about. Flipping the toggle
// rewrites neither the skill files nor the hook, so it is instant and the hook
// can stay installed forever.
func setAutoJoin(path string, on bool) error {
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	raw["autoJoin"] = on
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// --- the SessionStart hook entry in the harness settings file ---

// autoJoinSettingsPath is the harness settings file the hook is written into
// (Claude Code: ~/.claude/settings.json).
func autoJoinSettingsPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w (use --settings)", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// hookEntry is our SessionStart entry, kept as plain maps so every unknown key
// in the user's settings file survives a round trip untouched.
func hookEntry() map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type": "command",
				// async: session start must never wait on us.
				"command": autoJoinCommand,
				"async":   true,
				"timeout": 10,
			},
		},
	}
}

// readSettings loads a settings file; a missing file is an empty object.
func readSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	s := map[string]any{}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix the file, or pass --settings)", path, err)
	}
	return s, nil
}

func writeSettings(path string, s map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isOurs reports whether one hook item is the workwire auto-join hook.
func isOurs(item any) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	return cmd != "" && strings.Contains(cmd, autoJoinMarker)
}

// stripAutoJoin removes our hook from a SessionStart list, dropping any entry
// left with no hooks. Everything else the user configured is untouched.
func stripAutoJoin(list []any) ([]any, bool) {
	removed := false
	out := make([]any, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			out = append(out, e)
			continue
		}
		items, ok := m["hooks"].([]any)
		if !ok {
			out = append(out, e)
			continue
		}
		kept := make([]any, 0, len(items))
		for _, it := range items {
			if isOurs(it) {
				removed = true
				continue
			}
			kept = append(kept, it)
		}
		if len(kept) == 0 {
			continue // the entry existed only for us
		}
		m["hooks"] = kept
		out = append(out, m)
	}
	return out, removed
}

// installAutoJoin merges the auto-join hook into the harness settings file.
// Idempotent: our entry is replaced, never duplicated, and every other hook
// the user configured is preserved exactly.
func installAutoJoin(path string) error {
	s, err := readSettings(path)
	if err != nil {
		return err
	}
	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	list, _ := hooks["SessionStart"].([]any)
	list, _ = stripAutoJoin(list)
	hooks["SessionStart"] = append(list, hookEntry())
	s["hooks"] = hooks
	return writeSettings(path, s)
}

// uninstallAutoJoin removes our SessionStart hook and nothing else.
func uninstallAutoJoin(path string) (bool, error) {
	s, err := readSettings(path)
	if err != nil {
		return false, err
	}
	hooks, _ := s["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	list, _ := hooks["SessionStart"].([]any)
	list, removed := stripAutoJoin(list)
	if !removed {
		return false, nil
	}
	if len(list) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = list
	}
	if len(hooks) == 0 {
		delete(s, "hooks")
	} else {
		s["hooks"] = hooks
	}
	return true, writeSettings(path, s)
}

// --- the hook entrypoint ---

// cmdSessionStart is what the SessionStart hook runs. It never fails, never
// blocks, and never talks to the hub itself: it starts (or adopts) the
// detached listener for this folder and returns immediately.
func cmdSessionStart(cfg config.Config, args []string) error {
	dir, err := os.Getwd()
	if err != nil {
		return nil // nothing sane to join; a hook must not fail
	}
	sc := loadSkillConfig(skillConfigPath(cfg))
	if !sc.AutoJoin {
		return nil // deliberately off — say nothing
	}
	name := sc.AgentName
	if name == "" {
		name = filepath.Base(dir)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil
	}
	// One listener per folder is enough. The flock is the authority; if it is
	// held, the running listener already covers this folder and this session
	// is a passenger — it can ask and take part, it just is not the answerer.
	if cfg.ConfigDir != "" {
		lock, lerr := listen.AcquireLock(filepath.Join(cfg.ConfigDir, "run"), name)
		if lerr != nil {
			if _, held := lerr.(listen.ErrLocked); held {
				fmt.Printf("workwire: adopting the running listener for %s\n", name)
			}
			return nil
		}
		lock.Release() // free it for the listener we are about to start
	}
	spawnListener(cfg, name, dir)
	return nil
}

// spawnListener is a seam: tests must never actually fork a listener out of
// the test binary.
var spawnListener = startDetachedListener

// startDetachedListener spawns `workwire listen` in the background with its
// output in the config dir. A failure to spawn is deliberately silent: the
// session did not ask for this, so it must not be interrupted by it.
func startDetachedListener(cfg config.Config, name, dir string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "listen", "--agent", name, "--dir", dir)
	cmd.Dir = dir
	if cfg.ConfigDir != "" {
		if f, err := os.OpenFile(filepath.Join(cfg.ConfigDir, "auto-join.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			defer f.Close()
		}
	}
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	// Do not Wait: the listener outlives this process by design.
	fmt.Printf("workwire: auto-joined as %s (listening in the background)\n", name)
}
