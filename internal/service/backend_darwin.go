//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultLabel = DarwinLabel

// New returns the launchd backend.
func New() Backend { return launchd{} }

type launchd struct{}

func (launchd) Name() string { return DarwinLabel }

func plistPath(s Spec) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", s.Label+".plist"), nil
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func (l launchd) Install(s Spec) error {
	p, err := plistPath(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.ConfigDir, 0o755); err != nil {
		return err
	}
	// Idempotent: tear any previous registration down before rewriting.
	_ = run("launchctl", "bootout", domain()+"/"+s.Label)
	if err := os.WriteFile(p, []byte(RenderLaunchdPlist(s)), 0o644); err != nil {
		return err
	}
	if err := run("launchctl", "bootstrap", domain(), p); err != nil {
		// Older macOS (and some sandboxed shells) only speak load -w.
		if err2 := run("launchctl", "load", "-w", p); err2 != nil {
			return fmt.Errorf("launchctl bootstrap failed (%v) and load -w failed (%v)", err, err2)
		}
	}
	if err := run("launchctl", "kickstart", "-k", domain()+"/"+s.Label); err != nil {
		// kickstart is best-effort: RunAtLoad already started it.
		_ = err
	}
	return nil
}

func (l launchd) Uninstall(s Spec) error {
	p, err := plistPath(s)
	if err != nil {
		return err
	}
	bootoutErr := run("launchctl", "bootout", domain()+"/"+s.Label)
	if bootoutErr != nil {
		_ = run("launchctl", "unload", "-w", p)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l launchd) Status(s Spec) (string, error) {
	out, err := exec.Command("launchctl", "print", domain()+"/"+s.Label).CombinedOutput()
	if err != nil {
		return "not loaded", err
	}
	state, pid := "loaded", ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state = ") {
			state = strings.TrimPrefix(line, "state = ")
		}
		if strings.HasPrefix(line, "pid = ") {
			pid = strings.TrimPrefix(line, "pid = ")
		}
	}
	if pid != "" {
		return fmt.Sprintf("%s (pid %s)", state, pid), nil
	}
	return state, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}

// Hint: launchd user agents start at login already; nothing extra to say.
func (launchd) Hint() string { return "" }
