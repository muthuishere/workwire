// Package store persists envelopes in append-only NDJSON segments with
// hub-assigned per-recipient sequence cursors, retention/rotation invisible
// to clients, tombstone excision, and a single-writer flock on the data dir
// (hub-core R4, R5, R12, R13; ADR-008).
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/workwire/internal/envelope"
	"github.com/muthuishere/workwire/internal/origin"
)

// Options tune rotation and retention.
type Options struct {
	SegmentMaxBytes   int64
	RetentionAge      time.Duration
	RetentionMaxBytes int64
}

func (o *Options) defaults() {
	if o.SegmentMaxBytes <= 0 {
		o.SegmentMaxBytes = 64 << 20
	}
	if o.RetentionAge <= 0 {
		o.RetentionAge = 30 * 24 * time.Hour
	}
	if o.RetentionMaxBytes <= 0 {
		o.RetentionMaxBytes = 1 << 30
	}
}

// record is one NDJSON line in a segment or the tombstone file.
type record struct {
	Type string             `json:"type"` // "msg" | "tomb"
	Seq  int64              `json:"seq,omitempty"`
	Seqs map[string]int64   `json:"seqs,omitempty"` // per-recipient cursors (ADR-009)
	Env  *envelope.Envelope `json:"env,omitempty"`
	ID   string             `json:"id,omitempty"`
}

// Stored is an envelope with its hub-assigned sequence numbers: Seq is the
// envelope's own (lowest) sequence, Seqs carries one cursor per recipient so
// a single fanned-out envelope advances every recipient independently
// (ADR-009).
type Stored struct {
	Seq  int64
	Seqs map[string]int64
	Env  *envelope.Envelope
}

// SeqFor returns the recipient's own sequence number for this envelope.
func (s *Stored) SeqFor(agent string) (int64, bool) {
	if s.Seqs != nil {
		q, ok := s.Seqs[agent]
		return q, ok
	}
	if s.Env != nil && s.Env.To.Has(agent) {
		return s.Seq, true
	}
	return 0, false
}

type state struct {
	LastSeq int64 `json:"lastSeq"`
}

// Store is the single-writer envelope store.
type Store struct {
	dir  string
	opts Options

	mu       sync.Mutex
	msgs     []*Stored // ordered by seq (retained only)
	byID     map[string]*Stored
	threads  map[string][]*Stored
	tombs    map[string]bool
	lastSeq  int64
	minSeq   int64 // smallest retained seq; lastSeq+1 when nothing retained
	seg      *os.File
	segPath  string
	segSize  int64
	segIdx   int
	tombFile *os.File
	lock     *dirLock
	notify   chan struct{}
}

// Open acquires the data-dir lock, replays segments and tombstones, and
// returns a ready store. A locked data dir returns an error naming it.
func Open(dir string, opts Options) (*Store, error) {
	opts.defaults()
	if err := os.MkdirAll(filepath.Join(dir, "segments"), 0o755); err != nil {
		return nil, err
	}
	lock, err := acquireLock(filepath.Join(dir, ".lock"))
	if err != nil {
		return nil, fmt.Errorf("data dir %s is locked by another workwire hub: %w", dir, err)
	}
	s := &Store{
		dir:     dir,
		opts:    opts,
		byID:    map[string]*Stored{},
		threads: map[string][]*Stored{},
		tombs:   map[string]bool{},
		lock:    lock,
		notify:  make(chan struct{}),
	}
	if err := s.load(); err != nil {
		lock.release()
		return nil, err
	}
	tf, err := os.OpenFile(filepath.Join(dir, "tombstones.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		lock.release()
		return nil, err
	}
	s.tombFile = tf
	if err := s.openSegment(); err != nil {
		lock.release()
		return nil, err
	}
	return s, nil
}

// Close releases files and the lock.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seg != nil {
		s.seg.Close()
	}
	if s.tombFile != nil {
		s.tombFile.Close()
	}
	s.writeState()
	if s.lock != nil {
		s.lock.release()
	}
}

func (s *Store) segDir() string { return filepath.Join(s.dir, "segments") }

