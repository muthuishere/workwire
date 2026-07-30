// Spike-01: question -> running session -> answer roundtrip.
// Subcommands: serve | listen | ask | responder | agents
// Rough spike code, not production.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- envelope (ADR-001) ----------

type Envelope struct {
	ID          string            `json:"id"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	ThreadID    string            `json:"thread_id"`
	ReplyTo     string            `json:"reply_to,omitempty"`
	Text        string            `json:"text"`
	TS          string            `json:"ts"`
	Kind        string            `json:"kind,omitempty"` // question | answer | chat
	Meta        map[string]string `json:"meta,omitempty"`
	Attachments []string          `json:"attachments,omitempty"`
}

type StoredMsg struct {
	Seq int      `json:"seq"`
	Env Envelope `json:"envelope"`
}

type InboxItem struct {
	Seq     int        `json:"seq"`
	Env     Envelope   `json:"envelope"`
	Context []Envelope `json:"context"` // last X of thread, attached at read time (ADR-001)
}

type AgentCard struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities,omitempty"`
	CWD          string   `json:"cwd,omitempty"`
	LastSeen     string   `json:"last_seen,omitempty"`
}

// ---------- hub ----------

type Hub struct {
	mu       sync.Mutex
	seq      int
	msgs     []StoredMsg
	agents   map[string]AgentCard
	dataDir  string
	notifyCh chan struct{} // closed+replaced on every new message (long-poll wakeup)
	lastCtx  int
}

func newHub(dataDir string) *Hub {
	h := &Hub{agents: map[string]AgentCard{}, dataDir: dataDir, notifyCh: make(chan struct{}), lastCtx: 5}
	os.MkdirAll(dataDir, 0755)
	// replay NDJSON log
	f, err := os.Open(filepath.Join(dataDir, "messages.ndjson"))
	if err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var m StoredMsg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				h.msgs = append(h.msgs, m)
				if m.Seq > h.seq {
					h.seq = m.Seq
				}
			}
		}
		f.Close()
	}
	return h
}

func (h *Hub) append(e Envelope) StoredMsg {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	if e.ID == "" {
		e.ID = fmt.Sprintf("m-%d-%d", h.seq, time.Now().UnixNano())
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if e.ThreadID == "" {
		e.ThreadID = "t-" + e.ID
	}
	m := StoredMsg{Seq: h.seq, Env: e}
	h.msgs = append(h.msgs, m)
	b, _ := json.Marshal(m)
	f, err := os.OpenFile(filepath.Join(h.dataDir, "messages.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		f.Write(append(b, '\n'))
		f.Close()
	}
	close(h.notifyCh)
	h.notifyCh = make(chan struct{})
	return m
}

func (h *Hub) threadTail(threadID string, n int, beforeSeq int) []Envelope {
	var out []Envelope
	for _, m := range h.msgs {
		if m.Env.ThreadID == threadID && (beforeSeq == 0 || m.Seq < beforeSeq) {
			out = append(out, m.Env)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func (h *Hub) inbox(agent string, since int) []InboxItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []InboxItem
	for _, m := range h.msgs {
		if m.Seq > since && m.Env.To == agent {
			out = append(out, InboxItem{Seq: m.Seq, Env: m.Env, Context: h.threadTail(m.Env.ThreadID, h.lastCtx, m.Seq)})
		}
	}
	return out
}

func (h *Hub) serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]any{"service": "agenthub", "ok": true, "spike": "01"})
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		var e Envelope
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		m := h.append(e)
		jsonOut(w, map[string]any{"id": m.Env.ID, "seq": m.Seq, "thread_id": m.Env.ThreadID})
	})
	mux.HandleFunc("/inbox", func(w http.ResponseWriter, r *http.Request) {
		agent := r.URL.Query().Get("agent")
		since, _ := strconv.Atoi(r.URL.Query().Get("since"))
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		deadline := time.Now().Add(time.Duration(wait) * time.Second)
		for {
			items := h.inbox(agent, since)
			if len(items) > 0 || wait == 0 || time.Now().After(deadline) {
				next := since
				for _, it := range items {
					if it.Seq > next {
						next = it.Seq
					}
				}
				jsonOut(w, map[string]any{"messages": items, "next": next})
				return
			}
			h.mu.Lock()
			ch := h.notifyCh
			h.mu.Unlock()
			select {
			case <-ch:
			case <-time.After(time.Until(deadline)):
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var c AgentCard
			if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Name == "" {
				http.Error(w, "bad card", 400)
				return
			}
			c.LastSeen = time.Now().UTC().Format(time.RFC3339)
			h.mu.Lock()
			h.agents[c.Name] = c
			h.mu.Unlock()
			jsonOut(w, map[string]any{"ok": true})
			return
		}
		h.mu.Lock()
		var list []AgentCard
		for _, c := range h.agents {
			list = append(list, c)
		}
		h.mu.Unlock()
		jsonOut(w, map[string]any{"agents": list})
	})
	mux.HandleFunc("/threads/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/threads/")
		last, _ := strconv.Atoi(r.URL.Query().Get("last"))
		if last == 0 {
			last = 50
		}
		h.mu.Lock()
		msgs := h.threadTail(id, last, 0)
		h.mu.Unlock()
		jsonOut(w, map[string]any{"thread_id": id, "messages": msgs})
	})
	fmt.Println("hub listening on", addr)
	return http.ListenAndServe(addr, mux)
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---------- HTTP client helpers ----------

