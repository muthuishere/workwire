// Spike-03 end-to-end test.
//
// Proves, with the hub and the telegram adapter running as SEPARATE OS
// processes (ADR-004: adapter = export one env name + run one process):
//
//  1. adapter registers via POST /agents (zero hub code knows about telegram)
//  2. telegram-inbound (mock Bot API) -> hub envelope -> answering agent ->
//     threaded reply lands back in the same chat (chat id self-discovered)
//  3. GET /agents/<name>/card validates against the official A2A v0.3.0 JSON
//     schema (vendored schema/a2a-v0.3.0.json, definitions/AgentCard)
//  4. POST /agents/<name>/ask returns {thread_id}; a generic external curl
//     script completes the ask round trip.
package spike03

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"spike03/mocktg"
)

const fakeToken = "TEST_FAKE_TOKEN_1234" // obviously fake; mock server only

var (
	hubBin, adapterBin string
	hubURL             string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "spike03-bin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	hubBin = filepath.Join(dir, "hub")
	adapterBin = filepath.Join(dir, "telegram-adapter")
	for bin, pkg := range map[string]string{hubBin: "./cmd/hub", adapterBin: "./cmd/telegram-adapter"} {
		out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n%s", pkg, err, out)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startProc(t *testing.T, bin string, env ...string) *exec.Cmd {
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return cmd
}

func waitHTTP(t *testing.T, url string) {
	for i := 0; i < 100; i++ {
		if r, err := http.Get(url); err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("service at %s never came up", url)
}

func getJSON(t *testing.T, url string, out any) {
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("GET %s -> %d", url, r.StatusCode)
	}
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func postJSON(t *testing.T, url string, body, out any) {
	b, _ := json.Marshal(body)
	r, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("POST %s -> %d", url, r.StatusCode)
	}
	if out != nil {
		json.NewDecoder(r.Body).Decode(out)
	}
}

// runAssistant registers an "assistant" agent and answers every inbound
// envelope on its thread — the stand-in for a skill-connected agent session.
func runAssistant(t *testing.T, stop chan struct{}) {
	postJSON(t, hubURL+"/agents", map[string]any{
		"name": "assistant", "description": "test assistant that echoes questions",
	}, nil)
	go func() {
		var since int64
		for {
			select {
			case <-stop:
				return
			default:
			}
			var body struct {
				Messages []struct {
					ID       int64  `json:"id"`
					ThreadID string `json:"thread_id"`
					From     string `json:"from"`
					Text     string `json:"text"`
				} `json:"messages"`
			}
			r, err := http.Get(fmt.Sprintf("%s/inbox?for=assistant&since=%d&wait=2", hubURL, since))
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			json.NewDecoder(r.Body).Decode(&body)
			r.Body.Close()
			for _, m := range body.Messages {
				since = m.ID
				to := m.From
				if i := strings.Index(to, "/"); i > 0 {
					to = to[:i] // reply to the adapter peer, e.g. telegram/muthu -> telegram
				}
				b, _ := json.Marshal(map[string]any{
					"from": "assistant", "to": to, "thread_id": m.ThreadID,
					"text": "echo: " + m.Text,
				})
				http.Post(hubURL+"/send", "application/json", bytes.NewReader(b))
			}
		}
	}()
}

func TestEndToEnd(t *testing.T) {
	// mock telegram Bot API (offline: no real token in env)
	tg := mocktg.New(fakeToken)
	tgSrv := httptest.NewServer(tg.Handler())
	defer tgSrv.Close()

	// hub as its own process
	hubAddr := freePort(t)
	hubURL = "http://" + hubAddr
	startProc(t, hubBin, "SPIKE03_HUB_ADDR="+hubAddr)
	waitHTTP(t, hubURL+"/health")

	// answering agent
	stop := make(chan struct{})
	defer close(stop)
	runAssistant(t, stop)

	// adapter as its own process: ONE env name for the token + run. (ADR-004)
	startProc(t, adapterBin,
		"SPIKE_TELEGRAM_BOT_TOKEN="+fakeToken,
		"SPIKE_TELEGRAM_API_BASE="+tgSrv.URL,
		"SPIKE03_HUB_URL="+hubURL,
		"SPIKE03_TARGET_AGENT=assistant",
	)

	// 1. adapter registered itself — zero hub changes
	deadline := time.Now().Add(5 * time.Second)
	for {
		var agents struct {
			Agents []struct{ Name string } `json:"agents"`
		}
		getJSON(t, hubURL+"/agents", &agents)
		found := false
		for _, a := range agents.Agents {
			if a.Name == "telegram" {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("telegram adapter never registered on the hub")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 2. human on telegram asks -> assistant answers -> threaded reply in chat
	const chatID = 777001
	msgID := tg.Inject(chatID, "muthu", "hello agent")
	var got mocktg.SentMessage
	deadline = time.Now().Add(10 * time.Second)
	for {
		sent := tg.Sent()
		if len(sent) > 0 {
			got = sent[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reply ever delivered to the telegram chat")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.ChatID != chatID {
		t.Errorf("reply chat_id = %d, want %d (chat-id self-discovery failed)", got.ChatID, chatID)
	}
	if got.Text != "echo: hello agent" {
		t.Errorf("reply text = %q", got.Text)
	}
	if got.ReplyToMessageID != msgID {
		t.Errorf("reply_to_message_id = %d, want %d (threading failed)", got.ReplyToMessageID, msgID)
	}

	// 3. agent cards validate against the official A2A v0.3.0 schema
	compiler := jsonschema.NewCompiler()
	f, err := os.Open("schema/a2a-v0.3.0.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("a2a.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("a2a.json#/definitions/AgentCard")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assistant", "telegram"} {
		r, err := http.Get(hubURL + "/agents/" + name + "/card")
		if err != nil {
			t.Fatal(err)
		}
		card, err := jsonschema.UnmarshalJSON(r.Body)
		r.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(card); err != nil {
			t.Errorf("card for %q fails A2A v0.3.0 schema: %v", name, err)
		} else {
			t.Logf("card for %q validates against A2A v0.3.0 AgentCard schema", name)
		}
	}

	// 4a. ask round trip in-Go
	var ask struct {
		ThreadID string `json:"thread_id"`
	}
	postJSON(t, hubURL+"/agents/assistant/ask",
		map[string]any{"text": "ping?", "from": "external-a2a"}, &ask)
	if ask.ThreadID == "" {
		t.Fatal("ask returned no thread_id")
	}
	var th struct {
		Messages []struct{ From, Text string } `json:"messages"`
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		getJSON(t, hubURL+"/threads/"+ask.ThreadID+"?wait=2", &th)
		if len(th.Messages) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no answer on ask thread")
		}
	}
	last := th.Messages[len(th.Messages)-1]
	if last.From != "assistant" || last.Text != "echo: ping?" {
		t.Errorf("ask answer = %+v", last)
	}

	// 4b. generic external HTTP client script (curl + python3, no SDK)
	out, err := exec.Command("bash", "scripts/a2a-client.sh",
		hubURL, "assistant", "what is the meaning of life?").CombinedOutput()
	if err != nil {
		t.Fatalf("a2a-client.sh failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ANSWER: echo: what is the meaning of life?") {
		t.Errorf("a2a-client.sh output missing answer:\n%s", out)
	}
	t.Logf("external a2a client script round trip OK")
}
