package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/muthuishere/workwire/internal/config"
)

func writeInbox(t *testing.T, configDir, agent string, lines ...string) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "inbox.ndjson"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func qline(id, from, thread string) string {
	return fmt.Sprintf(`{"id":%q,"from":%q,"to":"repoA","thread_id":%q,"text":"q?","kind":"question"}`, id, from, thread)
}

func TestFindQuestion(t *testing.T) {
	configDir := t.TempDir()
	writeInbox(t, configDir, "repoA", qline("q-1", "asker", "t-1"), qline("q-2", "other", "t-2"))
	writeInbox(t, configDir, "repoB", qline("q-9", "human1", "t-9"))

	tests := []struct {
		name      string
		agent     string
		msgID     string
		wantOwner string
		wantFrom  string
		wantThr   string
		wantErr   bool
	}{
		{"finds by concrete id", "", "q-2", "repoA", "other", "t-2", false},
		{"scoped to named agent", "repoB", "q-9", "repoB", "human1", "t-9", false},
		{"id absent from named agent", "repoB", "q-1", "", "", "", true},
		{"unknown id", "", "q-404", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, owner, err := findQuestion(configDir, tc.agent, tc.msgID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", q)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// The answer stamps exactly these: reply_to = q.ID (the concrete
			// question id), thread_id = q.ThreadID, to = q.From.
			if q.ID != tc.msgID || q.From != tc.wantFrom || q.ThreadID != tc.wantThr || owner != tc.wantOwner {
				t.Fatalf("got id=%s from=%s thread=%s owner=%s", q.ID, q.From, q.ThreadID, owner)
			}
		})
	}
}

func TestAnswerRefusesLast(t *testing.T) {
	cfg := config.Defaults()
	cfg.ConfigDir = t.TempDir()
	err := cmdAnswer(cfg, []string{"last", "some answer"})
	if err == nil {
		t.Fatal("answer must refuse reply_to:\"last\"")
	}
}
