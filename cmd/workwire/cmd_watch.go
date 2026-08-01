package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/origin"
)

// cmdWatch is the session's end of the wire: it streams inbound envelopes as
// one line each, and — while it runs — keeps the answerer declaration fresh.
//
// Those two jobs belong in ONE process, and finding that out cost a failed
// test. The skill armed a Monitor on the inbox file and separately ran
// `workwire answering`; twenty-one minutes later a real session was still
// listening, its watch still armed, and the hub reported `answering: false`,
// because the declaration is a marker file that ages out after fifteen minutes
// and nothing renewed it. The peer looked dead while it was perfectly alive —
// the same fifteen-minute cliff as the answerer fork, just moved.
//
// Renewing from a long-lived process fixes it by construction: this command
// lives exactly as long as the session's watch does. If the session exits, the
// harness kills it, renewal stops, and the declaration decays — which is the
// honest answer. No timer to tune, no heartbeat to remember.
func cmdWatch(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name (default: derived from --dir)")
	dir := fs.String("dir", "", "working tree the name is derived from (default: cwd)")
	renew := fs.Duration("renew", 60*time.Second, "how often to refresh the answerer declaration")
	fs.Parse(args)

	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}
	name := *agent
	if name == "" {
		name = loadSkillConfig(skillConfigPath(cfg)).AgentName
	}
	if name == "" {
		d := *dir
		if d == "" {
			d, _ = os.Getwd()
		}
		name = origin.DeriveName(d)
	}
	if name == "" {
		return fmt.Errorf("could not derive an agent name — pass --agent <name>")
	}

	sess := filepath.Join(cfg.ConfigDir, "sessions", name)
	if err := os.MkdirAll(sess, 0o755); err != nil {
		return err
	}
	inbox := filepath.Join(sess, "inbox.ndjson")
	// Create it if absent so `tail -F` has something to follow immediately and
	// the first envelope is not delayed by a retry interval.
	if f, err := os.OpenFile(inbox, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}

	// Declare now, and keep declaring for as long as this process lives.
	declare(cfg, name, true)
	stop := make(chan struct{})
	defer func() {
		close(stop)
		// Standing down is part of being honest: a session that ends should
		// not leave the mesh believing someone is still reading.
		declare(cfg, name, false)
	}()
	go func() {
		t := time.NewTicker(*renew)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				declare(cfg, name, true)
			}
		}
	}()

	fmt.Fprintf(os.Stderr, "workwire watch: %s — streaming %s, renewing the answerer declaration every %s\n",
		name, inbox, *renew)

	// tail -F: follows truncation and re-creation, which the listener does on
	// rotation. -n0 so a restart does not replay the whole backlog as "new".
	cmd := exec.Command("tail", "-n0", "-F", inbox)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill() }()

	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e struct {
			ID       string `json:"id"`
			From     string `json:"from"`
			ThreadID string `json:"thread_id"`
			Kind     string `json:"kind"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		text := strings.ReplaceAll(e.Text, "\n", " ")
		if len(text) > 200 {
			text = text[:200] + "…"
		}
		kind := e.Kind
		if kind == "" {
			kind = "message"
		}
		// One line per envelope: this is the event a harness turns into a
		// notification, so it must be self-contained — who, which thread, and
		// the id needed to answer it.
		fmt.Printf("workwire<< id=%s from=%s thread=%s kind=%s :: %s\n", e.ID, e.From, e.ThreadID, kind, text)
	}
	return cmd.Wait()
}

// declare refreshes (or clears) the answerer marker and tells the hub. A hub
// that is down is not an error: the listener re-declares from the same local
// marker when it reconnects.
func declare(cfg config.Config, name string, attached bool) {
	args := []string{"--agent", name}
	if !attached {
		args = append(args, "--off")
	}
	// cmdAnswering prints a line per call; a renewal every minute would turn
	// the event stream into a log. Swallow its stdout, keep its effect.
	saved := os.Stdout
	if devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		os.Stdout = devnull
		defer func() { os.Stdout = saved; devnull.Close() }()
	}
	_ = cmdAnswering(cfg, args)
}
