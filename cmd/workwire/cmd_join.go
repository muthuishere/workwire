package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/listen"
	"github.com/muthuishere/workwire/internal/origin"
	persona_ "github.com/muthuishere/workwire/internal/persona"
	"github.com/muthuishere/workwire/internal/registry"
)

// joinDeclaredGroups opts the peer into the audiences its own AGENTS.md /
// CLAUDE.md declares (ADR-012). Self-service only: this joins the
// authenticated caller and nobody else. @all is joined by the hub.
func joinDeclaredGroups(c *client, dir string) {
	for _, g := range persona_.DeriveGroups(dir) {
		var out map[string]any
		code, err := c.do("POST", "/groups/"+url.PathEscape(g)+"/join", map[string]string{}, &out)
		if err != nil || code != 200 {
			fmt.Fprintf(os.Stderr, "could not join %s (%d)\n", g, code)
			continue
		}
		fmt.Printf("joined %s\n", g)
	}
}

// cmdJoin registers a peer and stores its credentials WITHOUT starting a
// listener (ADR-011 §3): a person takes part from a plain terminal with
// `--human`, and every later verb runs `--as <name>`. `workwire listen`
// still auto-registers agents.
func cmdJoin(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	human := fs.Bool("human", false, "join as a human peer (precedence at closure), not an agent")
	persona := fs.String("persona", "", "short self-description: who you are, what you own, what you will not speak for")
	dir := fs.String("dir", "", "working tree provenance is derived from (default: cwd)")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: workwire join <name> [--human] [--persona \"...\"]")
	}
	name := rest[0]
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}
	kind := registry.KindAgent
	if *human {
		kind = registry.KindHuman
	}
	prov := origin.Detect(*dir)
	// A peer does not hand-write a persona: it comes from the directory's own
	// AGENTS.md / CLAUDE.md, capped to one line (never the whole file).
	if *persona == "" {
		*persona = persona_.Derive(*dir)
	}
	card := map[string]any{
		"name":         name,
		"kind":         kind,
		"origin":       prov,
		"project":      prov.Cwd,
		"capabilities": []string{"ask"},
	}
	if *persona != "" {
		card["persona"] = *persona
	}

	creds, err := listen.LoadCredentials(cfg.ConfigDir)
	if err != nil {
		return err
	}
	c := newClient(cfg)
	if cr, ok := creds[name]; ok {
		// Already ours: re-register with the stored secret, refreshing
		// persona/kind/provenance without rotating the identity.
		c.token = cr.AgentSecret
		var out map[string]any
		code, err := c.do("POST", "/agents", card, &out)
		if err != nil {
			return err
		}
		if code != 200 && code != 201 {
			return fmt.Errorf("re-join as %s failed (%d): %v — stored credentials may be stale", name, code, out)
		}
		fmt.Printf("rejoined as %s (%s) %s\n", name, kind, origin.Describe(prov))
		joinDeclaredGroups(c, *dir)
		return nil
	}
	var out struct {
		AgentID     string `json:"agentId"`
		AgentSecret string `json:"agentSecret"`
		Suggestion  string `json:"suggestion"`
		Error       string `json:"error"`
	}
	code, err := c.do("POST", "/agents", card, &out)
	if err != nil {
		return err
	}
	switch code {
	case 201:
		if err := listen.SaveCredential(cfg.ConfigDir, name, listen.Credential{
			AgentID: out.AgentID, AgentSecret: out.AgentSecret,
		}); err != nil {
			return err
		}
	case 409:
		return fmt.Errorf("name %q is taken by another peer — try %q", name, out.Suggestion)
	default:
		return fmt.Errorf("join failed (%d): %s", code, out.Error)
	}
	c.token = out.AgentSecret
	joinDeclaredGroups(c, *dir)
	fmt.Printf("joined as %s (%s) %s\n", name, kind, origin.Describe(prov))
	fmt.Fprintf(os.Stderr, "no listener started — use --as %s on say/resolve/threads/inbox\n", name)
	return nil
}
