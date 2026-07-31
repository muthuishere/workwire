package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/workwire/internal/config"
	"github.com/muthuishere/workwire/internal/contacts"
	"github.com/muthuishere/workwire/internal/registry"
	"github.com/muthuishere/workwire/internal/store"
)

const adminToken = "test-token-fake"

type hub struct {
	t       *testing.T
	ts      *httptest.Server
	srv     *Server
	store   *store.Store
	reg     *registry.Registry
	dir     *contacts.Directory
	dataDir string
	cfg     config.Config
}

func newHub(t *testing.T, mutate func(*config.Config)) *hub {
	t.Helper()
	dataDir := t.TempDir()
	return openHub(t, dataDir, mutate)
}

func openHub(t *testing.T, dataDir string, mutate func(*config.Config)) *hub {
	t.Helper()
	cfg := config.Defaults()
	cfg.DataDir = dataDir
	cfg.WaitMax = 5
	if mutate != nil {
		mutate(&cfg)
	}
	st, err := store.Open(dataDir, store.Options{
		SegmentMaxBytes:   cfg.SegmentMaxBytes,
		RetentionAge:      time.Duration(cfg.RetentionDays) * 24 * time.Hour,
		RetentionMaxBytes: cfg.RetentionMaxBytes,
	})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	reg, err := registry.Open(dataDir, time.Duration(cfg.TTLSeconds)*time.Second)
	if err != nil {
		t.Fatalf("registry open: %v", err)
	}
	dir, err := contacts.Open(dataDir)
	if err != nil {
		t.Fatalf("contacts open: %v", err)
	}
	tok := adminToken
	if cfg.AuthMode == "open" {
		tok = ""
	}
	srv, err := New(cfg, st, reg, dir, tok)
	if err != nil {
		t.Fatalf("server new: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	h := &hub{t: t, ts: ts, srv: srv, store: st, reg: reg, dir: dir, dataDir: dataDir, cfg: cfg}
	t.Cleanup(h.close)
	return h
}

func (h *hub) close() {
	if h.ts != nil {
		h.ts.Close()
		h.ts = nil
	}
	if h.store != nil {
		h.store.Close()
		h.store = nil
	}
	if h.dir != nil {
		h.dir.Close()
		h.dir = nil
	}
}

// restart closes everything and reopens on the same data dir.
func (h *hub) restart() *hub {
	h.close()
	return openHub(h.t, h.dataDir, nil)
}

func (h *hub) req(token, method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, h.ts.URL+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// register an agent and return its secret.
func (h *hub) register(name string) string {
	h.t.Helper()
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": name})
	if code != 201 {
		h.t.Fatalf("register %s: got %d %v", name, code, out)
	}
	sec, _ := out["agentSecret"].(string)
	if sec == "" {
		h.t.Fatalf("register %s: empty secret", name)
	}
	return sec
}

func msgsOf(out map[string]any) []map[string]any {
	raw, _ := out["messages"].([]any)
	var res []map[string]any
	for _, m := range raw {
		res = append(res, m.(map[string]any))
	}
	return res
}

// --- health ---

func TestHealthOpenAndShape(t *testing.T) {
	h := newHub(t, nil)
	code, out := h.req("", "GET", "/health", nil) // no credentials
	if code != 200 {
		t.Fatalf("health: %d", code)
	}
	if out["service"] != "workwire" {
		t.Fatalf("service: %v", out["service"])
	}
	for _, k := range []string{"schemaVersion", "apiVersion"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	if len(out) != 3 {
		t.Fatalf("health leaks extra fields: %v", out)
	}
}

// --- send / envelope stamping ---

func TestSendStampsIdTsFrom(t *testing.T) {
	h := newHub(t, nil)
	sec := h.register("alice")
	code, out := h.req(sec, "POST", "/send", map[string]any{
		"to": "bob", "text": "hi", "from": "mallory", // forged from must be ignored
	})
	if code != 200 {
		t.Fatalf("send: %d %v", code, out)
	}
	for _, k := range []string{"id", "thread_id", "ts"} {
		if out[k] == "" || out[k] == nil {
			t.Fatalf("missing %s in %v", k, out)
		}
	}
	bobSec := h.register("bob")
	code, inbox := h.req(bobSec, "GET", "/inbox?agent=bob&since=0&wait=0", nil)
	if code != 200 {
		t.Fatalf("inbox: %d", code)
	}
	msgs := msgsOf(inbox)
	if len(msgs) != 1 {
		t.Fatalf("want 1 msg, got %d", len(msgs))
	}
	if msgs[0]["from"] != "alice" {
		t.Fatalf("from not server-stamped: %v", msgs[0]["from"])
	}
}

func TestSendValidation(t *testing.T) {
	h := newHub(t, nil)
	sec := h.register("alice")
	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing to", `{"text":"x"}`, 400},
		{"bad json", `{nope`, 400},
		{"ok", `{"to":"b","text":"x"}`, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", h.ts.URL+"/send", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+sec)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Fatalf("got %d want %d", resp.StatusCode, c.want)
			}
		})
	}
}

// --- reply_to:"last" ---

func TestReplyToLast(t *testing.T) {
	h := newHub(t, nil)
	alice, bob := h.register("alice"), h.register("bob")

	// alice starts, bob replies; alice's reply_to:"last" must resolve to bob's newest.
	_, first := h.req(alice, "POST", "/send", map[string]any{"to": "bob", "text": "q1"})
	tid := first["thread_id"].(string)
	_, bmsg := h.req(bob, "POST", "/send", map[string]any{"to": "alice", "text": "a1", "thread_id": tid})
	code, out := h.req(alice, "POST", "/send", map[string]any{"to": "bob", "text": "thanks", "thread_id": tid, "reply_to": "last"})
	if code != 200 {
		t.Fatalf("reply last: %d %v", code, out)
	}
	code, tr := h.req(alice, "GET", "/threads/"+tid, nil)
	if code != 200 {
		t.Fatal(code)
	}
	msgs := msgsOf(tr)
	last := msgs[len(msgs)-1]
	if last["reply_to"] != bmsg["id"] {
		t.Fatalf("reply_to not resolved to concrete id: %v want %v", last["reply_to"], bmsg["id"])
	}

	// thread with only own messages → 409
	_, own := h.req(alice, "POST", "/send", map[string]any{"to": "bob", "text": "solo"})
	code, _ = h.req(alice, "POST", "/send", map[string]any{"to": "bob", "text": "x", "thread_id": own["thread_id"], "reply_to": "last"})
	if code != 409 {
		t.Fatalf("own-only thread: want 409 got %d", code)
	}

	// empty (new) thread → 409
	code, _ = h.req(alice, "POST", "/send", map[string]any{"to": "bob", "text": "x", "reply_to": "last"})
	if code != 409 {
		t.Fatalf("empty thread: want 409 got %d", code)
	}
}

// --- inbox scoping and auth ---

func TestInboxUnscoped400(t *testing.T) {
	h := newHub(t, nil)
	code, _ := h.req(adminToken, "GET", "/inbox?since=0", nil)
	if code != 400 {
		t.Fatalf("want 400, got %d", code)
	}
}

func TestInboxAuth401And403(t *testing.T) {
	h := newHub(t, nil)
	h.register("repoA")
	other := h.register("repoB")
	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no credential", "", 401},
		{"wrong credential", "not-a-real-secret", 401},
		{"other agent's credential", other, 403},
		{"admin", adminToken, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _ := h.req(c.token, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
			if code != c.want {
				t.Fatalf("got %d want %d", code, c.want)
			}
		})
	}
}

