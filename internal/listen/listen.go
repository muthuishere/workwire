// Package listen is the dumb waiter of ADR-003: it long-polls the agent's
// hub inbox and appends each delivered question as one NDJSON line to the
// session inbox file — the session answers from its own live context.
// No LLM calls, no answer generation, ever (agent-skill R5).
package listen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options wires a Runner. ConfigDir holds credentials.json and the
// sessions/<agent>/ state dir; HubURL is the hub base.
type Options struct {
	Agent      string
	HubURL     string
	ConfigDir  string
	AdminToken string // registration bootstrap only (never printed)
	Wait       int    // long-poll seconds
	Context    int    // context depth attached at read time
	// Persona is the session's short self-description (ADR-009), derived by
	// the skill from the repo's own CLAUDE.md / AGENTS.md; sent at
	// registration so peers know which vantage point is talking.
	Persona string
	Heartbeat  time.Duration
	InboxPath  string // override; default <ConfigDir>/sessions/<agent>/inbox.ndjson
	// RotateMaxBytes rotates (truncates) the inbox file once it exceeds this
	// size AND the consumer's persisted offset says it is fully consumed.
	RotateMaxBytes int64
	Logf           func(format string, args ...any)
}

// State is the persisted poll cursor plus a bounded dedupe window of
// recently delivered envelope ids (at-least-once → exactly-once append).
type State struct {
	Next      int64    `json:"next"`
	Delivered []string `json:"delivered,omitempty"`
}

const dedupeWindow = 1000

// Runner is one listen loop for one agent.
type Runner struct {
	opts      Options
	http      *http.Client
	secret    string
	agentName string // possibly the hub-suggested name
	leaseID   string
	sessDir   string
	state     State
	delivered map[string]bool
}

