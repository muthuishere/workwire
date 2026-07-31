package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/registry"
)

// cmdGroups lists the audiences: name, member count, members, and a `*` on
// the ones you are in. A group holds no messages (ADR-012), so there is
// nothing else to list.
func cmdGroups(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("groups", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered peer")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	var out struct {
		Groups []struct {
			Name    string   `json:"name"`
			Members []string `json:"members"`
			Count   int      `json:"count"`
			Member  bool     `json:"member"`
		} `json:"groups"`
	}
	code, err := c.do("GET", "/groups", nil, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("groups listing failed (%d)", code)
	}
	if len(out.Groups) == 0 {
		fmt.Println("no groups")
		return nil
	}
	for _, g := range out.Groups {
		mark := " "
		if g.Member {
			mark = "*"
		}
		members := append([]string(nil), g.Members...)
		sort.Strings(members)
		fmt.Printf("%s %-18s %2d  %s\n", mark, g.Name, g.Count, strings.Join(members, ", "))
	}
	return nil
}

// cmdGroup is `workwire group join|leave|invite`. There is no create verb:
// joining a name that does not exist creates it, with no owner and no admin.
func cmdGroup(cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: workwire group join|leave|invite @<group> [...]")
	}
	switch args[0] {
	case "join", "leave":
		return groupMembership(cfg, args[0], args[1:])
	case "invite":
		return groupInvite(cfg, args[1:])
	default:
		return fmt.Errorf("unknown group verb %q (join | leave | invite)", args[0])
	}
}

func groupMembership(cfg config.Config, verb string, args []string) error {
	fs := flag.NewFlagSet("group "+verb, flag.ExitOnError)
	as := fs.String("as", "", "act as a registered peer")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: workwire group %s @<group> [--as <name>]", verb)
	}
	name := registry.NormalizeGroup(rest[0])
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	var out map[string]any
	code, err := c.do("POST", "/groups/"+url.PathEscape(name)+"/"+verb, map[string]string{}, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("group %s failed (%d): %v", verb, code, out["error"])
	}
	if verb == "join" {
		fmt.Printf("joined %s — discussions addressed to it now wake you\n", name)
		return nil
	}
	if out["collected"] == true {
		fmt.Printf("left %s — it was empty and has been collected\n", name)
		return nil
	}
	fmt.Printf("left %s — it no longer wakes you\n", name)
	return nil
}

// groupInvite sends a MESSAGE, never a mutation (ADR-012): nothing anywhere
// may add another peer to a group. The invitee joins, or ignores it.
func groupInvite(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("group invite", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered peer")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire group invite @<group> <peer> [\"reason\"]")
	}
	name := registry.NormalizeGroup(rest[0])
	peer := rest[1]
	reason := strings.Join(rest[2:], " ")
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	text := "you are invited to " + name
	if reason != "" {
		text += " — " + reason
	}
	text += ". Join with: workwire group join " + name + " (ignoring this is a valid answer; nobody can add you)"
	out, err := postSend(c, map[string]any{"to": peer, "text": text, "kind": "invite"})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "invited %s to %s — this asked, it did not add them\n", peer, name)
	fmt.Printf("%v\n", out["thread_id"])
	return nil
}
