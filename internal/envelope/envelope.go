// Package envelope defines the single canonical message shape stored and
// served by the workwire hub (hub-core R1).
package envelope

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// SchemaVersion is the envelope schema version reported by /health.
const SchemaVersion = 1

// Attachment references hub-stored media by id — never a host path
// (hub-core R10, ADR-006).
type Attachment struct {
	MediaID     string `json:"media_id"`
	Name        string `json:"name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

// Recipients is the `to` field: a single name on the wire stays a plain JSON
// string, several names are a JSON array (ADR-009). One envelope with one id
// is delivered to every recipient.
type Recipients []string

// UnmarshalJSON accepts a string or an array of strings.
func (r *Recipients) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*r = nil
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*r = nil
			return nil
		}
		*r = Recipients{s}
		return nil
	case '[':
		var list []string
		if err := json.Unmarshal(b, &list); err != nil {
			return err
		}
		out := make(Recipients, 0, len(list))
		seen := map[string]bool{}
		for _, n := range list {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
		if len(out) == 0 {
			*r = nil
			return nil
		}
		*r = out
		return nil
	}
	return errors.New("to must be a name or an array of names")
}

// MarshalJSON keeps the single-recipient wire shape a plain string so
// existing one-to-one clients see no change.
func (r Recipients) MarshalJSON() ([]byte, error) {
	switch len(r) {
	case 0:
		return json.Marshal("")
	case 1:
		return json.Marshal(r[0])
	default:
		return json.Marshal([]string(r))
	}
}

// Has reports whether name is a recipient.
func (r Recipients) Has(name string) bool {
	for _, n := range r {
		if n == name {
			return true
		}
	}
	return false
}

// First returns the first recipient (empty when there is none).
func (r Recipients) First() string {
	if len(r) == 0 {
		return ""
	}
	return r[0]
}

// Envelope is the one canonical message shape:
// {id, from, to, thread_id, reply_to, text, ts, kind, meta, attachments}.
// id, ts and from are hub-generated/stamped at ingest.
type Envelope struct {
	ID          string         `json:"id"`
	From        string         `json:"from"`
	To          Recipients     `json:"to"`
	ThreadID    string         `json:"thread_id"`
	ReplyTo     string         `json:"reply_to,omitempty"`
	Text        string         `json:"text"`
	TS          string         `json:"ts"`
	Kind        string         `json:"kind,omitempty"`
	Meta        map[string]any `json:"meta,omitempty"`
	Attachments []Attachment   `json:"attachments,omitempty"`
}

// Clone returns a deep-enough copy safe to mutate for projections.
func (e *Envelope) Clone() Envelope {
	c := *e
	if e.To != nil {
		c.To = append(Recipients(nil), e.To...)
	}
	if e.Meta != nil {
		c.Meta = make(map[string]any, len(e.Meta))
		for k, v := range e.Meta {
			c.Meta[k] = v
		}
	}
	if e.Attachments != nil {
		c.Attachments = append([]Attachment(nil), e.Attachments...)
	}
	return c
}

// NewID returns a hub-generated unique id with the given prefix (e.g. "m", "t").
func NewID(prefix string) string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable; fall back to time-based.
		return prefix + "-" + hex.EncodeToString([]byte(time.Now().UTC().Format("150405.000000000")))
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// Now returns the hub-clock UTC timestamp in RFC 3339 format.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