// --- cursors ---

func TestCursorAdvanceAndRedelivery(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "one"})
	h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "two"})

	code, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
	if code != 200 {
		t.Fatal(code)
	}
	msgs := msgsOf(out)
	if len(msgs) != 2 || msgs[0]["text"] != "one" || msgs[1]["text"] != "two" {
		t.Fatalf("bad delivery: %v", msgs)
	}
	next := out["next"].(float64)
	// polling again with next returns nothing new
	_, out2 := h.req(repoA, "GET", fmt.Sprintf("/inbox?agent=repoA&since=%d&wait=0", int64(next)), nil)
	if len(msgsOf(out2)) != 0 {
		t.Fatalf("expected empty, got %v", out2)
	}
	// re-poll with the OLD cursor: same envelopes (same ids) redelivered — dedupe by id
	_, out3 := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
	msgs3 := msgsOf(out3)
	if len(msgs3) != 2 || msgs3[0]["id"] != msgs[0]["id"] {
		t.Fatalf("redelivery ids differ: %v vs %v", msgs3, msgs)
	}
}

func TestCursorResetAfterRetention(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.SegmentMaxBytes = 200 }) // rotate fast
	alice := h.register("alice")
	repoA := h.register("repoA")
	for i := 0; i < 10; i++ {
		h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": fmt.Sprintf("m%d", i)})
	}
	if err := h.store.RotateNow(); err != nil {
		t.Fatal(err)
	}
	// expire everything older than now (age 0 window via far-future clock)
	if err := h.store.EnforceRetention(time.Now().Add(1000 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	code, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=3&wait=0", nil)
	if code != 200 {
		t.Fatal(code)
	}
	if out["reset"] != true {
		t.Fatalf("want reset:true, got %v", out)
	}
	if len(msgsOf(out)) != 0 {
		t.Fatalf("reset must carry no messages: %v", out)
	}
	next := int64(out["next"].(float64))
	// adopting the returned cursor works with no error and no skip surprises
	code, out2 := h.req(repoA, "GET", fmt.Sprintf("/inbox?agent=repoA&since=%d&wait=0", next), nil)
	if code != 200 || out2["reset"] == true {
		t.Fatalf("rebased poll: %d %v", code, out2)
	}
}

// --- long-poll ---

func TestLongPollWakesFast(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		code, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=5", nil)
		if code != 200 || len(msgsOf(out)) != 1 {
			done <- -1
			return
		}
		done <- time.Since(start)
	}()
	time.Sleep(300 * time.Millisecond) // let the poll park
	sendAt := time.Now()
	h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "wake"})
	elapsed := <-done
	if elapsed < 0 {
		t.Fatal("poll failed")
	}
	if wake := time.Since(sendAt); wake > 100*time.Millisecond {
		t.Fatalf("wake took %v, want <100ms", wake)
	}
	_ = elapsed
}

