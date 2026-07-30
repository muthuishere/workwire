// Package envelope defines the single canonical message shape stored and
// served by the workwire hub (hub-core R1).
package envelope

import (
	"crypto/rand"
	"encoding/hex"
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

// Envelope is the one canonical message shape:
// {id, from, to, thread_id, reply_to, text, ts, kind, meta, attachments}.
// id, ts and from are hub-generated/stamped at ingest.
type Envelope struct {
	ID          string         `json:"id"`
	From        string         `json:"from"`
	To          string         `json:"to"`
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
