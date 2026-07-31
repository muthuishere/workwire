package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSpec() Spec {
	return Spec{
		Label:      "com.workwire.hub",
		BinPath:    "/usr/local/bin/workwire",
		Args:       []string{"serve"},
		ConfigDir:  "/home/dev/.config/workwire",
		WorkingDir: "/home/dev",
	}
}

func TestRenderLaunchdPlist(t *testing.T) {
	got := RenderLaunchdPlist(testSpec())
	want := []string{
		"<key>Label</key>\n\t<string>com.workwire.hub</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>/usr/local/bin/workwire</string>\n\t\t<string>serve</string>\n\t</array>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>StandardOutPath</key>\n\t<string>/home/dev/.config/workwire/hub.log</string>",
		"<key>StandardErrorPath</key>\n\t<string>/home/dev/.config/workwire/hub.err.log</string>",
		"<key>WorkingDirectory</key>\n\t<string>/home/dev</string>",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("plist missing %q\n---\n%s", w, got)
		}
	}
	if !strings.HasPrefix(got, "<?xml version=\"1.0\"") {
		t.Fatalf("plist must start with the xml prolog:\n%s", got)
	}
}

func TestRenderLaunchdPlistEscapesXML(t *testing.T) {
	s := testSpec()
	s.BinPath = "/opt/a&b/work<wire>"
	got := RenderLaunchdPlist(s)
	if !strings.Contains(got, "<string>/opt/a&amp;b/work&lt;wire&gt;</string>") {
		t.Fatalf("plist did not escape the binary path:\n%s", got)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	got := RenderSystemdUnit(testSpec())
	want := []string{
		"[Unit]",
		"Description=workwire hub",
		"ExecStart=/usr/local/bin/workwire serve",
		"WorkingDirectory=/home/dev",
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("unit missing %q\n---\n%s", w, got)
		}
	}
}

func TestRenderQuotesSpacedPaths(t *testing.T) {
	s := testSpec()
	s.BinPath = "/Users/dev/Application Support/workwire"
	if want := `ExecStart="/Users/dev/Application Support/workwire" serve`; !strings.Contains(RenderSystemdUnit(s), want) {
		t.Fatalf("unit did not quote the spaced path: %s", RenderSystemdUnit(s))
	}
	if got, want := WindowsBinPath(s), `"/Users/dev/Application Support/workwire" serve`; got != want {
		t.Fatalf("WindowsBinPath = %q want %q", got, want)
	}
}

func TestWindowsBinPath(t *testing.T) {
	s := testSpec()
	s.BinPath = `C:\Program.Files\workwire.exe`
	if got, want := WindowsBinPath(s), `C:\Program.Files\workwire.exe serve`; got != want {
		t.Fatalf("WindowsBinPath = %q want %q", got, want)
	}
}

// Re-rendering the same spec must be byte-identical: that determinism is what
// makes `install --service` idempotent.
func TestRenderIsDeterministic(t *testing.T) {
	s := testSpec()
	if RenderLaunchdPlist(s) != RenderLaunchdPlist(s) {
		t.Fatal("plist render not deterministic")
	}
	if RenderSystemdUnit(s) != RenderSystemdUnit(s) {
		t.Fatal("unit render not deterministic")
	}
}

func TestResolveBinaryIsAbsoluteAndReal(t *testing.T) {
	p, err := ResolveBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("ResolveBinary not absolute: %s", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("ResolveBinary does not exist: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil && resolved != p {
		t.Fatalf("ResolveBinary left a symlink: %s -> %s", p, resolved)
	}
}

func TestNewSpecUsesServeAndConfigDir(t *testing.T) {
	s, err := NewSpec("/tmp/wwcfg")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Args) != 1 || s.Args[0] != "serve" {
		t.Fatalf("spec args = %v want [serve]", s.Args)
	}
	if s.ConfigDir != "/tmp/wwcfg" {
		t.Fatalf("spec config dir = %s", s.ConfigDir)
	}
	if s.Label == "" || !filepath.IsAbs(s.BinPath) {
		t.Fatalf("bad spec: %+v", s)
	}
	if s.LogPath() != "/tmp/wwcfg/hub.log" || s.ErrLogPath() != "/tmp/wwcfg/hub.err.log" {
		t.Fatalf("bad log paths: %s %s", s.LogPath(), s.ErrLogPath())
	}
}

// The backend for this OS must exist and name itself; no real install happens.
func TestBackendForThisOS(t *testing.T) {
	if n := New().Name(); n == "" {
		t.Fatal("backend has no name")
	}
}