func TestLongPollEmptyReturns200(t *testing.T) {
	h := newHub(t, nil)
	repoA := h.register("repoA")
	start := time.Now()
	code, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=1", nil)
	if code != 200 {
		t.Fatal(code)
	}
	if len(msgsOf(out)) != 0 {
		t.Fatalf("want empty: %v", out)
	}
	if _, ok := out["next"]; !ok {
		t.Fatal("missing next")
	}
	if e := time.Since(start); e < 900*time.Millisecond {
		t.Fatalf("returned too early: %v", e)
	}
}

func TestWaitClamped(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.WaitMax = 1 })
	repoA := h.register("repoA")
	start := time.Now()
	code, _ := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=3600", nil)
	if code != 200 {
		t.Fatal(code)
	}
	if e := time.Since(start); e > 3*time.Second {
		t.Fatalf("wait not clamped: %v", e)
	}
}

// --- context projection ---

func TestContextProjection(t *testing.T) {
	// 30 messages on one thread: raise the ADR-009 round cap out of the way,
	// this test is about projection, not convergence.
	h := newHub(t, func(c *config.Config) { c.MaxThreadMessages = 100 })
	alice := h.register("alice")
	repoA := h.register("repoA")
	_, first := h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "m0"})
	tid := first["thread_id"].(string)
	for i := 1; i < 30; i++ {
		h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": fmt.Sprintf("m%d", i), "thread_id": tid})
	}

	t.Run("default depth 5, kind context, cursor advances only for deliveries", func(t *testing.T) {
		_, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
		msgs := msgsOf(out)
		if len(msgs) != 30 {
			t.Fatalf("want 30 deliveries, got %d", len(msgs))
		}
		last := msgs[len(msgs)-1]
		ctx := last["context"].([]any)
		if len(ctx) != 5 {
			t.Fatalf("want 5 context entries, got %d", len(ctx))
		}
		for _, c := range ctx {
			if c.(map[string]any)["kind"] != "context" {
				t.Fatalf("context entry not stamped: %v", c)
			}
		}
		if int64(out["next"].(float64)) != h.store.LastSeq() {
			t.Fatalf("next should equal last delivered seq")
		}
	})
	t.Run("context clamped to 20", func(t *testing.T) {
		_, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0&context=50", nil)
		msgs := msgsOf(out)
		ctx := msgs[len(msgs)-1]["context"].([]any)
		if len(ctx) != 20 {
			t.Fatalf("want 20, got %d", len(ctx))
		}
	})
	t.Run("context=0 disables projection", func(t *testing.T) {
		_, out := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0&context=0", nil)
		for _, m := range msgsOf(out) {
			if _, ok := m["context"]; ok {
				t.Fatalf("context present with context=0: %v", m)
			}
		}
	})
}

// --- threads ---

func TestThreads(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	_, first := h.req(alice, "POST", "/send", map[string]any{"to": "b", "text": "m0"})
	tid := first["thread_id"].(string)
	for i := 1; i < 10; i++ {
		h.req(alice, "POST", "/send", map[string]any{"to": "b", "text": fmt.Sprintf("m%d", i), "thread_id": tid})
	}
	code, out := h.req(alice, "GET", "/threads/"+tid+"?last=3", nil)
	if code != 200 {
		t.Fatal(code)
	}
	if out["thread_id"] != tid {
		t.Fatalf("thread_id: %v", out["thread_id"])
	}
	msgs := msgsOf(out)
	if len(msgs) != 3 || msgs[2]["text"] != "m9" {
		t.Fatalf("last=3: %v", msgs)
	}
	code, _ = h.req(alice, "GET", "/threads/t-nope", nil)
	if code != 404 {
		t.Fatalf("unknown thread: want 404 got %d", code)
	}
}

// --- registration ---

