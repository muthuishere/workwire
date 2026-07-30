package server

// A2A v0.3.0 conformance tests (registry-a2a R6, R9): behave like a strict
// external A2A client — read the card, speak JSON-RPC 2.0 at the card url,
// and assert spec shapes: AgentCard required fields, message/send → Task,
// tasks/get lifecycle submitted→completed on reply_to == message_id,
// JSON-RPC error codes over HTTP 200, and request-id echo for string and
// number ids. Stdlib only.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// rpcCall POSTs a raw JSON-RPC body to the card url and decodes the
// response envelope without normalizing the id (raw echo check).
func rpcCall(t *testing.T, h *hub, token, agent string, rawBody string) (int, map[string]json.RawMessage) {
	t.Helper()
	req, err := http.NewRequest("POST", h.ts.URL+"/agents/"+agent+"/rpc", bytes.NewReader([]byte(rawBody)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return resp.StatusCode, out
}

func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("expected JSON string, got %s", raw)
	}
	return s
}

func asMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("expected JSON object, got %s: %v", raw, err)
	}
	return m
}

// --- R6: AgentCard required fields, strict-client view ---

func TestA2ACardRequiredFields(t *testing.T) {
	h := newHub(t, nil)
	h.register("card-agent")

	code, out := h.req(adminToken, "GET", "/agents/card-agent/card", nil)
	if code != 200 {
		t.Fatalf("card: %d %v", code, out)
	}
	if out["protocolVersion"] != "0.3.0" {
		t.Fatalf("protocolVersion: %v", out["protocolVersion"])
	}
	if out["name"] != "card-agent" {
		t.Fatalf("name: %v", out["name"])
	}
	url, _ := out["url"].(string)
	if url == "" || !strings.HasSuffix(url, "/agents/card-agent/rpc") {
		t.Fatalf("url must be the agent's rpc endpoint: %v", out["url"])
	}
	if v, _ := out["version"].(string); v == "" {
		t.Fatalf("version required, got %v", out["version"])
	}
	if pt, _ := out["preferredTransport"].(string); pt != "HTTP+JSON" {
		t.Fatalf("preferredTransport: %v", out["preferredTransport"])
	}
	caps, ok := out["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing/not object: %v", out["capabilities"])
	}
	if caps["streaming"] != false || caps["pushNotifications"] != false {
		t.Fatalf("capabilities must declare streaming/pushNotifications false: %v", caps)
	}
	for _, k := range []string{"defaultInputModes", "defaultOutputModes"} {
		modes, ok := out[k].([]any)
		if !ok || len(modes) == 0 {
			t.Fatalf("%s missing/empty: %v", k, out[k])
		}
		if modes[0] != "text/plain" {
			t.Fatalf("%s[0]: %v", k, modes[0])
		}
	}
	skills, ok := out["skills"].([]any)
	if !ok || len(skills) == 0 {
		t.Fatalf("skills required and non-empty (synthesized ask): %v", out["skills"])
	}
	for _, s := range skills {
		sk := s.(map[string]any)
		for _, k := range []string{"id", "name", "description"} {
			if v, _ := sk[k].(string); v == "" {
				t.Fatalf("skill missing %s: %v", k, sk)
			}
		}
		if _, ok := sk["tags"].([]any); !ok {
			t.Fatalf("skill tags must be an array: %v", sk["tags"])
		}
	}

	// unknown agent → 404
	code, _ = h.req(adminToken, "GET", "/agents/ghost/card", nil)
	if code != 404 {
		t.Fatalf("unknown agent card: %d", code)
	}
}

// --- R9: message/send returns a Task; id echo (string id) ---

