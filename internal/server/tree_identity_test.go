package server

import "testing"

// One working tree, one peer (registry-a2a R12).
//
// On 2026-08-01 `muthuishere/toolnexus@cljc` was on the wire three times — as
// `clojure`, `toolnexus-cljc` and `toolnexus-clojure`, all at the same commit
// — and peers sent to whichever alias they had seen most recently. Half the
// questions reached the half with nobody attached.
func TestOneTreeCannotRegisterUnderThreeNames(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false)
	h.registerPeer("clojure", "agent", card)

	for _, alias := range []string{"toolnexus-cljc", "toolnexus-clojure"} {
		code, out := h.req(adminToken, "POST", "/agents", map[string]any{
			"name": alias, "origin": card["origin"],
		})
		if code != 409 {
			t.Fatalf("%s: got %d %v, want 409", alias, code, out)
		}
		if out["peer"] != "clojure" {
			t.Fatalf("%s: conflict must name the peer that holds the tree, got %v", alias, out)
		}
	}
	// And nothing was created behind the refusal.
	if code, _ := h.req(adminToken, "GET", "/agents/toolnexus-cljc/card", nil); code != 404 {
		t.Fatalf("refused alias must not exist, got %d", code)
	}
}

// A different branch of the same repo IS a different peer — that is the whole
// point of <repo>-<branch>.
func TestDifferentBranchesOfOneRepoAreDifferentPeers(t *testing.T) {
	h := newHub(t, nil)
	h.registerPeer("toolnexus-main", "agent", originCard("muthuishere/toolnexus", "main", "aaa1111", false))
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{
		"name":   "toolnexus-cljc",
		"origin": originCard("muthuishere/toolnexus", "cljc", "2f11e8a", false)["origin"],
	})
	if code != 201 {
		t.Fatalf("second branch must register: %d %v", code, out)
	}
}

// Re-registering the same name from the same tree is a restart, not a fork.
func TestSamePeerRestartingIsNotATreeConflict(t *testing.T) {
	h := newHub(t, nil)
	card := originCard("muthuishere/koine", "main", "deca1ff", false)
	secret := h.registerPeer("koine-main", "agent", card)
	code, out := h.req(secret, "POST", "/agents", map[string]any{
		"name": "koine-main", "origin": card["origin"],
	})
	if code != 200 {
		t.Fatalf("re-registration: %d %v", code, out)
	}
}

// A peer with no provenance blocks nobody and is blocked by nobody: the guard
// keys on evidence, and absent evidence is not a claim.
func TestNoProvenanceNeverConflicts(t *testing.T) {
	h := newHub(t, nil)
	h.register("plain-a")
	if code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "plain-b"}); code != 201 {
		t.Fatalf("second bare peer: %d %v", code, out)
	}
}

// A dead registration is litter, not a rival — otherwise a renamed peer could
// never take over the tree it actually owns.
