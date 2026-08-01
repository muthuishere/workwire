package main

import (
	"flag"
	"fmt"
	"net/url"

	"github.com/muthuishere/workwire/internal/config"
)

// cmdAlias manages the NAMES that point at one identity (ADR-015).
//
// A working tree is the identity; a name is a label on it. Several labels for
// one identity is normal — peers have `ask clojure` in their notes long after
// the session started calling itself `toolnexus-cljc`. What is not normal, and
// was the actual 2026-08-01 defect, is several identities for one tree: three
// inboxes, three cursors, three answerers, and half the questions landing where
// nobody was reading.
func cmdAlias(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: workwire alias list | workwire alias rm <name>")
	}
	sub, rest := args[0], args[1:]
	c := newClient(cfg)

	switch sub {
	case "list":
		var out struct {
			Agents []struct {
				Name    string   `json:"name"`
				Aliases []string `json:"aliases"`
			} `json:"agents"`
		}
		if _, err := c.do("GET", "/agents", nil, &out); err != nil {
			return err
		}
		found := false
		for _, a := range out.Agents {
			if len(a.Aliases) == 0 {
				continue
			}
			found = true
			fmt.Printf("%-28s also answers to: %v\n", a.Name, a.Aliases)
		}
		if !found {
			fmt.Println("no aliases: every name on the wire is an identity of its own")
		}
		return nil

	case "rm", "remove":
		fs := flag.NewFlagSet("alias rm", flag.ExitOnError)
		names, err := parseInterspersed(fs, rest)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			return fmt.Errorf("usage: workwire alias rm <name> [<name>...]")
		}
		for _, name := range names {
			var res struct {
				Dropped  string `json:"dropped"`
				Identity string `json:"identity"`
				Error    string `json:"error"`
			}
			code, err := c.do("DELETE", "/agents/"+url.PathEscape(name)+"/alias", nil, &res)
			if err != nil {
				return err
			}
			if code != 200 {
				fmt.Printf("%s: %s\n", name, res.Error)
				continue
			}
			// The label is gone from the hub; the local credential and folder
			// binding are what would resurrect it on the next `listen`.
			dropLocalName(cfg, name)
			fmt.Printf("dropped alias %s (identity %s keeps its inbox, cursor and history)\n", res.Dropped, res.Identity)
		}
		return nil
	}
	return fmt.Errorf("unknown alias subcommand %q — use list or rm", sub)
}
