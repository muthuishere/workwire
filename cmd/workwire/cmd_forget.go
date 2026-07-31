package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/muthuishere/workwire/internal/config"
)

// cmdForget drops a peer registration that nothing will ever answer for again
// — the identity, its credential, its lease and its group memberships.
//
// Nothing it ever said is deleted: history is append-only (ADR-008), and a
// thread it argued in keeps its words and its provenance. What goes away is
// the ability to ADDRESS it, which is the point — a ghost peer that `peers`
// lists and `ask` can reach is worse than no peer at all, because a question
// sent to it waits for an answerer that no longer exists.
//
// The case this exists for is a rename: a session that joined as `api` now
// joins as `api-main`.
func cmdForget(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("forget", flag.ExitOnError)
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: workwire forget <name> [<name>...]")
	}

	c := newClient(cfg)
	for _, name := range rest {
		// Refuse to unname a peer that is still live: that is a running
		// session, not a leftover, and dropping it would strand its listener
		// holding a lease for an identity the hub no longer knows.
		if live, why := peerIsLive(c, name); live {
			fmt.Fprintf(os.Stderr, "workwire: %s is %s — stop its listener first, or it will keep re-registering\n", name, why)
			continue
		}
		if _, err := c.do("DELETE", "/agents/"+url.PathEscape(name), nil, nil); err != nil {
			fmt.Fprintf(os.Stderr, "workwire: %s: %v\n", name, err)
			continue
		}
		// The local session directory is this machine's copy of that peer's
		// inbox and cursor. Keep it: it is evidence, and it is small.
		fmt.Printf("forgot %s (its messages and threads are untouched; %s kept)\n",
			name, filepath.Join(cfg.ConfigDir, "sessions", name))
	}
	return nil
}

// peerIsLive reports whether the hub still sees a listener or an answerer for
// the name, with a short reason for the message.
func peerIsLive(c *client, name string) (bool, string) {
	var out struct {
		Agents []struct {
			Name      string `json:"name"`
			Listener  bool   `json:"listener"`
			Answering bool   `json:"answering"`
		} `json:"agents"`
	}
	if _, err := c.do("GET", "/agents", nil, &out); err != nil {
		return false, ""
	}
	for _, a := range out.Agents {
		if a.Name != name {
			continue
		}
		switch {
		case a.Answering:
			return true, "still answering"
		case a.Listener:
			return true, "still listening"
		}
	}
	return false, ""
}