func segName(idx int) string { return fmt.Sprintf("seg-%08d.ndjson", idx) }

func (s *Store) load() error {
	// state file gives lastSeq even after full retention wipe.
	if b, err := os.ReadFile(filepath.Join(s.dir, "state.json")); err == nil {
		var st state
		if json.Unmarshal(b, &st) == nil {
			s.lastSeq = st.LastSeq
		}
	}
	names, err := s.segmentNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := s.loadSegment(filepath.Join(s.segDir(), name)); err != nil {
			return err
		}
		var idx int
		fmt.Sscanf(name, "seg-%08d.ndjson", &idx)
		if idx > s.segIdx {
			s.segIdx = idx
		}
	}
	// tombstones replay last: they must survive rotation and restart.
	if f, err := os.Open(filepath.Join(s.dir, "tombstones.ndjson")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			var r record
			if json.Unmarshal(sc.Bytes(), &r) == nil && r.Type == "tomb" && r.ID != "" {
				s.tombs[r.ID] = true
			}
		}
		f.Close()
	}
	sort.Slice(s.msgs, func(i, j int) bool { return s.msgs[i].Seq < s.msgs[j].Seq })
	for _, list := range s.threads {
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
	}
	s.recomputeBounds()
	return nil
}

func (s *Store) segmentNames() ([]string, error) {
	ents, err := os.ReadDir(s.segDir())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "seg-") && strings.HasSuffix(e.Name(), ".ndjson") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) loadSegment(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // torn tail line after a crash: skip
		}
		if r.Type != "msg" || r.Env == nil {
			continue
		}
		st := &Stored{Seq: r.Seq, Seqs: r.Seqs, Env: r.Env}
		s.msgs = append(s.msgs, st)
		s.byID[r.Env.ID] = st
		s.threads[r.Env.ThreadID] = append(s.threads[r.Env.ThreadID], st)
		if r.Seq > s.lastSeq {
			s.lastSeq = r.Seq
		}
		for _, q := range r.Seqs {
			if q > s.lastSeq {
				s.lastSeq = q
			}
		}
	}
	return sc.Err()
}

func (s *Store) recomputeBounds() {
	if len(s.msgs) == 0 {
		s.minSeq = s.lastSeq + 1
		return
	}
	s.minSeq = s.msgs[0].Seq
}

func (s *Store) openSegment() error {
	if s.segIdx == 0 {
		s.segIdx = 1
	}
	s.segPath = filepath.Join(s.segDir(), segName(s.segIdx))
	f, err := os.OpenFile(s.segPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	s.seg = f
	s.segSize = fi.Size()
	return nil
}

func (s *Store) rotateLocked() error {
	s.seg.Close()
	s.segIdx++
	return s.openSegment()
}

func (s *Store) writeState() {
	b, _ := json.Marshal(state{LastSeq: s.lastSeq})
	_ = os.WriteFile(filepath.Join(s.dir, "state.json"), append(b, '\n'), 0o644)
}

// Append persists an envelope, assigns its hub sequence number, and wakes
// all long-poll waiters.
func (s *Store) Append(env *envelope.Envelope) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevSeq := s.lastSeq
	// One sequence number per recipient; the envelope keeps a single id
	// (ADR-009). A recipientless envelope still consumes one sequence.
	seqs := map[string]int64{}
	for _, to := range env.To {
		s.lastSeq++
		seqs[to] = s.lastSeq
	}
	if len(seqs) == 0 {
		s.lastSeq++
	}
	seq := prevSeq + 1
	b, err := json.Marshal(record{Type: "msg", Seq: seq, Seqs: seqs, Env: env})
	if err != nil {
		s.lastSeq = prevSeq
		return 0, err
	}
	b = append(b, '\n')
	if _, err := s.seg.Write(b); err != nil {
		s.lastSeq = prevSeq
		return 0, err
	}
	s.segSize += int64(len(b))
	if s.segSize >= s.opts.SegmentMaxBytes {
		if err := s.rotateLocked(); err != nil {
			return 0, err
		}
	}
	st := &Stored{Seq: seq, Seqs: seqs, Env: env}
	s.msgs = append(s.msgs, st)
	s.byID[env.ID] = st
	s.threads[env.ThreadID] = append(s.threads[env.ThreadID], st)
	if s.minSeq > seq {
		s.minSeq = seq
	}
	s.writeState()
	s.wakeLocked()
	return seq, nil
}

