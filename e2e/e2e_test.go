// Package e2e drives the real workwire binary over plain HTTP: build it,
// start `workwire serve` on a free port with a temp data dir (WORKWIRE_*
// env only), then walk the full ask→inbox→answer→thread-completion loop as
// two peers, prove cursor persistence across a hub restart, and prove
// redelivered envelopes keep their ids (dedupe-by-id). Stdlib only.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// buildBinary compiles cmd/workwire into dir and returns the binary path.
func buildBinary(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "workwire")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/workwire")
	cmd.Dir = ".." // repo root (tests run in e2e/)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type hubProc struct {
	t       *testing.T
	cmd     *exec.Cmd
	base    string
	bin     string
	cfgDir  string
	dataDir string
	port    int
	admin   string
	logs    *bytes.Buffer
}

// startHub launches `workwire serve` with WORKWIRE_* env pointing at the
// given config/data dirs and waits for /health.
func startHub(t *testing.T, bin, cfgDir, dataDir string, port int) *hubProc {
	t.Helper()
	logs := &bytes.Buffer{}
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(),
		"WORKWIRE_CONFIG_DIR="+cfgDir,
		"WORKWIRE_DATA_DIR="+dataDir,
		"WORKWIRE_BIND=127.0.0.1",
		fmt.Sprintf("WORKWIRE_PORT=%d", port),
		fmt.Sprintf("WORKWIRE_HUB_URL=http://127.0.0.1:%d", port),
		"WORKWIRE_AUTHMODE=token",
		"WORKWIRE_WAIT_MAX=10",
	)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	h := &hubProc{
		t: t, cmd: cmd, bin: bin, cfgDir: cfgDir, dataDir: dataDir, port: port,
		base: fmt.Sprintf("http://127.0.0.1:%d", port), logs: logs,
	}
	t.Cleanup(h.stop)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(h.base + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub never became healthy; logs:\n%s", logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The serve process mints ~0600 admin-token in the config dir.
	b, err := os.ReadFile(filepath.Join(cfgDir, "admin-token"))
	if err != nil {
		t.Fatalf("read admin token: %v", err)
	}
	h.admin = strings.TrimSpace(string(b))
	return h
}

// stop kills the process cleanly (interrupt, then kill) and reaps it.
func (h *hubProc) stop() {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	_ = h.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = h.cmd.Process.Kill()
		<-done
	}
	h.cmd = nil
}

// restart stops the process and starts a fresh one on the SAME dirs/port.
func (h *hubProc) restart() *hubProc {
	h.stop()
	return startHub(h.t, h.bin, h.cfgDir, h.dataDir, h.port)
}

