package server

import (
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/workwire/internal/store"
)

// A boolean could not tell "nobody is there" from "someone is there but busy",
// and every mechanism built on that boolean failed the same way in a new
// costume (ADR-016).
func TestPresenceIsGradedNotBoolean(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	old := now.Add(-45 * time.Minute).Format(time.RFC3339Nano)

	for _, c := range []struct {
		name     string
		listener bool
		spoke    string
		want     string
	}{
		{"spoke recently, listener live", true, recent, PresenceAttentive},
		{"listener live, long silent", true, old, PresenceQuiet},
		{"listener live, never spoke", true, "", PresenceQuiet},
		{"no listener", false, recent, PresenceGone},
	} {
		got, _ := presenceOf(c.listener, store.AgentStats{LastSpoke: c.spoke}, now)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A session mid-build is working, not dead. A window tuned for humans typing
// would call it gone, which is how we got three failures in three costumes.
func TestASixMinuteToolCallIsStillAttentive(t *testing.T) {
	now := time.Now()
	st := store.AgentStats{LastSpoke: now.Add(-6 * time.Minute).Format(time.RFC3339Nano)}
	if got, idle := presenceOf(true, st, now); got != PresenceAttentive {
		t.Fatalf("six minutes of work read as %q (idle=%ds) — that is a working session", got, idle)
	}
}

// Never having spoken is not "silent for a very long time"; rendering it as a
// huge number would read as a fault.
func TestNeverSpokenIsNotAHugeIdleNumber(t *testing.T) {
	_, idle := presenceOf(true, store.AgentStats{}, time.Now())
	if idle != -1 {
		t.Fatalf("idle for a peer that never spoke = %d, want -1", idle)
	}
}

// The sender learns the peer's state at send time, in words, with the idle
// time — IRC returns AWAY text to whoever messages you (RFC 2812).
func TestSenderIsToldWhyAndForHowLong(t *testing.T) {
	if d := describePresence("koine", PresenceAttentive, 5); d != "" {
		t.Fatalf("an attentive peer needs no note, got %q", d)
	}
	d := describePresence("koine", PresenceQuiet, 720)
	if d == "" || !strings.Contains(d, "12m") || !strings.Contains(d, "quiet") {
		t.Fatalf("quiet note must name the state and the idle time, got %q", d)
	}
	if g := describePresence("koine", PresenceGone, 99); !strings.Contains(g, "queued") {
		t.Fatalf("a gone peer's note must say the question is queued, got %q", g)
	}
}
