package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/origin"
)

// The name a session joins under is an identity claim, so the verb, the
// listener and skill.json must never disagree about it.
func TestNamePrecedenceAndDerivation(t *testing.T) {
	cfgDir := t.TempDir()
	cfg := config.Config{ConfigDir: cfgDir}

	tree := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", tree},
	} {
		c := exec.Command("git", args...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}

	// Derived: the tree decides, not the folder alone.
	if got, want := origin.DeriveName(tree), "api-main"; got != want {
		t.Fatalf("derived %q, want %q", got, want)
	}

	// skill.json pins the name above the derivation...
	if err := os.WriteFile(skillConfigPath(cfg), []byte(`{"agentName":"pinned"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSkillConfig(skillConfigPath(cfg)).AgentName; got != "pinned" {
		t.Fatalf("skill.json agentName = %q, want pinned", got)
	}

	// ...and an empty agentName falls through to the derivation rather than
	// joining under an empty name.
	if err := os.WriteFile(skillConfigPath(cfg), []byte(`{"agentName":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadSkillConfig(skillConfigPath(cfg)).AgentName; got != "" {
		t.Fatalf("empty agentName = %q, want empty", got)
	}
	if got := origin.DeriveName(tree); got == "" {
		t.Fatal("derivation returned an empty name")
	}
}
