package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/hubaddr"
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
	dir := fs.String("dir", "", "working tree provenance and persona are derived from (default: cwd)")
	maxRetries := fs.Int("max-retries", 0, "give up after N consecutive failed hub attempts (default 0 = retry forever)")
	groups := fs.String("groups", "", "comma-separated audiences to join (default: the `groups:` line in this directory's AGENTS.md / CLAUDE.md)")
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("listen requires --agent <name>")
	}
	// Persona comes from --dir (or cwd) — the tree this listener speaks for —
	// unless the caller stated one; one capped line, never the whole file.
	personaExplicit := *persona != ""
	if *persona == "" {
		*persona = persona_.Derive(*dir)
	}
	// Audiences come from the same declaration file as the persona
	// (ADR-012): write the file, say the phrase. @all is joined by the hub.
	declared := persona_.GroupsFromMarkdown("groups: " + *groups)
	if *groups == "" {
		declared = persona_.DeriveGroups(*dir)
	}
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}

	// Which tree this listener speaks for — the thing the name is derived
	// from, and the thing a name collision is a collision OF.
	originDir := *dir
	if originDir == "" {
		originDir, _ = os.Getwd()
	}
	// A name is one folder's identity. Another folder already holding it is a
	// conflict, not an adoption: sharing it would put two codebases behind one
	// peer, and the loser silently on the wire under no name at all.
	if other := nameConflict(cfg, *agent, originDir); other != "" {
		return fmt.Errorf("%s", conflictMessage(*agent, other, absOf(originDir), suggestFreeName(cfg, *agent, originDir)))
	}

	runDir := filepath.Join(cfg.ConfigDir, "run")
	// Local singleton fast path first: cheap, no network (agent-skill R4).
	lock, err := listen.AcquireLock(runDir, *agent)
	if err != nil {
		if _, ok := err.(listen.ErrLocked); ok {
			// One listener per folder is enough, and a second session in the
			// same folder is a normal, expected thing — not a failure. It
			// adopts the running listener and exits 0; only the lock holder
			// answers, so a question is never answered twice.
			fmt.Fprintf(os.Stderr, "workwire listen: adopting the running listener for %s (%s)\n", *agent, absOf(originDir))
			return nil
		}
		return err
	}
	defer lock.Release()
	// Say which folder holds it, so the next session can tell "same folder,
	// adopt" from "different folder, conflict".
	_ = listen.WriteHolder(runDir, *agent, absOf(originDir))
	defer listen.ClearHolder(runDir, *agent)
	saveFolderBinding(cfg, originDir, *agent)

	// Registration bootstrap credential. The locally minted admin token is a
	// credential for the LOCAL hub and never leaves it (auth R10): against a
	// remote hub only the env-named token counts, and if we hold neither that
	// nor a secret that hub already issued us, say so instead of trying.
	adminToken := configuredToken(cfg)
	if adminToken == "" {
		if hubaddr.IsLoopback(cfg.HubURL) {
			adminToken = localAdminToken(cfg)
		} else {
			creds, cerr := listen.LoadCredentials(cfg.ConfigDir, cfg.HubURL)
			if cerr != nil {
				return cerr
			}
			if _, ok := creds[*agent]; !ok {
				return remoteHubNoCredential(cfg)
			}
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
		// An explicit --persona is a deliberate act and always wins; an
		// inferred one never overwrites what the peer already registered.
		PersonaExplicit: personaExplicit,
		OriginDir:       *dir,
		Groups:          declared,
		Heartbeat:       time.Duration(cfg.HeartbeatSeconds) * time.Second,
		InboxPath:       *inbox,
		MaxRetries:      *maxRetries,
		Logf:            logf,
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
		// The hub renamed us; the folder keeps the name it actually got.
		saveFolderBinding(cfg, originDir, r.AgentName())
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
