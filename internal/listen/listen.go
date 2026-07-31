// Package listen is the dumb waiter of ADR-003: it long-polls the agent's
// hub inbox and appends each delivered question as one NDJSON line to the
// session inbox file — the session answers from its own live context.
// No LLM calls, no answer generation, ever (agent-skill R5).
package listen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muthuishere/workwire/internal/origin"
	"github.com/muthuishere/workwire/internal/registry"
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
	// Kind is "agent" (default) or "human" (ADR-011 §3).
	Kind string
	// Groups are the audiences this peer declared in its own AGENTS.md /
	// CLAUDE.md (ADR-012). The listener joins them at startup — joining is
	// always self-service, so this only ever adds THIS peer.
	Groups []string
	// OriginDir is the working tree provenance is derived from (default cwd).
	OriginDir string
	Heartbeat time.Duration
	InboxPath string // override; default <ConfigDir>/sessions/<agent>/inbox.ndjson
	// RotateMaxBytes rotates (truncates) the inbox file once it exceeds this
	// size AND the consumer's persisted offset says it is fully consumed.
	RotateMaxBytes int64
	Logf           func(format string, args ...any)
	// MaxRetries is the escape hatch: 0 (the default) means retry forever —
	// the listener must never die because the hub blinked. A positive value
	// gives up after that many consecutive failed attempts.
	MaxRetries int
	// BaseBackoff/MaxBackoff bound the exponential retry delay for transient
	// conditions; ContendedBackoff is the slow cadence used when the lease is
	// legitimately held by another host (don't hammer a live peer).
	BaseBackoff      time.Duration
	MaxBackoff       time.Duration
	ContendedBackoff time.Duration
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
	// cond is the last logged connection condition, so a long outage logs one
	// line per state transition instead of one per attempt.
	cond condition
	// contendedUntil is the expiry the hub reported for a lease held by
	// another host, used to retry on roughly lease cadence.
	contendedUntil time.Time
}

// condition classifies why the listener is not currently connected. Every one
// of them is recoverable: only a signal or the local flock ends the loop.
type condition int

const (
	condOK         condition = iota // connected and polling
	condUnreachable                 // hub down / restarting / network error
	condLeaseLost                   // lease gone or the hub forgot it
	condRejected                    // our credential or agent is unknown to the hub
	condContended                   // another host legitimately holds the lease
	condOther                       // anything else transient
)

func (c condition) String() string {
	switch c {
	case condOK:
		return "connected"
	case condUnreachable:
		return "hub unreachable"
	case condLeaseLost:
		return "lease lost"
	case condRejected:
		return "credential rejected"
	case condContended:
		return "lease held elsewhere"
	default:
		return "hub error"
	}
}

// hubError carries the HTTP status (0 = transport failure) so the run loop
// can tell "hub is down" from "hub says you are nobody".
type hubError struct {
	Status int
	Op     string
	Msg    string
	Err    error
}

func (e *hubError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
	if e.Msg != "" {
		return fmt.Sprintf("%s failed (%d): %s", e.Op, e.Status, e.Msg)
	}
	return fmt.Sprintf("%s failed (%d)", e.Op, e.Status)
}

func (e *hubError) Unwrap() error { return e.Err }

// classify maps an error onto the condition the run loop reacts to.
func classify(err error) condition {
	var he *hubError
	if !errors.As(err, &he) {
		return condOther
	}
	switch {
	case he.Status == 0:
		return condUnreachable
	case he.Status == http.StatusUnauthorized || he.Status == http.StatusForbidden:
		return condRejected
	case he.Status == http.StatusNotFound:
		// The hub does not know this agent (data dir wiped, aged out) — for a
		// lease call that also means the lease is gone with it.
		return condRejected
	case he.Status == http.StatusConflict || he.Status == http.StatusGone:
		// A conflict while we still hold a leaseId usually means the hub
		// forgot it (restart) and ours is dead; the run loop clears it and
		// retries before concluding another host really is the listener.
		return condLeaseLost
	default:
		return condOther
	}
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
	if opts.BaseBackoff <= 0 {
		opts.BaseBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	if opts.ContendedBackoff <= 0 {
		opts.ContendedBackoff = 30 * time.Second
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
		code, err := r.do("POST", "/agents", r.secret, card, nil)
		if err != nil {
			return &hubError{Op: "re-register", Err: err}
		}
		switch {
		case code == 200 || code == 201:
			return nil
		case code == 401 || code == 403 || code == 404:
			// The hub no longer knows this credential (data dir wiped, agent
			// aged out). Fall through to a fresh registration rather than
			// dying: the persisted cursor is keyed by name and survives.
			r.opts.Logf("stored credential for %s rejected (%d) — re-registering", r.agentName, code)
			r.secret = ""
		default:
			return &hubError{Op: "re-register", Status: code}
		}
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
			return &hubError{Op: "register", Err: err}
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
			return &hubError{Op: "register", Status: code, Msg: out.Error}
		}
	}
	return fmt.Errorf("could not register: every suggested name was taken")
}

