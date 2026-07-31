package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

// sendAs posts /send and returns the status and decoded body.
func (h *hub) send(token string, body map[string]any) (int, map[string]any) {
	h.t.Helper()
	return h.req(token, "POST", "/send", body)
}

// inboxOf drains an agent's inbox from `since` with no wait.
func (h *hub) inboxOf(token, agent string, since int64) (msgs []map[string]any, next int64) {
	h.t.Helper()
	_, out := h.req(token, "GET", fmt.Sprintf("/inbox?agent=%s&since=%d&wait=0", agent, since), nil)
	if n, ok := out["next"].(float64); ok {
		next = int64(n)
	}
	return msgsOf(out), next
}

func idsOf(msgs []map[string]any) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m["id"].(string))
	}
	return out
}

// --- `to` shapes -------------------------------------------------------

func TestToStringUnchanged(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	code, out := h.send(alice, map[string]any{"to": "repoA", "text": "one to one"})
	if code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	msgs, next := h.inboxOf(repoA, "repoA", 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(msgs))
	}
	// the wire shape of `to` stays a plain string for a single recipient
	if got := msgs[0]["to"]; got != "repoA" {
		t.Fatalf("to should stay a JSON string, got %#v", got)
	}
	if next == 0 {
		t.Fatal("cursor did not advance")
	}
	if more, _ := h.inboxOf(repoA, "repoA", next); len(more) != 0 {
		t.Fatalf("cursor not honored: %v", idsOf(more))
	}
}

func TestToArrayFansOutOneEnvelope(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	secrets := map[string]string{
		"repoA": h.register("repoA"),
		"repoB": h.register("repoB"),
		"repoC": h.register("repoC"),
	}
	code, out := h.send(alice, map[string]any{
		"to": []string{"repoA", "repoB", "repoC"}, "text": "should we cache tokens?",
	})
	if code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	shared := out["id"].(string)

	cursors := map[string]int64{}
	for name, sec := range secrets {
		msgs, next := h.inboxOf(sec, name, 0)
		if len(msgs) != 1 {
			t.Fatalf("%s: want 1 delivery, got %d", name, len(msgs))
		}
		if msgs[0]["id"] != shared {
			t.Fatalf("%s: envelope id must be shared: %v != %v", name, msgs[0]["id"], shared)
		}
		to, _ := msgs[0]["to"].([]any)
		if len(to) != 3 {
			t.Fatalf("%s: multi-recipient to must stay an array, got %#v", name, msgs[0]["to"])
		}
		cursors[name] = next
	}
	// per-recipient sequence numbers: one envelope, three distinct cursors
	seen := map[int64]bool{}
	for name, c := range cursors {
		if c == 0 {
			t.Fatalf("%s: cursor did not advance", name)
		}
		if seen[c] {
			t.Fatalf("%s: cursor %d shared with another recipient", name, c)
		}
		seen[c] = true
	}
	// dedupe-by-id still holds: nobody gets it twice
	for name, sec := range secrets {
		if msgs, _ := h.inboxOf(sec, name, cursors[name]); len(msgs) != 0 {
			t.Fatalf("%s: redelivered after cursor: %v", name, idsOf(msgs))
		}
	}
	// the sender is not a recipient of its own envelope
	if msgs, _ := h.inboxOf(alice, "alice", 0); len(msgs) != 0 {
		t.Fatalf("sender received its own envelope: %v", idsOf(msgs))
	}
}

func TestSendRejectsUnaddressedNewThread(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	code, out := h.send(alice, map[string]any{"text": "into the void"})
	if code != 400 {
		t.Fatalf("want 400, got %d %v", code, out)
	}
	if !strings.Contains(out["error"].(string), "to is required") {
		t.Fatalf("unhelpful error: %v", out["error"])
	}
}

// --- membership --------------------------------------------------------

