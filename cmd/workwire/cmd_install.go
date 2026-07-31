package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/service"
)

// The two-way agent skill payload is compiled into the binary (ADR-003,
// agent-skill R1): install needs no network and touches no runtime state.
//
//go:embed skills/workwire
var skillFS embed.FS

const installUsage = `usage: workwire install [--service] [--skills] [--all] [--dir <skills-dir>]

  --skills    install the two-way agent skill (default ~/.claude/skills/workwire)
  --service   run the hub as a background service (launchd / systemd --user / sc.exe)
  --all       both of the above

The recommended setup is ` + "`--service --skills`" + `.

A repo opts into joining by saying so in its OWN CLAUDE.md / AGENTS.md — the harness
reads that file every session, so the opt-in is per-repo, in version control, and
never reaches a repo that did not ask for it.

The service is OPTIONAL (ADR-001): without it the hub still auto-starts on
loopback or runs in the foreground with ` + "`workwire serve`" + `.
`

// installFlags is the parsed shape of `workwire install ...`, split out so the
// flag semantics are unit-testable without touching the real system.
type installFlags struct {
	skills  bool
	service bool
	dir     string
}

func parseInstallFlags(args []string) (installFlags, error) {
	fs_ := flag.NewFlagSet("install", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	skills := fs_.Bool("skills", false, "install the two-way agent skill")
	svc := fs_.Bool("service", false, "install the hub as a background service")
	all := fs_.Bool("all", false, "install the service and the skill")
	dir := fs_.String("dir", "", "skills directory override (default ~/.claude/skills)")
	if err := fs_.Parse(args); err != nil {
		return installFlags{}, err
	}
	f := installFlags{skills: *skills || *all, service: *svc || *all, dir: *dir}
	if !f.skills && !f.service {
		return f, fmt.Errorf("install requires --skills, --service or --all")
	}
	return f, nil
}

// cmdInstall installs the agent skill and/or the background service.
// Idempotent: re-install replaces skill files and re-registers the service —
// credentials, cursors, session inbox files and the data dir are never touched.
func cmdInstall(cfg config.Config, args []string) error {
	f, err := parseInstallFlags(args)
	if err != nil {
		fmt.Fprint(os.Stderr, installUsage)
		return err
	}
	if f.service {
		if err := installService(cfg); err != nil {
			return err
		}
	}
	if f.skills {
		if err := installSkills(cfg, f.dir); err != nil {
			return err
		}
	}
	if f.service && f.skills {
		fmt.Printf("\nyou're done: the hub runs in the background and the skill is installed.\n")
	}
	return nil
}

func installSkills(cfg config.Config, dir string) error {
	target := dir
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
	// The client-side config is created ONCE and never overwritten: somebody's
	// hub or token-env setting must survive an upgrade.
	created, err := ensureSkillConfig(skillConfigPath(cfg))
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("created %s\n", skillConfigPath(cfg))
	}
	fmt.Printf("skill ready: say \"listen with workwire\" in a session, or start manually with `workwire listen --agent <name>`\n")
	fmt.Printf("to join automatically, put a line in the repo's own CLAUDE.md / AGENTS.md:\n")
	fmt.Printf("  At the start of a session, join workwire (say: listen with workwire).\n")
	fmt.Printf("config: %s\n", filepath.Join(cfg.ConfigDir, "workwire.json"))
	return nil
}

func installService(cfg config.Config) error {
	spec, err := service.NewSpec(cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("resolve workwire binary: %w", err)
	}
	b := service.New()
	if err := b.Install(spec); err != nil {
		return fmt.Errorf("install service %s: %w", b.Name(), err)
	}
	fmt.Printf("service:  %s\n", b.Name())
	fmt.Printf("binary:   %s %s\n", spec.BinPath, "serve")
	fmt.Printf("logs:     %s\n          %s\n", spec.LogPath(), spec.ErrLogPath())
	if h := b.Hint(); h != "" {
		fmt.Printf("hint:     %s\n", h)
	}
	if !waitHealthy(cfg, 10) {
		state, _ := b.Status(spec)
		return fmt.Errorf("service installed (%s) but %s/health never answered — check %s",
			state, cfg.HubURL, spec.ErrLogPath())
	}
	state, _ := b.Status(spec)
	fmt.Printf("state:    %s\n", state)
	fmt.Printf("hub:      %s (healthy)\n", cfg.HubURL)
	fmt.Printf("verify:   workwire status\n")
	return nil
}

// waitHealthy polls /health with a short backoff while the service boots.
func waitHealthy(cfg config.Config, tries int) bool {
	c := newClient(cfg)
	delay := 100 * time.Millisecond
	for i := 0; i < tries; i++ {
		var out map[string]any
		if code, err := c.do("GET", "/health", nil, &out); err == nil && code == 200 && out["service"] == "workwire" {
			return true
		}
		time.Sleep(delay)
		if delay < 2*time.Second {
			delay *= 2
		}
	}
	return false
}

// cmdUninstall removes the background service. The data dir is left alone.
func cmdUninstall(cfg config.Config, args []string) error {
	fs_ := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	svc := fs_.Bool("service", false, "remove the background service")
	if err := fs_.Parse(args); err != nil {
		return err
	}
	if !*svc {
		fmt.Fprint(os.Stderr, "usage: workwire uninstall --service\n\nremoves the background service. Messages, cursors and credentials are kept.\n")
		return fmt.Errorf("uninstall requires --service")
	}
	spec, err := service.NewSpec(cfg.ConfigDir)
	if err != nil {
		return err
	}
	b := service.New()
	if err := b.Uninstall(spec); err != nil {
		return fmt.Errorf("uninstall service %s: %w", b.Name(), err)
	}
	fmt.Printf("removed service %s\n", b.Name())
	fmt.Printf("kept data dir %s (messages, cursors, credentials)\n", cfg.DataDir)
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
