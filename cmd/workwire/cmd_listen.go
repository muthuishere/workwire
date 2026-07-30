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
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("listen requires --agent <name>")
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
		Heartbeat:  time.Duration(cfg.HeartbeatSeconds) * time.Second,
		InboxPath:  *inbox,
		Logf:       logf,
	})
	if err != nil {
		return err
	}
	if err := r.EnsureRegistered(); err != nil {
		return err
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
