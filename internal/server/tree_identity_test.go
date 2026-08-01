package server

import (
	"strings"
	"testing"
)

// One tree, one identity — with as many NAMES as people actually use
// (ADR-015). On 2026-08-01 `muthuishere/toolnexus@cljc` was on the wire three
// times, as `clojure`, `toolnexus-cljc` and `toolnexus-clojure`, all at
// 2f11e8a. Three names was never the problem. Three separate inboxes was.
func TestOneTreeManyNamesIsOneIdentity(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false)
	h.registerPeer("clojure", "agent", card)

	for _, alias := range []string{"toolnexus-cljc", "toolnexus-clojure"} {
		code, out := h.req(adminToken, "POST", "/agents", map[string]any{
			"name": alias, "origin": card["origin"],
		})
		if code != 200 {
			t.Fatalf("%s: got %d %v, want 200 (aliased, not refused)", alias, code, out)
		}
		if out["aliasOf"] != "clojure" || out["name"] != "clojure" {
			t.Fatalf("%s must resolve to clojure, got %v", alias, out)
		}
	}

	// The registry shows ONE peer, not three.
	code, list := h.req(adminToken, "GET", "/agents", nil)
	if code != 200 {
		t.Fatalf("list: %d", code)
	}
	agents := list["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("want 1 identity, got %d: %v", len(agents), list)
	}
}

// The point of aliasing: whichever label a peer picked, the question lands in
// the same inbox. This is the failure that made half the questions vanish.
func TestEveryAliasDeliversToTheSameInbox(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false)
	h.registerPeer("clojure", "agent", card)
	h.req(adminToken, "POST", "/agents", map[string]any{"name": "toolnexus-cljc", "origin": card["origin"]})
	asker := h.registerHuman("asker")

	for _, label := range []string{"clojure", "toolnexus-cljc"} {
		if code, out := h.req(asker, "POST", "/send", map[string]any{
			"to": []string{label}, "text": "addressed as " + label,
		}); code != 200 {
			t.Fatalf("send to %s: %d %v", label, code, out)
		}
	}
	code, inbox := h.req(adminToken, "GET", "/inbox?agent=clojure&since=0", nil)
	if code != 200 {
		t.Fatalf("inbox: %d", code)
	}
	if n := len(inbox["messages"].([]any)); n != 2 {
		t.Fatalf("canonical inbox has %d of 2 messages — an alias kept its own inbox", n)
	}
	// And reading as the ALIAS shows the same inbox, not an empty one.
	_, aliasInbox := h.req(adminToken, "GET", "/inbox?agent=toolnexus-cljc&since=0", nil)
	if n := len(aliasInbox["messages"].([]any)); n != 2 {
		t.Fatalf("alias inbox has %d of 2 messages", n)
	}
}

// A different branch is a different codebase and stays a different peer.
func TestDifferentBranchesOfOneRepoStayDifferentPeers(t *testing.T) {
	h := newHub(t, nil)
	h.registerPeer("toolnexus-main", "agent", originCard("muthuishere/toolnexus", "main", "aaa1111", false))
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{
		"name":   "toolnexus-cljc",
		"origin": originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false)["origin"],
	})
	if code != 201 {
		t.Fatalf("second branch must be its own peer: %d %v", code, out)
	}
}

// Re-registering the same name from the same tree is a restart.
func TestSamePeerRestartingIsNotAnAlias(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/koine", "main", "deca1ff", false)
	secret := h.registerPeer("koine-main", "agent", card)
	code, out := h.req(secret, "POST", "/agents", map[string]any{"name": "koine-main", "origin": card["origin"]})
	if code != 200 || out["alias"] != nil {
		t.Fatalf("restart must not create an alias: %d %v", code, out)
	}
}

// Absent provenance is not a claim: a bare peer neither aliases nor blocks.
func TestNoProvenanceNeverAliases(t *testing.T) {
	h := newHub(t, nil)
	h.register("plain-a")
	if code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "plain-b"}); code != 201 {
		t.Fatalf("second bare peer: %d %v", code, out)
	}
}

// A person is not a session, even in the same folder. Aliasing a human onto an
// agent would hand that session human precedence at closure (ADR-011 §3), or
// take the person's name away entirely — which is what happened the first time
// `workwire join muthu --human` ran inside a repo that was already on the wire.
func TestAHumanIsNeverAliasedOntoAnAgent(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/workwire", "main", "d6cffb8", false)
	h.registerPeer("workwire-main", "agent", card)

	code, out := h.req(adminToken, "POST", "/agents", map[string]any{
		"name": "muthu", "kind": "human", "origin": card["origin"],
	})
	if code != 201 {
		t.Fatalf("a human in an agent's tree must get its own identity: %d %v", code, out)
	}
	if out["aliasOf"] != nil {
		t.Fatalf("human aliased onto an agent: %v", out)
	}
	code, card2 := h.req(adminToken, "GET", "/agents/muthu/card", nil)
	if code != 200 || card2["kind"] != "human" {
		t.Fatalf("muthu must exist as a human peer: %d %v", code, card2)
	}
}

// People address the WORK, not the label. A peer renames, a worktree changes
// branch, an alias is dropped — and every note that said `koine` points at
// nothing, with the message accepted and black-holed because an unheld name
// still looks like a valid recipient.
func TestAddressingARepoReachesThePeerStandingInIt(t *testing.T) {
	h := newHub(t, nil)
	h.registerPeer("koine-main", "agent", originCard("muthuishere/koine", "main", "deca1ff", false))
	sender := h.registerHuman("asker")

	for _, form := range []string{"koine", "muthuishere/koine", "koine@main", "muthuishere/koine@main"} {
		code, out := h.req(sender, "POST", "/send", map[string]any{"to": []string{form}, "text": "probe " + form})
		if code != 200 {
			t.Fatalf("send to %q: %d %v", form, code, out)
		}
		thread, _ := out["thread_id"].(string)
		_, inbox := h.req(adminToken, "GET", "/inbox?agent=koine-main&since=0", nil)
		found := false
		for _, raw := range inbox["messages"].([]any) {
			if m, _ := raw.(map[string]any); m["thread_id"] == thread {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q did not reach the peer standing in that tree", form)
		}
	}
}

// Two live branches of one repo are two codebases with two different answers.
// Picking one silently is how a question gets a confidently wrong reply.
func TestAmbiguousRepoAddressingIsReportedNotGuessed(t *testing.T) {
	h := newHub(t, nil)
	a := h.registerPeer("tn-main", "agent", originCard("muthuishere/toolnexus", "main", "aaa1111", false))
	b := h.registerPeer("tn-cljc", "agent", originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false))
	h.req(a, "POST", "/agents/tn-main/listen-lease", map[string]string{})
	h.req(b, "POST", "/agents/tn-cljc/listen-lease", map[string]string{})
	sender := h.registerHuman("asker")

	code, out := h.req(sender, "POST", "/send", map[string]any{"to": []string{"toolnexus"}, "text": "which one?"})
	if code != 400 {
		t.Fatalf("ambiguous repo = %d %v, want 400 naming the candidates", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "tn-main") || !strings.Contains(msg, "tn-cljc") {
		t.Fatalf("the error must name both peers: %q", msg)
	}
}