func TestA2AMessageSendReturnsTask(t *testing.T) {
	h := newHub(t, nil)
	respSec := h.register("responder")
	askerSec := h.register("asker")

	body := `{"jsonrpc":"2.0","id":"req-1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"where does auth live?"}],"messageId":"client-msg-1","kind":"message"}}}`
	code, out := rpcCall(t, h, askerSec, "responder", body)
	if code != 200 {
		t.Fatalf("message/send must be HTTP 200, got %d", code)
	}
	if rawString(t, out["jsonrpc"]) != "2.0" {
		t.Fatalf("jsonrpc: %s", out["jsonrpc"])
	}
	if string(out["id"]) != `"req-1"` {
		t.Fatalf("string id not echoed verbatim: %s", out["id"])
	}
	if _, hasErr := out["error"]; hasErr {
		t.Fatalf("unexpected error: %s", out["error"])
	}
	task := asMap(t, out["result"])
	if task["kind"] != "task" {
		t.Fatalf("result.kind must be \"task\": %v", task["kind"])
	}
	taskID, _ := task["id"].(string)
	if taskID == "" {
		t.Fatalf("task id required: %v", task)
	}
	if cid, _ := task["contextId"].(string); cid == "" {
		t.Fatalf("task contextId required: %v", task)
	}
	status, ok := task["status"].(map[string]any)
	if !ok {
		t.Fatalf("task status required: %v", task)
	}
	if st := status["state"]; st != "submitted" && st != "working" {
		t.Fatalf("unanswered task state must be submitted/working: %v", st)
	}

	// The question flows through the same envelope store as a plain /ask.
	// (identity: the responder's own inbox sees a kind:"question" envelope)
	code, inbox := h.req(respSec, "GET", "/inbox?agent=responder&since=0&wait=0", nil)
	if code != 200 {
		t.Fatalf("inbox: %d", code)
	}
	msgs := msgsOf(inbox)
	if len(msgs) != 1 || msgs[0]["kind"] != "question" || msgs[0]["text"] != "where does auth live?" {
		t.Fatalf("question not delivered through the envelope store: %v", msgs)
	}
	if msgs[0]["thread_id"] != taskID {
		t.Fatalf("task id must map to the thread id: %v vs %v", msgs[0]["thread_id"], taskID)
	}
}

// --- R9: tasks/get lifecycle submitted → completed ---

func TestA2ATaskLifecycleCompletesOnReply(t *testing.T) {
	h := newHub(t, nil)
	respSec := h.register("lifecycle-responder")
	askerSec := h.register("lifecycle-asker")

	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"what is the port?"}],"messageId":"lm-1"}}}`
	code, out := rpcCall(t, h, askerSec, "lifecycle-responder", body)
	if code != 200 {
		t.Fatalf("message/send: %d", code)
	}
	task := asMap(t, out["result"])
	taskID := task["id"].(string)

	// Find the hub-stamped question message_id from the responder's inbox.
	code, inbox := h.req(respSec, "GET", "/inbox?agent=lifecycle-responder&since=0&wait=0", nil)
	if code != 200 {
		t.Fatalf("inbox: %d", code)
	}
	msgs := msgsOf(inbox)
	if len(msgs) != 1 {
		t.Fatalf("want 1 question, got %d", len(msgs))
	}
	qid := msgs[0]["id"].(string)

	// tasks/get before the answer: submitted/working.
	getBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"id":%q}}`, taskID)
	code, out = rpcCall(t, h, askerSec, "lifecycle-responder", getBody)
	if code != 200 {
		t.Fatalf("tasks/get: %d", code)
	}
	task = asMap(t, out["result"])
	if st := task["status"].(map[string]any)["state"]; st != "submitted" && st != "working" {
		t.Fatalf("pre-answer state: %v", st)
	}

	// The responder answers: envelope with reply_to == message_id on the thread.
	code, sendOut := h.req(respSec, "POST", "/send", map[string]any{
		"to": "lifecycle-asker", "text": "port 14411", "thread_id": taskID, "reply_to": qid,
	})
	if code != 200 {
		t.Fatalf("answer send: %d %v", code, sendOut)
	}

	// tasks/get after the answer: completed, answer text present.
	code, out = rpcCall(t, h, askerSec, "lifecycle-responder", getBody)
	if code != 200 {
		t.Fatalf("tasks/get: %d", code)
	}
	if string(out["id"]) != "2" {
		t.Fatalf("number id not echoed verbatim: %s", out["id"])
	}
	task = asMap(t, out["result"])
	if st := task["status"].(map[string]any)["state"]; st != "completed" {
		t.Fatalf("post-answer state must be completed: %v", st)
	}
	blob, _ := json.Marshal(task)
	if !strings.Contains(string(blob), "port 14411") {
		t.Fatalf("answer text must be present in the task artifacts/history: %s", blob)
	}
	if arts, ok := task["artifacts"].([]any); !ok || len(arts) == 0 {
		t.Fatalf("completed task must carry artifacts: %v", task["artifacts"])
	}

	// Unrelated reply_to must NOT complete a fresh ask (R8 rule via tasks/get).
	code, out = rpcCall(t, h, askerSec, "lifecycle-responder",
		`{"jsonrpc":"2.0","id":3,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"second q"}],"messageId":"lm-2"}}}`)
	if code != 200 {
		t.Fatalf("second message/send: %d", code)
	}
	task2 := asMap(t, out["result"])
	tid2 := task2["id"].(string)
	// answer on the thread but replying to a bogus id
	h.req(respSec, "POST", "/send", map[string]any{
		"to": "lifecycle-asker", "text": "noise", "thread_id": tid2, "reply_to": "m-bogus",
	})
	code, out = rpcCall(t, h, askerSec, "lifecycle-responder",
		fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tasks/get","params":{"id":%q}}`, tid2))
	if code != 200 {
		t.Fatalf("tasks/get: %d", code)
	}
	task2 = asMap(t, out["result"])
	if st := task2["status"].(map[string]any)["state"]; st == "completed" {
		t.Fatalf("mismatched reply_to must not complete the task")
	}
}