func TestRegistrationLifecycle(t *testing.T) {
	h := newHub(t, nil)
	code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "repoA", "description": "repo A session"})
	if code != 201 {
		t.Fatalf("register: %d %v", code, out)
	}
	secret := out["agentSecret"].(string)
	if out["agentId"] == "" || secret == "" {
		t.Fatalf("missing identity: %v", out)
	}

	t.Run("listing includes repoA without secrets", func(t *testing.T) {
		code, out := h.req(adminToken, "GET", "/agents", nil)
		if code != 200 {
			t.Fatal(code)
		}
		b, _ := json.Marshal(out)
		if !bytes.Contains(b, []byte("repoA")) {
			t.Fatalf("repoA absent: %s", b)
		}
		if bytes.Contains(b, []byte("agentSecret")) || bytes.Contains(b, []byte("secretHash")) || bytes.Contains(b, []byte(secret)) {
			t.Fatalf("secret material leaked: %s", b)
		}
	})
	t.Run("collision 409 with suggestion shape", func(t *testing.T) {
		code, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "repoA"})
		if code != 409 {
			t.Fatalf("want 409 got %d", code)
		}
		if out["error"] != "name taken" || out["name"] != "repoA" || out["suggestion"] != "repoA-2" {
			t.Fatalf("bad 409 shape: %v", out)
		}
	})
	t.Run("suggestion increments when taken", func(t *testing.T) {
		h.register("repoA-2")
		_, out := h.req(adminToken, "POST", "/agents", map[string]any{"name": "repoA"})
		if out["suggestion"] != "repoA-3" {
			t.Fatalf("want repoA-3: %v", out)
		}
	})
	t.Run("re-register with the correct secret is 200, same identity", func(t *testing.T) {
		code, out2 := h.req(secret, "POST", "/agents", map[string]any{"name": "repoA", "description": "updated"})
		if code != 200 {
			t.Fatalf("re-register: %d %v", code, out2)
		}
		if out2["agentId"] != out["agentId"] {
			t.Fatalf("identity changed")
		}
		if _, has := out2["agentSecret"]; has {
			t.Fatalf("secret must not rotate/reappear: %v", out2)
		}
	})
	t.Run("missing name 400", func(t *testing.T) {
		code, _ := h.req(adminToken, "POST", "/agents", map[string]any{"description": "x"})
		if code != 400 {
			t.Fatalf("want 400 got %d", code)
		}
	})
}

func TestRegistryTTLAgeOut(t *testing.T) {
	h := newHub(t, nil)
	h.register("repoA")
	h.register("repoB")
	now := time.Now()
	h.reg.SetClock(func() time.Time { return now.Add(300 * time.Second) })
	// repoB heartbeats via an authenticated request... but clock is global; touch repoB explicitly
	h.reg.Touch("repoB")
	code, out := h.req(adminToken, "GET", "/agents", nil)
	if code != 200 {
		t.Fatal(code)
	}
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte("repoA")) {
		t.Fatalf("repoA should have aged out: %s", b)
	}
	if !bytes.Contains(b, []byte("repoB")) {
		t.Fatalf("repoB should be live: %s", b)
	}
	// aged-out name is still reserved: registering without the secret → 409
	code, out = h.req(adminToken, "POST", "/agents", map[string]any{"name": "repoA"})
	if code != 409 {
		t.Fatalf("aged-out name must stay reserved: %d %v", code, out)
	}
}

// --- card ---

func TestAgentCard(t *testing.T) {
	h := newHub(t, nil)
	h.register("repoA")
	code, card := h.req(adminToken, "GET", "/agents/repoA/card", nil)
	if code != 200 {
		t.Fatal(code)
	}
	checks := map[string]any{
		"protocolVersion":    "0.3.0",
		"name":               "repoA",
		"preferredTransport": "HTTP+JSON",
	}
	for k, v := range checks {
		if card[k] != v {
			t.Fatalf("%s: %v want %v", k, card[k], v)
		}
	}
	caps := card["capabilities"].(map[string]any)
	if caps["streaming"] != false || caps["pushNotifications"] != false {
		t.Fatalf("capabilities: %v", caps)
	}
	skills := card["skills"].([]any)
	if len(skills) != 1 || skills[0].(map[string]any)["id"] != "ask" {
		t.Fatalf("synthesized ask skill missing: %v", skills)
	}
	if !strings.Contains(card["url"].(string), "/agents/repoA/rpc") {
		t.Fatalf("card url: %v", card["url"])
	}
	code, _ = h.req(adminToken, "GET", "/agents/ghost/card", nil)
	if code != 404 {
		t.Fatalf("unknown card: want 404 got %d", code)
	}
}

// --- ask ---

func TestAsk(t *testing.T) {
	h := newHub(t, nil)
	repoA := h.register("repoA")
	asker := h.register("asker")

	code, out := h.req(asker, "POST", "/agents/repoA/ask", map[string]any{"text": "where does auth live?"})
	if code != 202 {
		t.Fatalf("ask: %d %v", code, out)
	}
	tid, qid := out["thread_id"].(string), out["message_id"].(string)
	if tid == "" || qid == "" {
		t.Fatalf("empty ids: %v", out)
	}
	_, inbox := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
	msgs := msgsOf(inbox)
	if len(msgs) != 1 || msgs[0]["id"] != qid || msgs[0]["kind"] != "question" || msgs[0]["from"] != "asker" {
		t.Fatalf("question envelope: %v", msgs)
	}

	t.Run("empty text 400", func(t *testing.T) {
		code, _ := h.req(asker, "POST", "/agents/repoA/ask", map[string]any{"text": ""})
		if code != 400 {
			t.Fatalf("want 400 got %d", code)
		}
	})
	t.Run("unauthenticated 401", func(t *testing.T) {
		code, _ := h.req("", "POST", "/agents/repoA/ask", map[string]any{"text": "x"})
		if code != 401 {
			t.Fatalf("want 401 got %d", code)
		}
	})
	t.Run("aged-out target still gets asks queued", func(t *testing.T) {
		now := time.Now()
		h.reg.SetClock(func() time.Time { return now.Add(400 * time.Second) })
		defer h.reg.SetClock(time.Now)
		code, _ := h.req(asker, "POST", "/agents/repoA/ask", map[string]any{"text": "still there?"})
		if code != 202 {
			t.Fatalf("want 202 got %d", code)
		}
	})
}

