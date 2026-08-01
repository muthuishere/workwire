// Package registry is the hub's dynamic agent registry: hub-issued
// agentId+secret, heartbeat/TTL liveness, 409+suggestion on name collision,
// a persisted last-seen cache, and the hub-side listen lease
// (registry-a2a R1–R5, R10; auth R3; ADR-007, ADR-008).
package registry

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/muthuishere/workwire/internal/envelope"
	"github.com/muthuishere/workwire/internal/origin"
)

// Peer kinds (ADR-011 §3). A peer declares one at registration; the default
// is an agent. Precedence at closure is decided by this field.
const (
	KindAgent = "agent"
	KindHuman = "human"
)

// NormalizeKind maps anything unrecognised to "agent" — the safe default:
// nobody gets human precedence by typo.
func NormalizeKind(k string) string {
	if k == KindHuman {
		return KindHuman
	}
	return KindAgent
}

// Skill is one entry of a card's skills[].
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// AskPolicy is "any" (default: any authenticated peer) or an allowPeers list.
type AskPolicy struct {
	Any        bool     `json:"any"`
	AllowPeers []string `json:"allowPeers,omitempty"`
}

// UnmarshalJSON accepts `"any"` or `{"allowPeers":[...]}`.
func (p *AskPolicy) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s != "any" && s != "" {
			return fmt.Errorf("invalid askPolicy %q", s)
		}
		p.Any = true
		p.AllowPeers = nil
		return nil
	}
	var obj struct {
		AllowPeers []string `json:"allowPeers"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	p.Any = false
	p.AllowPeers = obj.AllowPeers
	return nil
}

// Card is the registration body (registry-a2a R1).
type Card struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Skills       []Skill  `json:"skills,omitempty"`
	Project      string   `json:"project,omitempty"`
	// Persona is a short self-description derived by the skill from the
	// session's own CLAUDE.md / AGENTS.md and cwd: who this worker is, what
	// it owns, what it will not speak for (ADR-009). The hub neither invents
	// nor validates it.
	Persona   string     `json:"persona,omitempty"`
	AskPolicy *AskPolicy `json:"askPolicy,omitempty"`
	// Kind is "agent" (default) or "human" (ADR-011 §3).
	Kind string `json:"kind,omitempty"`
	// Origin is auto-derived provenance: which tree is talking (ADR-011 §1).
	// Sent at registration and refreshed on every heartbeat re-registration.
	Origin *origin.Info `json:"origin,omitempty"`
}

// Agent is a registered identity. SecretHash only — never the secret value.
type Agent struct {
	Name         string       `json:"name"`
	AgentID      string       `json:"agentId"`
	SecretHash   string       `json:"secretHash"`
	Description  string       `json:"description,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
	Skills       []Skill      `json:"skills,omitempty"`
	Project      string       `json:"project,omitempty"`
	Persona      string       `json:"persona,omitempty"`
	AskPolicy    *AskPolicy   `json:"askPolicy,omitempty"`
	Kind         string       `json:"kind,omitempty"`
	Origin       *origin.Info `json:"origin,omitempty"`
	LastSeen     time.Time    `json:"lastSeen"`
}

// IsHuman reports whether this peer registered as a person (ADR-011 §3).
func (a *Agent) IsHuman() bool { return a != nil && a.Kind == KindHuman }

// Lease is a hub-side listen lease (registry-a2a R10). In-memory only: after
// a hub restart the first acquire wins, which is safe (at most one listener).
type Lease struct {
	LeaseID string
	AgentID string
	Renewed time.Time
}

// Registry manages agents with a persisted last-seen cache in the data dir.
type Registry struct {
	mu     sync.Mutex
	path   string
	ttl    time.Duration
	agents map[string]*Agent // by name
	leases map[string]*Lease // by agent name
	// answerers records, per agent name, when something last declared itself
	// ATTACHED to answer. Deliberately separate from leases: the lease is a
	// delivery fact and this is an answerability fact — a listener can be
	// delivering into an inbox file nobody is reading. In-memory only —
	// after a hub restart nobody is answering until they say so again.
	answerers map[string]time.Time
	// groups is the audience map (ADR-012): group name -> member set. It
	// holds membership only; a group never holds a message.
	groups map[string]map[string]bool
	now    func() time.Time
}

