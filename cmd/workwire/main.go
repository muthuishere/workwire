// workwire — HTTP-only message hub for the work between workers.
// Verbs: serve, send, inbox, peers, ask, status, huddle, say, resolve, threads,
// groups, group, listen, answer, install, uninstall.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/auth"
	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/contacts"
	"github.com/muthuishere/workwire/internal/origin"
	"github.com/muthuishere/workwire/internal/registry"
	"github.com/muthuishere/workwire/internal/server"
	"github.com/muthuishere/workwire/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fatal(err)
	}
	verb, args := os.Args[1], os.Args[2:]
	switch verb {
	case "serve":
		err = cmdServe(cfg)
	case "send":
		err = cmdSend(cfg, args)
	case "inbox":
		err = cmdInbox(cfg, args)
	case "peers":
		err = cmdPeers(cfg)
	case "ask":
		err = cmdAsk(cfg, args)
	case "status":
		err = cmdStatus(cfg)
	case "huddle":
		err = cmdHuddle(cfg, args)
	case "say":
		err = cmdSay(cfg, args)
	case "resolve":
		err = cmdResolve(cfg, args)
	case "join":
		err = cmdJoin(cfg, args)
	case "groups":
		err = cmdGroups(cfg, args)
	case "group":
		err = cmdGroup(cfg, args)
	case "reopen":
		err = cmdReopen(cfg, args)
	case "threads":
		err = cmdThreads(cfg, args)
	case "listen":
		err = cmdListen(cfg, args)
	case "answer":
		err = cmdAnswer(cfg, args)
	case "install":
		err = cmdInstall(cfg, args)
	case "uninstall":
		err = cmdUninstall(cfg, args)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown verb %q\n\n", verb)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `workwire — HTTP-only message hub for agents and the humans working with them

Usage:
  workwire serve                          run the hub (port 14411 by default)
  workwire send --to <name> --text <t>    send a message (options: --thread, --reply-to, --as <agent>)
  workwire inbox --agent <name>           poll the inbox (options: --since, --wait, --context)
  workwire peers                          list live agents + contacts
  workwire ask <agent> <question>         ask an agent and wait for the answer
  workwire status                         probe the hub /health
  workwire huddle <name...> "<topic>"     open a discussion; names and @groups mix freely; prints the thread id
  workwire say <thread> "<text>"          contribute (--proposal to recommend, --dissent to object, --withdraw to drop yours)
  workwire resolve <thread> "<summary>"   close a discussion (agent initiator: only with zero open dissents; a human peer may override agent dissent)
  workwire threads                        list live discussions: id, state, count, dissent, members
  workwire join <name> [--human]          register a peer (person or agent) WITHOUT starting a listener
  workwire groups                         list audiences: name, member count, members (* = you are in it)
  workwire group join @<group>            opt in — creates the group if it does not exist (no owner, no admin)
  workwire group leave @<group>           opt out — leaving @all is how you go quiet
  workwire group invite @<group> <peer> ["reason"]   ASK a peer to join; sends a message, adds nobody
  workwire reopen <thread> "<reason>"     reopen a resolved or stalled thread (humans only)
  workwire listen --agent <name>          singleton listener: deliver inbound questions to the session inbox file
  workwire answer <id> <text>             answer a delivered question by its concrete envelope id
  workwire install --service --skills     one-line setup: hub as a background service + the agent skill
  workwire install --skills               install the two-way agent skill only (~/.claude/skills/workwire)
  workwire install --service              run the hub as a background service (launchd / systemd --user / sc.exe)
  workwire uninstall --service            remove the background service (data is kept)

The service is optional: without it, run "workwire serve" yourself or let a
loopback peer auto-start the hub.

Config: ~/.config/workwire/workwire.json (auto-created); WORKWIRE_* env overrides every key.
`)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "workwire:", err)
	os.Exit(1)
}

func cmdServe(cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	adminToken := ""
	if cfg.AuthMode == "token" {
		// Env-named token (containers) wins; otherwise mint/read the 0600
		// local admin token file (auth R2).
		if t := cfg.TokenFromEnv(); t != "" {
			adminToken = t
		} else {
			t, err := auth.EnsureAdminToken(cfg.ConfigDir)
			if err != nil {
				return fmt.Errorf("admin token: %w (set %s for env-only operation)", err, cfg.TokenEnv)
			}
			adminToken = t
		}
	}
	st, err := store.Open(cfg.DataDir, store.Options{
		SegmentMaxBytes:   cfg.SegmentMaxBytes,
		RetentionAge:      time.Duration(cfg.RetentionDays) * 24 * time.Hour,
		RetentionMaxBytes: cfg.RetentionMaxBytes,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	reg, err := registry.Open(cfg.DataDir, time.Duration(cfg.TTLSeconds)*time.Second)
	if err != nil {
		return err
	}
	dir, err := contacts.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	srv, err := server.New(cfg, st, reg, dir, adminToken)
	if err != nil {
		return err
	}
	go func() {
		for {
			time.Sleep(time.Hour)
			_ = st.EnforceRetention(time.Now())
		}
	}()
	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	fmt.Fprintf(os.Stderr, "workwire serve: listening on %s (authMode=%s, dataDir=%s)\n", addr, cfg.AuthMode, cfg.DataDir)
	return http.ListenAndServe(addr, srv.Handler())
}

// --- client side ---

type client struct {
	base  string
	token string
	http  *http.Client
}

func newClient(cfg config.Config) *client {
	token := cfg.TokenFromEnv()
	if token == "" && cfg.ConfigDir != "" {
		if b, err := os.ReadFile(filepath.Join(cfg.ConfigDir, auth.TokenFileName)); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	return &client{
		base:  strings.TrimSuffix(cfg.HubURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 90 * time.Second},
	}
}

// asAgent switches the client to a per-agent secret from credentials.json.
func (c *client) asAgent(cfg config.Config, name string) error {
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir for credentials")
	}
	b, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "credentials.json"))
	if err != nil {
		return fmt.Errorf("read credentials.json: %w", err)
	}
	var creds map[string]struct {
		AgentID     string `json:"agentId"`
		AgentSecret string `json:"agentSecret"`
	}
	if err := json.Unmarshal(b, &creds); err != nil {
		return err
	}
	entry, ok := creds[name]
	if !ok {
		return fmt.Errorf("no stored credentials for agent %q", name)
	}
	c.token = entry.AgentSecret
	return nil
}

func (c *client) do(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("bad response (%d): %s", resp.StatusCode, string(b))
		}
	}
	return resp.StatusCode, nil
}

func cmdStatus(cfg config.Config) error {
	c := newClient(cfg)
	var out map[string]any
	code, err := c.do("GET", "/health", nil, &out)
	if err != nil {
		return fmt.Errorf("hub unreachable at %s: %w", cfg.HubURL, err)
	}
	if code != 200 || out["service"] != "workwire" {
		return fmt.Errorf("unexpected /health response (%d): %v", code, out)
	}
	fmt.Printf("workwire hub at %s: ok (schemaVersion=%v, apiVersion=%v)\n", cfg.HubURL, out["schemaVersion"], out["apiVersion"])
	return nil
}

func cmdSend(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "recipient name")
	text := fs.String("text", "", "message text")
	thread := fs.String("thread", "", "existing thread id")
	replyTo := fs.String("reply-to", "", "reply_to id (or \"last\")")
	as := fs.String("as", "", "act as a registered agent (uses credentials.json)")
	fs.Parse(args)
	if *to == "" || *text == "" {
		return fmt.Errorf("send requires --to and --text")
	}
	c := newClient(cfg)
	if *as != "" {
		if err := c.asAgent(cfg, *as); err != nil {
			return err
		}
	}
	body := map[string]any{"to": *to, "text": *text}
	if *thread != "" {
		body["thread_id"] = *thread
	}
	if *replyTo != "" {
		body["reply_to"] = *replyTo
	}
	var out map[string]any
	code, err := c.do("POST", "/send", body, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("send failed (%d): %v", code, out)
	}
	fmt.Printf("sent %v on thread %v\n", out["id"], out["thread_id"])
	return nil
}

func cmdInbox(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("inbox", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name (required)")
	since := fs.Int64("since", 0, "cursor")
	wait := fs.Int("wait", 0, "long-poll seconds")
	ctxDepth := fs.Int("context", -1, "context depth")
	as := fs.String("as", "", "act as a registered agent")
	fs.Parse(args)
	if *agent == "" {
		return fmt.Errorf("inbox requires --agent")
	}
	c := newClient(cfg)
	name := *as
	if name == "" {
		name = *agent
	}
	if err := c.asAgent(cfg, name); err == nil {
		// authenticated as the agent itself
	} // else fall back to the admin token
	q := url.Values{}
	q.Set("agent", *agent)
	q.Set("since", fmt.Sprint(*since))
	if *wait > 0 {
		q.Set("wait", fmt.Sprint(*wait))
	}
	if *ctxDepth >= 0 {
		q.Set("context", fmt.Sprint(*ctxDepth))
	}
	var out map[string]any
	code, err := c.do("GET", "/inbox?"+q.Encode(), nil, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("inbox failed (%d): %v", code, out)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

func cmdPeers(cfg config.Config) error {
	c := newClient(cfg)
	var agents struct {
		Agents []map[string]any `json:"agents"`
	}
	if code, err := c.do("GET", "/agents", nil, &agents); err != nil {
		return err
	} else if code != 200 {
		return fmt.Errorf("agents listing failed (%d)", code)
	}
	var cts struct {
		Contacts []map[string]any `json:"contacts"`
	}
	if code, err := c.do("GET", "/contacts", nil, &cts); err != nil {
		return err
	} else if code != 200 {
		return fmt.Errorf("contacts listing failed (%d)", code)
	}
	// Unified people view: agents + contacts, labeled by kind (contacts R8).
	for _, a := range agents.Agents {
		// Persona is who you are actually talking to (ADR-009); fall back to the
		// card description when a peer registered without one.
		about, _ := a["persona"].(string)
		if strings.TrimSpace(about) == "" {
			about = fmt.Sprint(a["description"])
		}
		kind, _ := a["kind"].(string)
		if kind == "" {
			kind = "agent"
		}
		// Provenance sits next to the name: which tree is talking (ADR-011).
		prov := ""
		if m, ok := a["origin"].(map[string]any); ok {
			prov = origin.FromMap(m).String()
		}
		fmt.Printf("%-7s  %-16v %-34s %v\n", kind, a["name"], prov, about)
	}
	for _, ct := range cts.Contacts {
		fmt.Printf("contact  %-24v verified=%v\n", ct["name"], ct["verified"])
	}
	if len(agents.Agents)+len(cts.Contacts) == 0 {
		fmt.Println("no peers")
	}
	return nil
}

func cmdAsk(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	as := fs.String("as", "", "act as a registered agent")
	timeout := fs.Duration("timeout", 5*time.Minute, "overall wait for the answer")
	rest, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire ask <agent> <question>")
	}
	target, question := rest[0], strings.Join(rest[1:], " ")
	c := newClient(cfg)
	if *as != "" {
		if err := c.asAgent(cfg, *as); err != nil {
			return err
		}
	}
	var out struct {
		ThreadID  string `json:"thread_id"`
		MessageID string `json:"message_id"`
		Error     string `json:"error"`
	}
	code, err := c.do("POST", "/agents/"+url.PathEscape(target)+"/ask", map[string]string{"text": question}, &out)
	if err != nil {
		return err
	}
	if code != 202 {
		return fmt.Errorf("ask failed (%d): %s", code, out.Error)
	}
	fmt.Fprintf(os.Stderr, "asked %s (thread %s); waiting for the answer...\n", target, out.ThreadID)
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		var tr struct {
			Messages []struct {
				ID      string `json:"id"`
				From    string `json:"from"`
				ReplyTo string `json:"reply_to"`
				Text    string `json:"text"`
				Kind    string `json:"kind"`
			} `json:"messages"`
		}
		q := url.Values{}
		q.Set("wait", fmt.Sprint(cfg.WaitDefault))
		q.Set("answer_to", out.MessageID)
		code, err := c.do("GET", "/threads/"+url.PathEscape(out.ThreadID)+"?"+q.Encode(), nil, &tr)
		if err != nil {
			return err
		}
		if code != 200 {
			return fmt.Errorf("thread poll failed (%d)", code)
		}
		for _, m := range tr.Messages {
			// Completion is reply_to == the question's message_id; context
			// entries never count (registry-a2a R8).
			if m.ReplyTo == out.MessageID && m.Kind != "context" {
				fmt.Printf("%s: %s\n", m.From, m.Text)
				return nil
			}
		}
	}
	return fmt.Errorf("no answer within %s", *timeout)
}

// parseInterspersed parses flags that may appear before, between, or after
// positional args (Go's flag package stops at the first positional).
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}