func (s *Store) wakeLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

// Watch returns a channel closed on the next store change (broadcast wake).
func (s *Store) Watch() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

// Inbox returns retained messages addressed to agent with seq > since.
// A since older than retained history returns reset=true with next rebased
// to the earliest available cursor (hub-core R5).
func (s *Store) Inbox(agent string, since int64) (msgs []*Stored, next int64, reset bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	earliest := s.minSeq - 1
	if since < earliest {
		return nil, earliest, true
	}
	next = since
	for _, st := range s.msgs {
		q, ok := st.SeqFor(agent)
		if !ok || q <= since {
			continue
		}
		msgs = append(msgs, st)
		if q > next {
			next = q
		}
	}
	return msgs, next, false
}

// Dissent is one participant's objection on a thread (ADR-011 §2): who
// objected, whether they are a human or an agent, what they said, and the
// provenance they said it from.
type Dissent struct {
	Peer   string       `json:"peer"`
	Kind   string       `json:"kind,omitempty"` // "agent" | "human"
	Text   string       `json:"text,omitempty"`
	TS     string       `json:"ts,omitempty"`
	Origin *origin.Info `json:"origin,omitempty"`
}

// IsHuman reports whether the dissenter is a person — human dissent is not
// overridable by anybody but that person (ADR-011 §3).
func (d Dissent) IsHuman() bool { return d.Kind == "human" }

// ThreadState is the live view of a discussion (ADR-009): membership accrued
// from participation, message count, convergence state, and the open dissents
// that decide whether a close is valid (ADR-011).
type ThreadState struct {
	ThreadID string `json:"thread_id"`
	// Initiator opened the thread and is the only member who may resolve it
	// (ADR-009): participants surface perspectives, the initiator decides.
	Initiator string   `json:"initiator"`
	Members   []string `json:"members"`
	Count     int      `json:"count"`
	State     string   `json:"state"` // "open" | "resolved" | "stalled"
	Resolved  bool     `json:"-"`
	LastTS    string   `json:"last_ts,omitempty"`
	// Dissents are the OPEN objections right now, in the order they were
	// first raised. An agent initiator may not close while any is open.
	Dissents []Dissent `json:"dissents,omitempty"`
	// ClosedBy / ClosedOver record who closed the thread and which open
	// dissents that closure overrode, so the record shows what was overruled.
	ClosedBy     string    `json:"closed_by,omitempty"`
	ClosedByKind string    `json:"closed_by_kind,omitempty"`
	ClosedOver   []Dissent `json:"closed_over,omitempty"`
	// Topic is the thread's opening line, so a peer browsing threads it was
	// never addressed in can tell whether it touches what they own.
	Topic string `json:"topic,omitempty"`
	// Reopened is true when a human reopened the thread after a close or a
	// stall (ADR-011 §3a).
	Reopened bool `json:"reopened,omitempty"`
}

// HasMember reports whether name is a member of the thread.
func (t ThreadState) HasMember(name string) bool {
	for _, m := range t.Members {
		if m == name {
			return true
		}
	}
	return false
}

// OpenDissentsBy returns the open dissents raised by peers other than `peer`.
func (t ThreadState) OpenDissentsBy(exclude string) []Dissent {
	out := make([]Dissent, 0, len(t.Dissents))
	for _, d := range t.Dissents {
		if d.Peer != exclude {
			out = append(out, d)
		}
	}
	return out
}

// OpenHumanDissents returns the open dissents raised by humans other than
// `exclude` — the only dissents a human close cannot override.
func (t ThreadState) OpenHumanDissents(exclude string) []Dissent {
	out := make([]Dissent, 0, len(t.Dissents))
	for _, d := range t.Dissents {
		if d.IsHuman() && d.Peer != exclude {
			out = append(out, d)
		}
	}
	return out
}

