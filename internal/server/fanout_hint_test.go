package server

import (
	"strings"
	"testing"
)

// The hub cannot forbid a message to one peer — that is an ordinary thing —
// but it must not let one announcement retyped per recipient pass unremarked.
// 28 of 77 live threads on 2026-08-01 were exactly that shape.
func TestRepeatedAnnouncementIsFlaggedWithTheThreadToUse(t *testing.T) {
	h := newHub(t, nil)
	ann := h.registerHuman("ann")
	h.registerHuman("a")
	h.registerHuman("b")

	code, first := h.req(ann, "POST", "/send", map[string]any{"to": []string{"a"}, "text": "THE BRANCH IS PUSHED"})
	if code != 200 {
		t.Fatalf("first send: %d %v", code, first)
	}
	if first["hint"] != nil {
		t.Fatalf("a first send must not be flagged: %v", first)
	}

	code, second := h.req(ann, "POST", "/send", map[string]any{"to": []string{"b"}, "text": "THE BRANCH IS PUSHED"})
	if code != 200 {
		t.Fatalf("second send: %d %v", code, second)
	}
	if second["duplicate_of_thread"] != first["thread_id"] {
		t.Fatalf("must name the thread already carrying it: %v", second)
	}
	hint, _ := second["hint"].(string)
	if !strings.Contains(hint, "--to a,b,c") {
		t.Fatalf("the hint must say what to do instead: %q", hint)
	}
	// And it is a hint, not a refusal: the message still went.
	if second["id"] == nil {
		t.Fatalf("the send must still succeed: %v", second)
	}
}

// Continuing an existing thread is the CORRECT behaviour and must never be
// nagged about.
func TestReplyingOnTheSameThreadIsNeverFlagged(t *testing.T) {
	h := newHub(t, nil)
	ann := h.registerHuman("ann")
	h.registerHuman("a")

	_, first := h.req(ann, "POST", "/send", map[string]any{"to": []string{"a"}, "text": "status"})
	thread, _ := first["thread_id"].(string)
	_, again := h.req(ann, "POST", "/send", map[string]any{"thread_id": thread, "text": "status"})
	if again["hint"] != nil {
		t.Fatalf("same thread must not be flagged: %v", again)
	}
}

// Different text is different news.
func TestDifferentTextIsNotADuplicate(t *testing.T) {
	h := newHub(t, nil)
	ann := h.registerHuman("ann")
	h.registerHuman("a")
	h.registerHuman("b")

	h.req(ann, "POST", "/send", map[string]any{"to": []string{"a"}, "text": "one thing"})
	_, other := h.req(ann, "POST", "/send", map[string]any{"to": []string{"b"}, "text": "a different thing"})
	if other["hint"] != nil {
		t.Fatalf("distinct messages must not be flagged: %v", other)
	}
}

// One send to many is the shape we want; it must be silent.
func TestOneSendToManyIsSilent(t *testing.T) {
	h := newHub(t, nil)
	ann := h.registerHuman("ann")
	h.registerHuman("a")
	h.registerHuman("b")

	_, out := h.req(ann, "POST", "/send", map[string]any{"to": []string{"a", "b"}, "text": "v0.9.0 is tagged"})
	if out["hint"] != nil {
		t.Fatalf("the correct shape must not be nagged: %v", out)
	}
}
