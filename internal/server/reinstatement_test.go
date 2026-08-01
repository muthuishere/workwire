package server

import (
	"strings"
	"testing"
	"time"
)

// ageDissent backdates the dissent envelope on a thread, standing in for the
// passage of the abandon window.
func ageDissent(t *testing.T, h *hub, thread string, delta time.Duration) {
	t.Helper()
	list, ok := h.store.Thread(thread, 0)
	if !ok {
		t.Fatalf("thread %s not in store", thread)
	}
	for _, m := range list {
		if m.Env.Kind == "dissent" {
			m.Env.TS = time.Now().Add(delta).UTC().Format(time.RFC3339Nano)
		}
	}
}

// A dissent may only be withdrawn by its author. If that session dies, the
// thread was unclosable FOREVER — a deadlock, not a principle. Dung's
// reinstatement is the repair: an objection nobody can defend stops blocking,
// and stays on the record (ADR-017).
func TestAbandonedDissentStopsBlockingButIsRecorded(t *testing.T) {
	h := newHub(t, nil)
	initiator := h.register("api")
	dissenter := h.register("web")

	_, out := h.req(initiator, "POST", "/send", map[string]any{"to": []string{"web"}, "text": "ship it?"})
	thread, _ := out["thread_id"].(string)

	// web objects, then takes a lease and drops it — the shape of a session
	// that dissented and went away.
	if code, o := h.req(dissenter, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "dissent", "text": "tokens rotate under this",
	}); code != 200 {
		t.Fatalf("dissent: %d %v", code, o)
	}
	// While web is live, the dissent MUST still block: reinstatement may never
	// become a way to talk over someone who is present.
	if code, o := h.req(initiator, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "closing",
	}); code != 409 {
		t.Fatalf("a live dissenter must still block closure: %d %v", code, o)
	}

	// web goes away: no listener, nothing authored since the dissent. It must
	// STILL block until the abandon window has passed — a peer that stepped
	// out for a minute has not abandoned anything.
	h.reg.Forget("web")
	if code, o := h.req(initiator, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "too soon",
	}); code != 409 {
		t.Fatalf("a fresh dissent must block even with no listener: %d %v", code, o)
	}

	// Age it past the window by rewriting the recorded timestamp — the same
	// thing an hour of wall clock would do.
	ageDissent(t, h, thread, -2*time.Hour)

	code, res := h.req(initiator, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "closing over an abandoned objection",
	})
	if code != 200 {
		t.Fatalf("an abandoned dissent must not deadlock the thread: %d %v", code, res)
	}
	// And it is recorded as overridden, not erased.
	_, list := h.req(adminToken, "GET", "/threads", nil)
	for _, raw := range list["threads"].([]any) {
		ts, _ := raw.(map[string]any)
		if ts["thread_id"] != thread {
			continue
		}
		if ts["state"] != "resolved" {
			t.Fatalf("thread should be resolved, got %v", ts["state"])
		}
		over, _ := ts["closed_over"].([]any)
		if len(over) == 0 {
			t.Fatalf("the abandoned objection must be recorded as overridden: %v", ts)
		}
		txt := strings.ToLower(strings.Join([]string{ts["thread_id"].(string)}, ""))
		_ = txt
		return
	}
	t.Fatalf("thread %s not listed", thread)
}

// A dissenter that spoke AFTER dissenting has not abandoned it, even with no
// listener right now — wrongly discarding an objection is the failure this
// design exists to prevent.
func TestADissenterWhoKeptTalkingStillBlocks(t *testing.T) {
	h := newHub(t, nil)
	initiator := h.register("api")
	dissenter := h.register("web")

	_, out := h.req(initiator, "POST", "/send", map[string]any{"to": []string{"web"}, "text": "ship it?"})
	thread, _ := out["thread_id"].(string)
	h.req(dissenter, "POST", "/send", map[string]any{"thread_id": thread, "kind": "dissent", "text": "no"})
	h.req(dissenter, "POST", "/send", map[string]any{"thread_id": thread, "text": "here is the evidence"})

	if code, o := h.req(initiator, "POST", "/send", map[string]any{
		"thread_id": thread, "kind": "resolved", "text": "closing",
	}); code != 409 {
		t.Fatalf("a dissenter still speaking must block closure: %d %v", code, o)
	}
}
