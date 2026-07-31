package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muthuishere/workwire/internal/store"
)

// registerPeer registers a peer with an explicit kind/origin/persona and
// returns its secret.
func (h *hub) registerPeer(name, kind string, card map[string]any) string {
	h.t.Helper()
	body := map[string]any{"name": name, "kind": kind}
	for k, v := range card {
		body[k] = v
	}
	code, out := h.req(adminToken, "POST", "/agents", body)
	if code != 201 {
		h.t.Fatalf("register %s: %d %v", name, code, out)
	}
	sec, _ := out["agentSecret"].(string)
	if sec == "" {
		h.t.Fatalf("register %s: empty secret", name)
	}
	return sec
}

func originCard(repo, branch, commit string, dirty bool) map[string]any {
	return map[string]any{"origin": map[string]any{
		"repo": repo, "branch": branch, "commit": commit, "dirty": dirty,
		"cwd": "/work/" + repo, "host": "testbox",
	}}
}

func (h *hub) threadState(t *testing.T, tid string) store.ThreadState {
	t.Helper()
	ts, ok := h.store.ThreadState(tid, h.cfg.MaxThreadMessages)
	if !ok {
		t.Fatalf("thread %s not found", tid)
	}
	return ts
}

// --- provenance --------------------------------------------------------