// roleOf reads the peer kind the hub stamped on an envelope at ingest.
func roleOf(e *envelope.Envelope) string {
	if e.Meta == nil {
		return "agent"
	}
	if s, ok := e.Meta["peerRole"].(string); ok && s == "human" {
		return "human"
	}
	return "agent"
}

// originOf reads the provenance the hub stamped on an envelope at ingest.
func originOf(e *envelope.Envelope) *origin.Info {
	if e.Meta == nil {
		return nil
	}
	m, _ := e.Meta["origin"].(map[string]any)
	if m == nil {
		if oi, ok := e.Meta["origin"].(*origin.Info); ok {
			return oi
		}
		return nil
	}
	return origin.FromMap(m)
}

// threadStateLocked derives membership, dissent and state from the persisted
// envelopes themselves — nothing extra to keep in sync, and it survives
// restart because the segments do.
func (s *Store) threadStateLocked(id string, cap int) (ThreadState, bool) {
	list, ok := s.threads[id]
	if !ok {
		return ThreadState{}, false
	}
	ts := ThreadState{ThreadID: id, State: "open"}
	seen := map[string]bool{}
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		ts.Members = append(ts.Members, n)
	}
	open := map[string]Dissent{}
	var order []string
	collect := func() []Dissent {
		out := make([]Dissent, 0, len(open))
		for _, name := range order {
			if d, ok := open[name]; ok {
				out = append(out, d)
			}
		}
		return out
	}
	capBase := 0
	if len(list) > 0 {
		ts.Initiator = list[0].Env.From
		ts.Topic = list[0].Env.Text
	}
	for idx, st := range list {
		e := st.Env
		add(e.From)
		for _, to := range e.To {
			add(to)
		}
		switch e.Kind {
		case "dissent":
			// A dissent on a resolved thread is history, not a reopen
			// (ADR-011 §3a): the decision ends, the disagreement does not.
			if !ts.Resolved {
				if _, had := open[e.From]; !had {
					order = append(order, e.From)
				}
				open[e.From] = Dissent{
					Peer: e.From, Kind: roleOf(e), Text: e.Text, TS: e.TS, Origin: originOf(e),
				}
			}
		case "withdraw":
			// Clears only the sender's own dissent.
			if !ts.Resolved {
				delete(open, e.From)
			}
		case "resolved":
			ts.Resolved = true
			ts.ClosedBy = e.From
			ts.ClosedByKind = roleOf(e)
			// What the closure overrode: every dissent open at that moment
			// except the closer's own (you do not override yourself).
			for _, d := range collect() {
				if d.Peer != e.From {
					ts.ClosedOver = append(ts.ClosedOver, d)
				}
			}
		case "reopen":
			ts.Resolved = false
			ts.ClosedBy, ts.ClosedByKind, ts.ClosedOver = "", "", nil
			ts.Reopened = true
			capBase = idx + 1 // the round cap starts over from the reopen
		}
		ts.LastTS = e.TS
	}
	ts.Dissents = collect()
	ts.Count = len(list) - capBase
	switch {
	case ts.Resolved:
		ts.State = "resolved"
	case cap > 0 && ts.Count >= cap:
		ts.State = "stalled"
	}
	return ts, true
}

// ThreadState returns the membership/count/state of one thread.
func (s *Store) ThreadState(id string, cap int) (ThreadState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadStateLocked(id, cap)
}

// Threads lists every retained thread, newest activity first.
func (s *Store) Threads(cap int) []ThreadState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ThreadState, 0, len(s.threads))
	for id := range s.threads {
		if ts, ok := s.threadStateLocked(id, cap); ok {
			out = append(out, ts)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastTS != out[j].LastTS {
			return out[i].LastTS > out[j].LastTS
		}
		return out[i].ThreadID < out[j].ThreadID
	})
	return out
}