// Open loads the persisted registry cache from dataDir (registry-a2a R5).
func Open(dataDir string, ttl time.Duration) (*Registry, error) {
	r := &Registry{
		path:      filepath.Join(dataDir, "registry.json"),
		ttl:       ttl,
		agents:    map[string]*Agent{},
		leases:    map[string]*Lease{},
		answerers: map[string]time.Time{},
		now:       time.Now,
	}
	if b, err := os.ReadFile(r.path); err == nil {
		var list []*Agent
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("parse %s: %w", r.path, err)
		}
		for _, a := range list {
			r.agents[a.Name] = a
		}
	}
	r.loadGroupsLocked()
	return r, nil
}

// SetClock overrides the clock (tests).
func (r *Registry) SetClock(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func secretMatches(a *Agent, secret string) bool {
	if secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.SecretHash), []byte(hashSecret(secret))) == 1
}

func (r *Registry) persistLocked() {
	list := make([]*Agent, 0, len(r.agents))
	for _, a := range r.agents {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		_ = os.Rename(tmp, r.path)
	}
}

// RegisterResult describes the outcome of a registration attempt.
type RegisterResult struct {
	Created    bool   // 201: fresh name, secret minted
	Updated    bool   // 200: re-register by the credential holder
	Conflict   bool   // 409: name taken
	Suggestion string // free-name suggestion on conflict
	Agent      *Agent
	Secret     string // returned exactly once, on Created
	// KindConflict is a 409 of a different shape: the caller holds the right
	// credential but asked to change an established `kind`. KindWas names the
	// kind that stands (ADR-011 §3).
	KindConflict bool
	KindWas      string
	// TreeConflict is a 409 of a third shape: this working tree already has a
	// live peer under a DIFFERENT name (registry-a2a R12). TreeHolder names
	// it. Two names for one codebase make every question a coin flip — the
	// asker may address the half with nobody attached, and one session can
	// appear in a thread as two voices.
	TreeConflict bool
	TreeHolder   string
}

// Register implements POST /agents: first registration mints identity,
// re-registration with the correct secret updates the card without rotating
// the secret, and a taken name yields 409 with a `<name>-N` suggestion.
func (r *Registry) Register(card Card, presentedSecret string) RegisterResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	// A peer name may never collide with a group name (ADR-012): `@platform`
	// the audience and `platform` the peer cannot both exist.
	if r.nameCollidesLocked(card.Name) {
		return RegisterResult{Conflict: true, Suggestion: r.suggestLocked(card.Name)}
	}
	// One working tree, one peer. The hub already stamps provenance on every
	// card, so it can see what happened to `koine`/`koine-main` on
	// 2026-08-01 and refuse it (registry-a2a R12). Only LIVE peers conflict:
	// a leftover from a session that has gone is not a rival, it is litter,
	// and `DELETE /agents/<name>` is how it goes. A card with no cwd matches
	// nothing.
	if holder := r.treeHolderLocked(card); holder != "" {
		return RegisterResult{TreeConflict: true, TreeHolder: holder}
	}
	existing, ok := r.agents[card.Name]
	if ok {
		if !secretMatches(existing, presentedSecret) {
			return RegisterResult{Conflict: true, Suggestion: r.suggestLocked(card.Name)}
		}
		existing.Description = card.Description
		existing.Capabilities = card.Capabilities
		existing.Skills = card.Skills
		existing.Project = card.Project
		existing.Persona = card.Persona
		existing.AskPolicy = card.AskPolicy
		// `kind` is pinned once established: a peer that registered as an
		// agent cannot quietly acquire human decision precedence (ADR-011 §3)
		// by re-registering, and a human cannot be demoted by a card that
		// simply omitted the flag. A card with no `kind` keeps what stands.
		if card.Kind != "" && NormalizeKind(card.Kind) != NormalizeKind(existing.Kind) {
			return RegisterResult{
				KindConflict: true,
				KindWas:      NormalizeKind(existing.Kind),
				Agent:        existing,
			}
		}
		existing.Kind = NormalizeKind(existing.Kind)
		if card.Origin != nil {
			existing.Origin = card.Origin
		}
		existing.LastSeen = r.now()
		r.persistLocked()
		return RegisterResult{Updated: true, Agent: existing}
	}
	secret := envelope.NewID("s")
	a := &Agent{
		Name:         card.Name,
		AgentID:      envelope.NewID("a"),
		SecretHash:   hashSecret(secret),
		Description:  card.Description,
		Capabilities: card.Capabilities,
		Skills:       card.Skills,
		Project:      card.Project,
		Persona:      card.Persona,
		AskPolicy:    card.AskPolicy,
		Kind:         NormalizeKind(card.Kind),
		Origin:       card.Origin,
		LastSeen:     r.now(),
	}
	r.agents[card.Name] = a
	// Every peer joins the lobby at registration (ADR-012). Only on CREATE:
	// re-registration must never undo a deliberate `group leave @all`.
	if r.groups[DefaultGroup] == nil {
		r.groups[DefaultGroup] = map[string]bool{}
	}
	r.groups[DefaultGroup][a.Name] = true
	r.persistGroupsLocked()
	r.persistLocked()
	return RegisterResult{Created: true, Agent: a, Secret: secret}
}