func TestOriginRoundTripsToCardListingAndContext(t *testing.T) {
	h := newHub(t, nil)
	api := h.registerPeer("api", "agent", map[string]any{
		"persona": "owns the Go hub",
		"origin": map[string]any{
			"repo": "muthuishere/workwire", "branch": "main", "commit": "a1b2c3d",
			"cwd": "/work/hub", "host": "testbox",
		},
	})
	web := h.registerPeer("web", "agent", originCard("muthuishere/webclient", "feat/tokens", "f9e0d1", true))
	late := h.register("late")

	t.Run("card carries origin and kind", func(t *testing.T) {
		_, card := h.req(api, "GET", "/agents/api/card", nil)
		o, _ := card["origin"].(map[string]any)
		if o == nil || o["repo"] != "muthuishere/workwire" || o["branch"] != "main" || o["commit"] != "a1b2c3d" {
			t.Fatalf("card origin: %v", card["origin"])
		}
		if card["kind"] != "agent" {
			t.Fatalf("card kind: %v", card["kind"])
		}
	})

	t.Run("GET /agents carries origin", func(t *testing.T) {
		_, list := h.req(api, "GET", "/agents", nil)
		found := false
		for _, raw := range list["agents"].([]any) {
			a := raw.(map[string]any)
			if a["name"] != "web" {
				continue
			}
			found = true
			o, _ := a["origin"].(map[string]any)
			if o == nil || o["branch"] != "feat/tokens" || o["dirty"] != true {
				t.Fatalf("listing origin: %v", a["origin"])
			}
		}
		if !found {
			t.Fatalf("web missing from /agents: %v", list["agents"])
		}
	})

	// build a thread so a late joiner sees provenance in projected context
	_, out := h.send(api, map[string]any{"to": []string{"web"}, "text": "do tokens rotate?"})
	tid := out["thread_id"].(string)
	if code, out := h.send(web, map[string]any{"thread_id": tid, "text": "on my branch they do"}); code != 200 {
		t.Fatalf("web reply: %d %v", code, out)
	}

	t.Run("context entries carry origin next to from and persona", func(t *testing.T) {
		if code, out := h.send(api, map[string]any{"to": []string{"late"}, "thread_id": tid, "text": "thoughts?"}); code != 200 {
			t.Fatalf("pull in late: %d %v", code, out)
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
		seen := map[string]map[string]any{}
		for _, e := range ctx {
			if o, ok := e["origin"].(map[string]any); ok {
				seen[e["from"].(string)] = o
			}
		}
		if o := seen["api"]; o == nil || o["branch"] != "main" {
			t.Fatalf("api provenance missing from context: %v", ctx)
		}
		if o := seen["web"]; o == nil || o["branch"] != "feat/tokens" || o["dirty"] != true {
			t.Fatalf("web provenance missing from context: %v", ctx)
		}
	})
}

func TestOriginRefreshesOnHeartbeatReregistration(t *testing.T) {
	h := newHub(t, nil)
	api := h.registerPeer("api", "agent", originCard("muthuishere/workwire", "main", "a1b2c3d", false))
	// heartbeat re-registration with the SAME secret, new branch
	code, out := h.req(api, "POST", "/agents", map[string]any{
		"name": "api", "kind": "agent",
		"origin": map[string]any{"repo": "muthuishere/workwire", "branch": "feat/dissent", "commit": "9f9f9f9", "dirty": true},
	})
	if code != 200 {
		t.Fatalf("re-register: %d %v", code, out)
	}
	_, card := h.req(api, "GET", "/agents/api/card", nil)
	o, _ := card["origin"].(map[string]any)
	if o == nil || o["branch"] != "feat/dissent" || o["commit"] != "9f9f9f9" || o["dirty"] != true {
		t.Fatalf("branch switch not reflected: %v", card["origin"])
	}
}

func TestRegistrationWithoutOriginIsClean(t *testing.T) {
	h := newHub(t, nil)
	// a non-git directory reports cwd/host only: no repo, no branch, no error
	plain := h.registerPeer("plain", "agent", map[string]any{
		"origin": map[string]any{"cwd": "/tmp/scratch", "host": "testbox"},
	})
	code, card := h.req(plain, "GET", "/agents/plain/card", nil)
	if code != 200 {
		t.Fatalf("card: %d %v", code, card)
	}
	o, _ := card["origin"].(map[string]any)
	if o == nil {
		t.Fatalf("origin dropped entirely: %v", card)
	}
	if o["repo"] != nil || o["branch"] != nil {
		t.Fatalf("non-git peer must report no repo/branch: %v", o)
	}
	if o["cwd"] != "/tmp/scratch" {
		t.Fatalf("cwd lost: %v", o)
	}
}

// --- dissent and valid closure -----------------------------------------

// scene builds the multi-app scene of ADR-011 §5: two agents in different
// repos on different branches, plus two humans.
type scene struct {
	h                      *hub
	api, web, muthu, priya string
	tid                    string
}

func newScene(t *testing.T) *scene {
	t.Helper()
	h := newHub(t, nil)
	s := &scene{
		h:   h,
		api: h.registerPeer("api", "agent", originCard("muthuishere/workwire", "main", "a1b2c3d", false)),
		web: h.registerPeer("web", "agent", originCard("muthuishere/webclient", "feat/tokens", "f9e0d1", true)),
		muthu: h.registerPeer("muthu", "human", map[string]any{
			"persona": "owns the API roadmap",
			"origin":  map[string]any{"cwd": "/home/muthu", "host": "laptop"},
		}),
		priya: h.registerPeer("priya", "human", map[string]any{
			"persona": "owns the web roadmap",
			"origin":  map[string]any{"cwd": "/home/priya", "host": "laptop2"},
		}),
	}
	_, out := h.send(s.api, map[string]any{
		"to": []string{"web", "muthu", "priya"}, "text": "do we cache tokens for 24h?",
	})
	s.tid = out["thread_id"].(string)
	return s
}

func TestAgentCloseBlockedByDissentAndAllowedAfterWithdraw(t *testing.T) {
	s := newScene(t)
	h := s.h
	if code, out := h.send(s.web, map[string]any{"thread_id": s.tid, "text": "tokens rotate every 5m on my branch", "kind": "dissent"}); code != 200 {
		t.Fatalf("dissent: %d %v", code, out)
	}
	ts := h.threadState(t, s.tid)
	if len(ts.Dissents) != 1 || ts.Dissents[0].Peer != "web" {
		t.Fatalf("open dissent not tracked: %+v", ts.Dissents)
	}
	if ts.Dissents[0].Origin == nil || ts.Dissents[0].Origin.Branch != "feat/tokens" {
		t.Fatalf("dissent carries no provenance: %+v", ts.Dissents[0].Origin)
	}

	t.Run("the agent initiator may not close over it", func(t *testing.T) {
		code, out := h.send(s.api, map[string]any{"thread_id": s.tid, "text": "we cache", "kind": "resolved"})
		if code != 409 {
			t.Fatalf("want 409, got %d %v", code, out)
		}
		msg := out["error"].(string)
		for _, want := range []string{"web", "feat/tokens", "withdraw", "human"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("409 must name the dissenter, its provenance and both paths (%q missing): %q", want, msg)
			}
		}
		if h.threadState(t, s.tid).State != "open" {
			t.Fatal("rejected close must leave the thread open")
		}
	})

	t.Run("a withdraw by the dissenter unblocks the close", func(t *testing.T) {
		if code, out := h.send(s.web, map[string]any{"thread_id": s.tid, "text": "checked main, you're right", "kind": "withdraw"}); code != 200 {
			t.Fatalf("withdraw: %d %v", code, out)
		}
		if ds := h.threadState(t, s.tid).Dissents; len(ds) != 0 {
			t.Fatalf("withdraw did not clear the dissent: %+v", ds)
		}
		if code, out := h.send(s.api, map[string]any{"thread_id": s.tid, "text": "we cache", "kind": "resolved"}); code != 200 {
			t.Fatalf("close after withdraw: %d %v", code, out)
		}
		if st := h.threadState(t, s.tid); st.State != "resolved" || st.ClosedBy != "api" {
			t.Fatalf("state after close: %+v", st)
		}
	})
}

