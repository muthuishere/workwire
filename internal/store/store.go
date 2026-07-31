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

// record is one NDJSON line in a segment, the tombstone file or the thread
// checkpoint file.
type record struct {
	Type string             `json:"type"` // "msg" | "tomb" | "chk"
	Seq  int64              `json:"seq,omitempty"`
	Seqs map[string]int64   `json:"seqs,omitempty"` // per-recipient cursors (ADR-009)
	Env  *envelope.Envelope `json:"env,omitempty"`
	ID   string             `json:"id,omitempty"`
	// DeletedBy / DeletedAt record who excised the envelope and when, so the
	// deletion itself is accountable (ADR-008). Tombstone records only.
	DeletedBy string `json:"deletedBy,omitempty"`
	DeletedAt string `json:"deletedAt,omitempty"`
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
	tombInfo map[string]Tombstone
	lastSeq  int64
	minSeq   int64 // smallest retained seq; lastSeq+1 when nothing retained
	seg      *os.File
	segPath  string
	segSize  int64
	segIdx   int
	segStart time.Time // when the active segment received its first record
	tombFile *os.File
	lock     *dirLock
	notify   chan struct{}

	// chk is the retention-immune thread checkpoint (ADR-008 sidecar pattern,
	// same shape as tombstones.ndjson): the state-changing envelopes of every
	// thread, kept outside the segment set so derived thread state — initiator,
	// topic, dissents, closure — survives a segment being dropped.
	chkFile *os.File
	chk     map[string][]*Stored
	chkIDs  map[string]bool
}

