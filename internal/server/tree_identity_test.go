package server

import "testing"

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
