package server

import (
	"net/http"
	"testing"
)

// agentEntry returns one agent's entry from GET /agents.
func (h *hub) agentEntry(name string) map[string]any {
	h.t.Helper()
	code, out := h.req(adminToken, "GET", "/agents", nil)
	if code != 200 {
		h.t.Fatalf("list agents: %d", code)
	}
	list, _ := out["agents"].([]any)
	for _, a := range list {
		m, _ := a.(map[string]any)
		if m["name"] == name {
			return m
		}
	}
	h.t.Fatalf("agent %q not listed: %v", name, out)
	return nil
}

// `listener` is a delivery fact and `answering` is an answerability fact.
// Auto-join takes a lease for every folder while only an engaged session
// answers, so the two must be reported separately — a lease alone may never
// silence the ask warning.
func TestAnsweringIsReportedSeparatelyFromTheLease(t *testing.T) {
	h := newHub(t, nil)
	secret := h.register("api")

	// A lease, but nobody attached to answer.
	if code, out := h.req(secret, "POST", "/agents/api/listen-lease", map[string]string{}); code != 200 {
		t.Fatalf("lease: %d %v", code, out)
	}
	a := h.agentEntry("api")
	if a["listener"] != true {
		t.Fatalf("expected a live listener: %v", a)
	}
	if a["answering"] != false {
		t.Fatalf("a lease must not imply an answerer: %v", a)
	}

	code, ask := h.req(adminToken, "POST", "/agents/api/ask", map[string]string{"text": "who owns retries?"})
	if code != http.StatusAccepted {
		t.Fatalf("ask: %d %v", code, ask)
	}
	if ask["listener"] != true || ask["answering"] != false {
		t.Fatalf("ask response must distinguish delivery from answerability: %v", ask)
	}

	// Now an answerer declares itself.
	if code, out := h.req(secret, "POST", "/agents/api/answering", map[string]bool{"attached": true}); code != 200 {
		t.Fatalf("declare answering: %d %v", code, out)
	}
	if a := h.agentEntry("api"); a["answering"] != true {
		t.Fatalf("declared answerer not reported: %v", a)
	}
	code, ask = h.req(adminToken, "POST", "/agents/api/ask", map[string]string{"text": "and now?"})
	if code != http.StatusAccepted || ask["answering"] != true {
		t.Fatalf("ask must report the live answerer: %d %v", code, ask)
	}

	// And stands down again — the lease is untouched.
	if code, out := h.req(secret, "POST", "/agents/api/answering", map[string]bool{"attached": false}); code != 200 {
		t.Fatalf("stand down: %d %v", code, out)
	}
	if a := h.agentEntry("api"); a["answering"] != false || a["listener"] != true {
		t.Fatalf("stand-down must clear answering and keep the lease: %v", a)
	}
}

// Nobody declares answerability on another peer's behalf.
func TestAnsweringRequiresTheAgentsOwnCredential(t *testing.T) {
	h := newHub(t, nil)
	h.register("api")
	other := h.register("docs")
	if code, _ := h.req(other, "POST", "/agents/api/answering", map[string]bool{"attached": true}); code != http.StatusForbidden {
		t.Fatalf("expected 403 for a foreign credential, got %d", code)
	}
	if code, _ := h.req(adminToken, "POST", "/agents/ghost/answering", map[string]bool{"attached": true}); code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown agent, got %d", code)
	}
}
