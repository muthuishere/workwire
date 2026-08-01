package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/hubaddr"
)

// cmdDoctor answers "is my side alive?" from local state ALONE.
//
// /metrics cannot answer a question about a hub that is down, and a hub that
// is down is the most likely thing to be wrong. Every fact needed is already
// on disk — both configs, both hub logs, credentials, folder bindings, run
// locks, session inboxes (spikes/05-observability S8) — so this reads files
// and never requires the hub. It probes the hub last, and a dead hub is a
// finding, not a failure.
func cmdDoctor(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	fs.Parse(args)

	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}
	problems := 0
	warn := func(format string, a ...any) {
		problems++
		fmt.Printf("  ✗ "+format+"\n", a...)
	}
	ok := func(format string, a ...any) { fmt.Printf("  ✓ "+format+"\n", a...) }
	note := func(format string, a ...any) { fmt.Printf("    "+format+"\n", a...) }

	fmt.Printf("config dir: %s\nhub url:    %s\n\n", cfg.ConfigDir, cfg.HubURL)

	// --- the hub itself -----------------------------------------------------
	fmt.Println("hub")
	c := newClient(cfg)
	var health map[string]any
	if code, err := c.do("GET", "/health", nil, &health); err == nil && code == 200 {
		ok("reachable at %s (apiVersion %v)", cfg.HubURL, health["apiVersion"])
	} else {
		warn("NOT reachable at %s", cfg.HubURL)
		if hubaddr.IsLoopback(cfg.HubURL) {
			note("loopback: start it with `workwire serve`, or `workwire install --service`")
		} else {
			note("remote: workwire never starts a remote hub — check the host")
		}
	}
	for _, log := range []string{"hub.err.log", "hub.log"} {
		p := filepath.Join(cfg.ConfigDir, log)
		if fi, err := os.Stat(p); err == nil {
			age := time.Since(fi.ModTime()).Round(time.Second)
			note("%-12s %6d bytes, last write %s ago", log, fi.Size(), age)
			if tail := lastLines(p, 2); tail != "" {
				for _, line := range strings.Split(tail, "\n") {
					note("  | %s", trim(line, 110))
				}
			}
		}
	}

	// --- this machine's listeners ------------------------------------------
	fmt.Println("\nlisteners on this machine")
	runDir := filepath.Join(cfg.ConfigDir, "run")
	entries, _ := os.ReadDir(runDir)
	held := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".lock")
		owner := readFirstLine(filepath.Join(runDir, name+".owner"))
		held++
		if owner == "" {
			note("%-28s lock present, no owner recorded", name)
		} else {
			note("%-28s %s", name, owner)
		}
	}
	if held == 0 {
		warn("no listen locks — nothing on this machine is listening")
		note("say `listen with workwire` in a session, or run `workwire listen --dir <repo>`")
	}

	// --- session inboxes: the "arriving but unread" shape -------------------
	fmt.Println("\nsession inboxes (unread bytes = delivered, not collected)")
	sessDir := filepath.Join(cfg.ConfigDir, "sessions")
	sessions, _ := os.ReadDir(sessDir)
	names := make([]string, 0, len(sessions))
	for _, e := range sessions {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		note("none yet")
	}
	for _, name := range names {
		inbox := filepath.Join(sessDir, name, "inbox.ndjson")
		fi, err := os.Stat(inbox)
		if err != nil {
			continue
		}
		offset := int64(0)
		fmt.Sscanf(readFirstLine(filepath.Join(sessDir, name, "inbox.offset")), "%d", &offset)
		unread := fi.Size() - offset
		answerer := filepath.Join(sessDir, name, "answerer")
		attached := ""
		if afi, err := os.Stat(answerer); err == nil {
			attached = fmt.Sprintf(", answerer declared %s ago", time.Since(afi.ModTime()).Round(time.Second))
		}
		switch {
		case unread > 0 && attached == "":
			warn("%-28s %d unread bytes and NOTHING attached to answer", name, unread)
			note("that is the exact shape of 'questions arriving, nobody reading'")
		case unread > 0:
			note("%-28s %d unread bytes%s", name, unread, attached)
		default:
			note("%-28s caught up%s", name, attached)
		}
	}

	// --- credentials and bindings ------------------------------------------
	fmt.Println("\nlocal identity state")
	for _, f := range []string{"credentials.json", "folders.json", "skill.json", "workwire.json", "admin-token"} {
		p := filepath.Join(cfg.ConfigDir, f)
		fi, err := os.Stat(p)
		if err != nil {
			note("%-18s absent", f)
			continue
		}
		mode := fi.Mode().Perm()
		// A file holding a secret that others can read is a finding, not a note.
		if (f == "credentials.json" || f == "admin-token" || f == "skill.json" || f == "workwire.json") && mode&0o077 != 0 {
			warn("%-18s mode %04o — others can read a file that may hold a secret (chmod 600 %s)", f, mode, p)
			continue
		}
		note("%-18s %6d bytes, mode %04o", f, fi.Size(), mode)
	}

	fmt.Println()
	if problems == 0 {
		fmt.Println("no problems found on this machine.")
		return nil
	}
	fmt.Printf("%d problem(s) above.\n", problems)
	return nil
}

func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

func lastLines(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