func TestThreadMembershipAndFanout(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	repoB := h.register("repoB")
	repoC := h.register("repoC")

	_, out := h.send(alice, map[string]any{"to": []string{"repoA", "repoB"}, "text": "topic"})
	tid := out["thread_id"].(string)
	aCur, bCur := int64(0), int64(0)
	if _, n := h.inboxOf(repoA, "repoA", 0); true {
		aCur = n
	}
	if _, n := h.inboxOf(repoB, "repoB", 0); true {
		bCur = n
	}
	aliceCur := int64(0)

	t.Run("reply with only thread_id reaches all members but not the sender", func(t *testing.T) {
		code, out := h.send(repoA, map[string]any{"thread_id": tid, "text": "my code says otherwise"})
		if code != 200 {
			t.Fatalf("reply: %d %v", code, out)
		}
		reply := out["id"].(string)
		for _, c := range []struct {
			name, tok string
			cur       *int64
		}{{"alice", alice, &aliceCur}, {"repoB", repoB, &bCur}} {
			msgs, next := h.inboxOf(c.tok, c.name, *c.cur)
			if len(msgs) != 1 || msgs[0]["id"] != reply {
				t.Fatalf("%s: want the reply, got %v", c.name, idsOf(msgs))
			}
			*c.cur = next
		}
		if msgs, _ := h.inboxOf(repoA, "repoA", aCur); len(msgs) != 0 {
			t.Fatalf("sender received its own reply: %v", idsOf(msgs))
		}
	})

	t.Run("sending into a thread joins you to it", func(t *testing.T) {
		if code, out := h.send(repoC, map[string]any{"thread_id": tid, "text": "late but relevant"}); code != 200 {
			t.Fatalf("join: %d %v", code, out)
		}
		_, ts := h.req(alice, "GET", "/threads", nil)
		found := false
		for _, raw := range ts["threads"].([]any) {
			th := raw.(map[string]any)
			if th["thread_id"] != tid {
				continue
			}
			found = true
			var names []string
			for _, m := range th["members"].([]any) {
				names = append(names, m.(string))
			}
			for _, want := range []string{"alice", "repoA", "repoB", "repoC"} {
				if !contains(names, want) {
					t.Fatalf("member %s missing from %v", want, names)
				}
			}
			if th["initiator"] != "alice" {
				t.Fatalf("initiator: %v", th["initiator"])
			}
		}
		if !found {
			t.Fatalf("thread %s not listed", tid)
		}
		// the new member now receives fan-out
		if code, _ := h.send(alice, map[string]any{"thread_id": tid, "text": "noted"}); code != 200 {
			t.Fatal("follow-up send")
		}
		if msgs, _ := h.inboxOf(repoC, "repoC", 0); len(msgs) == 0 {
			t.Fatal("joined member got no fan-out")
		}
	})

	t.Run("membership survives a hub restart", func(t *testing.T) {
		h2 := h.restart()
		ts, ok := h2.store.ThreadState(tid, h2.cfg.MaxThreadMessages)
		if !ok {
			t.Fatal("thread lost across restart")
		}
		if ts.Initiator != "alice" {
			t.Fatalf("initiator lost: %q", ts.Initiator)
		}
		for _, want := range []string{"alice", "repoA", "repoB", "repoC"} {
			if !contains(ts.Members, want) {
				t.Fatalf("member %s lost across restart: %v", want, ts.Members)
			}
		}
	})
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// --- convergence -------------------------------------------------------

func TestResolveClosesThread(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	_, out := h.send(alice, map[string]any{"to": []string{"repoA"}, "text": "cache tokens?"})
	tid := out["thread_id"].(string)

	t.Run("a participant may not resolve", func(t *testing.T) {
		code, out := h.send(repoA, map[string]any{"thread_id": tid, "text": "settled", "kind": "resolved"})
		if code != 403 {
			t.Fatalf("want 403, got %d %v", code, out)
		}
		if msg := out["error"].(string); !strings.Contains(msg, "proposal") || !strings.Contains(msg, "alice") {
			t.Fatalf("error must name the initiator and point at proposal: %q", msg)
		}
	})

	t.Run("a participant proposal is accepted and does not close the thread", func(t *testing.T) {
		if code, out := h.send(repoA, map[string]any{"thread_id": tid, "text": "I'd cache", "kind": "proposal"}); code != 200 {
			t.Fatalf("proposal: %d %v", code, out)
		}
		ts, _ := h.store.ThreadState(tid, h.cfg.MaxThreadMessages)
		if ts.State != "open" {
			t.Fatalf("proposal closed the thread: %s", ts.State)
		}
		if code, _ := h.send(alice, map[string]any{"thread_id": tid, "text": "still talking"}); code != 200 {
			t.Fatal("thread should still accept sends")
		}
	})

	t.Run("the initiator resolves and further sends 409", func(t *testing.T) {
		if code, out := h.send(alice, map[string]any{"thread_id": tid, "text": "we cache", "kind": "resolved"}); code != 200 {
			t.Fatalf("resolve: %d %v", code, out)
		}
		ts, _ := h.store.ThreadState(tid, h.cfg.MaxThreadMessages)
		if ts.State != "resolved" {
			t.Fatalf("state: %s", ts.State)
		}
		for _, tok := range []string{alice, repoA} {
			code, out := h.send(tok, map[string]any{"thread_id": tid, "text": "one more thing"})
			if code != 409 {
				t.Fatalf("want 409 after resolve, got %d %v", code, out)
			}
			if !strings.Contains(out["error"].(string), "resolved") {
				t.Fatalf("error must say the thread is resolved: %v", out["error"])
			}
		}
	})
}

func TestRoundCapStallsThread(t *testing.T) {
	const cap = 6
	h := newHub(t, func(c *config.Config) { c.MaxThreadMessages = cap })
	alice := h.register("alice")
	repoA := h.register("repoA")
	_, out := h.send(alice, map[string]any{"to": []string{"repoA"}, "text": "round 1"})
	tid := out["thread_id"].(string)

	tokens := []string{repoA, alice}
	for i := 1; i < cap; i++ { // fill up to exactly the cap
		code, out := h.send(tokens[i%2], map[string]any{"thread_id": tid, "text": fmt.Sprintf("round %d", i+1)})
		if code != 200 {
			t.Fatalf("message %d rejected early: %d %v", i+1, code, out)
		}
	}
	ts, _ := h.store.ThreadState(tid, cap)
	if ts.Count != cap || ts.State != "stalled" {
		t.Fatalf("want %d messages and state stalled, got %d/%s", cap, ts.Count, ts.State)
	}
	code, out := h.send(repoA, map[string]any{"thread_id": tid, "text": "one round too many"})
	if code != 409 {
		t.Fatalf("want 409 at the cap, got %d %v", code, out)
	}
	msg := out["error"].(string)
	for _, want := range []string{"stalled", fmt.Sprint(cap), "maxThreadMessages", "WORKWIRE_MAX_THREAD_MESSAGES", "alice"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("cap error must name %q: %q", want, msg)
		}
	}
}