// --- JSON-RPC error codes over HTTP 200 ---

func TestA2AJSONRPCErrors(t *testing.T) {
	h := newHub(t, nil)
	h.register("err-agent")

	// Parse error: not JSON at all → -32700, HTTP 200, id null.
	code, out := rpcCall(t, h, adminToken, "err-agent", `{not json`)
	if code != 200 {
		t.Fatalf("parse error must ride HTTP 200: %d", code)
	}
	e := asMap(t, out["error"])
	if e["code"].(float64) != -32700 {
		t.Fatalf("parse error code: %v", e["code"])
	}
	if string(out["id"]) != "null" {
		t.Fatalf("parse-error id must be null: %s", out["id"])
	}

	// Unknown method → -32601 with the request id echoed.
	code, out = rpcCall(t, h, adminToken, "err-agent",
		`{"jsonrpc":"2.0","id":"x-9","method":"tasks/teleport","params":{}}`)
	if code != 200 {
		t.Fatalf("unknown method must ride HTTP 200: %d", code)
	}
	e = asMap(t, out["error"])
	if e["code"].(float64) != -32601 {
		t.Fatalf("unknown method code: %v", e["code"])
	}
	if string(out["id"]) != `"x-9"` {
		t.Fatalf("id echo on error: %s", out["id"])
	}

	// Missing jsonrpc field → -32600 invalid request.
	code, out = rpcCall(t, h, adminToken, "err-agent",
		`{"id":5,"method":"message/send","params":{}}`)
	if code != 200 {
		t.Fatalf("invalid request must ride HTTP 200: %d", code)
	}
	e = asMap(t, out["error"])
	if e["code"].(float64) != -32600 {
		t.Fatalf("invalid request code: %v", e["code"])
	}

	// message/send with no text part → -32602 invalid params.
	code, out = rpcCall(t, h, adminToken, "err-agent",
		`{"jsonrpc":"2.0","id":6,"method":"message/send","params":{"message":{"role":"user","parts":[],"messageId":"m0"}}}`)
	if code != 200 {
		t.Fatalf("invalid params must ride HTTP 200: %d", code)
	}
	e = asMap(t, out["error"])
	if e["code"].(float64) != -32602 {
		t.Fatalf("invalid params code: %v", e["code"])
	}

	// tasks/get for an unknown task → JSON-RPC error, not HTTP failure.
	code, out = rpcCall(t, h, adminToken, "err-agent",
		`{"jsonrpc":"2.0","id":7,"method":"tasks/get","params":{"id":"t-nope"}}`)
	if code != 200 {
		t.Fatalf("task-not-found must ride HTTP 200: %d", code)
	}
	if _, hasErr := out["error"]; !hasErr {
		t.Fatalf("unknown task must be a JSON-RPC error: %v", out)
	}
}