// Thread returns the last n envelopes of a thread in order (all when n<=0)
// and whether the thread exists in retained history.
func (s *Store) Thread(id string, n int) ([]*Stored, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, ok := s.threads[id]
	if !ok {
		return nil, false
	}
	if n > 0 && len(list) > n {
		list = list[len(list)-n:]
	}
	return append([]*Stored(nil), list...), true
}

// Get returns a stored envelope by id.
func (s *Store) Get(id string) (*Stored, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.byID[id]
	return st, ok
}

// LastInbound returns the newest envelope on a thread not authored by
// sender (reply_to:"last" resolution, hub-core R3).
func (s *Store) LastInbound(threadID, sender string) (*Stored, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.threads[threadID]
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].Env.From != sender {
			return list[i], true
		}
	}
	return nil, false
}

// IsTombstoned reports whether an envelope id has been excised.
func (s *Store) IsTombstoned(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tombs[id]
}

func (s *Store) appendTombLocked(id string) error {
	b, _ := json.Marshal(record{Type: "tomb", ID: id})
	if _, err := s.tombFile.Write(append(b, '\n')); err != nil {
		return err
	}
	s.tombs[id] = true
	return nil
}

// TombstoneMessage excises one envelope's content. Returns false when the id
// is unknown; repeating is idempotent.
func (s *Store) TombstoneMessage(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[id]; !ok && !s.tombs[id] {
		return false, nil
	}
	if s.tombs[id] {
		return true, nil
	}
	if err := s.appendTombLocked(id); err != nil {
		return true, err
	}
	s.wakeLocked()
	return true, nil
}

// TombstoneThread excises every envelope on a thread.
func (s *Store) TombstoneThread(threadID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, ok := s.threads[threadID]
	if !ok {
		return false, nil
	}
	for _, st := range list {
		if !s.tombs[st.Env.ID] {
			if err := s.appendTombLocked(st.Env.ID); err != nil {
				return true, err
			}
		}
	}
	s.wakeLocked()
	return true, nil
}

// Render projects an envelope for reads, honoring tombstones: the id and
// graph fields remain, the content is excised.
func (s *Store) Render(env *envelope.Envelope) envelope.Envelope {
	if !s.IsTombstoned(env.ID) {
		return env.Clone()
	}
	c := env.Clone()
	c.Text = ""
	c.Attachments = nil
	c.Meta = map[string]any{"tombstoned": true}
	return c
}

// LastSeq returns the current cursor high-water mark.
func (s *Store) LastSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeq
}

// EnforceRetention drops whole segments beyond the age window or size
// budget (never the active segment). Tombstones live in their own file and
// always survive. Cursors into dropped history rebase via reset (R5).
func (s *Store) EnforceRetention(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.segmentNames()
	if err != nil {
		return err
	}
	type segInfo struct {
		name string
		size int64
		mod  time.Time
	}
	var segs []segInfo
	var total int64
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(s.segDir(), n))
		if err != nil {
			continue
		}
		segs = append(segs, segInfo{n, fi.Size(), fi.ModTime()})
		total += fi.Size()
	}
	removed := false
	for _, si := range segs {
		if filepath.Join(s.segDir(), si.name) == s.segPath {
			continue // never drop the active segment
		}
		if now.Sub(si.mod) > s.opts.RetentionAge || total > s.opts.RetentionMaxBytes {
			if err := os.Remove(filepath.Join(s.segDir(), si.name)); err != nil {
				return err
			}
			total -= si.size
			removed = true
		}
	}
	if removed {
		// rebuild the in-memory index from the surviving segments.
		s.msgs = nil
		s.byID = map[string]*Stored{}
		s.threads = map[string][]*Stored{}
		names, _ := s.segmentNames()
		for _, n := range names {
			_ = s.loadSegment(filepath.Join(s.segDir(), n))
		}
		sort.Slice(s.msgs, func(i, j int) bool { return s.msgs[i].Seq < s.msgs[j].Seq })
		for _, list := range s.threads {
			sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
		}
		s.recomputeBounds()
		s.writeState()
	}
	return nil
}

// RotateNow force-rotates the active segment (used by retention tests and
// the periodic retention loop so old segments become droppable).
func (s *Store) RotateNow() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked()
}
