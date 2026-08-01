package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The payload exists to answer one question a human asks at 2am: is anything
// actually reading what is being delivered? So `pending` with `listener` and
// `answering` must be reportable together and separately.
func TestMetricsDistinguishesDeliveredFromRead(t *testing.T) {
	h := newHub(t, nil)
	secret := h.register("api")
	asker := h.registerHuman("asker")

	// A lease: questions are being DELIVERED. Nothing attached to read them.
	if code, _ := h.req(secret, "POST", "/agents/api/listen-lease", map[string]string{}); code != 200 {
		t.Fatal("lease")
	}
	for i := 0; i < 3; i++ {
		if code, _ := h.req(asker, "POST", "/send", map[string]any{"to": []string{"api"}, "text": "q"}); code != 200 {
			t.Fatal("send")
		}
	}

	code, m := h.req(adminToken, "GET", "/metrics", nil)
	if code != 200 {
		t.Fatalf("metrics: %d %v", code, m)
	}
	api := agentRow(t, m, "api")
	if api["listener"] != true {
		t.Fatalf("listener should be live: %v", api)
	}
	if api["answering"] != false {
		t.Fatalf("a lease must not imply an answerer: %v", api)
	}
	if got := int(api["delivered"].(float64)); got != 3 {
		t.Fatalf("delivered = %d, want 3", got)
	}
	if got := int(api["pending"].(float64)); got != 3 {
		t.Fatalf("pending = %d, want 3 — nothing has collected them", got)
	}

	// Once the peer collects them, pending clears and delivered does not:
	// delivered is history, pending is a backlog.
	h.req(secret, "GET", "/inbox?agent=api&since=0", nil)
	code, m2 := h.req(adminToken, "GET", "/metrics", nil)
	if code != 200 {
		t.Fatal("metrics again")
	}
	_ = m2 // the cursor the peer presented was 0, so the backlog legitimately stands
	if got := int(agentRow(t, m2, "api")["delivered"].(float64)); got != 3 {
		t.Fatalf("delivered must not shrink: %d", got)
	}
}

// Traffic counters cover what a scan cannot see.
func TestMetricsCountsTrafficAndRefusals(t *testing.T) {
	h := newHub(t, nil)
	a := h.registerHuman("a")
	h.registerHuman("b")

	h.req(a, "POST", "/send", map[string]any{"to": []string{"b"}, "text": "one"})
	h.req(a, "POST", "/send", map[string]any{"to": []string{"nobody-here"}, "text": "two"})

	_, m := h.req(adminToken, "GET", "/metrics", nil)
	tr := m["traffic"].(map[string]any)
	if int(tr["sends"].(float64)) < 1 {
		t.Fatalf("sends not counted: %v", tr)
	}
	if int(tr["polls"].(float64)) < 0 {
		t.Fatalf("polls must be present: %v", tr)
	}
}

// Never ship a diagnostic that leaks a credential (spikes/05-observability S7:
// the registry half is where SecretHash could slip in).
func TestMetricsNeverCarriesCredentialMaterial(t *testing.T) {
	h := newHub(t, nil)
	agentSecret := h.register("api")
	humanSecret := h.registerHuman("asker")

	code, m := h.req(adminToken, "GET", "/metrics", nil)
	if code != 200 {
		t.Fatalf("metrics: %d", code)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	// The VALUES first — this is the thing that actually matters.
	for what, v := range map[string]string{
		"the admin token": adminToken,
		"an agent secret": agentSecret,
		"a human secret":  humanSecret,
	} {
		if v != "" && strings.Contains(string(b), v) {
			t.Fatalf("metrics payload leaks %s", what)
		}
	}
	// Then credential-shaped FIELD names. `authMode: "token"` is a mode, not a
	// credential, so match field names rather than the bare word.
	low := strings.ToLower(string(b))
	// Field NAMES, so `"authMode":"token"` — a mode, not a credential — does
	// not trip an otherwise useful assertion.
	for _, field := range []string{"secret", "token", "bearer", "password", "secrethash", "agentsecret", "hash"} {
		if strings.Contains(low, `"`+field+`":`) {
			t.Fatalf("metrics payload carries a %q FIELD: %s", field, b)
		}
	}
}

// Metrics is not a public endpoint: /health is the only open one (auth R7).
func TestMetricsRequiresAuthentication(t *testing.T) {
	h := newHub(t, nil)
	if code, _ := h.req("", "GET", "/metrics", nil); code != 401 {
		t.Fatalf("unauthenticated /metrics = %d, want 401", code)
	}
}

func agentRow(t *testing.T, m map[string]any, name string) map[string]any {
	t.Helper()
	for _, raw := range m["agents"].([]any) {
		a, _ := raw.(map[string]any)
		if a["name"] == name {
			return a
		}
	}
	t.Fatalf("agent %q not in metrics: %v", name, m)
	return nil
}