// card is the registration body. Provenance is re-derived every time it is
// built, so the heartbeat re-registration picks up a branch switch made
// mid-session (ADR-011 §1).
func (r *Runner) card(name string) map[string]any {
	dir := r.opts.OriginDir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	card := map[string]any{
		"name":         name,
		"project":      dir,
		"capabilities": []string{"ask"},
		"kind":         registry.NormalizeKind(r.opts.Kind),
		"origin":       origin.Detect(dir),
	}
	if r.opts.Persona != "" {
		card["persona"] = r.opts.Persona
	}
	return card
}

// Heartbeat re-registers the current card, refreshing provenance on the hub.
// A failure is not fatal: the listener keeps listening with stale provenance.
func (r *Runner) Heartbeat() error {
	if r.secret == "" {
		return nil
	}
	code, err := r.do("POST", "/agents", r.secret, r.card(r.agentName), nil)
	if err != nil {
		return err
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("heartbeat re-register failed (%d)", code)
	}
	return nil
}

// JoinDeclaredGroups joins every group the peer declared (ADR-012). Failure
// is not fatal: a peer that cannot join an audience still listens, it is
// just addressable only by name. @all is joined by the hub at registration.
func (r *Runner) JoinDeclaredGroups() {
	for _, g := range r.opts.Groups {
		name := registry.NormalizeGroup(g)
		if name == "" {
			continue
		}
		code, err := r.do("POST", "/groups/"+url.PathEscape(name)+"/join", r.secret, map[string]string{}, nil)
		if err != nil || code != 200 {
			r.opts.Logf("could not join %s (%d): %v", name, code, err)
			continue
		}
		r.opts.Logf("joined %s", name)
	}
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
		return &hubError{Op: "lease acquire", Err: err}
	}
	switch code {
	case 200:
		r.leaseID = out.LeaseID
		r.contendedUntil = time.Time{}
		return nil
	case 409:
		r.contendedUntil = time.Time{}
		if t, perr := time.Parse(time.RFC3339Nano, out.ExpiresAt); perr == nil {
			r.contendedUntil = t
		}
		return &hubError{Op: "lease acquire", Status: 409,
			Msg: fmt.Sprintf("listen lease for %s is held elsewhere (expires %s)", r.agentName, out.ExpiresAt)}
	default:
		return &hubError{Op: "lease acquire", Status: code}
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
		return 0, &hubError{Op: "inbox poll", Err: err}
	}
	if code != 200 {
		return 0, &hubError{Op: "inbox poll", Status: code}
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

// connect makes the listener usable: registered (recovering the credential if
// the hub rejected it) and holding the listen lease.
func (r *Runner) connect() error {
	if r.secret == "" {
		if err := r.EnsureRegistered(); err != nil {
			return err
		}
	}
	return r.AcquireLease()
}

// backoffFor is the delay before the next attempt: exponential with jitter for
// transient conditions, roughly lease cadence for legitimate contention (a
// live peer must not be hammered — this listener just waits its turn).
func (r *Runner) backoffFor(c condition, attempt int) time.Duration {
	if c == condContended {
		d := r.opts.ContendedBackoff
		if !r.contendedUntil.IsZero() {
			if until := time.Until(r.contendedUntil); until > 0 && until < d {
				d = until + time.Second
			}
		}
		return jitter(d)
	}
	d := r.opts.BaseBackoff
	for i := 1; i < attempt && d < r.opts.MaxBackoff; i++ {
		d *= 2
	}
	if d > r.opts.MaxBackoff {
		d = r.opts.MaxBackoff
	}
	return jitter(d)
}

// jitter spreads retries by ±25% so many listeners don't stampede a hub that
// just came back.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := time.Duration(rand.Int63n(int64(d/2) + 1))
	return d - d/4 + delta
}

