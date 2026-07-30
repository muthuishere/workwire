// telegram-adapter: a channel adapter as an EXTERNAL PEER (ADR-004).
//
// Setup = export one env var (the bot token, by NAME) + run this process.
// Zero hub code changes: it registers via POST /agents like any agent.
//
//   - Registers on the hub as "telegram" (heartbeat re-register).
//   - Long-polls Telegram getUpdates (no webhook, no public URL).
//   - Self-discovers the chat id from the first inbound message.
//   - Forwards telegram-inbound messages as hub envelopes to a target agent.
//   - Long-polls its hub inbox and delivers hub messages back to the chat,
//     as threaded replies (reply_to_message_id of the originating message).
//
// Env:
//
//	SPIKE_TELEGRAM_BOT_TOKEN  bot token (required; value is never logged)
//	SPIKE_TELEGRAM_API_BASE   default https://api.telegram.org (mock override)
//	SPIKE03_HUB_URL           default http://127.0.0.1:24303
//	SPIKE03_TARGET_AGENT      default "assistant" (where inbound msgs go)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

const agentName = "telegram"

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type adapter struct {
	apiBase, hubURL, target string
	token                   string // used only to build request URLs; never logged
	client                  *http.Client

	mu            sync.Mutex
	chatID        int64
	tgMsgByThread map[string]int64 // hub thread -> originating tg message_id
}

func main() {
	log.SetPrefix("[telegram-adapter] ")
	token := os.Getenv("SPIKE_TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("SPIKE_TELEGRAM_BOT_TOKEN is not set; export it (by name) and rerun")
	}
	a := &adapter{
		apiBase:       envOr("SPIKE_TELEGRAM_API_BASE", "https://api.telegram.org"),
		hubURL:        envOr("SPIKE03_HUB_URL", "http://127.0.0.1:24303"),
		target:        envOr("SPIKE03_TARGET_AGENT", "assistant"),
		token:         token,
		client:        &http.Client{Timeout: 60 * time.Second},
		tgMsgByThread: map[string]int64{},
	}
	if err := a.register(); err != nil {
		log.Fatalf("register on hub: %v", err)
	}
	log.Printf("registered on hub %s as %q; forwarding inbound to %q", a.hubURL, agentName, a.target)
	go a.heartbeat()
	go a.hubInboxLoop()
	a.telegramLoop() // blocks
}

// ---- hub side ----

func (a *adapter) postJSON(u string, body any, out any) error {
	b, _ := json.Marshal(body)
	resp, err := a.client.Post(u, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s -> %d: %s", u, resp.StatusCode, msg)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (a *adapter) register() error {
	return a.postJSON(a.hubURL+"/agents", map[string]any{
		"name":        agentName,
		"description": "Telegram channel adapter (external peer). Messages sent to 'telegram' are delivered to the linked chat.",
		"skills": []map[string]any{{
			"id": "telegram-relay", "name": "relay",
			"description": "Relay messages between a Telegram chat and the hub, with threaded replies.",
			"tags":        []string{"channel", "telegram"},
		}},
	}, nil)
}

func (a *adapter) heartbeat() {
	for range time.Tick(20 * time.Second) {
		if err := a.register(); err != nil {
			log.Printf("heartbeat: %v", err)
		}
	}
}

// hubInboxLoop delivers hub envelopes addressed to "telegram" back to the chat.
func (a *adapter) hubInboxLoop() {
	var since int64
	for {
		u := fmt.Sprintf("%s/inbox?for=%s&since=%d&wait=25", a.hubURL, agentName, since)
		resp, err := a.client.Get(u)
		if err != nil {
			log.Printf("hub inbox: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		var body struct {
			Messages []struct {
				ID       int64  `json:"id"`
				ThreadID string `json:"thread_id"`
				From     string `json:"from"`
				Text     string `json:"text"`
			} `json:"messages"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			log.Printf("hub inbox decode: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, m := range body.Messages {
			since = m.ID
			a.deliverToChat(m.ThreadID, m.Text)
		}
	}
}

// ---- telegram side ----

func (a *adapter) tgCall(method string, params any, out any) error {
	b, _ := json.Marshal(params)
	u := a.apiBase + "/bot" + a.token + "/" + method
	resp, err := a.client.Post(u, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// never include the URL (contains the token) in errors/logs
		return fmt.Errorf("telegram %s -> HTTP %d", method, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *adapter) deliverToChat(threadID, text string) {
	a.mu.Lock()
	chatID := a.chatID
	replyTo := a.tgMsgByThread[threadID]
	a.mu.Unlock()
	if chatID == 0 {
		log.Printf("no chat linked yet; dropping outbound (thread %s)", threadID)
		return
	}
	params := map[string]any{"chat_id": chatID, "text": text}
	if replyTo != 0 {
		params["reply_to_message_id"] = replyTo
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := a.tgCall("sendMessage", params, &res); err != nil || !res.OK {
		log.Printf("sendMessage failed (ok=%v): %v", res.OK, err)
		return
	}
	log.Printf("delivered to chat (thread %s, reply_to %d)", threadID, replyTo)
}

func (a *adapter) telegramLoop() {
	var offset int64
	for {
		var res struct {
			OK     bool `json:"ok"`
			Result []struct {
				UpdateID int64 `json:"update_id"`
				Message  struct {
					MessageID int64 `json:"message_id"`
					From      struct {
						Username string `json:"username"`
					} `json:"from"`
					Chat struct {
						ID int64 `json:"id"`
					} `json:"chat"`
					Text string `json:"text"`
				} `json:"message"`
			} `json:"result"`
		}
		err := a.tgCall("getUpdates", map[string]any{"offset": offset, "timeout": 25}, &res)
		if err != nil || !res.OK {
			log.Printf("getUpdates (ok=%v): %v", res.OK, err)
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range res.Result {
			offset = u.UpdateID + 1
			m := u.Message
			if m.Text == "" || m.Chat.ID == 0 {
				continue
			}
			a.mu.Lock()
			if a.chatID == 0 {
				a.chatID = m.Chat.ID // self-discovered from first message
				log.Printf("chat id self-discovered from first message")
			}
			a.mu.Unlock()
			a.forwardToHub(m.MessageID, m.From.Username, m.Text)
		}
	}
}

// forwardToHub turns a telegram message into a hub envelope addressed to the
// target agent; remembers thread->tg message id so the answer threads back.
func (a *adapter) forwardToHub(tgMsgID int64, username, text string) {
	var resp struct {
		ThreadID string `json:"thread_id"`
	}
	err := a.postJSON(a.hubURL+"/send", map[string]any{
		"from": agentName + "/" + username,
		"to":   a.target,
		"text": text,
		"meta": map[string]string{"tg_message_id": fmt.Sprint(tgMsgID)},
	}, &resp)
	if err != nil {
		log.Printf("forward to hub: %v", err)
		return
	}
	a.mu.Lock()
	a.tgMsgByThread[resp.ThreadID] = tgMsgID
	a.mu.Unlock()
	log.Printf("forwarded inbound (%s) to %q as thread %s", url.PathEscape(username), a.target, resp.ThreadID)
}
