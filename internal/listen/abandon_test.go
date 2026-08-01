package listen

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// ADR-018. The listener is started detached (`nohup … & disown`), so it
// outlives the session that started it — on 2026-08-01 five of six live peers
// were listeners with no session behind them, one of them 22 hours old with an
// offset that had never moved. Unread bytes nobody consumes are the proof, and
// the proof has to be exact: standing a LIVE session down is a worse failure
// than the ghost we are fixing.
func TestAbandonedNeedsBothUnreadAndNoConsumer(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1", "repoA")
	r.opts.AbandonAfter = time.Hour
	base := time.Now()
	r.now = func() time.Time { return base }

	write := func(path, s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No inbox at all: a peer nobody has asked anything. Never abandoned.
	if ab, _, _ := r.Abandoned(); ab {
		t.Fatal("a listener with no inbox must never stand down")
	}

	// Delivered and unread — but only just. The clock starts, it does not fire.
	write(r.InboxPath(), "{\"id\":\"m-1\"}\n")
	ab, unread, waited := r.Abandoned()
	if ab || unread == 0 || waited != 0 {
		t.Fatalf("first unread should start the clock, not trip it: %v %d %v", ab, unread, waited)
	}

	// Still nobody reading an hour later: that is the ghost.
	r.now = func() time.Time { return base.Add(time.Hour + time.Second) }
	ab, unread, waited = r.Abandoned()
	if !ab {
		t.Fatal("unread for longer than AbandonAfter with no consumer must stand down")
	}
	if unread != int64(len("{\"id\":\"m-1\"}\n")) {
		t.Fatalf("unread byte count is what we report to the operator, got %d", unread)
	}
	if waited < time.Hour {
		t.Fatalf("waited should be how long it sat there, got %v", waited)
	}
}

// A live session with `workwire watch` armed consumes within seconds of
// delivery whether or not the agent has answered yet — so consumption, not
// answering, is what keeps the listener alive.
func TestAbandonedResetsWhenAnyConsumerEvidenceAppears(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(*Runner) string
	}{
		{"offset advanced by the watch", (*Runner).offsetPath},
		{"answerer declaration touched", (*Runner).answererMarkPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t, "http://127.0.0.1:1", "repoA")
			r.opts.AbandonAfter = time.Hour
			base := time.Now()
			r.now = func() time.Time { return base }

			if err := os.WriteFile(r.InboxPath(), []byte("{\"id\":\"m-1\"}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			r.Abandoned() // starts the clock

			// Somebody reads it 59 minutes in.
			r.now = func() time.Time { return base.Add(59 * time.Minute) }
			if err := os.WriteFile(tc.path(r), []byte("1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if ab, _, _ := r.Abandoned(); ab {
				t.Fatal("consumer evidence must reset the clock")
			}

			// The full window from the ORIGINAL delivery has now passed, but
			// only 2 minutes since the consumer showed itself.
			r.now = func() time.Time { return base.Add(61 * time.Minute) }
			if ab, _, _ := r.Abandoned(); ab {
				t.Fatal("the window must run from the last consumer evidence, not from delivery")
			}
		})
	}
}

// A fully consumed inbox is the normal state of a healthy quiet peer: the
// session is there, it has read everything, and there is simply no traffic.
// Standing that down would take live sessions off the wire.
func TestAbandonedIgnoresAFullyConsumedInbox(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1", "repoA")
	r.opts.AbandonAfter = time.Minute
	base := time.Now()
	r.now = func() time.Time { return base }

	body := "{\"id\":\"m-1\"}\n"
	if err := os.WriteFile(r.InboxPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.offsetPath(), []byte(fmt.Sprintf("%d\n", len(body))), 0o644); err != nil {
		t.Fatal(err)
	}
	// Age every trace of the consumer well past the window: the session has
	// been idle for a day, and has nothing waiting.
	old := base.Add(-24 * time.Hour)
	for _, p := range []string{r.InboxPath(), r.offsetPath()} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	r.now = func() time.Time { return base.Add(24 * time.Hour) }
	if ab, unread, _ := r.Abandoned(); ab {
		t.Fatalf("a consumed inbox is a healthy quiet peer, not a ghost (unread=%d)", unread)
	}
}

// The escape hatch has to actually disable it: a peer that is deliberately a
// mailbox (`--abandon-after 0`) must keep its lease forever.
func TestAbandonedCanBeDisabled(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1", "repoA")
	r.opts.AbandonAfter = -1
	base := time.Now()
	r.now = func() time.Time { return base }

	if err := os.WriteFile(r.InboxPath(), []byte("{\"id\":\"m-1\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.Abandoned()
	r.now = func() time.Time { return base.Add(30 * 24 * time.Hour) }
	if ab, _, _ := r.Abandoned(); ab {
		t.Fatal("--abandon-after 0 must never stand down")
	}
}

// The default must not be so tight that an ordinary busy session loses its
// listener, nor so loose that a dead one survives the day.
func TestAbandonAfterDefaults(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1", "repoA")
	if r.opts.AbandonAfter != 30*time.Minute {
		t.Fatalf("default AbandonAfter = %v, want 30m", r.opts.AbandonAfter)
	}
}
