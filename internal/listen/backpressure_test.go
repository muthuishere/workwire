package listen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The session inbox is a delivery BUFFER, not the store. Rotation only fires
// on a fully-consumed file, so before this a session that stopped reading grew
// the file without limit — `koine` reached 1.9 MB with 693 KB unread on
// 2026-08-01. The fix is to stop taking delivery, not to buffer forever: the
// hub holds every envelope against the cursor and re-offers it on resume.
func TestBacklogPausesDeliveryAndResumesWhenDrained(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{
		sessDir: dir,
		opts:    Options{BacklogMaxBytes: 1000, Logf: func(string, ...any) {}},
	}
	inbox := filepath.Join(dir, "inbox.ndjson")
	write := func(n int) {
		if err := os.WriteFile(inbox, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	consumed := func(off int64) {
		if err := os.WriteFile(filepath.Join(dir, "inbox.offset"), []byte(fmt.Sprint(off)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Under the cap: take delivery.
	write(500)
	consumed(0)
	if paused, unread := r.backlogged(); paused {
		t.Fatalf("paused at %d unread, cap 1000", unread)
	}

	// At the cap with nobody reading: stop.
	write(1200)
	if paused, unread := r.backlogged(); !paused {
		t.Fatalf("did not pause at %d unread, cap 1000", unread)
	}
	r.pausedLogged = true

	// Hysteresis: reading a little must NOT restart the flood.
	consumed(400) // 800 unread, still above half
	if paused, _ := r.backlogged(); !paused {
		t.Fatal("resumed while still more than half backlogged — that restarts a flood")
	}

	// Drained past half: resume.
	consumed(900) // 300 unread
	if paused, unread := r.backlogged(); paused {
		t.Fatalf("still paused at %d unread", unread)
	}
}

// A missing inbox file, a missing offset, and a disabled cap must all be
// "carry on" — backpressure may never become a reason not to deliver.
func TestBacklogNeverBlocksOnMissingState(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{sessDir: dir, opts: Options{BacklogMaxBytes: 1000, Logf: func(string, ...any) {}}}
	if paused, _ := r.backlogged(); paused {
		t.Fatal("paused with no inbox file at all")
	}
	if err := os.WriteFile(filepath.Join(dir, "inbox.ndjson"), []byte("line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if paused, _ := r.backlogged(); paused {
		t.Fatal("paused with no offset file")
	}
	r.opts.BacklogMaxBytes = 0
	if err := os.WriteFile(filepath.Join(dir, "inbox.ndjson"), []byte(strings.Repeat("x", 1<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if paused, _ := r.backlogged(); paused {
		t.Fatal("a disabled cap must never pause")
	}
}
