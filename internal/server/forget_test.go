package server

import "testing"

// A rename leaves the old identity behind. Without a way to drop it the
// registry accumulates peers that `peers` lists and `ask` can address, each
// waiting on an answerer that will never exist again.
func TestForgetDropsTheRegistrationButNotTheHistory(t *testing.T) {
	h := newHub(t, nil)
	secret := h.register("api")
	h.register("api-main")

	// It said something first: forgetting the peer must not unsay it.
	if code, _ := h.req(secret, "POST", "/send", map[string]any{
		"to": []string{"api-main"}, "text": "before the rename",
	}); code != 200 && code != 201 {
		t.Fatalf("send: %d", code)
	}

	if code, out := h.req(adminToken, "DELETE", "/agents/api", nil); code != 204 {
		t.Fatalf("forget: %d %v", code, out)
	}
	code, out := h.req(adminToken, "GET", "/agents", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	for _, a := range out["agents"].([]any) {
		if m, _ := a.(map[string]any); m["name"] == "api" {
			t.Fatalf("api still listed after forget: %v", out)
		}
	}
	// The message it sent is still delivered and readable.
	code, inbox := h.req(adminToken, "GET", "/inbox?agent=api-main&since=0", nil)
	if code != 200 {
		t.Fatalf("inbox: %d", code)
	}
	msgs, _ := inbox["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("forget deleted history: %v", inbox)
	}

	// Gone means gone: a second forget is a 404, not a silent success.
	if code, _ := h.req(adminToken, "DELETE", "/agents/api", nil); code != 404 {
		t.Fatalf("second forget = %d, want 404", code)
	}
}

// Forgetting is admin, or the peer standing down for good — never one peer
// unnaming another.
func TestForgetRefusesAnotherAgentsCredential(t *testing.T) {
	h := newHub(t, nil)
	h.register("api")
	other := h.register("web")

	if code, _ := h.req(other, "DELETE", "/agents/api", nil); code != 403 {
		t.Fatalf("cross-agent forget = %d, want 403", code)
	}
	if code, _ := h.req(adminToken, "GET", "/agents/api/card", nil); code != 200 {
		t.Fatalf("api should still exist: %d", code)
	}
}
