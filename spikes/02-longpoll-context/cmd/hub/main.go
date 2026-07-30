// spike02 hub: dumb HTTP-only envelope hub (ADR-001).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	spike02 "spike02"
)

const maxLastMessages = 20 // hard cap on read-time context depth

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	addr := flag.String("addr", envOr("AGENTHUB_BIND", "127.0.0.1:14411"), "bind address")
	dataDir := flag.String("data", envOr("AGENTHUB_DATA_DIR", "./data"), "data dir")
	lastMessages := flag.Int("lastMessages", 5, "default context depth")
	flag.Parse()

	store, err := spike02.OpenStore(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("hub on %s, data=%s, cursor=%d", *addr, *dataDir, store.Lines())

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"service": "agenthub", "ok": true, "cursor": store.Lines()})
	})

	mux.HandleFunc("POST /send", func(w http.ResponseWriter, r *http.Request) {
		var e spike02.Envelope
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		stored, cursor, err := store.Append(e)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, map[string]any{"id": stored.ID, "thread_id": stored.ThreadID,
			"reply_to": stored.ReplyTo, "cursor": cursor})
	})

	// GET /inbox?since=N&wait=25&context=X[&to=name]
	mux.HandleFunc("GET /inbox", func(w http.ResponseWriter, r *http.Request) {
		since := qInt(r, "since", 0)
		wait := qInt(r, "wait", 0)
		ctxDepth := qInt(r, "context", *lastMessages)
		if ctxDepth > maxLastMessages {
			ctxDepth = maxLastMessages
		}
		to := r.URL.Query().Get("to")
		deadline := time.Now().Add(time.Duration(wait) * time.Second)
		for {
			msgs, cursor, reset := store.Since(since)
			msgs = filterTo(msgs, to)
			if reset {
				writeJSON(w, map[string]any{"messages": []any{}, "cursor": cursor, "reset": true})
				return
			}
			if len(msgs) > 0 || wait <= 0 || time.Now().After(deadline) {
				out := make([]spike02.Envelope, len(msgs))
				for i, m := range msgs {
					out[i] = store.Project(m, ctxDepth)
				}
				writeJSON(w, map[string]any{"messages": out, "cursor": cursor})
				return
			}
			select { // long-poll: block until an append or the deadline
			case <-store.Changed():
			case <-time.After(time.Until(deadline)):
			case <-r.Context().Done():
				return
			}
		}
	})

	mux.HandleFunc("GET /threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		last := qInt(r, "last", 0)
		writeJSON(w, map[string]any{"thread_id": r.PathValue("id"),
			"messages": store.Thread(r.PathValue("id"), last)})
	})

	mux.HandleFunc("GET /last", func(w http.ResponseWriter, r *http.Request) {
		m, ok := store.Last(r.URL.Query().Get("thread"), r.URL.Query().Get("peer"))
		if !ok {
			http.Error(w, "none", 404)
			return
		}
		writeJSON(w, m)
	})

	log.Fatal(http.ListenAndServe(*addr, mux))
}

func filterTo(msgs []spike02.Envelope, to string) []spike02.Envelope {
	if to == "" {
		return msgs
	}
	var out []spike02.Envelope
	for _, m := range msgs {
		if strings.EqualFold(m.To, to) {
			out = append(out, m)
		}
	}
	return out
}

func qInt(r *http.Request, k string, d int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil {
		return v
	}
	return d
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
