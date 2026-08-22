package alarm

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"
)

// Event kinds. Every state change the engine makes produces exactly one.
const (
	KindActive     = "active"
	KindRTN        = "rtn"
	KindAck        = "ack"
	KindShelve     = "shelve"
	KindUnshelve   = "unshelve"
	KindSuppress   = "suppress"
	KindUnsuppress = "unsuppress"
)

// Event is one append-only journal row.
//
// It is deliberately flat and all-strings-plus-a-timestamp: an alarm event
// has no numeric value to downsample, which is exactly why this is not
// hist.Sink — that interface is Insert(ts, map[string]float64), one float
// per tag, and the query it serves is a per-tag average over buckets, not
// "what happened at site RTU9 last Tuesday".
type Event struct {
	TS       int64    `json:"ts"` // epoch ms, from the engine's clock
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Kind     string   `json:"kind"`
	Priority Priority `json:"priority"`
	Site     string   `json:"site,omitempty"`
	State    string   `json:"state,omitempty"` // the state arrived at
	By       string   `json:"by,omitempty"`    // operator string, unauthenticated
}

// Time is the event's timestamp as a time.Time.
func (e Event) Time() time.Time { return time.UnixMilli(e.TS) }

// Filter narrows a journal query. Every slice is an OR within itself and an
// AND across fields; an empty slice means "any". Limit caps the result,
// newest first, and zero means DefaultQueryLimit.
type Filter struct {
	Sites      []string `json:"sites,omitempty"`
	Priorities []string `json:"priorities,omitempty"`
	IDs        []string `json:"ids,omitempty"`
	Kinds      []string `json:"kinds,omitempty"`
	Limit      int      `json:"limit,omitempty"`
}

// DefaultQueryLimit bounds a journal query that does not ask for a bound,
// so a flapping storm cannot turn one HTTP request into a hundred thousand
// rows.
const DefaultQueryLimit = 1000

func (f Filter) limit() int {
	if f.Limit <= 0 {
		return DefaultQueryLimit
	}
	return f.Limit
}

func (f Filter) match(e Event) bool {
	return inSet(f.Sites, e.Site) && inSet(f.IDs, e.ID) &&
		inSet(f.Kinds, e.Kind) && inSet(f.Priorities, e.Priority.String())
}

func inSet(set []string, v string) bool {
	if len(set) == 0 {
		return true
	}
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// Journal is the alarm history. Append is called from the scan goroutine
// and must not block on anything slow; Query is called from an HTTP
// handler.
type Journal interface {
	Append(Event) error
	Query(from, to time.Time, f Filter) ([]Event, error)
}

// RingJournal keeps the last n events in memory. It is always on, in front
// of any durable sink, so the journal view works on a box with no database
// at all — and it is bounded by construction, which is the answer to a
// flapping storm across a couple of thousand alarms.
type RingJournal struct {
	mu   sync.Mutex
	buf  []Event
	n    int // events written since construction
	size int
}

// NewRing builds a ring of at most n events. n <= 0 uses DefaultKeep.
func NewRing(n int) *RingJournal {
	if n <= 0 {
		n = DefaultKeep
	}
	return &RingJournal{buf: make([]Event, 0, n), size: n}
}

func (r *RingJournal) Append(e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < r.size {
		r.buf = append(r.buf, e)
	} else {
		r.buf[r.n%r.size] = e
	}
	r.n++
	return nil
}

// Len is how many events the ring currently holds.
func (r *RingJournal) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}

func (r *RingJournal) Query(from, to time.Time, f Filter) ([]Event, error) {
	r.mu.Lock()
	// Unroll the ring into append order: once it has wrapped, the oldest
	// entry is wherever the next write would land.
	events := make([]Event, 0, len(r.buf))
	if r.n <= r.size {
		events = append(events, r.buf...)
	} else {
		start := r.n % r.size
		events = append(events, r.buf[start:]...)
		events = append(events, r.buf[:start]...)
	}
	r.mu.Unlock()
	return selectEvents(events, from, to, f), nil
}

