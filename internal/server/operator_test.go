package server

import (
	"strings"
	"testing"
)

// Housekeeping should sound like housekeeping. Closing 29 duplicate threads
// on 2026-08-01 required registering a HUMAN peer purely to have the
// authority, which put the heaviest voice on the mesh behind pure maintenance.
func TestOperatorMayCloseAThreadItDidNotOpen(t *testing.T) {
	h := newHub(t, nil)
	a := h.register("api")
	h.register("web")

	code, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"web"}, "text": "duplicate announcement"})
	if code != 200 {
		t.Fatalf("open: %d %v", code, out)
	}
	thread, _ := out["thread_id"].(string)

	if code, out := h.req(adminToken, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "consolidated onto t-canonical",
	}); code != 200 {
		t.Fatalf("operator close: %d %v", code, out)
	}
	for _, ts := range threadList(t, h) {
		if ts["thread_id"] == thread && ts["state"] != "resolved" {
			t.Fatalf("thread not closed: %v", ts)
		}
	}
}

// An operator tidies a mesh; it does not overrule a person.
func TestOperatorMayNotCloseOverAHumansDissent(t *testing.T) {
	h := newHub(t, nil)
	a := h.register("api")
	person := h.registerHuman("priya")

	_, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"priya"}, "text": "topic"})
	thread, _ := out["thread_id"].(string)
	if code, o := h.req(person, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "dissent", "text": "not settled",
	}); code != 200 {
		t.Fatalf("dissent: %d %v", code, o)
	}
	code, res := h.req(adminToken, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "tidying up",
	})
	if code != 409 {
		t.Fatalf("operator closed over a human dissent: %d %v", code, res)
	}
	if msg, _ := res["error"].(string); !strings.Contains(msg, "priya") {
		t.Fatalf("the refusal must name the person: %q", msg)
	}
}

// Agent dissent is a different matter: maintenance may retire a thread whose
// only objection came from a session.
func TestOperatorMayCloseOverAgentDissent(t *testing.T) {
	h := newHub(t, nil)
	a := h.register("api")
	b := h.register("web")

	_, out := h.req(a, "POST", "/send", map[string]any{"to": []string{"web"}, "text": "topic"})
	thread, _ := out["thread_id"].(string)
	h.req(b, "POST", "/send", map[string]any{"thread_id": thread, "kind": "dissent", "text": "I object"})

	if code, res := h.req(adminToken, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "retired",
	}); code != 200 {
		t.Fatalf("operator close over agent dissent: %d %v", code, res)
	}
}

func threadList(t *testing.T, h *hub) []map[string]any {
	t.Helper()
	code, out := h.req(adminToken, "GET", "/threads", nil)
	if code != 200 {
		t.Fatalf("threads: %d", code)
	}
	var list []map[string]any
	for _, raw := range out["threads"].([]any) {
		m, _ := raw.(map[string]any)
		list = append(list, m)
	}
	return list
}
