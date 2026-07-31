package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
	persona_ "github.com/muthuishere/workwire/internal/persona"
)

// cmdListen runs the singleton dumb waiter (ADR-003, agent-skill R4/R5):
// flock + hub lease, long-poll the hub inbox, append questions to the
// session inbox file. It never answers anything itself.
func cmdListen(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name (required)")
	inbox := fs.String("inbox", "", "session inbox file override (default <config>/sessions/<agent>/inbox.ndjson)")
	wait := fs.Int("wait", cfg.WaitDefault, "long-poll seconds")
	ctxDepth := fs.Int("context", cfg.LastMessages, "context depth attached at read time")
	persona := fs.String("persona", "", "short self-description sent at registration: who this worker is, what it owns, what it will not speak for")
	maxRetries := fs.Int("max-retries", 0, "give up after N consecutive failed hub attempts (default 0 = retry forever)")
	groups := fs.String("groups", "", "comma-separated audiences to join (default: the `groups:` line in this directory's AGENTS.md / CLAUDE.md)")
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("listen requires --agent <name>")
	}
	// Persona comes from this directory's own AGENTS.md / CLAUDE.md unless
	// the caller overrode it — one capped line, never the whole file.
	if *persona == "" {
		*persona = persona_.Derive("")
	}
	// Audiences come from the same declaration file as the persona
	// (ADR-012): write the file, say the phrase. @all is joined by the hub.
	declared := persona_.GroupsFromMarkdown("groups: " + *groups)
	if *groups == "" {
		declared = persona_.DeriveGroups("")
	}
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}

	// Local singleton fast path first: cheap, no network (agent-skill R4).
	lock, err := listen.AcquireLock(filepath.Join(cfg.ConfigDir, "run"), *agent)
	if err != nil {
		if _, ok := err.(listen.ErrLocked); ok {
			return fmt.Errorf("%w — adopt the running listener instead of starting a second", err)
		}
		return err
	}
	defer lock.Release()

	adminToken := cfg.TokenFromEnv()
	if adminToken == "" {
		if b, err := os.ReadFile(filepath.Join(cfg.ConfigDir, auth.TokenFileName)); err == nil {
			adminToken = strings.TrimSpace(string(b))
		}
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "workwire listen: "+format+"\n", a...)
	}
	r, err := listen.New(listen.Options{
		Agent:      *agent,
		HubURL:     cfg.HubURL,
		ConfigDir:  cfg.ConfigDir,
		AdminToken: adminToken,
		Wait:       *wait,
		Context:    *ctxDepth,
		Persona:    *persona,
		Groups:     declared,
		Heartbeat:  time.Duration(cfg.HeartbeatSeconds) * time.Second,
		InboxPath:  *inbox,
		MaxRetries: *maxRetries,
		Logf:       logf,
	})
	if err != nil {
		return err
	}
	// A hub that is down or restarting at startup is NOT fatal: Run retries
	// registration on its own backoff. Only the local flock (above) or a
	// signal ends this process.
	if err := r.EnsureRegistered(); err != nil {
		logf("initial registration failed: %v — retrying in the background", err)
	}
	if r.AgentName() != *agent {
		logf("registered as %s", r.AgentName())
	}

	stop := make(chan struct{})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		logf("shutting down (releasing lease)")
		close(stop)
	}()
	return r.Run(stop)
}
