package listen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// The listener delivers; it never answers. So holding a lease must never make
// it claim an answerer — only evidence from the session side does: a
// declaration file (`workwire answering`) or a consumed-to offset.
func TestAnswererAttachedIsSessionEvidenceNotTheLease(t *testing.T) {
	r := newRunner(t, "http://127.0.0.1:1", "repoA")
	r.opts.AnswererIdle = time.Minute

	if r.AnswererAttached() {
		t.Fatal("a fresh listener must not claim an answerer")
	}

	// The consumer advancing its offset is evidence.
	if err := os.WriteFile(r.offsetPath(), []byte("128\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.AnswererAttached() {
		t.Fatal("a freshly advanced offset should count as an attached answerer")
	}

	// Evidence goes stale: an answerer that stopped renewing has gone.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(r.offsetPath(), old, old); err != nil {
		t.Fatal(err)
	}
	if r.AnswererAttached() {
		t.Fatal("stale evidence must not keep claiming an answerer")
	}

	// An explicit declaration counts before anything has been answered.
	if err := os.WriteFile(r.answererMarkPath(), []byte("attached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !r.AnswererAttached() {
		t.Fatal("an explicit declaration should count")
	}
}

// The declaration reaches the hub, is renewed while it holds, and is
// withdrawn when the evidence goes away.
func TestDeclareAnsweringReportsToTheHub(t *testing.T) {
	var got []bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /agents/{name}/answering", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Attached bool `json:"attached"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body.Attached)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"answering":%v}`, body.Attached)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	r := newRunner(t, ts.URL, "repoA")
	r.secret = "s3cret-test"
	r.opts.AnswererIdle = time.Minute

	// Nothing attached and nothing previously declared: no call at all.
	r.declareAnswering()
	if len(got) != 0 {
		t.Fatalf("declared answerability with no evidence: %v", got)
	}

	if err := os.WriteFile(r.answererMarkPath(), []byte("attached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.declareAnswering()
	r.declareAnswering() // renewal keeps saying true; the TTL needs it
	if len(got) != 2 || !got[0] || !got[1] {
		t.Fatalf("expected two true declarations, got %v", got)
	}

	if err := os.Remove(r.answererMarkPath()); err != nil {
		t.Fatal(err)
	}
	r.declareAnswering()
	if len(got) != 3 || got[2] {
		t.Fatalf("expected a withdrawal, got %v", got)
	}
	r.declareAnswering()
	if len(got) != 3 {
		t.Fatalf("a withdrawn declaration must not be repeated: %v", got)
	}
}