// Tombstone is the accountability record of one excision.
type Tombstone struct {
	ID        string `json:"id"`
	DeletedBy string `json:"deletedBy,omitempty"`
	DeletedAt string `json:"deletedAt,omitempty"`
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
		dir:      dir,
		opts:     opts,
		byID:     map[string]*Stored{},
		threads:  map[string][]*Stored{},
		tombs:    map[string]bool{},
		tombInfo: map[string]Tombstone{},
		chk:      map[string][]*Stored{},
		chkIDs:   map[string]bool{},
		lock:     lock,
		notify:   make(chan struct{}),
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
	cf, err := os.OpenFile(filepath.Join(dir, "threads.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		lock.release()
		return nil, err
	}
	s.chkFile = cf
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
	if s.chkFile != nil {
		s.chkFile.Close()
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
	// the thread checkpoint replays before tombstones: it is a sidecar outside
	// the segment set, so it carries state-changing envelopes whose segment may
	// already have been dropped by retention.
	s.scanSidecar("threads.ndjson", func(r record) {
		if r.Type != "chk" || r.Env == nil || s.chkIDs[r.Env.ID] {
			return
		}
		st := &Stored{Seq: r.Seq, Seqs: r.Seqs, Env: r.Env}
		s.chkIDs[r.Env.ID] = true
		s.chk[r.Env.ThreadID] = append(s.chk[r.Env.ThreadID], st)
	})
	// tombstones replay last: they must survive rotation and restart.
	s.scanSidecar("tombstones.ndjson", func(r record) {
		if r.Type != "tomb" || r.ID == "" {
			return
		}
		s.tombs[r.ID] = true
		s.tombInfo[r.ID] = Tombstone{ID: r.ID, DeletedBy: r.DeletedBy, DeletedAt: r.DeletedAt}
	})
	for _, list := range s.chk {
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
	}
	sort.Slice(s.msgs, func(i, j int) bool { return s.msgs[i].Seq < s.msgs[j].Seq })
	for _, list := range s.threads {
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
	}
	s.recomputeBounds()
	return nil
}

// scanSidecar replays one NDJSON sidecar file (tombstones or the thread
// checkpoint), ignoring a missing file and torn tail lines.
func (s *Store) scanSidecar(name string, fn func(record)) {
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var r record
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			fn(r)
		}
	}
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
	// An empty segment has no age yet; a resumed one is as old as its last
	// write, which is the best available lower bound on its first write.
	if fi.Size() > 0 {
		s.segStart = fi.ModTime()
	} else {
		s.segStart = time.Time{}
	}
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
	if s.segSize == 0 {
		s.segStart = time.Now()
	}
	s.segSize += int64(len(b))
	st := &Stored{Seq: seq, Seqs: seqs, Env: env}
	// Checkpoint BEFORE rotating: whether this envelope changes derived thread
	// state depends on the thread being empty, which the append below changes.
	if s.isCheckpointableLocked(env) {
		if err := s.appendCheckpointLocked(st); err != nil {
			return 0, err
		}
	}
	if s.segSize >= s.opts.SegmentMaxBytes {
		if err := s.rotateLocked(); err != nil {
			return 0, err
		}
	}
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

// isCheckpointableLocked reports whether an envelope carries derived thread
// state that must outlive retention: the thread's head (initiator + topic) and
// every kind that moves dissent or closure (ADR-011).
func (s *Store) isCheckpointableLocked(env *envelope.Envelope) bool {
	switch env.Kind {
	case "dissent", "withdraw", "resolved", "reopen":
		return true
	}
	return len(s.threads[env.ThreadID]) == 0 && len(s.chk[env.ThreadID]) == 0
}

func (s *Store) appendCheckpointLocked(st *Stored) error {
	if s.chkIDs[st.Env.ID] {
		return nil
	}
	b, err := json.Marshal(record{Type: "chk", Seq: st.Seq, Seqs: st.Seqs, Env: st.Env})
	if err != nil {
		return err
	}
	if _, err := s.chkFile.Write(append(b, '\n')); err != nil {
		return err
	}
	s.chkIDs[st.Env.ID] = true
	s.chk[st.Env.ThreadID] = append(s.chk[st.Env.ThreadID], st)
	return nil
}

// threadEnvelopesLocked merges the retention-immune checkpoint with the
// retained segments for one thread, deduped by envelope id and ordered by
// sequence. truncated reports that at least one checkpointed envelope is no
// longer in retained history, and earliestRetained is the timestamp of the
// oldest envelope that still is.
func (s *Store) threadEnvelopesLocked(id string) (list []*Stored, truncated bool, earliestRetained string) {
	retained := s.threads[id]
	chk := s.chk[id]
	if len(retained) == 0 && len(chk) == 0 {
		return nil, false, ""
	}
	if len(retained) > 0 {
		earliestRetained = retained[0].Env.TS
	}
	seen := make(map[string]bool, len(retained)+len(chk))
	for _, st := range retained {
		seen[st.Env.ID] = true
	}
	list = append(list, retained...)
	for _, st := range chk {
		if seen[st.Env.ID] {
			continue
		}
		truncated = true
		list = append(list, st)
	}
	if truncated {
		list = append([]*Stored(nil), list...)
		sort.Slice(list, func(i, j int) bool { return list[i].Seq < list[j].Seq })
	}
	return list, truncated, earliestRetained
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
	// Truncated is true when retention has dropped part of this thread's
	// history, so `count` and the rendered messages are incomplete. Initiator,
	// dissents and closure are still exact — they come from the retention-immune
	// thread checkpoint (ADR-008). EarliestRetained is the timestamp of the
	// oldest envelope still readable via `GET /threads/<id>`.
	Truncated        bool   `json:"truncated,omitempty"`
	EarliestRetained string `json:"earliest_retained,omitempty"`
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
	list, truncated, earliest := s.threadEnvelopesLocked(id)
	if list == nil {
		return ThreadState{}, false
	}
	// R13: excision applies to ALL reads, and derived thread state is a read.
	text := func(e *envelope.Envelope) string {
		if s.tombs[e.ID] {
			return ""
		}
		return e.Text
	}
	ts := ThreadState{ThreadID: id, State: "open", Truncated: truncated}
	if truncated {
		ts.EarliestRetained = earliest
	}
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
		ts.Topic = text(list[0].Env)
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
					Peer: e.From, Kind: roleOf(e), Text: text(e), TS: e.TS, Origin: originOf(e),
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
	// While the thread is open, its open dissents are what block a close.
	// Once resolved, the record of what was overridden is ClosedOver — the
	// objections are held (a human reopen restores them), not advertised as
	// still blocking something that is already decided.
	if !ts.Resolved {
		ts.Dissents = collect()
	}
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

// Threads lists every retained, non-deleted thread, newest activity first
// (hub-core R22): a thread whose every envelope has been tombstoned is gone
// from discovery, not served as an empty husk.
func (s *Store) Threads(cap int) []ThreadState {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make(map[string]bool, len(s.threads))
	for id := range s.threads {
		ids[id] = true
	}
	for id := range s.chk {
		ids[id] = true
	}
	out := make([]ThreadState, 0, len(ids))
	for id := range ids {
		if s.threadDeletedLocked(id) {
			continue
		}
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

// Tombstone returns the excision record for an id, when there is one.
func (s *Store) Tombstone(id string) (Tombstone, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tombInfo[id]
	return t, ok
}

// threadDeletedLocked reports whether every envelope of a thread is excised.
func (s *Store) threadDeletedLocked(id string) bool {
	list, _, _ := s.threadEnvelopesLocked(id)
	if len(list) == 0 {
		return false
	}
	for _, st := range list {
		if !s.tombs[st.Env.ID] {
			return false
		}
	}
	return true
}

func (s *Store) appendTombLocked(id, by string) error {
	t := Tombstone{ID: id, DeletedBy: by, DeletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	b, _ := json.Marshal(record{Type: "tomb", ID: id, DeletedBy: t.DeletedBy, DeletedAt: t.DeletedAt})
	if _, err := s.tombFile.Write(append(b, '\n')); err != nil {
		return err
	}
	s.tombs[id] = true
	s.tombInfo[id] = t
	return nil
}

// TombstoneMessage excises one envelope's content, recording who deleted it.
// Returns false when the id is unknown; repeating is idempotent.
func (s *Store) TombstoneMessage(id, by string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.byID[id]
	if !known && !s.tombs[id] && !s.chkIDs[id] {
		return false, nil
	}
	if s.tombs[id] {
		return true, nil
	}
	if err := s.appendTombLocked(id, by); err != nil {
		return true, err
	}
	s.wakeLocked()
	return true, nil
}

// TombstoneThread excises every envelope on a thread, checkpointed ones
// included — a dropped segment must not leave a dissent readable.
func (s *Store) TombstoneThread(threadID, by string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, _, _ := s.threadEnvelopesLocked(threadID)
	if len(list) == 0 {
		return false, nil
	}
	for _, st := range list {
		if !s.tombs[st.Env.ID] {
			if err := s.appendTombLocked(st.Env.ID, by); err != nil {
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

// RotateNow force-rotates the active segment so its records become droppable
// (EnforceRetention never drops the active segment). Called by Maintain and
// by tests.
func (s *Store) RotateNow() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked()
}

// rotateInterval is how long the active segment may keep accepting records
// before Maintain closes it. A quarter of the retention window bounds how far
// past the window a record can outlive it, at four segments per window.
func (s *Store) rotateInterval() time.Duration {
	d := s.opts.RetentionAge / 4
	if d <= 0 {
		d = time.Hour
	}
	return d
}

// Maintain is the periodic retention pass. It rotates the active segment once
// it is old enough, THEN drops what has expired: without the rotation an
// age-based policy could never fire, because rotation otherwise happens only
// on size and EnforceRetention never drops the active segment (hub-core R4).
func (s *Store) Maintain(now time.Time) error {
	s.mu.Lock()
	aged := s.segSize > 0 && !s.segStart.IsZero() && now.Sub(s.segStart) >= s.rotateInterval()
	var err error
	if aged {
		err = s.rotateLocked()
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.EnforceRetention(now)
}