func TestAskPolicy(t *testing.T) {
	h := newHub(t, nil)
	h.req(adminToken, "POST", "/agents", map[string]any{
		"name": "guarded", "askPolicy": map[string]any{"allowPeers": []string{"friend"}},
	})
	guardedSec := func() string { // fetch: re-register won't work; register returned above
		return ""
	}
	_ = guardedSec
	friend := h.register("friend")
	stranger := h.register("stranger")

	code, _ := h.req(friend, "POST", "/agents/guarded/ask", map[string]any{"text": "hi"})
	if code != 202 {
		t.Fatalf("allowed peer: want 202 got %d", code)
	}
	code, out := h.req(stranger, "POST", "/agents/guarded/ask", map[string]any{"text": "hi"})
	if code != 403 || out["error"] != "ask_denied" {
		t.Fatalf("denied peer: %d %v", code, out)
	}
	// enforcement is at ingest: the denied question never reaches the inbox
	_, inbox := h.req(adminToken, "GET", "/inbox?agent=guarded&since=0&wait=0", nil)
	for _, m := range msgsOf(inbox) {
		if m["from"] == "stranger" {
			t.Fatalf("denied ask was enqueued: %v", m)
		}
	}
}

// --- ask completion via /threads?wait&answer_to ---

func TestAskCompletion(t *testing.T) {
	h := newHub(t, nil)
	repoA := h.register("repoA")
	asker := h.register("asker")
	_, ask := h.req(asker, "POST", "/agents/repoA/ask", map[string]any{"text": "q?"})
	tid, qid := ask["thread_id"].(string), ask["message_id"].(string)

	type result struct {
		found bool
		reply string
	}
	done := make(chan result, 1)
	go func() {
		_, out := h.req(asker, "GET", "/threads/"+tid+"?wait=5&answer_to="+qid, nil)
		for _, m := range msgsOf(out) {
			if m["reply_to"] == qid {
				done <- result{true, m["text"].(string)}
				return
			}
		}
		done <- result{false, ""}
	}()
	time.Sleep(200 * time.Millisecond)
	// unrelated traffic must not complete the ask
	h.req(asker, "POST", "/send", map[string]any{"to": "repoA", "text": "noise", "thread_id": tid})
	select {
	case <-done:
		t.Fatal("wait completed on unrelated traffic")
	case <-time.After(300 * time.Millisecond):
	}
	// the answer (reply_to == question id) completes it
	h.req(repoA, "POST", "/send", map[string]any{"to": "asker", "text": "the answer", "thread_id": tid, "reply_to": qid})
	select {
	case r := <-done:
		if !r.found || r.reply != "the answer" {
			t.Fatalf("bad completion: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answer did not complete the wait")
	}
}

// --- JSON-RPC shim ---

func TestJSONRPC(t *testing.T) {
	h := newHub(t, nil)
	repoA := h.register("repoA")
	asker := h.register("asker")

	rpc := func(body map[string]any) (int, map[string]any) {
		return h.req(asker, "POST", "/agents/repoA/rpc", body)
	}
	code, out := rpc(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "message/send",
		"params": map[string]any{"message": map[string]any{
			"role": "user", "parts": []map[string]any{{"kind": "text", "text": "where is auth?"}},
		}},
	})
	if code != 200 {
		t.Fatal(code)
	}
	res := out["result"].(map[string]any)
	state := res["status"].(map[string]any)["state"].(string)
	if state != "submitted" && state != "working" {
		t.Fatalf("state: %v", state)
	}
	taskID := res["id"].(string)

	// same envelope store: repoA sees the question on its inbox
	_, inbox := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
	msgs := msgsOf(inbox)
	if len(msgs) != 1 || msgs[0]["kind"] != "question" {
		t.Fatalf("rpc question not delivered: %v", msgs)
	}
	qid := msgs[0]["id"].(string)

	// answer, then tasks/get shows completed with the answer text
	h.req(repoA, "POST", "/send", map[string]any{"to": "asker", "text": "in internal/auth", "thread_id": taskID, "reply_to": qid})
	code, out = rpc(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tasks/get", "params": map[string]any{"id": taskID}})
	if code != 200 {
		t.Fatal(code)
	}
	res = out["result"].(map[string]any)
	if res["status"].(map[string]any)["state"] != "completed" {
		t.Fatalf("want completed: %v", res)
	}
	b, _ := json.Marshal(res["artifacts"])
	if !bytes.Contains(b, []byte("in internal/auth")) {
		t.Fatalf("answer text missing from artifacts: %s", b)
	}

	t.Run("unknown method -32601 over HTTP 200", func(t *testing.T) {
		code, out := rpc(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tasks/teleport", "params": map[string]any{}})
		if code != 200 {
			t.Fatal(code)
		}
		if out["error"].(map[string]any)["code"].(float64) != -32601 {
			t.Fatalf("want -32601: %v", out)
		}
	})
	t.Run("malformed JSON -32700 over HTTP 200", func(t *testing.T) {
		req, _ := http.NewRequest("POST", h.ts.URL+"/agents/repoA/rpc", strings.NewReader("{nope"))
		req.Header.Set("Authorization", "Bearer "+asker)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != 200 || out["error"].(map[string]any)["code"].(float64) != -32700 {
			t.Fatalf("%d %v", resp.StatusCode, out)
		}
	})
}

// --- listen lease ---

func TestListenLease(t *testing.T) {
	h := newHub(t, nil)
	repoA := h.register("repoA")
	other := h.register("other")

	code, out := h.req(repoA, "POST", "/agents/repoA/listen-lease", map[string]any{})
	if code != 200 {
		t.Fatalf("acquire: %d %v", code, out)
	}
	leaseID := out["leaseId"].(string)
	if out["ttl"].(float64) != 120 {
		t.Fatalf("ttl: %v", out["ttl"])
	}
	t.Run("second acquire without leaseId is 409", func(t *testing.T) {
		code, out := h.req(repoA, "POST", "/agents/repoA/listen-lease", map[string]any{})
		if code != 409 {
			t.Fatalf("want 409: %d %v", code, out)
		}
		if out["holder"] == "" || out["expiresAt"] == "" {
			t.Fatalf("409 shape: %v", out)
		}
	})
	t.Run("re-acquire with current leaseId renews", func(t *testing.T) {
		code, out := h.req(repoA, "POST", "/agents/repoA/listen-lease", map[string]any{"leaseId": leaseID})
		if code != 200 || out["leaseId"] != leaseID {
			t.Fatalf("renew: %d %v", code, out)
		}
	})
	t.Run("other agent's credential is 403; none is 401", func(t *testing.T) {
		if code, _ := h.req(other, "POST", "/agents/repoA/listen-lease", map[string]any{}); code != 403 {
			t.Fatalf("want 403 got %d", code)
		}
		if code, _ := h.req("", "POST", "/agents/repoA/listen-lease", map[string]any{}); code != 401 {
			t.Fatalf("want 401 got %d", code)
		}
	})
	t.Run("expired lease is claimable, old leaseId rejected", func(t *testing.T) {
		now := time.Now()
		h.reg.SetClock(func() time.Time { return now.Add(300 * time.Second) })
		defer h.reg.SetClock(time.Now)
		code, out := h.req(repoA, "POST", "/agents/repoA/listen-lease", map[string]any{})
		if code != 200 {
			t.Fatalf("claim expired: %d %v", code, out)
		}
		newLease := out["leaseId"].(string)
		if newLease == leaseID {
			t.Fatal("lease id should be fresh")
		}
		code, _ = h.req(repoA, "POST", "/agents/repoA/listen-lease", map[string]any{"leaseId": leaseID})
		if code != 409 {
			t.Fatalf("stale renew: want 409 got %d", code)
		}
		// release with the current leaseId → 204
		code, _ = h.req(repoA, "DELETE", "/agents/repoA/listen-lease", map[string]any{"leaseId": newLease})
		if code != 204 {
			t.Fatalf("release: want 204 got %d", code)
		}
	})
}

// --- tombstones ---

func TestTombstoneExcision(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	_, sent := h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "sk-SECRETVALUE-fake"})
	mid, tid := sent["id"].(string), sent["thread_id"].(string)
	h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "follow-up", "thread_id": tid})

	code, _ := h.req(adminToken, "DELETE", "/messages/"+mid, nil)
	if code != 200 {
		t.Fatalf("delete: %d", code)
	}
	// idempotent repeat
	if code, _ := h.req(adminToken, "DELETE", "/messages/"+mid, nil); code != 200 {
		t.Fatalf("repeat delete: %d", code)
	}
	if code, _ := h.req(adminToken, "DELETE", "/messages/m-nope", nil); code != 404 {
		t.Fatal("unknown id should 404")
	}

	assertExcised := func(hh *hub) {
		t.Helper()
		_, tr := hh.req(alice, "GET", "/threads/"+tid, nil)
		for _, m := range msgsOf(tr) {
			if m["id"] == mid {
				if m["text"] != "" {
					t.Fatalf("thread read leaks content: %v", m)
				}
			}
		}
		_, inbox := hh.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
		for _, m := range msgsOf(inbox) {
			if m["id"] == mid && m["text"] != "" {
				t.Fatalf("inbox replay leaks content: %v", m)
			}
			if ctx, ok := m["context"].([]any); ok {
				for _, c := range ctx {
					cm := c.(map[string]any)
					if cm["id"] == mid && cm["text"] != "" {
						t.Fatalf("context projection leaks content: %v", cm)
					}
				}
			}
		}
	}
	assertExcised(h)

	// survives restart + NDJSON replay
	h2 := h.restart()
	alice = h2.register("alice-2") // fresh creds not needed for admin reads; reads below use admin
	_ = alice
	code, tr := h2.req(adminToken, "GET", "/threads/"+tid, nil)
	if code != 200 {
		t.Fatal(code)
	}
	for _, m := range msgsOf(tr) {
		if m["id"] == mid && m["text"] != "" {
			t.Fatalf("tombstone did not survive restart: %v", m)
		}
	}
}

