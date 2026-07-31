package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildWorkwire builds the real binary once per test run. The in-process test
// calls cmdSessionStart directly and therefore structurally cannot traverse
// main's config loader — which is exactly where the "always exits 0" property
// was being broken. Only executing the binary proves it.
func buildWorkwire(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "workwire")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build workwire: %v", err)
	}
	return bin
}

// runSessionStart executes `workwire session-start` against a scratch config
// dir and returns its exit code and combined output.
func runSessionStart(t *testing.T, bin, configDir, wd string) (int, string, time.Duration) {
	t.Helper()
	cmd := exec.Command(bin, "session-start")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(),
		"WORKWIRE_CONFIG_DIR="+configDir,
		// Never touch the machine's real hub: nothing here should reach a
		// network, and if anything tries, it must fail closed and fast.
		"WORKWIRE_HUB_URL=http://127.0.0.1:1",
	)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(start)
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run session-start: %v", err)
		}
	}
	return code, string(out), took
}

func asExitError(err error, dst **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*dst = ee
		return true
	}
	return false
}

// The hook can never block or fail a session start. Not on a corrupt config,
// not on a missing one, not on a config dir it cannot read.
func TestSessionStartBinaryAlwaysExitsZero(t *testing.T) {
	bin := buildWorkwire(t)
	wd := t.TempDir()

	cases := []struct {
		name  string
		setup func(t *testing.T) string // returns the config dir
	}{
		{"missing config", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "does-not-exist-yet")
		}},
		{"corrupt config", func(t *testing.T) string {
			d := t.TempDir()
			if err := os.WriteFile(filepath.Join(d, "workwire.json"), []byte(`{ "lastMessages": 8,, }`), 0o644); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"corrupt config with auto-join ON", func(t *testing.T) string {
			d := t.TempDir()
			if err := os.WriteFile(filepath.Join(d, "workwire.json"), []byte(`not json at all`), 0o644); err != nil {
				t.Fatal(err)
			}
			// Auto-join on means the hook reaches the spawn path, and the child
			// re-reads the same unreadable file.
			if err := os.WriteFile(filepath.Join(d, "skill.json"), []byte(`{"autoJoin":true,"agentName":"stress-corrupt-cfg"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"unreadable config dir", func(t *testing.T) string {
			if os.Geteuid() == 0 {
				t.Skip("root reads everything")
			}
			d := t.TempDir()
			if err := os.Chmod(d, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
			return d
		}},
		{"corrupt skill.json", func(t *testing.T) string {
			d := t.TempDir()
			if err := os.WriteFile(filepath.Join(d, "skill.json"), []byte(`{{{`), 0o644); err != nil {
				t.Fatal(err)
			}
			return d
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(tc.name, "unreadable") {
				t.Skip("POSIX mode bits")
			}
			dir := tc.setup(t)
			code, out, took := runSessionStart(t, bin, dir, wd)
			if code != 0 {
				t.Fatalf("session-start exited %d (must always be 0)\n%s", code, out)
			}
			// "and fast": the hook is on the session's start path.
			if took > 5*time.Second {
				t.Fatalf("session-start took %s — the hook must return immediately", took)
			}
		})
	}
}

// The spawned listener re-reads the same config, so tolerance has to reach it
// too: a green exit code with a dead auto-join is the failure this guards.
func TestSessionStartPropagatesToleranceToTheChild(t *testing.T) {
	bin := buildWorkwire(t)
	wd := t.TempDir()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "workwire.json"), []byte(`{ oops`), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "workwire-stress-child"
	if err := os.WriteFile(filepath.Join(d, "skill.json"),
		[]byte(`{"autoJoin":true,"agentName":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runSessionStart(t, bin, d, wd)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "auto-joined as "+name) {
		t.Fatalf("auto-join did not happen behind a green exit code: %q", out)
	}
	// The child is detached; give it a moment, then stop it and check it did
	// not die on the same corrupt file.
	deadline := time.Now().Add(5 * time.Second)
	logPath := filepath.Join(d, "auto-join.log")
	var log string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logPath); err == nil {
			log = string(b)
			if strings.Contains(log, "workwire listen:") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopListener(t, name)
	// A child that died on the loader prints main's fatal form ("workwire:
	// parse <path>"); the parent's own advisory line is a different shape.
	if strings.Contains(log, "workwire: parse ") {
		t.Fatalf("the spawned listener died on the corrupt config:\n%s", log)
	}
	if !strings.Contains(log, "workwire listen:") {
		t.Fatalf("the spawned listener never logged anything:\n%s", log)
	}
}

// stopListener kills only the listener this test started, by its unique name.
func stopListener(t *testing.T, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	out, err := exec.Command("pgrep", "-f", "listen --agent "+name).Output()
	if err != nil {
		return
	}
	for _, pid := range strings.Fields(string(out)) {
		_ = exec.Command("kill", pid).Run()
	}
}