// New prepares a Runner: resolves paths and loads persisted state.
func New(opts Options) (*Runner, error) {
	if opts.Agent == "" {
		return nil, fmt.Errorf("agent name is required")
	}
	if opts.Wait <= 0 {
		opts.Wait = 25
	}
	if opts.Context < 0 {
		opts.Context = 5
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 30 * time.Second
	}
	if opts.RotateMaxBytes <= 0 {
		opts.RotateMaxBytes = 8 << 20
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	r := &Runner{
		opts:      opts,
		http:      &http.Client{Timeout: time.Duration(opts.Wait+30) * time.Second},
		agentName: opts.Agent,
		sessDir:   filepath.Join(opts.ConfigDir, "sessions", opts.Agent),
		delivered: map[string]bool{},
	}
	if err := os.MkdirAll(r.sessDir, 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(r.statePath()); err == nil {
		_ = json.Unmarshal(b, &r.state)
		for _, id := range r.state.Delivered {
			r.delivered[id] = true
		}
	}
	return r, nil
}

func (r *Runner) statePath() string { return filepath.Join(r.sessDir, "cursor.json") }

// InboxPath is the session inbox file the running session tails.
func (r *Runner) InboxPath() string {
	if r.opts.InboxPath != "" {
		return r.opts.InboxPath
	}
	return filepath.Join(r.sessDir, "inbox.ndjson")
}

// offsetPath is where the session-side consumer persists its byte offset
// (Spike-01 mechanism (a)); the rotation guard reads it, never writes it.
func (r *Runner) offsetPath() string { return filepath.Join(r.sessDir, "inbox.offset") }

// AgentName is the identity actually registered (hub may have suggested one).
func (r *Runner) AgentName() string { return r.agentName }

// adoptName re-homes session state under the registered name when the hub
// suggested a different one, so cursor and inbox file follow the identity.
func (r *Runner) adoptName(name string) {
	if name == r.agentName {
		return
	}
	r.agentName = name
	r.sessDir = filepath.Join(r.opts.ConfigDir, "sessions", name)
	_ = os.MkdirAll(r.sessDir, 0o755)
	r.state = State{}
	r.delivered = map[string]bool{}
	if b, err := os.ReadFile(r.statePath()); err == nil {
		_ = json.Unmarshal(b, &r.state)
		for _, id := range r.state.Delivered {
			r.delivered[id] = true
		}
	}
}

func (r *Runner) saveState() error {
	b, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	return writeFileAtomic(r.statePath(), append(b, '\n'), 0o644)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// --- hub client ---

func (r *Runner) do(method, path, token string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimSuffix(r.opts.HubURL, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.http.Do(req)
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

// EnsureRegistered loads stored credentials for the agent, or registers via
// POST /agents on first run and stores the hub-issued secret 0600. A 409
// name conflict adopts the hub's suggestion (agent-skill R3) — never a
// silent takeover.
func (r *Runner) EnsureRegistered() error {
	creds, err := LoadCredentials(r.opts.ConfigDir)
	if err != nil {
		return err
	}
	if c, ok := creds[r.agentName]; ok {
		r.secret = c.AgentSecret
		// Re-register/heartbeat with the stored secret: same identity.
		card := r.card(r.agentName)
		if code, err := r.do("POST", "/agents", r.secret, card, nil); err != nil {
			return fmt.Errorf("re-register: %w", err)
		} else if code != 200 && code != 201 {
			return fmt.Errorf("re-register as %s failed (%d) — stored credentials may be stale", r.agentName, code)
		}
		return nil
	}
	name := r.agentName
	for attempt := 0; attempt < 5; attempt++ {
		var out struct {
			AgentID     string `json:"agentId"`
			AgentSecret string `json:"agentSecret"`
			Suggestion  string `json:"suggestion"`
			Error       string `json:"error"`
		}
		code, err := r.do("POST", "/agents", r.opts.AdminToken, r.card(name), &out)
		if err != nil {
			return fmt.Errorf("register: %w", err)
		}
		switch code {
		case 201:
			r.adoptName(name)
			r.secret = out.AgentSecret
			return SaveCredential(r.opts.ConfigDir, name, Credential{AgentID: out.AgentID, AgentSecret: out.AgentSecret})
		case 409:
			if out.Suggestion == "" {
				return fmt.Errorf("name %q taken and no suggestion offered", name)
			}
			r.opts.Logf("name %q taken; registering as %q", name, out.Suggestion)
			name = out.Suggestion
		default:
			return fmt.Errorf("register failed (%d): %s", code, out.Error)
		}
	}
	return fmt.Errorf("could not register: every suggested name was taken")
}

func (r *Runner) card(name string) map[string]any {
	cwd, _ := os.Getwd()
	card := map[string]any{
		"name":         name,
		"project":      cwd,
		"capabilities": []string{"ask"},
	}
	if r.opts.Persona != "" {
		card["persona"] = r.opts.Persona
	}
	return card
}

// AcquireLease takes (or renews) the hub-side listen lease — the
// cross-machine singleton authority (agent-skill R4). A 409 means another
// live listener holds it.
func (r *Runner) AcquireLease() error {
	var out struct {
		LeaseID   string `json:"leaseId"`
		Holder    string `json:"holder"`
		ExpiresAt string `json:"expiresAt"`
	}
	code, err := r.do("POST", "/agents/"+url.PathEscape(r.agentName)+"/listen-lease",
		r.secret, map[string]string{"leaseId": r.leaseID}, &out)
	if err != nil {
		return err
	}
	switch code {
	case 200:
		r.leaseID = out.LeaseID
		return nil
	case 409:
		return fmt.Errorf("listen lease for %s is held elsewhere (expires %s) — another host is the listener", r.agentName, out.ExpiresAt)
	default:
		return fmt.Errorf("lease acquire failed (%d)", code)
	}
}

// ReleaseLease gives the lease back on graceful shutdown.
func (r *Runner) ReleaseLease() {
	if r.leaseID == "" {
		return
	}
	_, _ = r.do("DELETE", "/agents/"+url.PathEscape(r.agentName)+"/listen-lease",
		r.secret, map[string]string{"leaseId": r.leaseID}, nil)
	r.leaseID = ""
}

// PollOnce runs one long-poll cycle: fetch, append new envelopes to the
// session inbox file, persist the cursor. Returns how many lines it appended.
func (r *Runner) PollOnce() (int, error) {
	q := url.Values{}
	q.Set("agent", r.agentName)
	q.Set("since", fmt.Sprint(r.state.Next))
	q.Set("wait", fmt.Sprint(r.opts.Wait))
	q.Set("context", fmt.Sprint(r.opts.Context))
	var out struct {
		Messages []json.RawMessage `json:"messages"`
		Next     int64             `json:"next"`
		Reset    bool              `json:"reset"`
	}
	code, err := r.do("GET", "/inbox?"+q.Encode(), r.secret, nil, &out)
	if err != nil {
		return 0, err
	}
	if code != 200 {
		return 0, fmt.Errorf("inbox poll failed (%d)", code)
	}
	if out.Reset {
		// Cursor outside retained history: adopt the hub's cursor as
		// authoritative — no silent skip, no crash (agent-skill R5).
		r.opts.Logf("cursor reset by hub: %d -> %d", r.state.Next, out.Next)
	}
	n, err := r.Deliver(out.Messages)
	if err != nil {
		return n, err
	}
	r.state.Next = out.Next
	if err := r.saveState(); err != nil {
		return n, err
	}
	r.maybeRotate()
	return n, nil
}

// Deliver appends each not-yet-seen envelope (with its attached context) as
// one NDJSON line, deduped by envelope id.
func (r *Runner) Deliver(msgs []json.RawMessage) (int, error) {
	if len(msgs) == 0 {
		return 0, nil
	}
	f, err := os.OpenFile(r.InboxPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	appended := 0
	for _, raw := range msgs {
		var meta struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil || meta.ID == "" {
			continue
		}
		if r.delivered[meta.ID] {
			continue
		}
		line := append(bytes.TrimSpace(raw), '\n')
		if _, err := f.Write(line); err != nil {
			return appended, err
		}
		appended++
		r.delivered[meta.ID] = true
		r.state.Delivered = append(r.state.Delivered, meta.ID)
		if len(r.state.Delivered) > dedupeWindow {
			drop := r.state.Delivered[:len(r.state.Delivered)-dedupeWindow]
			for _, id := range drop {
				delete(r.delivered, id)
			}
			r.state.Delivered = append([]string(nil), r.state.Delivered[len(r.state.Delivered)-dedupeWindow:]...)
		}
	}
	return appended, nil
}

// maybeRotate truncates the inbox file when it is oversized AND the
// consumer's persisted offset covers it entirely; the offset is rebased to 0
// so nothing is lost or re-delivered (agent-skill R5 rotation).
func (r *Runner) maybeRotate() {
	fi, err := os.Stat(r.InboxPath())
	if err != nil || fi.Size() < r.opts.RotateMaxBytes {
		return
	}
	b, err := os.ReadFile(r.offsetPath())
	if err != nil {
		return
	}
	var off int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &off); err != nil || off < fi.Size() {
		return // unconsumed tail — never rotate under the consumer
	}
	if err := os.Truncate(r.InboxPath(), 0); err != nil {
		return
	}
	_ = writeFileAtomic(r.offsetPath(), []byte("0\n"), 0o644)
	r.opts.Logf("rotated inbox file (%d bytes fully consumed)", fi.Size())
}

// Run is the loop: lease renewal on a ticker, long-polls in between, until
// stop is closed. Graceful shutdown releases the lease.
func (r *Runner) Run(stop <-chan struct{}) error {
	if err := r.AcquireLease(); err != nil {
		return err
	}
	defer r.ReleaseLease()
	r.opts.Logf("listening as %s (inbox file %s)", r.agentName, r.InboxPath())
	renew := time.NewTicker(r.opts.Heartbeat)
	defer renew.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-renew.C:
			if err := r.AcquireLease(); err != nil {
				return fmt.Errorf("lease lost: %w", err)
			}
		default:
		}
		n, err := r.PollOnce()
		if err != nil {
			r.opts.Logf("poll error: %v (retrying)", err)
			select {
			case <-stop:
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if n > 0 {
			r.opts.Logf("delivered %d envelope(s)", n)
		}
	}
}

// --- credentials (compatible with cmd/workwire client.asAgent) ---

// Credential is one entry of ~/.config/workwire/credentials.json.
type Credential struct {
	AgentID     string `json:"agentId"`
	AgentSecret string `json:"agentSecret"`
}

// LoadCredentials reads the credentials map; a missing file is empty.
func LoadCredentials(configDir string) (map[string]Credential, error) {
	b, err := os.ReadFile(filepath.Join(configDir, "credentials.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Credential{}, nil
		}
		return nil, err
	}
	out := map[string]Credential{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse credentials.json: %w", err)
	}
	return out, nil
}

// SaveCredential merges one entry and writes the file with mode 0600
// (agent-skill R3). Secret values are never logged.
func SaveCredential(configDir, name string, c Credential) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	creds, err := LoadCredentials(configDir)
	if err != nil {
		return err
	}
	creds[name] = c
	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(configDir, "credentials.json"), append(b, '\n'), 0o600)
}
