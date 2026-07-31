//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultLabel = LinuxUnit

// New returns the systemd --user backend.
func New() Backend { return systemd{} }

type systemd struct{}

func (systemd) Name() string { return LinuxUnit + ".service" }

func unitPath(s Spec) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", s.Label+".service"), nil
}

func requireSystemd() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found on PATH: this host has no systemd, so --service cannot install a unit. " +
			"Run `workwire serve` under your own supervisor (docker, supervisord, an init script) instead")
	}
	return nil
}

func (systemd) Install(s Spec) error {
	if err := requireSystemd(); err != nil {
		return err
	}
	p, err := unitPath(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(s.ConfigDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(RenderSystemdUnit(s)), 0o644); err != nil {
		return err
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	// enable --now is idempotent; it re-execs a running unit's new definition
	// only after an explicit restart, so ask for one.
	if err := run("systemctl", "--user", "enable", "--now", s.Label); err != nil {
		return err
	}
	return run("systemctl", "--user", "restart", s.Label)
}

func (systemd) Uninstall(s Spec) error {
	if err := requireSystemd(); err != nil {
		return err
	}
	p, err := unitPath(s)
	if err != nil {
		return err
	}
	_ = run("systemctl", "--user", "disable", "--now", s.Label)
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return run("systemctl", "--user", "daemon-reload")
}

func (systemd) Status(s Spec) (string, error) {
	if err := requireSystemd(); err != nil {
		return "", err
	}
	out, err := exec.Command("systemctl", "--user", "is-active", s.Label).CombinedOutput()
	state := strings.TrimSpace(string(out))
	if state == "" {
		state = "unknown"
	}
	if err != nil {
		return state, err
	}
	return state, nil
}

// Hint is printed after install: without linger the user unit only runs
// while the user has a login session.
func (systemd) Hint() string {
	u := os.Getenv("USER")
	if u == "" {
		u = "$USER"
	}
	return fmt.Sprintf("for boot-start without a login session: loginctl enable-linger %s", u)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}
