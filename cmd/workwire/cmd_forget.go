package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
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
	stale := fs.Bool("stale", false, "forget EVERY registration with no live listener (leftovers, renames, dead aliases)")
	purge := fs.Bool("purge", false, "also delete this machine's session directory for the peer (inbox + cursor). Evidence is kept without it.")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	c := newClient(cfg)
	if *stale {
		names, err := stalePeers(c)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Println("nothing stale: every registration has a live listener")
			return nil
		}
		rest = append(rest, names...)
	}
	if len(rest) == 0 {
		return fmt.Errorf("usage: workwire forget <name> [<name>...] | workwire forget --stale")
	}

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
		// Dropping the hub registration alone is theatre: the credential and
		// the folder binding on THIS machine are what let the alias
		// re-register the moment anything runs `listen` under the old name.
		// That is why `koine`, `clojure` and two `toolnexus` aliases kept
		// reappearing after being forgotten on 2026-08-01.
		local := dropLocalName(cfg, name)
		// The session directory is this machine's copy of that peer's inbox and
		// cursor — evidence, and small. Deleting it is opt-in.
		sess := filepath.Join(cfg.ConfigDir, "sessions", name)
		if *purge {
			if err := os.RemoveAll(sess); err == nil {
				local = append(local, "session dir")
			}
		}
		detail := "nothing local to clear"
		if len(local) > 0 {
			detail = "cleared " + strings.Join(local, ", ")
		}
		if !*purge {
			detail += fmt.Sprintf("; kept %s", sess)
		}
		fmt.Printf("forgot %s — %s. Its messages and threads are untouched.\n", name, detail)
	}
	return nil
}

// dropLocalName clears the state on THIS machine that would re-create a name
// on the next `listen`: the hub-issued credential and any folder bound to it.
// Dropping only the hub's row is theatre — that is why `koine`, `clojure` and
// two `toolnexus` labels kept coming back after being forgotten.
func dropLocalName(cfg config.Config, name string) []string {
	var cleared []string
	if err := listen.DropCredential(cfg.ConfigDir, cfg.HubURL, name); err == nil {
		cleared = append(cleared, "credential")
	}
	if n := dropFolderBindings(cfg, name); n > 0 {
		cleared = append(cleared, fmt.Sprintf("%d folder binding(s)", n))
	}
	return cleared
}

// stalePeers lists every registration the hub still serves that has no live
// listener — renamed peers, dead aliases, sessions that never came back. These
// are exactly the names that make `ask` wait on nobody and make one codebase
// answer under three identities.
func stalePeers(c *client) ([]string, error) {
	var out struct {
		Agents []struct {
			Name     string `json:"name"`
			Listener bool   `json:"listener"`
		} `json:"agents"`
	}
	if _, err := c.do("GET", "/agents", nil, &out); err != nil {
		return nil, err
	}
	var names []string
	for _, a := range out.Agents {
		if !a.Listener {
			names = append(names, a.Name)
		}
	}
	return names, nil
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
