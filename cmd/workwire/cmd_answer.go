package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/workwire/internal/config"
)

// cmdAnswer posts an answer stamped with the CONCRETE question envelope id
// (agent-skill R6; ADR-001 forbids reply_to:"last" on the answer path). The
// question is looked up in the session inbox files the listener wrote.
func cmdAnswer(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("answer", flag.ExitOnError)
	agent := fs.String("agent", "", "answer as this agent (default: the agent whose inbox holds the question)")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: workwire answer [--agent <name>] <message_id> <text>")
	}
	msgID, text := rest[0], strings.Join(rest[1:], " ")
	if msgID == "last" {
		return fmt.Errorf("refusing reply_to:\"last\": answer with the concrete question id from the inbox line")
	}
	if cfg.ConfigDir == "" {
		return fmt.Errorf("no config dir resolvable; set WORKWIRE_CONFIG_DIR")
	}
	q, owner, err := findQuestion(cfg.ConfigDir, *agent, msgID)
	if err != nil {
		return err
	}
	c := newClient(cfg)
	if err := c.asAgent(cfg, owner); err != nil {
		return err
	}
	body := map[string]any{
		"to":        q.From,
		"text":      text,
		"thread_id": q.ThreadID,
		"reply_to":  q.ID,
	}
	var out map[string]any
	code, err := c.do("POST", "/send", body, &out)
	if err != nil {
		return err
	}
	if code != 200 {
		return fmt.Errorf("answer failed (%d): %v", code, out)
	}
	fmt.Printf("answered %s -> %s on thread %s (envelope %v)\n", q.ID, q.From, q.ThreadID, out["id"])
	return nil
}

type inboxLine struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	ThreadID string `json:"thread_id"`
}

// findQuestion scans sessions/<agent>/inbox.ndjson (all agents when none is
// named) for the envelope with the given id, returning it plus the owning
// agent name (the inbox's directory).
func findQuestion(configDir, agent, msgID string) (*inboxLine, string, error) {
	sessions := filepath.Join(configDir, "sessions")
	var agents []string
	if agent != "" {
		agents = []string{agent}
	} else {
		entries, err := os.ReadDir(sessions)
		if err != nil {
			return nil, "", fmt.Errorf("no session inboxes under %s: %w", sessions, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				agents = append(agents, e.Name())
			}
		}
	}
	for _, a := range agents {
		path := filepath.Join(sessions, a, "inbox.ndjson")
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
		var found *inboxLine
		for sc.Scan() {
			var l inboxLine
			if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
				continue
			}
			if l.ID == msgID {
				cp := l
				found = &cp
			}
		}
		f.Close()
		if found != nil {
			return found, a, nil
		}
	}
	return nil, "", fmt.Errorf("no inbox line with id %q found under %s — answer only delivered questions, by their concrete id", msgID, sessions)
}
