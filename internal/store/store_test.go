package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/muthuishere/workwire/internal/envelope"
)

func mustOpen(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func env(from, to, thread, text string) *envelope.Envelope {
	return &envelope.Envelope{
		ID: envelope.NewID("m"), From: from, To: to,
		ThreadID: thread, Text: text, TS: envelope.Now(),
	}
}

func TestSingleWriterLock(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	if _, err := Open(dir, Options{}); err == nil {
		t.Fatal("second open on a locked data dir must fail")
	}
	s.Close()
	// lock released with the process/close: a new open succeeds immediately
	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	s2.Close()
}

func TestRotationInvisibleToCursors(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{SegmentMaxBytes: 300}) // rotate every couple of messages
	for i := 0; i < 20; i++ {
		if _, err := s.Append(env("a", "repoA", "t-1", fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	// consumer at cursor 5 within retained history: no reset, exact continuation
	msgs, next, reset := s.Inbox("repoA", 5)
	if reset {
		t.Fatal("rotation must not cause reset")
	}
	if len(msgs) != 15 || msgs[0].Env.Text != "m5" || next != 20 {
		t.Fatalf("continuation broken: %d msgs, next=%d", len(msgs), next)
	}
}

func TestRestartPreservesSeqAndThreads(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	for i := 0; i < 5; i++ {
		s.Append(env("a", "repoA", "t-1", fmt.Sprintf("m%d", i)))
	}
	s.Close()
	s2 := mustOpen(t, dir, Options{})
	if s2.LastSeq() != 5 {
		t.Fatalf("lastSeq: %d", s2.LastSeq())
	}
	list, ok := s2.Thread("t-1", 0)
	if !ok || len(list) != 5 {
		t.Fatalf("thread lost: %v %d", ok, len(list))
	}
	msgs, next, reset := s2.Inbox("repoA", 3)
	if reset || len(msgs) != 2 || next != 5 {
		t.Fatalf("cursor across restart: %d msgs next=%d reset=%v", len(msgs), next, reset)
	}
}

func TestRetentionResetSemantics(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{SegmentMaxBytes: 200})
	for i := 0; i < 10; i++ {
		s.Append(env("a", "repoA", "t-1", fmt.Sprintf("m%d", i)))
	}
	s.RotateNow()
	if err := s.EnforceRetention(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	msgs, next, reset := s.Inbox("repoA", 3)
	if !reset || len(msgs) != 0 {
		t.Fatalf("want reset with no messages: reset=%v msgs=%d", reset, len(msgs))
	}
	// after rebasing, new appends flow normally with monotonic seq
	s.Append(env("a", "repoA", "t-2", "fresh"))
	msgs, next2, reset := s.Inbox("repoA", next)
	if reset || len(msgs) != 1 || next2 != 11 {
		t.Fatalf("rebase broken: %d msgs next=%d reset=%v", len(msgs), next2, reset)
	}
}

func TestTombstoneSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	e := env("a", "b", "t-1", "the secret text")
	s.Append(e)
	if found, _ := s.TombstoneMessage(e.ID); !found {
		t.Fatal("tombstone should find the message")
	}
	if r := s.Render(e); r.Text != "" || r.ID != e.ID {
		t.Fatalf("render: %+v", r)
	}
	s.Close()
	s2 := mustOpen(t, dir, Options{})
	got, ok := s2.Get(e.ID)
	if !ok {
		t.Fatal("id must survive for dedupe")
	}
	if !s2.IsTombstoned(e.ID) {
		t.Fatal("tombstone lost on replay")
	}
	if r := s2.Render(got.Env); r.Text != "" {
		t.Fatalf("content resurfaced: %+v", r)
	}
}

func TestLastInbound(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	e1 := env("alice", "bob", "t-1", "q")
	e2 := env("bob", "alice", "t-1", "a")
	s.Append(e1)
	s.Append(e2)
	got, ok := s.LastInbound("t-1", "alice")
	if !ok || got.Env.ID != e2.ID {
		t.Fatalf("want bob's message: %v", got)
	}
	if _, ok := s.LastInbound("t-1", "carol"); !ok {
		t.Fatal("carol should see e2 (newest not hers)")
	}
	if _, ok := s.LastInbound("t-empty", "alice"); ok {
		t.Fatal("empty thread has no inbound")
	}
}