// note logs a condition change once — an hour-long outage is one line, not
// eighteen hundred.
func (r *Runner) note(c condition, err error) {
	if c == r.cond {
		return
	}
	r.cond = c
	if c == condOK {
		r.opts.Logf("hub reachable again — listening as %s", r.agentName)
		return
	}
	r.opts.Logf("%s: %v — retrying (the listener does not exit)", c, err)
}

// errStopped is the internal signal that the stop channel fired mid-backoff;
// it is never returned to the caller (a signalled shutdown is not an error).
var errStopped = errors.New("listener stopped")

// stopErr maps the internal sentinel back to a clean nil exit.
func stopErr(err error) error {
	if errors.Is(err, errStopped) {
		return nil
	}
	return err
}

// sleep waits d, or returns false if the listener was asked to stop.
func sleep(stop <-chan struct{}, d time.Duration) bool {
	select {
	case <-stop:
		return false
	case <-time.After(d):
		return true
	}
}

// Run is the loop: connect (register + lease), renew on a ticker, long-poll in
// between, until stop is closed. It NEVER exits on a transient condition —
// hub down, hub restarted, lease forgotten, credential rejected are all
// retried forever with backoff. Only a signal (stop) ends it, and graceful
// shutdown releases the lease.
func (r *Runner) Run(stop <-chan struct{}) error {
	defer r.ReleaseLease()
	connected := false
	attempt := 0
	renew := time.NewTicker(r.opts.Heartbeat)
	defer renew.Stop()

	// fail records a recoverable condition and waits out its backoff. It
	// returns nil to keep looping, errStopped when the listener was signalled,
	// or the underlying error when --max-retries was exhausted.
	fail := func(err error) error {
		attempt++
		c := classify(err)
		if c == condLeaseLost {
			if r.leaseID != "" {
				// Our leaseId may be the reason the hub said no; drop it and
				// re-acquire from scratch on the next attempt.
				r.leaseID = ""
			} else {
				c = condContended
			}
		}
		if c == condRejected {
			// Re-run the registration path from scratch on the next attempt;
			// the persisted cursor is keyed by name and is not touched.
			r.secret = ""
		}
		connected = false
		r.note(c, err)
		if r.opts.MaxRetries > 0 && attempt >= r.opts.MaxRetries {
			r.opts.Logf("giving up after %d attempts (--max-retries)", attempt)
			return err
		}
		if !sleep(stop, r.backoffFor(c, attempt)) {
			return errStopped
		}
		return nil
	}

	for {
		select {
		case <-stop:
			return nil
		default:
		}
		if !connected {
			if err := r.connect(); err != nil {
				if ferr := fail(err); ferr != nil {
					return stopErr(ferr)
				}
				continue
			}
			connected = true
			attempt = 0
			r.note(condOK, nil)
			r.JoinDeclaredGroups()
			r.opts.Logf("listening as %s (inbox file %s)", r.agentName, r.InboxPath())
		}
		select {
		case <-stop:
			return nil
		case <-renew.C:
			if err := r.AcquireLease(); err != nil {
				if ferr := fail(err); ferr != nil {
					return stopErr(ferr)
				}
				continue
			}
			// Provenance is refreshed on every heartbeat: people switch
			// branches mid-session (ADR-011 §1).
			if err := r.Heartbeat(); err != nil {
				r.opts.Logf("provenance refresh failed: %v", err)
			}
		default:
		}
		n, err := r.PollOnce()
		if err != nil {
			if ferr := fail(err); ferr != nil {
				return stopErr(ferr)
			}
			continue
		}
		attempt = 0
		if r.cond != condOK {
			r.note(condOK, nil)
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
