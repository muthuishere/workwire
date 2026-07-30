// Package contacts is the hub's people directory: TOFU harvest from inbound
// traffic, explicit verified adds with aliases, fuzzy lookup, and tombstone
// purge that survives restart (contacts spec; ADR-005, ADR-007, ADR-008).
package contacts

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/muthuishere/workwire/internal/envelope"
)

// Contact is one directory entry, keyed by (peer, id).
type Contact struct {
	ContactID string   `json:"contactId"`
	Name      string   `json:"name"`
	Aliases   []string `json:"aliases"`
	Peer      string   `json:"peer"`
	ID        string   `json:"id"`
	Verified  bool     `json:"verified"`
	LastSeen  string   `json:"lastSeen"`
}

type record struct {
	Type    string   `json:"type"` // "contact" | "tomb"
	Contact *Contact `json:"contact,omitempty"`
	ID      string   `json:"id,omitempty"` // contactId for tombs
}

// Directory is the contacts store backed by contacts.ndjson in the data dir.
type Directory struct {
	mu      sync.Mutex
	f       *os.File
	byKey   map[string]*Contact // peer\x00id -> entry
	byID    map[string]*Contact
	tombs   map[string]bool // tombstoned contactIds
	everIDs map[string]bool // every contactId ever seen (404 vs idempotent 200)
}

func key(peer, id string) string { return peer + "\x00" + id }

// Open replays contacts.ndjson.
func Open(dataDir string) (*Directory, error) {
	path := filepath.Join(dataDir, "contacts.ndjson")
	d := &Directory{
		byKey:   map[string]*Contact{},
		byID:    map[string]*Contact{},
		tombs:   map[string]bool{},
		everIDs: map[string]bool{},
	}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			var r record
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			switch r.Type {
			case "contact":
				if r.Contact != nil {
					d.applyLocked(r.Contact)
				}
			case "tomb":
				d.applyTombLocked(r.ID)
			}
		}
		f.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	d.f = f
	return d, nil
}

// Close closes the backing file.
func (d *Directory) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f != nil {
		d.f.Close()
	}
}

func (d *Directory) applyLocked(c *Contact) {
	d.everIDs[c.ContactID] = true
	d.byKey[key(c.Peer, c.ID)] = c
	d.byID[c.ContactID] = c
}

func (d *Directory) applyTombLocked(contactID string) {
	d.tombs[contactID] = true
	d.everIDs[contactID] = true
	if c, ok := d.byID[contactID]; ok {
		delete(d.byKey, key(c.Peer, c.ID))
		delete(d.byID, contactID)
	}
}

func (d *Directory) appendLocked(r record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = d.f.Write(append(b, '\n'))
	return err
}

// Harvest upserts from an accepted envelope's sender (contacts R1): harvested
// entries are unverified; harvest never downgrades verification; a rename
// keeps the prior name as an alias; a purged key re-harvests fresh and
// unverified.
func (d *Directory) Harvest(name, peer, id, ts string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	existing, ok := d.byKey[key(peer, id)]
	var c Contact
	if ok {
		c = *existing
		c.LastSeen = ts
		if name != "" && name != c.Name {
			if c.Name != "" && !contains(c.Aliases, c.Name) {
				c.Aliases = append(append([]string(nil), c.Aliases...), c.Name)
			}
			c.Name = name
		}
	} else {
		c = Contact{
			ContactID: envelope.NewID("c"),
			Name:      name,
			Aliases:   []string{},
			Peer:      peer,
			ID:        id,
			Verified:  false,
			LastSeen:  ts,
		}
	}
	if d.appendLocked(record{Type: "contact", Contact: &c}) == nil {
		d.applyLocked(&c)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Add is the explicit POST /contacts path (contacts R2): creates or merges
// an entry, always verified (an explicit add is an act of trust).
func (d *Directory) Add(name, peer, id string, aliases []string) (*Contact, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var c Contact
	if existing, ok := d.byKey[key(peer, id)]; ok {
		c = *existing
		c.Name = name
		for _, a := range aliases {
			if !contains(c.Aliases, a) {
				c.Aliases = append(c.Aliases, a)
			}
		}
	} else {
		c = Contact{
			ContactID: envelope.NewID("c"),
			Name:      name,
			Aliases:   append([]string{}, aliases...),
			Peer:      peer,
			ID:        id,
			LastSeen:  envelope.Now(),
		}
	}
	c.Verified = true
	if err := d.appendLocked(record{Type: "contact", Contact: &c}); err != nil {
		return nil, err
	}
	d.applyLocked(&c)
	out := c
	return &out, nil
}

// Verify sets verified:true on an entry (contacts R6, idempotent).
func (d *Directory) Verify(contactID string) (*Contact, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.byID[contactID]
	if !ok {
		return nil, false
	}
	nc := *c
	nc.Verified = true
	if d.appendLocked(record{Type: "contact", Contact: &nc}) != nil {
		return nil, false
	}
	d.applyLocked(&nc)
	out := nc
	return &out, true
}

// DeleteResult classifies a purge attempt.
type DeleteResult int

const (
	// Deleted: entry removed now, or already tombstoned (idempotent 200).
	Deleted DeleteResult = iota
	// NotFound: contactId never existed (404).
	NotFound
)

// Delete purges via tombstone (contacts R7): survives restart and replay.
func (d *Directory) Delete(contactID string) DeleteResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tombs[contactID] {
		return Deleted
	}
	if !d.everIDs[contactID] {
		return NotFound
	}
	if d.appendLocked(record{Type: "tomb", ID: contactID}) == nil {
		d.applyTombLocked(contactID)
	}
	return Deleted
}

// Search matches q case-insensitively and fuzzily against names and aliases,
// best match first; empty q lists all non-tombstoned entries (contacts R3).
func (d *Directory) Search(q string) []Contact {
	d.mu.Lock()
	defer d.mu.Unlock()
	q = strings.ToLower(q)
	type scored struct {
		c     Contact
		score int
	}
	var out []scored
	for _, c := range d.byKey {
		s := matchScore(c, q)
		if q == "" || s > 0 {
			out = append(out, scored{*c, s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].c.Name < out[j].c.Name
	})
	res := make([]Contact, 0, len(out))
	for _, s := range out {
		res = append(res, s.c)
	}
	return res
}

func matchScore(c *Contact, q string) int {
	if q == "" {
		return 1
	}
	best := 0
	terms := append([]string{c.Name}, c.Aliases...)
	for _, t := range terms {
		lt := strings.ToLower(t)
		switch {
		case lt == q:
			best = max(best, 100)
		case strings.HasPrefix(lt, q):
			best = max(best, 80)
		case strings.Contains(lt, q):
			best = max(best, 60)
		case fuzzyMatch(lt, q):
			best = max(best, 40)
		}
	}
	return best
}

// fuzzyMatch: all runes of q appear in order in s.
func fuzzyMatch(s, q string) bool {
	i := 0
	for _, r := range s {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	return i == len(q)
}
