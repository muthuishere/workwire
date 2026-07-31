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
		ID: envelope.NewID("m"), From: from, To: envelope.Recipients{to},
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
	if found, _ := s.TombstoneMessage(e.ID, "muthu"); !found {
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

// multi returns an envelope addressed to several recipients (ADR-009).
func multi(from string, to []string, thread, text string) *envelope.Envelope {
	return &envelope.Envelope{
		ID: envelope.NewID("m"), From: from, To: envelope.Recipients(to),
		ThreadID: thread, Text: text, TS: envelope.Now(),
	}
}

func TestFanoutPerRecipientCursorsSurviveReplay(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	e := multi("alice", []string{"repoA", "repoB", "repoC"}, "t-1", "topic")
	if _, err := s.Append(e); err != nil {
		t.Fatal(err)
	}
	check := func(s *Store, label string) {
		t.Helper()
		seen := map[int64]bool{}
		for _, name := range []string{"repoA", "repoB", "repoC"} {
			msgs, next, reset := s.Inbox(name, 0)
			if reset || len(msgs) != 1 {
				t.Fatalf("%s/%s: want 1 message, got %d (reset=%v)", label, name, len(msgs), reset)
			}
			if msgs[0].Env.ID != e.ID {
				t.Fatalf("%s/%s: envelope id must be shared", label, name)
			}
			if seen[next] {
				t.Fatalf("%s/%s: cursor %d not per-recipient", label, name, next)
			}
			seen[next] = true
			if more, _, _ := s.Inbox(name, next); len(more) != 0 {
				t.Fatalf("%s/%s: redelivered after its own cursor", label, name)
			}
		}
		if msgs, _, _ := s.Inbox("nobody", 0); len(msgs) != 0 {
			t.Fatalf("%s: non-recipient received the envelope", label)
		}
	}
	check(s, "live")
	s.Close()
	check(mustOpen(t, dir, Options{}), "replayed")
}

func TestThreadStateMembershipAndConvergence(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	if _, err := s.Append(multi("alice", []string{"repoA", "repoB"}, "t-1", "topic")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(multi("repoC", []string{"alice"}, "t-1", "joining")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		cap         int
		wantState   string
		wantMembers int
	}{
		{"open under the cap", 24, "open", 4},
		{"stalled at the cap", 2, "stalled", 4},
		{"cap disabled", 0, "open", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, ok := s.ThreadState("t-1", tc.cap)
			if !ok {
				t.Fatal("thread missing")
			}
			if ts.State != tc.wantState {
				t.Fatalf("state: %s", ts.State)
			}
			if len(ts.Members) != tc.wantMembers {
				t.Fatalf("members: %v", ts.Members)
			}
			if ts.Initiator != "alice" {
				t.Fatalf("initiator: %q", ts.Initiator)
			}
		})
	}
	if _, err := s.Append(&envelope.Envelope{
		ID: envelope.NewID("m"), From: "alice", To: envelope.Recipients{"repoA"},
		ThreadID: "t-1", Kind: "resolved", Text: "done", TS: envelope.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if ts, _ := s.ThreadState("t-1", 24); ts.State != "resolved" || !ts.Resolved {
		t.Fatalf("resolved envelope did not close the thread: %+v", ts)
	}
	if all := s.Threads(24); len(all) != 1 || all[0].ThreadID != "t-1" {
		t.Fatalf("Threads listing: %+v", all)
	}
}

// kindEnv builds an envelope with an explicit kind and stamped peer role, the
// way ingest does.
func kindEnv(from, thread, kind, text, role string) *envelope.Envelope {
	e := env(from, "", thread, text)
	e.To = nil
	e.Kind = kind
	e.Meta = map[string]any{"peerRole": role}
	return e
}

// F2: derived thread state is a read, so R13 excision applies to it too.
func TestThreadStateHonoursTombstones(t *testing.T) {
	cases := []struct {
		name       string
		deleteHead bool
		deleteDiss bool
		wantTopic  string
		wantDiss   string
	}{
		{"nothing deleted", false, false, "TOPIC-SECRET", "DISSENT-SECRET"},
		{"topic deleted", true, false, "", "DISSENT-SECRET"},
		{"dissent deleted", false, true, "TOPIC-SECRET", ""},
		{"both deleted", true, true, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustOpen(t, t.TempDir(), Options{})
			head := kindEnv("muthu", "t-1", "", "TOPIC-SECRET", "human")
			diss := kindEnv("priya", "t-1", "dissent", "DISSENT-SECRET", "human")
			s.Append(head)
			s.Append(diss)
			if tc.deleteHead {
				s.TombstoneMessage(head.ID, "muthu")
			}
			if tc.deleteDiss {
				s.TombstoneMessage(diss.ID, "muthu")
			}
			ts, ok := s.ThreadState("t-1", 0)
			if !ok {
				t.Fatal("thread missing")
			}
			if ts.Topic != tc.wantTopic {
				t.Fatalf("topic = %q, want %q", ts.Topic, tc.wantTopic)
			}
			if len(ts.Dissents) != 1 {
				t.Fatalf("want exactly one open dissent, got %d", len(ts.Dissents))
			}
			if ts.Dissents[0].Text != tc.wantDiss {
				t.Fatalf("dissent text = %q, want %q", ts.Dissents[0].Text, tc.wantDiss)
			}
			// The accountability record itself is never excised: who objected
			// survives even when what they said does not (ADR-011).
			if ts.Dissents[0].Peer != "priya" || ts.Dissents[0].Kind != "human" {
				t.Fatalf("dissent attribution lost: %+v", ts.Dissents[0])
			}
		})
	}
}

// F2: R22 lists every NON-deleted thread.
func TestThreadsSkipsFullyDeletedThreads(t *testing.T) {
	s := mustOpen(t, t.TempDir(), Options{})
	keep := kindEnv("muthu", "t-keep", "", "kept", "human")
	gone := kindEnv("muthu", "t-gone", "", "GONE-SECRET", "human")
	s.Append(keep)
	s.Append(gone)
	if found, _ := s.TombstoneThread("t-gone", "muthu"); !found {
		t.Fatal("thread tombstone should find the thread")
	}
	list := s.Threads(0)
	if len(list) != 1 || list[0].ThreadID != "t-keep" {
		t.Fatalf("want only t-keep listed, got %+v", list)
	}
}

// F2: the excision itself is accountable.
func TestTombstoneRecordsDeleter(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{})
	e := env("a", "b", "t-1", "secret")
	s.Append(e)
	s.TombstoneMessage(e.ID, "muthu")
	s.Close()
	s2 := mustOpen(t, dir, Options{})
	tomb, ok := s2.Tombstone(e.ID)
	if !ok {
		t.Fatal("tombstone record missing after replay")
	}
	if tomb.DeletedBy != "muthu" || tomb.DeletedAt == "" {
		t.Fatalf("deletion record incomplete: %+v", tomb)
	}
}

// F7: an age-based policy must actually collect. Before the fix nothing ever
// rotated the active segment on age, so retention never had anything to drop.
func TestAgedRetentionCollects(t *testing.T) {
	dir := t.TempDir()
	s := mustOpen(t, dir, Options{RetentionAge: time.Hour})
	for i := 0; i < 20; i++ {
		s.Append(env("a", "repoA", "t-1", fmt.Sprintf("m%d", i)))
	}
	// Well inside the window: nothing rotates, nothing is collected.
	if err := s.Maintain(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if msgs, _, reset := s.Inbox("repoA", 0); reset || len(msgs) != 20 {
		t.Fatalf("early maintain must not drop anything: %d msgs reset=%v", len(msgs), reset)
	}
	// Past the window: the aged active segment rotates, then expires.
	if err := s.Maintain(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(time.Now().Add(4 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	msgs, _, reset := s.Inbox("repoA", 0)
	if len(msgs) != 0 || !reset {
		t.Fatalf("aged policy never collected: %d msgs still retained (reset=%v)", len(msgs), reset)
	}
}

// F1: retention drops segments; it must not rewrite the audit record.
func TestRetentionPreservesThreadState(t *testing.T) {
	dir := t.TempDir()
	// A tiny segment bound puts a segment boundary inside one thread's life.
	s := mustOpen(t, dir, Options{SegmentMaxBytes: 400})
	s.Append(kindEnv("muthu", "t-1", "", "the original topic", "human"))
	s.Append(kindEnv("api", "t-1", "", "chatter", "agent"))
	s.Append(kindEnv("web", "t-1", "dissent", "tokens rotate", "agent"))
	s.Append(kindEnv("muthu", "t-1", "resolved", "shipping anyway", "human"))
	before, ok := s.ThreadState("t-1", 0)
	if !ok {
		t.Fatal("thread missing")
	}
	// Unrelated traffic, then age everything out except the active segment.
	for i := 0; i < 20; i++ {
		s.Append(env("api", "repoA", "t-noise", fmt.Sprintf("n%d", i)))
	}
	if err := s.EnforceRetention(time.Now().Add(365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, ok := s.ThreadState("t-1", 0)
	if !ok {
		t.Fatal("thread state lost to retention")
	}
	checks := []struct {
		field     string
		got, want any
	}{
		{"initiator", after.Initiator, before.Initiator},
		{"topic", after.Topic, before.Topic},
		{"state", after.State, before.State},
		{"resolved", after.Resolved, before.Resolved},
		{"closed_by", after.ClosedBy, before.ClosedBy},
		{"closed_by_kind", after.ClosedByKind, before.ClosedByKind},
		{"closed_over", len(after.ClosedOver), len(before.ClosedOver)},
		{"open dissents", len(after.Dissents), len(before.Dissents)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s rewritten by retention: got %v, want %v", c.field, c.got, c.want)
		}
	}
	if before.Initiator != "muthu" || before.ClosedBy != "muthu" || len(before.ClosedOver) != 1 {
		t.Fatalf("precondition wrong, the test is not asserting what it thinks: %+v", before)
	}
	// ...and the loss that DID happen is declared rather than hidden.
	if !after.Truncated {
		t.Error("a thread whose head was dropped must report truncated:true")
	}
}