var hubURL = envOr("SPIKE_HUB", "http://127.0.0.1:14411")

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func post(path string, body any, out any) error {
	b, _ := json.Marshal(body)
	resp, err := http.Post(hubURL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		x, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, x)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func get(path string, out any) error {
	resp, err := http.Get(hubURL + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---------- listen (singleton, delivery into "running session") ----------

func runDir(base string) string { return filepath.Join(base, "run") }

// lockfile with pid + liveness check (ADR-003 singleton)
func acquireLock(base, agent string) (func(), error) {
	os.MkdirAll(runDir(base), 0755)
	lock := filepath.Join(runDir(base), agent+".lock")
	if b, err := os.ReadFile(lock); err == nil {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			return nil, fmt.Errorf("listener already running for %s (pid %d)", agent, pid)
		}
		fmt.Println("reaping stale lock for pid", pid)
	}
	os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())), 0644)
	return func() { os.Remove(lock) }, nil
}

func cmdListen(args []string) {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name")
	base := fs.String("dir", "./state", "state dir")
	mech := fs.String("mech", "file", "delivery mechanism: file|fifo|cmd")
	callback := fs.String("callback", "", "callback command (mech=cmd), receives JSON on stdin")
	fs.Parse(args)
	if *agent == "" {
		die("need --agent")
	}
	unlock, err := acquireLock(*base, *agent)
	if err != nil {
		die(err.Error())
	}
	defer unlock()

	// register on the hub
	post("/agents", AgentCard{Name: *agent, Description: "spike01 agent", CWD: mustCwd()}, nil)

	cursorFile := filepath.Join(*base, *agent+".cursor")
	since := 0
	if b, err := os.ReadFile(cursorFile); err == nil {
		since, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	}
	sessionInbox := filepath.Join(*base, "sessions", *agent, "inbox.ndjson")
	os.MkdirAll(filepath.Dir(sessionInbox), 0755)
	fmt.Printf("listen: agent=%s mech=%s since=%d\n", *agent, *mech, since)

	for {
		var res struct {
			Messages []InboxItem `json:"messages"`
			Next     int         `json:"next"`
		}
		if err := get(fmt.Sprintf("/inbox?agent=%s&since=%d&wait=25", *agent, since), &res); err != nil {
			fmt.Println("poll error (hub down?), retrying:", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, it := range res.Messages {
			if it.Env.Kind != "question" {
				continue
			}
			payload, _ := json.Marshal(it)
			fmt.Printf("deliver seq=%d id=%s via %s\n", it.Seq, it.Env.ID, *mech)
			switch *mech {
			case "file": // (a) agent-telegram pattern: append to session inbox file
				f, err := os.OpenFile(sessionInbox, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				if err == nil {
					f.Write(append(payload, '\n'))
					f.Close()
				}
			case "fifo": // (b) named pipe
				if err := deliverFIFO(filepath.Join(*base, "sessions", *agent, "inbox.fifo"), payload); err != nil {
					fmt.Println("FIFO delivery failed:", err)
				}
			case "cmd": // (c) callback command
				c := exec.Command("sh", "-c", *callback)
				c.Stdin = bytes.NewReader(payload)
				c.Stdout, c.Stderr = os.Stdout, os.Stderr
				if err := c.Run(); err != nil {
					fmt.Println("callback failed:", err)
				}
			}
		}
		if res.Next > since {
			since = res.Next
			os.WriteFile(cursorFile, []byte(strconv.Itoa(since)), 0644)
		}
	}
}

// FIFO delivery: open O_WRONLY|O_NONBLOCK — fails with ENXIO when no reader is
// attached (i.e. the session is not actively blocked on the pipe). A blocking
// open would hang the listen loop indefinitely. This is the concrete problem
// with FIFOs for this use case: no reader => message lost or loop stalled.
func deliverFIFO(path string, payload []byte) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := syscall.Mkfifo(path, 0644); err != nil {
			return err
		}
	}
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("no reader on fifo (session not waiting): %w", err)
	}
	defer syscall.Close(fd)
	_, err = syscall.Write(fd, append(payload, '\n'))
	return err
}

