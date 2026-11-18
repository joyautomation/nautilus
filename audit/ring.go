package audit

import (
	"sync"
	"time"
)

// Ring keeps the last n entries in memory: the recent view on a box with
// no file (the playground, an SDK program), and in front of a file.
type Ring struct {
	mu   sync.Mutex
	buf  []Entry
	next int
	full bool
	now  func() time.Time
}

// NewRing keeps at most n entries (default 1000 when n <= 0).
func NewRing(n int) *Ring {
	if n <= 0 {
		n = 1000
	}
	return &Ring{buf: make([]Entry, n), now: time.Now}
}

// Record keeps e, dropping the oldest when full. It never fails.
func (r *Ring) Record(e Entry) (Entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e = fill(e, r.now())
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	return e, nil
}

// Recent returns the kept entries, newest first.
func (r *Ring) Recent() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	out := make([]Entry, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, r.buf[(r.next-i+len(r.buf))%len(r.buf)])
	}
	return out
}

// Tee records to every recorder in order and returns the first's entry.
// It stops at the first error, so a file that could not be written is
// never reported as recorded.
type Tee []Recorder

func (t Tee) Record(e Entry) (Entry, error) {
	var first Entry
	for i, r := range t {
		got, err := r.Record(e)
		if err != nil {
			return got, err
		}
		if i == 0 {
			first = got
			e = got // the same ID and TS everywhere
		}
	}
	return first, nil
}