func TestThreadExcision(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	_, first := h.req(alice, "POST", "/send", map[string]any{"to": "b", "text": "one"})
	tid := first["thread_id"].(string)
	for i := 0; i < 5; i++ {
		h.req(alice, "POST", "/send", map[string]any{"to": "b", "text": "more", "thread_id": tid})
	}
	code, _ := h.req(adminToken, "DELETE", "/threads/"+tid, nil)
	if code != 200 {
		t.Fatal(code)
	}
	_, tr := h.req(alice, "GET", "/threads/"+tid, nil)
	for _, m := range msgsOf(tr) {
		if m["text"] != "" {
			t.Fatalf("thread excision incomplete: %v", m)
		}
	}
	if code, _ := h.req(adminToken, "DELETE", "/threads/t-nope", nil); code != 404 {
		t.Fatal("unknown thread should 404")
	}
}

// --- restart persistence ---

func TestRestartPreservesStoreAndRegistry(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")
	repoA := h.register("repoA")
	_, sent := h.req(alice, "POST", "/send", map[string]any{"to": "repoA", "text": "before restart"})
	tid := sent["thread_id"].(string)
	_, inbox := h.req(repoA, "GET", "/inbox?agent=repoA&since=0&wait=0", nil)
	cursor := int64(inbox["next"].(float64))

	h2 := h.restart()

	// thread intact
	code, tr := h2.req(adminToken, "GET", "/threads/"+tid, nil)
	if code != 200 || len(msgsOf(tr)) != 1 {
		t.Fatalf("thread lost: %d %v", code, tr)
	}
	// registry cache: repoA still listed and its name reserved, secret still works
	code, agents := h2.req(adminToken, "GET", "/agents", nil)
	b, _ := json.Marshal(agents)
	if code != 200 || !bytes.Contains(b, []byte("repoA")) {
		t.Fatalf("registry cache lost: %s", b)
	}
	// old agent secret still authenticates
	code, _ = h2.req(repoA, "GET", "/inbox?agent=repoA&since="+fmt.Sprint(cursor)+"&wait=0", nil)
	if code != 200 {
		t.Fatalf("credential lost after restart: %d", code)
	}
	// cursor semantics: nothing new after the pre-restart cursor, then exactly one new message
	h2.req(alice, "GET", "/health", nil)
	aliceSec := repoA // reuse: send as repoA to repoA? need a sender — use admin
	_ = aliceSec
	h2.req(adminToken, "POST", "/send", map[string]any{"to": "repoA", "text": "after restart"})
	_, inbox2 := h2.req(repoA, "GET", fmt.Sprintf("/inbox?agent=repoA&since=%d&wait=0", cursor), nil)
	msgs := msgsOf(inbox2)
	if len(msgs) != 1 || msgs[0]["text"] != "after restart" {
		t.Fatalf("cursor broken across restart: %v", msgs)
	}
}