// ---------- responder: simulates the running agent session ----------
// Tails the session inbox file (the artifact mechanism (a) delivers into),
// answers each question using the attached thread context + a local
// "knowledge" file standing in for the session's live repo context.

func cmdResponder(args []string) {
	fs := flag.NewFlagSet("responder", flag.ExitOnError)
	agent := fs.String("agent", "", "agent name")
	base := fs.String("dir", "./state", "state dir")
	knowledge := fs.String("knowledge", "", "knowledge file (simulated session context)")
	fs.Parse(args)
	sessionInbox := filepath.Join(*base, "sessions", *agent, "inbox.ndjson")
	posFile := sessionInbox + ".pos"
	pos := int64(0)
	if b, err := os.ReadFile(posFile); err == nil {
		pos, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	}
	know := ""
	if *knowledge != "" {
		b, _ := os.ReadFile(*knowledge)
		know = strings.TrimSpace(string(b))
	}
	fmt.Printf("responder: agent=%s tailing %s from offset %d\n", *agent, sessionInbox, pos)
	for {
		f, err := os.Open(sessionInbox)
		if err != nil {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		f.Seek(pos, 0)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			pos += int64(len(line)) + 1
			var it InboxItem
			if json.Unmarshal([]byte(line), &it) != nil {
				continue
			}
			// build answer from "live context": knowledge file + thread context
			answer := answerFrom(know, it)
			fmt.Printf("session %s answering %s on thread %s\n", *agent, it.Env.ID, it.Env.ThreadID)
			post("/send", Envelope{
				From: *agent, To: it.Env.From, ThreadID: it.Env.ThreadID,
				ReplyTo: it.Env.ID, Kind: "answer", Text: answer,
			}, nil)
			os.WriteFile(posFile, []byte(strconv.FormatInt(pos, 10)), 0644)
		}
		f.Close()
		time.Sleep(300 * time.Millisecond)
	}
}

func answerFrom(know string, it InboxItem) string {
	q := strings.ToLower(it.Env.Text)
	// dumb keyword lookup over knowledge lines — stands in for the LLM session
	for _, line := range strings.Split(know, "\n") {
		l := strings.ToLower(line)
		hit := 0
		for _, w := range strings.Fields(q) {
			w = strings.Trim(w, "?.,\"'")
			if len(w) > 3 && strings.Contains(l, w) {
				hit++
			}
		}
		if hit >= 2 {
			return fmt.Sprintf("From my session context: %s (thread had %d prior msgs)", line, len(it.Context))
		}
	}
	return fmt.Sprintf("I don't have that in my context. (question was: %q, thread context: %d msgs)", it.Env.Text, len(it.Context))
}

// ---------- ask ----------

func cmdAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	from := fs.String("from", "asker", "asker name")
	timeout := fs.Int("timeout", 60, "seconds to wait for answer")
	fs.Parse(args)
	if fs.NArg() < 2 {
		die("usage: ask [--from X] <agent> \"<question>\"")
	}
	target, q := fs.Arg(0), fs.Arg(1)
	start := time.Now()
	var sent struct {
		ID       string `json:"id"`
		ThreadID string `json:"thread_id"`
	}
	if err := post("/send", Envelope{From: *from, To: target, Kind: "question", Text: q}, &sent); err != nil {
		die(err.Error())
	}
	fmt.Printf("asked %s on thread %s, waiting...\n", target, sent.ThreadID)
	deadline := time.Now().Add(time.Duration(*timeout) * time.Second)
	for time.Now().Before(deadline) {
		var res struct {
			Messages []Envelope `json:"messages"`
		}
		get("/threads/"+sent.ThreadID, &res)
		for _, m := range res.Messages {
			if m.Kind == "answer" && m.ReplyTo == sent.ID {
				fmt.Printf("ANSWER (%.2fs): %s\n", time.Since(start).Seconds(), m.Text)
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	die("timeout waiting for answer")
}

// ---------- misc ----------

func mustCwd() string { d, _ := os.Getwd(); return d }
func die(s string)    { fmt.Fprintln(os.Stderr, "error:", s); os.Exit(1) }

func main() {
	if len(os.Args) < 2 {
		die("usage: spike01 serve|listen|ask|responder|agents")
	}
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "127.0.0.1:14411", "listen addr")
		data := fs.String("data", "./state/hub", "data dir")
		fs.Parse(os.Args[2:])
		die(newHub(*data).serve(*addr).Error())
	case "listen":
		cmdListen(os.Args[2:])
	case "responder":
		cmdResponder(os.Args[2:])
	case "ask":
		cmdAsk(os.Args[2:])
	case "agents":
		var out any
		get("/agents", &out)
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	default:
		die("unknown subcommand " + os.Args[1])
	}
}
