//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const defaultLabel = WindowsSvc

// New returns the sc.exe backend.
func New() Backend { return scService{} }

type scService struct{}

func (scService) Name() string { return WindowsSvc }

// elevationHint is returned instead of half-installing when sc.exe refuses for
// lack of privileges (error 5, access denied).
func elevationHint(s Spec) error {
	return fmt.Errorf("access denied: creating a Windows service needs an elevated shell.\n"+
		"Open PowerShell or cmd as Administrator and run:\n"+
		"  sc.exe create %s binPath= \"%s\" start= auto\n"+
		"  sc.exe start %s", s.Label, WindowsBinPath(s), s.Label)
}

func (scService) Install(s Spec) error {
	if err := os.MkdirAll(s.ConfigDir, 0o755); err != nil {
		return err
	}
	// Idempotent: drop any previous definition first.
	_ = run("sc.exe", "stop", s.Label)
	_ = run("sc.exe", "delete", s.Label)
	if err := run("sc.exe", "create", s.Label, "binPath= "+WindowsBinPath(s), "start= auto"); err != nil {
		if isAccessDenied(err) {
			return elevationHint(s)
		}
		return err
	}
	if err := run("sc.exe", "start", s.Label); err != nil {
		if isAccessDenied(err) {
			return elevationHint(s)
		}
		return err
	}
	return nil
}

func (scService) Uninstall(s Spec) error {
	_ = run("sc.exe", "stop", s.Label)
	if err := run("sc.exe", "delete", s.Label); err != nil {
		if isAccessDenied(err) {
			return elevationHint(s)
		}
		return err
	}
	return nil
}

func (scService) Status(s Spec) (string, error) {
	out, err := exec.Command("sc.exe", "query", s.Label).CombinedOutput()
	if err != nil {
		return "not installed", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "STATE") {
			return strings.TrimSpace(line), nil
		}
	}
	return "installed", nil
}

func isAccessDenied(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "access is denied") || strings.Contains(s, "error 5")
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

// Hint: sc.exe services are auto-start already; nothing extra to say.
func (scService) Hint() string { return "" }