// --- contacts ---

func TestContacts(t *testing.T) {
	h := newHub(t, nil)
	alice := h.register("alice")

	t.Run("harvest from traffic is unverified", func(t *testing.T) {
		h.req(alice, "POST", "/send", map[string]any{"to": "someone", "text": "hi"})
		_, out := h.req(adminToken, "GET", "/contacts?q=alice", nil)
		cts := out["contacts"].([]any)
		if len(cts) != 1 {
			t.Fatalf("harvested contact missing: %v", out)
		}
		if cts[0].(map[string]any)["verified"] != false {
			t.Fatalf("harvested must be unverified: %v", cts[0])
		}
	})
	t.Run("explicit add is verified with aliases and fuzzy lookup", func(t *testing.T) {
		code, c := h.req(adminToken, "POST", "/contacts", map[string]any{
			"name": "Suguna N", "peer": "telegram", "id": "777001", "aliases": []string{"Suguna", "amma"},
		})
		if code != 201 {
			t.Fatalf("add: %d %v", code, c)
		}
		if c["verified"] != true || c["contactId"] == "" {
			t.Fatalf("add shape: %v", c)
		}
		for _, q := range []string{"suguna", "amma", "sugu"} {
			_, out := h.req(adminToken, "GET", "/contacts?q="+q, nil)
			if len(out["contacts"].([]any)) == 0 {
				t.Fatalf("lookup %q found nothing", q)
			}
		}
		_, out := h.req(adminToken, "GET", "/contacts?q=zzz", nil)
		if len(out["contacts"].([]any)) != 0 {
			t.Fatalf("zzz should match nothing: %v", out)
		}
	})
	t.Run("missing name 400", func(t *testing.T) {
		code, _ := h.req(adminToken, "POST", "/contacts", map[string]any{"peer": "telegram", "id": "1"})
		if code != 400 {
			t.Fatalf("want 400 got %d", code)
		}
	})
	t.Run("unauthenticated 401", func(t *testing.T) {
		code, _ := h.req("", "POST", "/contacts", map[string]any{"name": "X", "peer": "p", "id": "1"})
		if code != 401 {
			t.Fatalf("want 401 got %d", code)
		}
	})
	t.Run("verify then purge tombstone survives restart", func(t *testing.T) {
		_, c := h.req(adminToken, "POST", "/contacts", map[string]any{"name": "Temp", "peer": "telegram", "id": "888"})
		cid := c["contactId"].(string)
		code, v := h.req(adminToken, "POST", "/contacts/"+cid+"/verify", nil)
		if code != 200 || v["verified"] != true {
			t.Fatalf("verify: %d %v", code, v)
		}
		if code, _ := h.req(adminToken, "DELETE", "/contacts/"+cid, nil); code != 200 {
			t.Fatal("delete should 200")
		}
		if code, _ := h.req(adminToken, "DELETE", "/contacts/"+cid, nil); code != 200 {
			t.Fatal("repeat delete should be idempotent 200")
		}
		if code, _ := h.req(adminToken, "DELETE", "/contacts/does-not-exist", nil); code != 404 {
			t.Fatal("unknown contact should 404")
		}
		_, out := h.req(adminToken, "GET", "/contacts?q=Temp", nil)
		if len(out["contacts"].([]any)) != 0 {
			t.Fatalf("purged entry still visible: %v", out)
		}
		h2 := h.restart()
		_, out = h2.req(adminToken, "GET", "/contacts?q=Temp", nil)
		if len(out["contacts"].([]any)) != 0 {
			t.Fatalf("tombstone lost on restart: %v", out)
		}
	})
}