// --- persona + late-joiner context -------------------------------------

func TestPersonaRoundTripsToContext(t *testing.T) {
	h := newHub(t, nil)
	persona := "owns the auth service; will not speak for the web UI"
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "repoA", "persona": persona})
	if code != 201 {
		t.Fatalf("register: %d %v", code, out)
	}
	repoA := out["agentSecret"].(string)
	alice := h.register("alice")
	late := h.register("late")

	t.Run("persona on the card and the listing", func(t *testing.T) {
		_, card := h.req(alice, "GET", "/agents/repoA/card", nil)
		if card["persona"] != persona {
			t.Fatalf("card persona: %v", card["persona"])
		}
		_, list := h.req(alice, "GET", "/agents", nil)
		found := false
		for _, raw := range list["agents"].([]any) {
			a := raw.(map[string]any)
			if a["name"] == "repoA" && a["persona"] == persona {
				found = true
			}
		}
		if !found {
			t.Fatalf("persona missing from /agents: %v", list["agents"])
		}
	})

	_, out = h.send(alice, map[string]any{"to": []string{"repoA"}, "text": "cache tokens?"})
	tid := out["thread_id"].(string)
	h.send(repoA, map[string]any{"thread_id": tid, "text": "no — they rotate every 5m"})

	t.Run("context serves a member who never polled the thread", func(t *testing.T) {
		// `late` is pulled in only now; it has never seen the thread.
		if code, out := h.send(alice, map[string]any{"to": []string{"late"}, "thread_id": tid, "text": "thoughts?"}); code != 200 {
			t.Fatalf("pull in late joiner: %d %v", code, out)
		}
		msgs, _ := h.inboxOf(late, "late", 0)
		if len(msgs) != 1 {
			t.Fatalf("want 1 delivery, got %d", len(msgs))
		}
		raw, _ := json.Marshal(msgs[0]["context"])
		var ctx []map[string]any
		if err := json.Unmarshal(raw, &ctx); err != nil {
			t.Fatal(err)
		}
		if len(ctx) < 3 {
			t.Fatalf("late joiner needs the thread so far, got %d entries", len(ctx))
		}
		gotPersona := false
		for _, e := range ctx {
			if e["kind"] != "context" {
				t.Fatalf("context entry not stamped: %v", e)
			}
			if e["from"] == "repoA" && e["persona"] == persona {
				gotPersona = true
			}
		}
		if !gotPersona {
			t.Fatalf("speaker persona missing from projected context: %v", ctx)
		}
	})
}