func TestWithdrawClearsOnlyTheSendersOwnDissent(t *testing.T) {
	s := newScene(t)
	h := s.h
	h.send(s.web, map[string]any{"thread_id": s.tid, "text": "web objects", "kind": "dissent"})
	h.send(s.priya, map[string]any{"thread_id": s.tid, "text": "priya objects", "kind": "dissent"})
	// web withdraws: priya's dissent must survive
	h.send(s.web, map[string]any{"thread_id": s.tid, "text": "fine", "kind": "withdraw"})
	ts := h.threadState(t, s.tid)
	if len(ts.Dissents) != 1 || ts.Dissents[0].Peer != "priya" {
		t.Fatalf("withdraw touched someone else's dissent: %+v", ts.Dissents)
	}
	if ts.Dissents[0].Kind != "human" {
		t.Fatalf("dissenter kind not recorded: %+v", ts.Dissents[0])
	}
}

func TestHumanClosesOverAgentDissentsAndRecordsThem(t *testing.T) {
	s := newScene(t)
	h := s.h
	h.send(s.web, map[string]any{"thread_id": s.tid, "text": "tokens rotate", "kind": "dissent"})
	h.send(s.api, map[string]any{"thread_id": s.tid, "text": "then caching is wrong", "kind": "dissent"})

	t.Run("summary is required", func(t *testing.T) {
		code, out := h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "", "kind": "resolved"})
		if code != 400 {
			t.Fatalf("want 400 without a summary, got %d %v", code, out)
		}
	})

	t.Run("a human closes over N agent dissents", func(t *testing.T) {
		code, out := h.send(s.muthu, map[string]any{
			"thread_id": s.tid, "text": "we ship the 24h cache and revisit in a week", "kind": "resolved",
		})
		if code != 200 {
			t.Fatalf("human close: %d %v", code, out)
		}
		ts := h.threadState(t, s.tid)
		if ts.State != "resolved" || ts.ClosedBy != "muthu" || ts.ClosedByKind != "human" {
			t.Fatalf("closing record: %+v", ts)
		}
		var over []string
		for _, d := range ts.ClosedOver {
			over = append(over, d.Peer)
		}
		if len(over) != 2 || !contains(over, "web") || !contains(over, "api") {
			t.Fatalf("closing record must list the overridden dissents, got %v", over)
		}
	})
}