// --- media ---

func TestMedia(t *testing.T) {
	h := newHub(t, nil)
	req, _ := http.NewRequest("POST", h.ts.URL+"/media", strings.NewReader("attachment-bytes"))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("upload: %d %v", resp.StatusCode, out)
	}
	id := out["id"].(string)

	greq, _ := http.NewRequest("GET", h.ts.URL+"/media/"+id, nil)
	greq.Header.Set("Authorization", "Bearer "+adminToken)
	gresp, err := http.DefaultClient.Do(greq)
	if err != nil {
		t.Fatal(err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != 200 || gresp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("get: %d %s", gresp.StatusCode, gresp.Header.Get("Content-Type"))
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(gresp.Body)
	if buf.String() != "attachment-bytes" {
		t.Fatalf("bytes: %q", buf.String())
	}
	if code, _ := h.req(adminToken, "GET", "/media/m-nope", nil); code != 404 {
		t.Fatal("unknown media should 404")
	}
}

// --- open mode ---

func TestOpenMode(t *testing.T) {
	h := newHub(t, func(c *config.Config) { c.AuthMode = "open" })
	code, out := h.req("", "POST", "/send", map[string]any{"to": "x", "text": "hello"})
	if code != 200 {
		t.Fatalf("open-mode send: %d %v", code, out)
	}
}

// --- listener liveness: registered is not the same as reachable ---

// A peer with no live listen lease cannot answer right now. The hub says so
// on ask and in the agents listing, so an asker is never left staring at a
// silent five-minute timeout wondering why.
func TestListenerLivenessSurfaced(t *testing.T) {
	h := newHub(t, nil)
	asker := h.register("asker")
	h.register("quiet") // registered, never listened
	target := h.register("live")
	if code, _ := h.req(target, "POST", "/agents/live/listen-lease", map[string]any{}); code != 200 {
		t.Fatal("lease setup failed")
	}

	tests := []struct {
		name string
		to   string
		want bool
	}{
		{"registered with no listener", "quiet", false},
		{"registered and listening", "live", true},
	}
	for _, tc := range tests {
		t.Run("ask reports "+tc.name, func(t *testing.T) {
			code, out := h.req(asker, "POST", "/agents/"+tc.to+"/ask", map[string]any{"text": "hello?"})
			if code != 202 {
				t.Fatalf("ask: %d %v", code, out)
			}
			if got, _ := out["listener"].(bool); got != tc.want {
				t.Fatalf("listener=%v want %v (%v)", got, tc.want, out)
			}
			if out["last_seen"] == nil || out["last_seen"] == "" {
				t.Fatalf("ask must report last_seen: %v", out)
			}
			// The question is queued either way — never dropped.
			if out["message_id"] == "" {
				t.Fatalf("question not queued: %v", out)
			}
		})
	}

	t.Run("agents listing marks liveness", func(t *testing.T) {
		code, out := h.req(asker, "GET", "/agents", nil)
		if code != 200 {
			t.Fatalf("list: %d", code)
		}
		got := map[string]bool{}
		for _, a := range out["agents"].([]any) {
			m := a.(map[string]any)
			live, _ := m["listener"].(bool)
			got[m["name"].(string)] = live
		}
		if got["live"] != true || got["quiet"] != false {
			t.Fatalf("liveness in listing: %v", got)
		}
	})

	t.Run("a held lease keeps reading as live", func(t *testing.T) {
		code, out := h.req(target, "POST", "/agents/live/listen-lease", map[string]any{})
		if code != 409 {
			t.Fatalf("expected the lease to still be held: %d %v", code, out)
		}
		_, lo := h.req(asker, "POST", "/agents/live/ask", map[string]any{"text": "still there?"})
		if live, _ := lo["listener"].(bool); !live {
			t.Fatal("held lease must read as a live listener")
		}
	})
}
