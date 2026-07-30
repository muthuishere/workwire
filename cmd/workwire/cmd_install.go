package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/muthuishere/workwire/internal/config"
)

// The two-way agent skill payload is compiled into the binary (ADR-003,
// agent-skill R1): install needs no network and touches no runtime state.
//
//go:embed skills/workwire
var skillFS embed.FS

// cmdInstall writes the embedded skill into the harness skills directory.
// Idempotent: re-install replaces skill files only — credentials, cursors,
// and session inbox files are never touched.
func cmdInstall(cfg config.Config, args []string) error {
	fs_ := flag.NewFlagSet("install", flag.ExitOnError)
	skills := fs_.Bool("skills", false, "install the two-way agent skill")
	dir := fs_.String("dir", "", "skills directory override (default ~/.claude/skills)")
	fs_.Parse(args)
	if !*skills {
		fmt.Fprint(os.Stderr, "usage: workwire install --skills [--dir <skills-dir>]\n\ninstalls the two-way agent skill into the harness skills directory (default ~/.claude/skills/workwire)\n")
		return fmt.Errorf("install requires --skills")
	}
	target := *dir
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w (use --dir)", err)
		}
		target = filepath.Join(home, ".claude", "skills")
	}
	written, err := writeSkill(target)
	if err != nil {
		return err
	}
	for _, p := range written {
		fmt.Printf("installed %s\n", p)
	}
	fmt.Printf("skill ready: invoke it in a session, or start manually with `workwire listen --agent <name>`\n")
	fmt.Printf("config: %s\n", filepath.Join(cfg.ConfigDir, "workwire.json"))
	return nil
}

// writeSkill copies skills/workwire/** from the embedded FS under target.
func writeSkill(target string) ([]string, error) {
	var written []string
	err := fs.WalkDir(skillFS, "skills/workwire", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("skills", path)
		dst := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
		written = append(written, dst)
		return nil
	})
	return written, err
}
