package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/origin"
)

// clientAs builds a hub client, optionally acting as a registered agent.
func clientAs(cfg config.Config, as string) (*client, error) {
	c := newClient(cfg)
	if as != "" {
		if err := c.asAgent(cfg, as); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// postSend issues one /send and returns the decoded response.
func postSend(c *client, body map[string]any) (map[string]any, error) {
	var out map[string]any
	code, err := c.do("POST", "/send", body, &out)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		if msg, ok := out["error"].(string); ok {
			return nil, fmt.Errorf("send failed (%d): %s", code, msg)
		}
		return nil, fmt.Errorf("send failed (%d): %v", code, out)
	}
	return out, nil
}

// cmdHuddle opens a discussion: one envelope addressed to every named member
// (ADR-009). The sender is the thread initiator and the only one who may
// later resolve it.
func cmdHuddle(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("huddle", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered agent")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire huddle <name...> \"<topic>\"")
	}
	members, topic := rest[:len(rest)-1], rest[len(rest)-1]
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	out, err := postSend(c, map[string]any{"to": members, "text": topic})
	if err != nil {
		return err
	}
	// thread id on stdout so it pipes into `workwire say`; the human line on stderr.
	fmt.Fprintf(os.Stderr, "huddle open with %s — you are the initiator and decide when it resolves\n", strings.Join(members, ", "))
	fmt.Printf("%v\n", out["thread_id"])
	return nil
}

// cmdSay contributes to an existing discussion; the hub fans it out to every
// current member except the sender, and joins the sender if new.
func cmdSay(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("say", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered agent")
	proposal := fs.Bool("proposal", false, "send as kind \"proposal\" — a recommendation, not a verdict")
	dissent := fs.Bool("dissent", false, "register an OPEN objection: an agent initiator may not close over it")
	withdraw := fs.Bool("withdraw", false, "withdraw your own dissent (yours only)")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire say <thread> \"<text>\"")
	}
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	body := map[string]any{"thread_id": rest[0], "text": strings.Join(rest[1:], " ")}
	switch {
	case *dissent && *withdraw:
		return fmt.Errorf("--dissent and --withdraw are opposites; pick one")
	case *dissent:
		body["kind"] = "dissent"
	case *withdraw:
		body["kind"] = "withdraw"
	case *proposal:
		body["kind"] = "proposal"
	}
	out, err := postSend(c, body)
	if err != nil {
		return err
	}
	switch {
	case *dissent:
		fmt.Printf("dissent %v recorded on thread %v — it stays open until you withdraw it or a human decides\n", out["id"], out["thread_id"])
	case *withdraw:
		fmt.Printf("withdrew your dissent on thread %v (%v)\n", out["thread_id"], out["id"])
	default:
		fmt.Printf("said %v on thread %v\n", out["id"], out["thread_id"])
	}
	return nil
}

// cmdReopen reopens a resolved-by-agent or stalled thread. Humans only: a
// human ruling is final and agents may not reopen anything (ADR-011 §3a).
func cmdReopen(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("reopen", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered peer")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire reopen <thread> \"<reason>\"")
	}
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	out, err := postSend(c, map[string]any{
		"thread_id": rest[0], "text": strings.Join(rest[1:], " "), "kind": "reopen",
	})
	if err != nil {
		return err
	}
	fmt.Printf("reopened thread %v (%v)\n", out["thread_id"], out["id"])
	return nil
}

// cmdResolve closes a discussion. Only the thread initiator may do this —
// participants recommend with `workwire say --proposal` (ADR-009).
func cmdResolve(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered agent")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire resolve <thread> \"<summary>\"")
	}
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	out, err := postSend(c, map[string]any{
		"thread_id": rest[0], "text": strings.Join(rest[1:], " "), "kind": "resolved",
	})
	if err != nil {
		return err
	}
	fmt.Printf("resolved thread %v (%v)\n", out["thread_id"], out["id"])
	return nil
}

// cmdThreads lists live discussions: id, members, message count, state.
func cmdThreads(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("threads", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered agent")
	state := fs.String("state", "", "filter by state: open | resolved | stalled")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	c, err := clientAs(cfg, *as)
	if err != nil {
		return err
	}
	var out struct {
		Threads []struct {
			ThreadID  string   `json:"thread_id"`
			Initiator string   `json:"initiator"`
			Members   []string `json:"members"`
			Count     int      `json:"count"`
			State     string   `json:"state"`
			Dissents  []struct {
				Peer   string `json:"peer"`
				Kind   string `json:"kind"`
				Origin struct {
					Repo   string `json:"repo"`
					Branch string `json:"branch"`
					Commit string `json:"commit"`
					Dirty  bool   `json:"dirty"`
				} `json:"origin"`
			} `json:"dissents"`
			Member     bool   `json:"member"`
			Topic      string `json:"topic"`
			ClosedBy   string `json:"closed_by"`
			ClosedOver []struct {
				Peer string `json:"peer"`
			} `json:"closed_over"`
		} `json:"threads"`
		Max int `json:"maxThreadMessages"`
	}
	path := "/threads"
	if *state != "" {
		path += "?state=" + *state
	}
	code, err := c.do("GET", path, nil, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("threads listing failed (%d)", code)
	}
	if len(out.Threads) == 0 {
		fmt.Println("no threads")
		return nil
	}
	for _, t := range out.Threads {
		// A contested thread must LOOK contested: dissent count and who.
		flag := ""
		if n := len(t.Dissents); n > 0 {
			var who []string
			for _, d := range t.Dissents {
				prov := (&origin.Info{Repo: d.Origin.Repo, Branch: d.Origin.Branch, Commit: d.Origin.Commit, Dirty: d.Origin.Dirty}).String()
				if prov != "" {
					who = append(who, fmt.Sprintf("%s[%s %s]", d.Peer, d.Kind, prov))
				} else {
					who = append(who, fmt.Sprintf("%s[%s]", d.Peer, d.Kind))
				}
			}
			flag = fmt.Sprintf("  dissent:%d %s", n, strings.Join(who, ","))
		}
		if t.ClosedBy != "" && len(t.ClosedOver) > 0 {
			var over []string
			for _, d := range t.ClosedOver {
				over = append(over, d.Peer)
			}
			flag += fmt.Sprintf("  closed by %s over %s", t.ClosedBy, strings.Join(over, ","))
		}
		// A leading `*` marks a thread you are already in; the rest are
		// discoverable — join one by contributing when it touches what you own.
		mark := " "
		if t.Member {
			mark = "*"
		}
		topic := t.Topic
		if len(topic) > 44 {
			topic = topic[:43] + "…"
		}
		fmt.Printf("%s %-22s %-9s %2d/%d  %-44s %s%s\n", mark, t.ThreadID, t.State, t.Count, out.Max, topic, strings.Join(t.Members, ", "), flag)
	}
	return nil
}
