package server

import (
	"time"

	"github.com/muthuishere/workwire/internal/store"
)

// Presence states (ADR-016). A boolean could not carry the difference between
// "nobody is there" and "someone is there but busy", and every mechanism we
// built on that boolean failed the same way in a new costume.
const (
	// PresenceAttentive: this peer has AUTHORED something recently — an
	// answer, a message — which is the only evidence of processing the hub can
	// honestly hold. A running watch is not evidence; a lease is not evidence.
	PresenceAttentive = "attentive"
	// PresenceQuiet: a live listener, so delivery works and nothing is lost,
	// but no sign of anyone reading. A FIRST-CLASS state, not a failure — a
	// session mid-build is legitimately quiet (XEP-0085 calls this `inactive`).
	PresenceQuiet = "quiet"
	// PresenceGone: no live listener. Questions queue against the cursor and
	// arrive when it returns.
	PresenceGone = "gone"
)

// attentiveWindow is how recently a peer must have spoken to count as
// attentive. Deliberately generous: an agent session that spends six minutes
// in a tool call is working, not dead, and a threshold tuned for chat would
// call it gone. XEP-0085 suggests ~2 min to `inactive` for humans typing;
// LLM sessions are an order of magnitude slower, so this is 10.
const attentiveWindow = 10 * time.Minute

// presenceOf grades a peer from delivery facts plus evidence of processing.
func presenceOf(listener bool, st store.AgentStats, now time.Time) (state string, idleSeconds int) {
	if !listener {
		return PresenceGone, idleFrom(st.LastSpoke, now)
	}
	idle := idleFrom(st.LastSpoke, now)
	if st.LastSpoke != "" && time.Duration(idle)*time.Second <= attentiveWindow {
		return PresenceAttentive, idle
	}
	return PresenceQuiet, idle
}

// idleFrom returns seconds since a peer last authored anything; -1 when it has
// never spoken, which is different from "spoke a long time ago" and must not
// be rendered as a huge number.
func idleFrom(ts string, now time.Time) int {
	if ts == "" {
		return -1
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return -1
	}
	d := int(now.Sub(t).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// describePresence is what a SENDER is told, at send time, in one line — the
// cheapest useful thing in the whole survey (IRC RFC 2812 returns the peer's
// own AWAY text to whoever messages them). Silence teaches a sender nothing;
// "quiet for 12m" tells them whether to wait or go read the repo.
func describePresence(name, state string, idle int) string {
	switch state {
	case PresenceAttentive:
		return ""
	case PresenceQuiet:
		if idle < 0 {
			return name + " is listening but has not said anything yet — delivered, and it will be read when that session next looks"
		}
		return name + " is quiet (" + humanIdle(idle) + " since it last said anything) — delivered, and it will be read when that session next looks"
	default:
		return name + " has no live listener — the question is queued against its cursor and arrives when it comes back"
	}
}

func humanIdle(sec int) string {
	switch {
	case sec < 90:
		return itoa(sec) + "s"
	case sec < 5400:
		return itoa(sec/60) + "m"
	default:
		return itoa(sec/3600) + "h"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