// treeHolderLocked names a LIVE peer, other than card.Name, already speaking
// for card's working tree. Empty when there is none.
// The tree is identified by repo@branch when provenance has them, and by cwd
// otherwise. repo@branch is the stronger key and the one that matters: on
// 2026-08-01 `muthuishere/toolnexus@cljc` was on the wire THREE times — as
// `clojure`, `toolnexus-cljc` and `toolnexus-clojure`, all at 2f11e8a — from
// paths that differed only by which worktree the session happened to open.
// Peers then sent to whichever alias they had seen last. Same repo, same
// branch, same commit is one voice, whatever the directory is called.
func (r *Registry) treeHolderLocked(card Card) string {
	if card.Origin == nil {
		return ""
	}
	key := treeKey(card.Origin)
	if key == "" {
		return ""
	}
	now := r.now()
	for name, a := range r.agents {
		if name == card.Name || a.Origin == nil {
			continue
		}
		if treeKey(a.Origin) != key {
			continue
		}
		if now.Sub(a.LastSeen) > r.ttl {
			continue // a dead registration blocks nothing
		}
		return name
	}
	return ""
}

// treeKey is the identity of a working tree: `repo@branch` when both are
// known, else the absolute cwd, else nothing (a peer with no provenance
// matches nobody and blocks nobody).
func treeKey(o *origin.Info) string {
	if o == nil {
		return ""
	}
	if o.Repo != "" && o.Branch != "" {
		return o.Repo + "@" + o.Branch
	}
	return o.Cwd
}

func (r *Registry) suggestLocked(name string) string {
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", name, i)
		_, taken := r.agents[cand]
		if !taken && !r.nameCollidesLocked(cand) {
			return cand
		}
	}
}

// Authenticate resolves an agent by secret value.
func (r *Registry) Authenticate(secret string) (*Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, a := range r.agents {
		if secretMatches(a, secret) {
			return a, true
		}
	}
	return nil, false
}

// Get returns an agent by name regardless of liveness (registry is
// discovery-only; identity persists past TTL — registry-a2a R5, R7).
func (r *Registry) Get(name string) (*Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[name]
	return a, ok
}

// Forget removes an agent from the registry entirely: the identity, its
// credential, its lease and its group memberships. Nothing in the envelope
// store is touched — what a peer said stays said, and threads keep their
// history (ADR-008: deletion is a tombstone, never a rewrite).
//
// This exists because a peer can be RENAMED — a session that used to join as
// `api` now joins as `api-main` — and without a way to drop the old identity
// the registry accumulates ghosts that `peers` lists and `ask` can address,
// each one a listener nobody will ever start again.
func (r *Registry) Forget(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[name]; !ok {
		return false
	}
	delete(r.agents, name)
	delete(r.leases, name)
	r.persistLocked()
	r.forgetGroupsLocked(name)
	return true
}