func TestHumanBlockedByAnotherHumansDissent(t *testing.T) {
	s := newScene(t)
	h := s.h
	h.send(s.priya, map[string]any{"thread_id": s.tid, "text": "the web team absorbs the cost, no", "kind": "dissent"})
	h.send(s.web, map[string]any{"thread_id": s.tid, "text": "and the code disagrees too", "kind": "dissent"})

	t.Run("a human may not overrule a colleague", func(t *testing.T) {
		code, out := h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "shipping it", "kind": "resolved"})
		if code != 409 {
			t.Fatalf("want 409, got %d %v", code, out)
		}
		msg := out["error"].(string)
		if !strings.Contains(msg, "priya") || !strings.Contains(msg, "withdraw") {
			t.Fatalf("409 must name the human dissenter and the withdraw path: %q", msg)
		}
		// the agent dissent is overridable and must not be reported as blocking
		if strings.Contains(msg, "feat/tokens") {
			t.Fatalf("an agent dissent must not be reported as blocking: %q", msg)
		}
	})

	t.Run("after that human withdraws, the close lands", func(t *testing.T) {
		if code, out := h.send(s.priya, map[string]any{"thread_id": s.tid, "text": "ok, convinced", "kind": "withdraw"}); code != 200 {
			t.Fatalf("withdraw: %d %v", code, out)
		}
		if code, out := h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "shipping it, web dissent noted", "kind": "resolved"}); code != 200 {
			t.Fatalf("close after human withdraw: %d %v", code, out)
		}
		ts := h.threadState(t, s.tid)
		if len(ts.ClosedOver) != 1 || ts.ClosedOver[0].Peer != "web" {
			t.Fatalf("the surviving agent dissent must be recorded as closed over: %+v", ts.ClosedOver)
		}
	})
}

func TestOwnHumanDissentDoesNotBlockOwnClose(t *testing.T) {
	s := newScene(t)
	h := s.h
	h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "I still don't like it", "kind": "dissent"})
	if code, out := h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "but we ship it anyway", "kind": "resolved"}); code != 200 {
		t.Fatalf("own dissent blocked own close: %d %v", code, out)
	}
	ts := h.threadState(t, s.tid)
	if len(ts.ClosedOver) != 0 {
		t.Fatalf("you do not override yourself: %+v", ts.ClosedOver)
	}
}

func TestHumanClosesAThreadTheyDidNotInitiate(t *testing.T) {
	s := newScene(t)
	h := s.h
	// priya was addressed but has never spoken; the initiator is `api`.
	if code, out := h.send(s.priya, map[string]any{"thread_id": s.tid, "text": "web's roadmap wins here", "kind": "resolved"}); code != 200 {
		t.Fatalf("human non-initiator close: %d %v", code, out)
	}
	if st := h.threadState(t, s.tid); st.ClosedBy != "priya" {
		t.Fatalf("closed_by: %+v", st)
	}
}

func TestReopenIsHumansOnly(t *testing.T) {
	s := newScene(t)
	h := s.h
	if code, out := h.send(s.api, map[string]any{"thread_id": s.tid, "text": "settled", "kind": "resolved"}); code != 200 {
		t.Fatalf("agent close: %d %v", code, out)
	}

	t.Run("an agent may not reopen", func(t *testing.T) {
		code, out := h.send(s.web, map[string]any{"thread_id": s.tid, "text": "not settled", "kind": "reopen"})
		if code != 403 {
			t.Fatalf("want 403, got %d %v", code, out)
		}
		if h.threadState(t, s.tid).State != "resolved" {
			t.Fatal("a rejected reopen must not reopen the thread")
		}
	})

	t.Run("a human reopens a thread an agent closed", func(t *testing.T) {
		if code, out := h.send(s.priya, map[string]any{"thread_id": s.tid, "text": "we never covered the web side", "kind": "reopen"}); code != 200 {
			t.Fatalf("human reopen: %d %v", code, out)
		}
		ts := h.threadState(t, s.tid)
		if ts.State != "open" || ts.ClosedBy != "" {
			t.Fatalf("reopen did not clear closure: %+v", ts)
		}
		if code, out := h.send(s.web, map[string]any{"thread_id": s.tid, "text": "here is the web side"}); code != 200 {
			t.Fatalf("send after reopen: %d %v", code, out)
		}
	})
}

