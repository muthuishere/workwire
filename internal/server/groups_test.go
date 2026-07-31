package server

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
)

// --- helpers -----------------------------------------------------------

func (h *hub) groupJoin(token, group string) (int, map[string]any) {
	h.t.Helper()
	return h.req(token, "POST", "/groups/"+url.PathEscape(group)+"/join", map[string]string{})
}

func (h *hub) groupLeave(token, group string) (int, map[string]any) {
	h.t.Helper()
	return h.req(token, "POST", "/groups/"+url.PathEscape(group)+"/leave", map[string]string{})
}

// groupList returns name -> sorted members from GET /groups.
func (h *hub) groupList(token string) map[string][]string {
	h.t.Helper()
	code, out := h.req(token, "GET", "/groups", nil)
	if code != 200 {
		h.t.Fatalf("GET /groups: %d %v", code, out)
	}
	got := map[string][]string{}
	list, _ := out["groups"].([]any)
	for _, raw := range list {
		g, _ := raw.(map[string]any)
		name, _ := g["name"].(string)
		var members []string
		if ms, ok := g["members"].([]any); ok {
			for _, m := range ms {
				members = append(members, fmt.Sprint(m))
			}
		}
		sort.Strings(members)
		got[name] = members
	}
	return got
}

// registerHuman registers a peer with kind:"human".
func (h *hub) registerHuman(name string) string {
	h.t.Helper()
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": name, "kind": "human"})
	if code != 201 {
		h.t.Fatalf("register human %s: %d %v", name, code, out)
	}
	return out["agentSecret"].(string)
}

// recipientsOf reads the delivered envelope's `to`, in either wire shape.
func recipientsOf(m map[string]any) []string {
	switch v := m["to"].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, n := range v {
			out = append(out, fmt.Sprint(n))
		}
		return out
	}
	return nil
}

// --- membership --------------------------------------------------------

// A group is created by joining it: there is no create verb, no owner and
// no privileges for whoever arrived first (ADR-012).
func TestGroupCreateOnJoin(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	if got := h.groupList(web)["@payments"]; got != nil {
		t.Fatalf("@payments should not exist yet, got %v", got)
	}
	code, out := h.groupJoin(web, "@payments")
	if code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	if got := h.groupList(web)["@payments"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("create-on-join failed: %v", got)
	}
	// A second joiner has exactly the same standing as the first.
	api := h.register("api")
	if code, out := h.groupJoin(api, "@payments"); code != 200 {
		t.Fatalf("second join: %d %v", code, out)
	}
	if got := h.groupList(web)["@payments"]; strings.Join(got, ",") != "api,web" {
		t.Fatalf("members: %v", got)
	}
}

// Everybody lands in the lobby at registration — agent and human alike.
func TestDefaultGroupAutoJoinAtRegistration(t *testing.T) {
	h := newHub(t, nil)
	cases := []struct {
		name   string
		human  bool
		expect string
	}{
		{name: "agent-peer", expect: "agent-peer"},
		{name: "muthu", human: true, expect: "muthu"},
	}
	var token string
	for _, c := range cases {
		if c.human {
			token = h.registerHuman(c.name)
		} else {
			token = h.register(c.name)
		}
		members := h.groupList(token)["@all"]
		found := false
		for _, m := range members {
			if m == c.expect {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s (human=%v) not in @all: %v", c.name, c.human, members)
		}
	}
}

// Leaving @all is how a peer goes quiet; @all itself persists even empty.
func TestLeaveDefaultGroupAndEmptyGroupGC(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	if code, out := h.groupLeave(web, "@all"); code != 200 {
		t.Fatalf("leave @all: %d %v", code, out)
	}
	groups := h.groupList(web)
	if got, ok := groups["@all"]; !ok {
		t.Fatal("@all was garbage-collected; the lobby must persist")
	} else if len(got) != 0 {
		t.Fatalf("@all should be empty now: %v", got)
	}
	// An ad-hoc group evaporates instead of rotting in the listing.
	if code, _ := h.groupJoin(web, "@adhoc"); code != 200 {
		t.Fatal("join @adhoc")
	}
	if code, out := h.groupLeave(web, "@adhoc"); code != 200 || out["collected"] != true {
		t.Fatalf("leave @adhoc: %d %v", code, out)
	}
	if _, ok := h.groupList(web)["@adhoc"]; ok {
		t.Fatal("empty @adhoc was not garbage-collected")
	}
	// Leaving something you are not in is a 404, not a silent success.
	if code, _ := h.groupLeave(web, "@nope"); code != 404 {
		t.Fatalf("leave a group you are not in: %d", code)
	}
}

// A group and a peer may never share a bare name — in either order.
func TestGroupPeerNameCollision(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	// group -> peer: `@web` collides with the registered peer `web`
	if code, out := h.groupJoin(web, "@web"); code != 409 {
		t.Fatalf("group colliding with a peer should be rejected: %d %v", code, out)
	}
	// peer -> group: register `platform` after `@platform` exists
	if code, _ := h.groupJoin(web, "@platform"); code != 200 {
		t.Fatal("join @platform")
	}
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "platform"})
	if code != 409 {
		t.Fatalf("peer colliding with a group should be rejected: %d %v", code, out)
	}
	if sugg, _ := out["suggestion"].(string); sugg != "platform-2" {
		t.Fatalf("want a usable suggestion, got %q", sugg)
	}
}