// Touch refreshes liveness: ANY authenticated request from an agent counts
// as a heartbeat (registry-a2a R3, ADR-008). It also renews the agent's
// listen lease when it holds one (registry-a2a R10 renew-rides-heartbeat).
func (r *Registry) Touch(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[name]
	if !ok {
		return
	}
	a.LastSeen = r.now()
	// Renewal rides liveness only while the lease is still live: an
	// already-lapsed lease is claimable and never resurrected implicitly.
	if l, ok := r.leases[name]; ok && l.AgentID == a.AgentID && a.LastSeen.Sub(l.Renewed) <= r.ttl {
		l.Renewed = a.LastSeen
	}
	r.persistLocked()
}

// Live lists agents within TTL (registry-a2a R4). No secret material.
func (r *Registry) Live() []*Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var out []*Agent
	for _, a := range r.agents {
		if now.Sub(a.LastSeen) <= r.ttl {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// LeaseResult is the outcome of a lease acquire/renew.
type LeaseResult struct {
	OK        bool
	LeaseID   string
	TTL       int
	Holder    string
	ExpiresAt time.Time
}

// AcquireLease implements POST /agents/<name>/listen-lease: 200 when free or
// expired (new leaseId), 200 renew when the current leaseId is presented,
// 409 with holder/expiry while another live lease exists.
func (r *Registry) AcquireLease(name, presentedLeaseID string) LeaseResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[name]
	if !ok {
		return LeaseResult{}
	}
	ttlSecs := int(r.ttl / time.Second)
	now := r.now()
	l, held := r.leases[name]
	if held && now.Sub(l.Renewed) <= r.ttl {
		if presentedLeaseID == l.LeaseID {
			l.Renewed = now
			return LeaseResult{OK: true, LeaseID: l.LeaseID, TTL: ttlSecs}
		}
		return LeaseResult{OK: false, Holder: l.LeaseID, ExpiresAt: l.Renewed.Add(r.ttl)}
	}
	nl := &Lease{LeaseID: envelope.NewID("l"), AgentID: a.AgentID, Renewed: now}
	r.leases[name] = nl
	return LeaseResult{OK: true, LeaseID: nl.LeaseID, TTL: ttlSecs}
}

// ListenerLive reports whether a live listen lease exists for the agent —
// i.e. whether anyone is actually there to answer right now, as opposed to
// merely being registered. Discovery (Live/Get) is deliberately looser.
func (r *Registry) ListenerLive(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[name]
	return ok && r.now().Sub(l.Renewed) <= r.ttl
}

// DeclareAnswering records (or clears) a peer's statement that something is
// attached to ANSWER, not merely to receive. A lease says a listener is
// delivering questions into a session inbox; it says nothing about whether
// anyone is reading them. Only the peer itself can know, so it declares, and
// the declaration ages out on the same TTL as liveness — an answerer that
// stopped renewing has gone, exactly like a peer that stopped heartbeating.
func (r *Registry) DeclareAnswering(name string, attached bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[name]; !ok {
		return false
	}
	if !attached {
		delete(r.answerers, name)
		return true
	}
	r.answerers[name] = r.now()
	return true
}

// AnswererLive reports whether a live answerer declaration exists — the
// honest answer to "will this question be answered now?", as distinct from
// ListenerLive's "is it being delivered?".
func (r *Registry) AnswererLive(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.answerers[name]
	return ok && r.now().Sub(at) <= r.ttl
}

// ReleaseLease implements DELETE /agents/<name>/listen-lease with the
// current leaseId. Returns false when the leaseId does not match a live lease.
func (r *Registry) ReleaseLease(name, leaseID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.leases[name]
	if !ok || l.LeaseID != leaseID {
		return false
	}
	delete(r.leases, name)
	return true
}

// AskAllowed enforces the target's ask policy against the authenticated
// asker before routing (auth R8). Default: any authenticated peer.
func AskAllowed(target *Agent, asker string) bool {
	if target.AskPolicy == nil || target.AskPolicy.Any {
		return true
	}
	for _, p := range target.AskPolicy.AllowPeers {
		if p == asker {
			return true
		}
	}
	return false
}
