package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/muthuishere/workwire/internal/config"
)

// cmdAnswering is how an answerer says "I am here" — and, on the way out,
// "I am gone".
//
// A listen lease means questions are being DELIVERED into a session inbox
// file. It has never meant anyone is reading them — a listener outlives the
// session that started it, and a folder can have a listener with no session
// attached at all. So answerability is declared by the thing that knows — the
// attached answerer — and `ask` / `peers` report it separately from the lease.
//
// The declaration is local evidence (a touched file the listener sees) plus a
// best-effort hub call, so it works even while the hub is down.
func cmdAnswering(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("answering", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name (required)")
	off := fs.Bool("off", false, "stand down: nothing is attached to answer for this peer any more")
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("answering requires --agent <name>")
	}
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}
	mark := filepath.Join(cfg.ConfigDir, "sessions", *agent, "answerer")
	if *off {
		if err := os.Remove(mark); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(mark), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(mark, []byte("attached\n"), 0o644); err != nil {
			return err
		}
		now := time.Now()
		_ = os.Chtimes(mark, now, now)
	}
	// Tell the hub now rather than waiting for the listener's next heartbeat;
	// a hub that is down or a peer not yet registered is not an error — the
	// listener re-declares from the same local evidence when it reconnects.
	c := newClient(cfg)
	if err := c.asAgent(cfg, *agent); err == nil {
		_, _ = c.do("POST", "/agents/"+url.PathEscape(*agent)+"/answering",
			map[string]bool{"attached": !*off}, nil)
	}
	if *off {
		fmt.Printf("%s: no answerer attached\n", *agent)
	} else {
		fmt.Printf("%s: answerer attached\n", *agent)
	}
	return nil
}