// Membership is registry state and survives a hub restart.
func TestGroupMembershipSurvivesRestart(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	if code, _ := h.groupJoin(web, "@platform"); code != 200 {
		t.Fatal("join")
	}
	if code, _ := h.groupLeave(web, "@all"); code != 200 {
		t.Fatal("leave @all")
	}
	h2 := h.restart()
	groups := h2.groupList(web)
	if got := groups["@platform"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("@platform lost across restart: %v", got)
	}
	if got := groups["@all"]; len(got) != 0 {
		t.Fatalf("leaving @all did not survive restart: %v", got)
	}
}

// --- addressing --------------------------------------------------------

// `to:"@group"` expands ONCE, at ingest, to a snapshot of current members;
// a later joiner does not retroactively enter that thread.
func TestGroupExpandsAtIngestSnapshot(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	api := h.register("api")
	for _, tok := range []string{web, api} {
		if code, _ := h.groupJoin(tok, "@platform"); code != 200 {
			t.Fatal("join @platform")
		}
	}
	code, out := h.send(web, map[string]any{"to": "@platform", "text": "should /send take an array?"})
	if code != 200 {
		t.Fatalf("send to group: %d %v", code, out)
	}
	threadID := out["thread_id"].(string)
	msgs, _ := h.inboxOf(api, "api", 0)
	if len(msgs) != 1 {
		t.Fatalf("group member did not receive: %v", msgs)
	}
	if got := recipientsOf(msgs[0]); strings.Join(got, ",") != "api" {
		t.Fatalf("sender must be excluded from their own fan-out: %v", got)
	}
	// dba joins AFTER the fact — no retroactive delivery.
	dba := h.register("dba")
	if code, _ := h.groupJoin(dba, "@platform"); code != 200 {
		t.Fatal("late join")
	}
	if late, _ := h.inboxOf(dba, "dba", 0); len(late) != 0 {
		t.Fatalf("late joiner was retroactively added to the thread: %v", late)
	}
	// ...but discovery still lets them walk in (ADR-011).
	code, tl := h.req(dba, "GET", "/threads", nil)
	if code != 200 {
		t.Fatalf("threads: %d", code)
	}
	found := false
	for _, raw := range tl["threads"].([]any) {
		e := raw.(map[string]any)
		if e["thread_id"] == threadID {
			found = true
			if e["member"] != false {
				t.Fatal("late joiner should not be a member yet")
			}
		}
	}
	if !found {
		t.Fatal("late joiner cannot discover the thread")
	}
}

// Groups and individual names mix freely in one `to`, deduped.
func TestMixedGroupAndIndividualAddressing(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	api := h.register("api")
	dba := h.register("dba")
	for _, tok := range []string{web, api} {
		if code, _ := h.groupJoin(tok, "@platform"); code != 200 {
			t.Fatal("join")
		}
	}
	// `api` appears twice (once via the group, once by name) and `web` is
	// the sender: one delivery each, sender excluded.
	code, out := h.send(web, map[string]any{"to": []string{"@platform", "api", "dba"}, "text": "topic"})
	if code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	for _, c := range []struct {
		name, token string
		want        int
	}{
		{"api", api, 1},
		{"dba", dba, 1},
		{"web", web, 0},
	} {
		msgs, _ := h.inboxOf(c.token, c.name, 0)
		if len(msgs) != c.want {
			t.Fatalf("%s: want %d deliveries, got %d", c.name, c.want, len(msgs))
		}
	}
	msgs, _ := h.inboxOf(api, "api", 0)
	got := recipientsOf(msgs[0])
	sort.Strings(got)
	if strings.Join(got, ",") != "api,dba" {
		t.Fatalf("recipients not deduped/sender-excluded: %v", got)
	}
}

// Addressing a group nobody created is a loud 400, not a silent no-op.
func TestUnknownGroupRejected(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	code, out := h.send(web, map[string]any{"to": "@nobody", "text": "hello?"})
	if code != 400 {
		t.Fatalf("want 400 for an unknown group, got %d %v", code, out)
	}
}

// --- consent -----------------------------------------------------------

// An invite is an ordinary MESSAGE: it changes no membership anywhere.
func TestInviteDeliversMessageAndChangesNothing(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	dba := h.register("dba")
	if code, _ := h.groupJoin(web, "@payments"); code != 200 {
		t.Fatal("join")
	}
	code, out := h.send(web, map[string]any{
		"to":   "dba",
		"kind": "invite",
		"text": "you are invited to @payments. Join with: workwire group join @payments",
	})
	if code != 200 {
		t.Fatalf("invite send: %d %v", code, out)
	}
	msgs, _ := h.inboxOf(dba, "dba", 0)
	if len(msgs) != 1 || !strings.Contains(msgs[0]["text"].(string), "group join @payments") {
		t.Fatalf("invite envelope not delivered: %v", msgs)
	}
	if got := h.groupList(web)["@payments"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("an invite must not mutate membership: %v", got)
	}
}

// NO endpoint may add peer B to a group on peer A's say-so — that would let
// anyone force-wake anyone else's session.
func TestNoPeerCanAddAnotherPeer(t *testing.T) {
	h := newHub(t, nil)
	web := h.register("web")
	h.register("dba")
	if code, _ := h.groupJoin(web, "@payments"); code != 200 {
		t.Fatal("join")
	}
	cases := []struct {
		name, token, verb string
	}{
		{"agent adds a peer", web, "join"},
		{"agent removes a peer", web, "leave"},
		{"admin adds a peer", adminToken, "join"},
	}
	for _, c := range cases {
		code, out := h.req(c.token, "POST", "/groups/@payments/"+c.verb, map[string]string{"peer": "dba"})
		if code != 403 {
			t.Fatalf("%s: want 403, got %d %v", c.name, code, out)
		}
	}
	if got := h.groupList(web)["@payments"]; len(got) != 1 || got[0] != "web" {
		t.Fatalf("membership was changed by a third party: %v", got)
	}
}
