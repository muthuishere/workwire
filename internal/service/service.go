// Package service installs the workwire hub as a per-user background service
// (launchd on darwin, systemd --user on linux, sc.exe on windows).
//
// This is OPTIONAL by design (ADR-001: no daemon install ceremony). The default
// path stays "run `workwire serve`, or let a loopback peer auto-start it";
// `workwire install --service` is only for people who want the hub to survive
// logout/reboot. Nothing else in workwire reads or requires the service.
package service

import (
	"os"
	"path/filepath"
	"strings"
)

// Spec is everything a backend needs to render and register a service.
// It carries no secrets: the service inherits nothing sensitive and reads its
// own config from ConfigDir at start (auth R2).
type Spec struct {
	// Label is the OS-level service identifier.
	Label string
	// BinPath is the absolute, symlink-resolved path to the workwire binary.
	BinPath string
	// Args are the arguments after the binary (normally just "serve").
	Args []string
	// ConfigDir is workwire's config dir; also where logs are written.
	ConfigDir string
	// WorkingDir is the service working directory (usually the home dir).
	WorkingDir string
}

// Default label values, shared with the docs and the uninstall path.
const (
	DarwinLabel  = "com.workwire.hub"
	LinuxUnit    = "workwire"
	WindowsSvc   = "WorkwireHub"
	logFileName  = "hub.log"
	errFileName  = "hub.err.log"
	uninstallFmt = "removed %s\n"
)

// LogPath is where the service's stdout is written.
func (s Spec) LogPath() string { return filepath.Join(s.ConfigDir, logFileName) }

// ErrLogPath is where the service's stderr is written.
func (s Spec) ErrLogPath() string { return filepath.Join(s.ConfigDir, errFileName) }

// Backend is a per-OS service manager. Install must be idempotent: re-running
// replaces the unit/plist and reloads it.
type Backend interface {
	// Name is the human-facing service name (label / unit / service name).
	Name() string
	// Install writes the service definition and starts it.
	Install(Spec) error
	// Uninstall stops, disables and removes the service definition. It must
	// never touch the data dir.
	Uninstall(Spec) error
	// Status returns a one-line human-readable state.
	Status(Spec) (string, error)
	// Hint is an optional post-install note (empty when there is nothing to say).
	Hint() string
}

// ResolveBinary returns the absolute, symlink-resolved path of the running
// binary — services must not depend on PATH or on a symlink that may move.
func ResolveBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// NewSpec builds the default spec for this host.
func NewSpec(configDir string) (Spec, error) {
	bin, err := ResolveBinary()
	if err != nil {
		return Spec{}, err
	}
	home, _ := os.UserHomeDir()
	return Spec{
		Label:      defaultLabel,
		BinPath:    bin,
		Args:       []string{"serve"},
		ConfigDir:  configDir,
		WorkingDir: home,
	}, nil
}

// xmlEscape escapes the few characters that matter inside a plist string.
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
