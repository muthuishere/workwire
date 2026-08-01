// Groups are AUDIENCES, not rooms (ADR-012): a named, durable set of peers
// you can address. A group holds no messages and has no state to converge —
// threads remain the only discussion object. Membership is runtime registry
// state, never config (ADR-001), persisted next to the registry so it
// survives a hub restart.

package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultGroup is the lobby every peer joins at registration and may leave.
// It is the one group that is never garbage-collected when empty.
const DefaultGroup = "@all"

// GroupPrefix marks a group reference so a group can never be mistaken for a
// peer name.
const GroupPrefix = "@"

// IsGroup reports whether a `to` entry addresses a group.
func IsGroup(name string) bool { return strings.HasPrefix(name, GroupPrefix) }

// NormalizeGroup returns the canonical `@name` form of a group reference,
// or "" when the name is unusable.
func NormalizeGroup(name string) string {
	n := strings.TrimSpace(name)
	n = strings.TrimLeft(n, GroupPrefix)
	if n == "" {
		return ""
	}
	return GroupPrefix + n
}

// Group is one audience: a name and its current members.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

func (r *Registry) groupsPath() string { return filepath.Join(filepath.Dir(r.path), "groups.json") }

// loadGroupsLocked reads the persisted membership map.
func (r *Registry) loadGroupsLocked() {
	r.groups = map[string]map[string]bool{}
	b, err := os.ReadFile(r.groupsPath())
	if err != nil {
		return
	}
	var raw map[string][]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return
	}
	for name, members := range raw {
		set := map[string]bool{}
		for _, m := range members {
			set[m] = true
		}
		r.groups[name] = set
	}
}

func (r *Registry) persistGroupsLocked() {
	out := map[string][]string{}
	for name, set := range r.groups {
		members := make([]string, 0, len(set))
		for m := range set {
			members = append(members, m)
		}
		sort.Strings(members)
		out[name] = members
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	tmp := r.groupsPath() + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, r.groupsPath())
}

// nameCollidesLocked reports whether a peer name would collide with an
// existing group of the same name (`@platform` vs peer `platform`).
func (r *Registry) nameCollidesLocked(peer string) bool {
	_, ok := r.groups[GroupPrefix+peer]
	return ok
}

// GroupCollidesWithPeer reports whether a group name is already taken by a
// registered peer. A group and a peer may never share a bare name.
func (r *Registry) GroupCollidesWithPeer(group string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.groupCollidesLocked(group)
}

func (r *Registry) groupCollidesLocked(group string) bool {
	_, ok := r.agents[strings.TrimPrefix(NormalizeGroup(group), GroupPrefix)]
	return ok
}

// JoinGroup adds ONE peer — always the caller — to a group, creating the
// group when it does not exist (ADR-012: no create verb, no owner, no
// admin). It never adds anybody else: that is an invite, which is a message.
func (r *Registry) JoinGroup(group, peer string) (string, error) {
	name := NormalizeGroup(group)
	if name == "" {
		return "", fmt.Errorf("group name is required")
	}
	if peer == "" {
		return "", fmt.Errorf("peer name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.groupCollidesLocked(name) {
		return "", fmt.Errorf("group %s collides with the registered peer %q", name, strings.TrimPrefix(name, GroupPrefix))
	}
	if r.groups[name] == nil {
		r.groups[name] = map[string]bool{}
	}
	r.groups[name][peer] = true
	r.persistGroupsLocked()
	return name, nil
}

// LeaveGroup removes the caller. An emptied group is garbage-collected so
// ad-hoc audiences evaporate instead of rotting — except @all, which is the
// lobby and persists even when nobody is in it.
func (r *Registry) LeaveGroup(group, peer string) (string, bool) {
	name := NormalizeGroup(group)
	if name == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	set, ok := r.groups[name]
	if !ok {
		return name, false
	}
	if !set[peer] {
		return name, false
	}
	delete(set, peer)
	if len(set) == 0 && name != DefaultGroup {
		delete(r.groups, name)
	}
	r.persistGroupsLocked()
	return name, true
}

// forgetGroupsLocked drops a peer from every audience it joined. Called with
// r.mu held by Forget; an emptied non-default group is GC'd, exactly as
// leaving it one at a time would (ADR-012).
func (r *Registry) forgetGroupsLocked(peer string) {
	for name, set := range r.groups {
		if !set[peer] {
			continue
		}
		delete(set, peer)
		if len(set) == 0 && name != DefaultGroup {
			delete(r.groups, name)
		}
	}
	r.persistGroupsLocked()
}

// GroupMembers returns a snapshot of the group's current members, sorted.
func (r *Registry) GroupMembers(group string) ([]string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.groupMembersLocked(NormalizeGroup(group))
}

func (r *Registry) groupMembersLocked(name string) ([]string, bool) {
	set, ok := r.groups[name]
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out, true
}

// Groups lists every audience with its members, sorted by name.
func (r *Registry) Groups() []Group {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Group, 0, len(r.groups))
	for name := range r.groups {
		members, _ := r.groupMembersLocked(name)
		if members == nil {
			members = []string{}
		}
		out = append(out, Group{Name: name, Members: members})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ExpandRecipients resolves a `to` list ONCE, at ingest (ADR-012): every
// `@group` entry becomes a snapshot of that group's current members, plain
// names pass through, the sender never appears in their own fan-out, and the
// result is deduped with order preserved. A later joiner does NOT
// retroactively enter the resulting thread — they discover it (ADR-011).
func (r *Registry) ExpandRecipients(to []string, sender string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	seen := map[string]bool{}
	add := func(n string, fromGroup bool) {
		// Every recipient resolves to its IDENTITY before delivery (ADR-015):
		// an alias must land in the same inbox as the canonical name, or a
		// label silently becomes a second peer with its own cursor again.
		if a, ok := r.resolveLocked(n); ok {
			n = a.Name
		} else if fromGroup {
			// A group member nothing is registered under is a ghost — a typo
			// or a forgotten peer left in the audience. Fanning out to it
			// black-holes the message: no inbox, no cursor, nobody to read it.
			// `@all` on 2026-08-01 carried `toolexus-clojure`, a misspelling
			// of a real peer. An explicitly typed name still passes through
			// (a peer may be addressed before it first registers).
			return
		}
		// A group never fans out to the peer who addressed it.
		if n == "" || seen[n] || (fromGroup && n == sender) {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	for _, entry := range to {
		if !IsGroup(entry) {
			add(entry, false)
			continue
		}
		name := NormalizeGroup(entry)
		members, ok := r.groupMembersLocked(name)
		if !ok {
			return nil, fmt.Errorf("unknown group %s — join it to create it", name)
		}
		for _, m := range members {
			add(m, true)
		}
	}
	return out, nil
}