// selectEvents applies a range and a filter and returns newest first,
// capped. Shared by every in-process journal so the three implementations
// cannot drift on what "matches" means.
//
// Events are collected in reverse order and then STABLY sorted, so events
// sharing a timestamp come back in reverse order of appending. A whole scan
// resolves at one instant — an alarm can activate, be acked and return
// inside the same millisecond in virtual time — and "newest first" has to
// mean the last one appended, not whichever the sort happened to pick.
func selectEvents(events []Event, from, to time.Time, f Filter) []Event {
	lo, hi := bound(from, to)
	out := make([]Event, 0, min(len(events), f.limit()))
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.TS < lo || e.TS > hi || !f.match(e) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if len(out) > f.limit() {
		out = out[:f.limit()]
	}
	return out
}

// bound turns a zero from/to into "unbounded", so a caller can ask for
// everything without inventing sentinel dates.
func bound(from, to time.Time) (lo, hi int64) {
	lo, hi = int64(-1)<<62, int64(1)<<62
	if !from.IsZero() {
		lo = from.UnixMilli()
	}
	if !to.IsZero() {
		hi = to.UnixMilli()
	}
	return lo, hi
}

// FileJournal appends events as JSONL and rotates at a size cap, keeping
// one previous generation. Two files, not n, on purpose: this is the sink
// for a box with no database, where the point is that the last few days
// survive a restart — anything wanting real retention wants Postgres.
type FileJournal struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	size     int64
	maxBytes int64
}

// DefaultFileBytes is the rotation threshold NewFile uses.
const DefaultFileBytes = 8 << 20 // 8 MiB

// NewFile opens (creating it if needed) an append-only JSONL journal.
func NewFile(path string) (*FileJournal, error) { return NewFileSize(path, DefaultFileBytes) }

// NewFileSize is NewFile with an explicit rotation threshold; maxBytes <= 0
// uses DefaultFileBytes.
func NewFileSize(path string, maxBytes int64) (*FileJournal, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultFileBytes
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &FileJournal{path: path, f: f, size: st.Size(), maxBytes: maxBytes}, nil
}

func (j *FileJournal) Append(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.size+int64(len(b)) > j.maxBytes && j.size > 0 {
		if err := j.rotate(); err != nil {
			return err
		}
	}
	n, err := j.f.Write(b)
	j.size += int64(n)
	return err
}

// rotate moves the current file aside to <path>.1, replacing any previous
// generation, and starts a fresh one.
func (j *FileJournal) rotate() error {
	if err := j.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(j.path, j.path+".1"); err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	j.f, j.size = f, 0
	return nil
}

// Query reads the rotated generation and then the live one, so results
// span a rotation rather than silently losing everything before it.
func (j *FileJournal) Query(from, to time.Time, f Filter) ([]Event, error) {
	j.mu.Lock()
	path := j.path
	j.mu.Unlock()

	var events []Event
	for _, p := range []string{path + ".1", path} {
		got, err := readJSONL(p)
		if err != nil {
			return nil, err
		}
		events = append(events, got...)
	}
	return selectEvents(events, from, to, f), nil
}

func readJSONL(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			// A torn last line — the process died mid-write — must not make
			// the whole journal unreadable.
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	return out, nil
}

func (j *FileJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.f.Close()
}

// MultiJournal fans an append out to several journals — the intended shape
// is NewMulti(ring, durable), so the last few thousand events are always in
// memory and everything is also on disk or in Postgres.
//
// A query goes to the MOST durable journal that can answer it: the list is
// tried in reverse and the first one that does not error wins. That way a
// database outage degrades the journal page to "the last 5 000 events"
// instead of to an error.
type MultiJournal struct {
	js []Journal
}

func NewMulti(js ...Journal) *MultiJournal { return &MultiJournal{js: js} }

// Append writes to every journal and returns the first error, having still
// attempted all of them: a failing sink must not cost the ring its copy.
func (m *MultiJournal) Append(e Event) error {
	var first error
	for _, j := range m.js {
		if err := j.Append(e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *MultiJournal) Query(from, to time.Time, f Filter) ([]Event, error) {
	var last error
	for i := len(m.js) - 1; i >= 0; i-- {
		out, err := m.js[i].Query(from, to, f)
		if err == nil {
			return out, nil
		}
		last = err
	}
	if last == nil {
		return nil, fmt.Errorf("alarm: MultiJournal has no journals")
	}
	return nil, last
}
