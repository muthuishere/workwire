package server

import (
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

// A stalled thread must tell the people in it (hub-core R26). Two peers each
// concluded their messages had been silently lost on 2026-08-01; they had not
// been — every over-cap send was refused with a 409. The silence was the bug.
func TestStalledThreadNotifiesInitiatorAndMembers(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.MaxThreadMessages = 4 })
	a := h.registerHuman("initiator")
	b := h.registerHuman("member")

	code, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"member"}, "text": "topic"})
	if code != 200 {
		t.Fatalf("open: %d %v", code, out)
	}
	thread, _ := out["thread_id"].(string)

	// Fill to the cap.
	for i := 0; i < 3; i++ {
		if code, out := h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "text": "round"}); code != 200 {
			t.Fatalf("fill %d: %d %v", i, code, out)
		}
	}
	// The next one is refused...
	if code, _ := h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "text": "over"}); code != 409 {
		t.Fatalf("over-cap send should be refused, got %d", code)
	}
	// ...and BOTH sides have been told, once.
	for who, tok := range map[string]string{"initiator": a, "member": b} {
		n := countStallNotices(t, h, tok, who, thread)
		if n != 1 {
			t.Fatalf("%s received %d stall notices, want exactly 1", who, n)
		}
	}

	// A second refused send does not produce a second notice.
	h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "text": "over again"})
	if n := countStallNotices(t, h, a, "initiator", thread); n != 1 {
		t.Fatalf("stall notice repeated: %d", n)
	}
}

// The notice must not itself consume a round, or the count participants see
// disagrees with the cap the hub enforces.
func TestStallNoticeDoesNotCountTowardTheCap(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.MaxThreadMessages = 3 })
	a := h.registerHuman("a")
	b := h.registerHuman("b")
	_, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"b"}, "text": "topic"})
	thread, _ := out["thread_id"].(string)
	h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "text": "two"})
	h.req(a, "POST", "/send", map[string]any{"thread_id": thread, "text": "three"})

	code, list := h.req(adminToken, "GET", "/threads", nil)
	if code != 200 {
		t.Fatalf("threads: %d", code)
	}
	for _, raw := range list["threads"].([]any) {
		ts, _ := raw.(map[string]any)
		if ts["thread_id"] != thread {
			continue
		}
		if int(ts["count"].(float64)) != 3 {
			t.Fatalf("count = %v, want 3 (the notice must not count)", ts["count"])
		}
		if ts["state"] != "stalled" {
			t.Fatalf("state = %v, want stalled", ts["state"])
		}
		return
	}
	t.Fatalf("thread %s not listed", thread)
}

// Below the cap nobody is told anything.
func TestNoStallNoticeBeforeTheCap(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.MaxThreadMessages = 10 })
	a := h.registerHuman("a")
	b := h.registerHuman("b")
	_, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"b"}, "text": "topic"})
	thread, _ := out["thread_id"].(string)
	h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "text": "reply"})
	if n := countStallNotices(t, h, a, "a", thread); n != 0 {
		t.Fatalf("premature stall notice: %d", n)
	}
}

func countStallNotices(t *testing.T, h *hub, token, agent, thread string) int {
	t.Helper()
	code, out := h.req(token, "GET", "/inbox?agent="+agent+"&since=0", nil)
	if code != 200 {
		t.Fatalf("inbox %s: %d %v", agent, code, out)
	}
	n := 0
	msgs, _ := out["messages"].([]any)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m["kind"] == "stalled" && m["thread_id"] == thread {
			n++
			if !strings.Contains(m["text"].(string), "stalled") {
				t.Fatalf("stall notice must say what happened: %v", m["text"])
			}
			if m["from"] != "workwire-hub" {
				t.Fatalf("stall notice must come from the hub, got %v", m["from"])
			}
		}
	}
	return n
}