func TestDissentOnResolvedThreadIsHistoryNotAReopen(t *testing.T) {
	s := newScene(t)
	h := s.h
	if code, out := h.send(s.muthu, map[string]any{"thread_id": s.tid, "text": "we cache; decided", "kind": "resolved"}); code != 200 {
		t.Fatalf("human close: %d %v", code, out)
	}

	t.Run("dissent is still accepted and recorded", func(t *testing.T) {
		code, out := h.send(s.web, map[string]any{"thread_id": s.tid, "text": "for the record: my branch says otherwise", "kind": "dissent"})
		if code != 200 {
			t.Fatalf("dissent on a resolved thread must be accepted: %d %v", code, out)
		}
		ts := h.threadState(t, s.tid)
		if ts.State != "resolved" {
			t.Fatalf("dissent reopened the thread: %+v", ts)
		}
		if len(ts.Dissents) != 0 {
			t.Fatalf("a post-closure dissent is history, not an open dissent: %+v", ts.Dissents)
		}
		// it is still stored on the thread as history
		list, _ := h.store.Thread(s.tid, 0)
		found := false
		for _, st := range list {
			if st.Env.Kind == "dissent" && st.Env.From == "web" {
				found = true
			}
		}
		if !found {
			t.Fatal("post-closure dissent was not preserved")
		}
	})

	t.Run("every other send still 409s", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"thread_id": s.tid, "text": "one more thing"},
			{"thread_id": s.tid, "text": "I'd reconsider", "kind": "proposal"},
			{"thread_id": s.tid, "text": "dropping mine", "kind": "withdraw"},
			{"thread_id": s.tid, "text": "again", "kind": "resolved"},
		} {
			code, out := h.send(s.web, body)
			if code != 409 {
				t.Fatalf("want 409 for %v, got %d %v", body["kind"], code, out)
			}
		}
	})
}

func TestDissentSurvivesRestart(t *testing.T) {
	s := newScene(t)
	h := s.h
	h.send(s.web, map[string]any{"thread_id": s.tid, "text": "objection", "kind": "dissent"})
	h2 := h.restart()
	ts, ok := h2.store.ThreadState(s.tid, h2.cfg.MaxThreadMessages)
	if !ok {
		t.Fatal("thread lost")
	}
	if len(ts.Dissents) != 1 || ts.Dissents[0].Peer != "web" || ts.Dissents[0].Origin.Branch != "feat/tokens" {
		t.Fatalf("dissent (with provenance) lost across restart: %+v", ts.Dissents)
	}
}

// --- discovery ---------------------------------------------------------

// Addressing controls delivery; discovery controls participation: a peer
// that was never addressed still SEES the thread and may walk in.
func TestUninvitedPeerDiscoversAndJoinsAThread(t *testing.T) {
	s := newScene(t)
	h := s.h
	db := h.registerPeer("dba", "human", map[string]any{"persona": "owns the schema"})

	code, list := h.req(db, "GET", "/threads", nil)
	if code != 200 {
		t.Fatalf("threads: %d %v", code, list)
	}
	var entry map[string]any
	for _, raw := range list["threads"].([]any) {
		th := raw.(map[string]any)
		if th["thread_id"] == s.tid {
			entry = th
		}
	}
	if entry == nil {
		t.Fatalf("a non-member must still see the thread: %v", list["threads"])
	}
	if entry["member"] != false {
		t.Fatalf("caller wrongly marked as a member: %v", entry["member"])
	}
	if entry["topic"] != "do we cache tokens for 24h?" {
		t.Fatalf("topic missing from the listing: %v", entry["topic"])
	}

	// join by contributing
	if code, out := h.send(db, map[string]any{"thread_id": s.tid, "text": "the schema forces a 1h TTL"}); code != 200 {
		t.Fatalf("uninvited join: %d %v", code, out)
	}
	if !h.threadState(t, s.tid).HasMember("dba") {
		t.Fatal("sending into a thread must join you to it")
	}
	_, list = h.req(db, "GET", "/threads", nil)
	for _, raw := range list["threads"].([]any) {
		th := raw.(map[string]any)
		if th["thread_id"] == s.tid && th["member"] != true {
			t.Fatalf("membership not reflected after joining: %v", th["member"])
		}
	}
}