func (h *hubProc) req(token, method, path string, body any) (int, map[string]any) {
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
	req, err := http.NewRequest(method, h.base+path, rd)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (h *hubProc) register(name string) string {
	h.t.Helper()
	code, out := h.req(h.admin, "POST", "/agents", map[string]any{
		"name": name, "description": name + " e2e peer",
	})
	if code != 201 {
		h.t.Fatalf("register %s: %d %v", name, code, out)
	}
	sec, _ := out["agentSecret"].(string)
	if sec == "" {
		h.t.Fatalf("register %s: no secret", name)
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

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e builds and runs the real binary")
	}
	tmp := t.TempDir()
	bin := buildBinary(t, tmp)
	cfgDir := filepath.Join(tmp, "cfg")
	dataDir := filepath.Join(tmp, "data")
	hub := startHub(t, bin, cfgDir, dataDir, freePort(t))

	// --- register two peers ---
	secA := hub.register("peerA")
	secB := hub.register("peerB")

	// --- A asks B ---
	code, ask := hub.req(secA, "POST", "/agents/peerB/ask", map[string]any{
		"text": "what framework do you use?",
	})
	if code != 202 {
		t.Fatalf("ask: %d %v", code, ask)
	}
	threadID, _ := ask["thread_id"].(string)
	qid, _ := ask["message_id"].(string)
	if threadID == "" || qid == "" {
		t.Fatalf("ask response incomplete: %v", ask)
	}

	// --- B long-polls its inbox and receives the question with context ---
	code, inbox := hub.req(secB, "GET", "/inbox?agent=peerB&since=0&wait=5", nil)
	if code != 200 {
		t.Fatalf("inbox: %d %v", code, inbox)
	}
	msgs := msgsOf(inbox)
	if len(msgs) != 1 {
		t.Fatalf("want 1 delivered question, got %d: %v", len(msgs), inbox)
	}
	q := msgs[0]
	if q["id"] != qid || q["kind"] != "question" || q["from"] != "peerA" || q["thread_id"] != threadID {
		t.Fatalf("delivered question wrong: %v", q)
	}
	ctx, _ := q["context"].([]any)
	if len(ctx) == 0 {
		t.Fatalf("delivered question must carry the context projection: %v", q)
	}
	if ctx[0].(map[string]any)["kind"] != "context" {
		t.Fatalf("context entries must be kind:context: %v", ctx[0])
	}
	next, ok := inbox["next"].(float64)
	if !ok || next <= 0 {
		t.Fatalf("inbox must return an advancing cursor: %v", inbox["next"])
	}

	// --- A waits on the thread; B answers mid-wait; the wait completes ---
	type waitRes struct {
		code int
		out  map[string]any
	}
	waitCh := make(chan waitRes, 1)
	go func() {
		c, o := hub.req(secA, "GET", "/threads/"+threadID+"?wait=10&answer_to="+qid, nil)
		waitCh <- waitRes{c, o}
	}()
	time.Sleep(300 * time.Millisecond) // ensure the wait is in flight
	code, sendOut := hub.req(secB, "POST", "/send", map[string]any{
		"to": "peerA", "text": "plain Go, no frameworks", "thread_id": threadID, "reply_to": qid,
	})
	if code != 200 {
		t.Fatalf("answer send: %d %v", code, sendOut)
	}
	var wr waitRes
	select {
	case wr = <-waitCh:
	case <-time.After(8 * time.Second):
		t.Fatalf("thread wait did not complete after the answer landed")
	}
	if wr.code != 200 {
		t.Fatalf("thread wait: %d %v", wr.code, wr.out)
	}
	var answer map[string]any
	for _, m := range msgsOf(wr.out) {
		if m["reply_to"] == qid {
			answer = m
		}
	}
	if answer == nil || answer["text"] != "plain Go, no frameworks" || answer["from"] != "peerB" {
		t.Fatalf("completed thread missing the answer: %v", wr.out)
	}

	// --- cursor persistence across a hub restart ---
	hub = hub.restart()

	// Registered identities survive the restart (persisted registry).
	code, out := hub.req(secB, "GET", fmt.Sprintf("/inbox?agent=peerB&since=%d&wait=0", int64(next)), nil)
	if code != 200 {
		t.Fatalf("post-restart inbox with saved cursor: %d %v", code, out)
	}
	if len(msgsOf(out)) != 0 {
		t.Fatalf("saved cursor must not redeliver after restart: %v", out)
	}
	if r, _ := out["reset"].(bool); r {
		t.Fatalf("restart within retention must not rebase the cursor: %v", out)
	}
	n2, _ := out["next"].(float64)
	if int64(n2) != int64(next) {
		t.Fatalf("cursor moved on an empty read: %v -> %v", next, n2)
	}

	// --- dedupe-by-id on redelivery: an old cursor redelivers the SAME id ---
	code, out = hub.req(secB, "GET", "/inbox?agent=peerB&since=0&wait=0", nil)
	if code != 200 {
		t.Fatalf("redelivery inbox: %d %v", code, out)
	}
	re := msgsOf(out)
	if len(re) != 1 {
		t.Fatalf("redelivery: want the 1 question again, got %d", len(re))
	}
	if re[0]["id"] != qid {
		t.Fatalf("redelivered envelope changed id (%v vs %v) — dedupe-by-id impossible", re[0]["id"], qid)
	}
	seen := map[string]bool{qid: true}
	if seen[re[0]["id"].(string)] != true {
		t.Fatalf("consumer-side dedupe by id failed")
	}

	// New traffic after restart still flows on the same thread and store.
	code, ask2 := hub.req(secA, "POST", "/agents/peerB/ask", map[string]any{
		"text": "still alive?", "thread_id": threadID,
	})
	if code != 202 {
		t.Fatalf("post-restart ask: %d %v", code, ask2)
	}
	code, out = hub.req(secB, "GET", fmt.Sprintf("/inbox?agent=peerB&since=%d&wait=5", int64(next)), nil)
	if code != 200 {
		t.Fatalf("post-restart delivery: %d", code)
	}
	fresh := msgsOf(out)
	if len(fresh) != 1 || fresh[0]["id"] != ask2["message_id"] || fresh[0]["thread_id"] != threadID {
		t.Fatalf("post-restart question not delivered from the saved cursor: %v", out)
	}

	hub.stop() // explicit clean kill; Cleanup is the safety net
}
