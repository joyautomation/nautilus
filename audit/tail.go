package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// A copy of the journal kept elsewhere (the Postgres sink, #709 PR 9) is
// fed by a Tailer: it reads the journal's files in write order and hands
// their complete lines to a CopyStore, which keeps them and remembers the
// last one. The files are the source of truth; the copy catches up from
// them after any outage. The copy's position is the last line it holds
// (its file, the byte offset after it, its Seq and hash), never an entry's
// ID: a ULID is wall-clock and random within a millisecond, so ordering by
// it would skip entries (docs/design/identity-audit.md §4).
//
// Everything that decides what is copied lives here, beside the chain it
// checks; the store only keeps rows.

// Line is one journal line as the copy keeps it.
type Line struct {
	File   string // the journal file's base name
	Offset int64  // byte offset just past the line's newline in File: where the next line starts
	Seq    uint64
	Hash   string // the chain hash of Raw: the next line's Prev
	Prev   string
	Raw    []byte // the line's bytes, without the newline
	Entry  Entry
}

// Gap is entries the copy will never hold: the journal pruned the files
// they were in before the copy read them (it was unreachable for longer
// than audit.keep-days). After is the last line the copy holds; Next is
// the first it holds again.
type Gap struct {
	AfterSeq, NextSeq uint64
	AfterFile         string // the pruned file the copy had reached
}

// CopyBatch is what a store applies at once: all of it or none.
type CopyBatch struct {
	Gap   *Gap // recorded with the batch whose first line follows it
	Lines []Line
}

// CopyStore keeps a copy of the journal.
type CopyStore interface {
	// Last is the newest line the copy holds (File, Offset, Seq and Hash
	// set), ok false when it holds none.
	Last(ctx context.Context) (last Line, ok bool, err error)
	// Apply adds the batch's lines in order, with its gap, atomically. A
	// line the copy already holds with the same Seq and hash is not an
	// error; the same Seq with another hash is a *CopyMismatch.
	Apply(ctx context.Context, b CopyBatch) error
}

// CopyMismatch is the journal and its copy disagreeing: a line the copy
// holds was changed or removed in the files, or the files no longer chain
// on from it. The tailer stops on it and never writes over the copy: the
// copy is the evidence. Anything else a store returns is an outage, and
// the tailer tries again.
type CopyMismatch struct {
	File   string
	Offset int64
	Reason string
}

func (m *CopyMismatch) Error() string {
	return fmt.Sprintf("audit copy stopped: %s at %s offset %d: the journal no longer matches its copy (run naut audit verify)", m.Reason, m.File, m.Offset)
}

// Copy states.
const (
	CopyStarting = "starting" // not yet reached the store
	CopyCurrent  = "current"  // holds every durable line
	CopyBehind   = "behind"   // the store is unreachable or refusing; it catches up when it is back
	CopyStopped  = "stopped"  // a mismatch: stopped until an operator looks
)

// CopyStatus is a Tailer's state for the drivers block.
type CopyStatus struct {
	State     string
	Copied    uint64 // Seq of the newest line the copy holds
	Head      uint64 // Seq of the journal's newest durable line
	Gaps      int    // gaps recorded since start
	LastError string
	Since     time.Time // when State began
}

// Tailer copies a journal directory into a CopyStore. Step and Run are
// not safe for concurrent use with each other; Status is.
type Tailer struct {
	dir   string
	store CopyStore
	// head is the journal's newest durable line: the tailer never copies
	// past it, so a line whose fsync failed (and which the journal cuts
	// back) is never copied. FileJournal.Head moves only after the sync.
	head  func() (string, uint64)
	batch int
	// maxRead caps the lines read from a file before they are applied.
	maxRead int
	now     func() time.Time
	// afterRead, when set (tests), runs after a file is read to its end
	// and before the directory is listed for the next one.
	afterRead func()

	// OnChange, when set before Run, is called with the new status each
	// time the state changes (to log it).
	OnChange func(CopyStatus)

	resumed bool
	have    bool   // the copy holds a line: pos is it
	pos     Line   // the copy's newest line (File, Offset, Seq, Hash)
	file    string // the file being read
	off     int64  // where reading resumes in it
	// pruned is set when the file the copy had reached was pruned before
	// the rest of it was read: the next line may follow a gap.
	pruned bool
	halt   error
	// suspect is where the last read found a mismatch (file:offset): it
	// stops the copy only when found there again (confirm).
	suspect string

	mu sync.Mutex
	st CopyStatus
}

// NewTailer tails the journal in dir into store, never past head.
func NewTailer(dir string, store CopyStore, head func() (string, uint64)) *Tailer {
	return &Tailer{dir: dir, store: store, head: head, batch: 256, maxRead: 4096, now: time.Now,
		st: CopyStatus{State: CopyStarting, Since: time.Now()}}
}

// Status is the copy's state now.
func (t *Tailer) Status() CopyStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

func (t *Tailer) setStatus(f func(*CopyStatus)) {
	t.mu.Lock()
	was := t.st.State
	f(&t.st)
	changed := t.st.State != was
	if changed {
		t.st.Since = t.now()
	}
	st := t.st
	t.mu.Unlock()
	if changed && t.OnChange != nil {
		t.OnChange(st)
	}
}

// Run steps every interval until ctx ends, backing off while the store is
// unreachable (up to 30 s), and returns early on a mismatch.
func (t *Tailer) Run(ctx context.Context, every time.Duration) {
	wait := every
	for {
		_, err := t.Step(ctx)
		var mm *CopyMismatch
		switch {
		case errors.As(err, &mm):
			return
		case err != nil && ctx.Err() == nil:
			wait = min(wait*2, 30*time.Second)
		default:
			wait = every
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Step copies every durable line not yet copied and returns how many it
// copied. A *CopyMismatch stops the tailer for good: every later Step
// returns it again and copies nothing.
func (t *Tailer) Step(ctx context.Context) (int, error) {
	if t.halt != nil {
		return 0, t.halt
	}
	n, err := t.step(ctx)
	_, headSeq := t.head()
	var mm *CopyMismatch
	if err != nil && ctx.Err() != nil && !errors.As(err, &mm) {
		return n, err // stopping: not an outage
	}
	switch {
	case errors.As(err, &mm):
		t.halt = err
		t.setStatus(func(s *CopyStatus) { s.State, s.LastError, s.Head = CopyStopped, err.Error(), headSeq })
	case err != nil:
		t.setStatus(func(s *CopyStatus) {
			s.State, s.LastError, s.Head = CopyBehind, err.Error(), headSeq
			if s.Copied < t.pos.Seq {
				s.Copied = t.pos.Seq
			}
		})
	default:
		t.setStatus(func(s *CopyStatus) {
			s.Head, s.Copied, s.LastError = headSeq, t.pos.Seq, ""
			if t.pos.Seq >= headSeq {
				s.State = CopyCurrent
			} else {
				s.State = CopyBehind
			}
		})
	}
	return n, err
}

func (t *Tailer) step(ctx context.Context) (int, error) {
	if !t.resumed {
		if err := t.resume(ctx); err != nil {
			return 0, err
		}
	}
	_, headSeq := t.head()
	copied := 0
	for {
		if t.file == "" {
			files, err := listFiles(t.dir)
			if err != nil {
				return copied, err
			}
			if len(files) == 0 {
				return copied, nil
			}
			t.file, t.off = files[0], 0
		}
		_, n, err := t.drain(ctx, headSeq)
		copied += n
		if err != nil {
			return copied, err
		}
		if t.afterRead != nil {
			t.afterRead()
		}
		// A later file means t.file was rotated. Its lines were all
		// durable before the later file was made, so with the head read
		// again it is read to its end (a line may have landed since the
		// first read) and must end there: a partial line, or a line past
		// the head, left in a rotated file is damage, not a write in
		// progress.
		next, gone, err := t.nextFile()
		if err != nil || next == "" {
			return copied, err
		}
		if !gone {
			_, headSeq = t.head()
			atEnd, n, err := t.drain(ctx, headSeq)
			copied += n
			if err != nil {
				return copied, err
			}
			if !atEnd {
				return copied, &CopyMismatch{File: t.file, Offset: t.off,
					Reason: "the file ends in a partial line or a line past the journal's head, but a later file follows it"}
			}
		} else {
			// Pruned while it was being read (the copy was behind):
			// whatever was left in it is gone; the next line may follow
			// a gap.
			t.pruned = true
		}
		t.file, t.off = next, 0
	}
}

// drain copies t.file's durable lines from t.off, a bounded piece at a
// time, and reports whether it reached the file's end. The good lines
// read before a bad one are copied before it stops: they are the
// evidence the copy exists to keep.
func (t *Tailer) drain(ctx context.Context, headSeq uint64) (atEnd bool, n int, err error) {
	for {
		lines, atEnd, full, rerr := t.read(headSeq)
		k, err := t.apply(ctx, lines)
		n += k
		if err != nil {
			return false, n, err
		}
		if rerr != nil {
			return false, n, t.confirm(rerr)
		}
		if !full {
			return atEnd, n, nil
		}
	}
}

// confirm lets a mismatch found while reading stop the copy only when the
// next Step finds it again at the same place. A read can race the journal
// cutting back a failed write (the cut-back rewrites the file's tail while
// it is being read), and that must not read as damage.
func (t *Tailer) confirm(err error) error {
	var mm *CopyMismatch
	if !errors.As(err, &mm) {
		return err
	}
	at := fmt.Sprintf("%s:%d", mm.File, mm.Offset)
	if t.suspect == at {
		return err
	}
	t.suspect = at
	return fmt.Errorf("audit copy: rechecking %s offset %d before stopping: %s", mm.File, mm.Offset, mm.Reason)
}

// resume finds where the copy left off and checks that the journal still
// holds that line, byte for byte.
func (t *Tailer) resume(ctx context.Context) error {
	last, ok, err := t.store.Last(ctx)
	if err != nil {
		return err
	}
	t.resumed = true
	if !ok {
		return nil
	}
	t.have, t.pos = true, last
	if filepath.Base(last.File) != last.File || !IsJournalFile(last.File) {
		return &CopyMismatch{File: last.File, Offset: last.Offset,
			Reason: "the copy's newest row names a file that is not one of the journal's"}
	}
	data, err := os.ReadFile(filepath.Join(t.dir, last.File))
	if errors.Is(err, os.ErrNotExist) {
		files, err := listFiles(t.dir)
		if err != nil {
			t.resumed = false
			return err
		}
		for _, f := range files {
			if fileKey(f) > fileKey(last.File) {
				// The copy's file was pruned while it was down: go on
				// from the next one; its first line may follow a gap.
				t.file, t.off, t.pruned = f, 0, true
				return nil
			}
		}
		return &CopyMismatch{File: last.File, Offset: last.Offset,
			Reason: "the file holding the copy's newest line is gone and no later file exists"}
	}
	if err != nil {
		t.resumed = false
		return err
	}
	if last.Offset < 1 || int64(len(data)) < last.Offset || data[last.Offset-1] != '\n' {
		return &CopyMismatch{File: last.File, Offset: last.Offset,
			Reason: fmt.Sprintf("the file is %d bytes and has no line ending where the copy's newest line (seq %d) ended", len(data), last.Seq)}
	}
	line := data[bytes.LastIndexByte(data[:last.Offset-1], '\n')+1 : last.Offset-1]
	if h := lineHash(line); h != last.Hash {
		return &CopyMismatch{File: last.File, Offset: last.Offset,
			Reason: fmt.Sprintf("the line the copy holds as seq %d was changed (copy %s, file %s)", last.Seq, last.Hash, h)}
	}
	t.file, t.off = last.File, last.Offset
	return nil
}

// read returns up to t.maxRead of t.file's complete lines from t.off
// that are durable (Seq at most headSeq), checked against the chain, one
// line at a time, so a long backlog never sits in memory whole. full
// reports that it stopped at the cap; atEnd that it reached the file's
// end: every complete line was durable, and no partial line follows.
func (t *Tailer) read(headSeq uint64) (lines []Line, atEnd, full bool, err error) {
	f, err := os.Open(filepath.Join(t.dir, t.file))
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, false, nil // pruned: nextFile reports it gone
	}
	if err != nil {
		return nil, false, false, err
	}
	defer f.Close()
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return nil, false, false, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	prev := t.pos
	have := t.have
	pruned := t.pruned
	off := t.off
	for len(lines) < t.maxRead {
		raw, err := r.ReadBytes('\n')
		if err == io.EOF {
			return lines, len(raw) == 0, false, nil // a partial line waits
		}
		if err != nil {
			return lines, false, false, err
		}
		raw = raw[:len(raw)-1]
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil || e.Seq == 0 {
			return lines, false, false, &CopyMismatch{File: t.file, Offset: off, Reason: "a line that is not an entry"}
		}
		if e.Seq > headSeq {
			return lines, false, false, nil // not durable yet
		}
		// Each line chains on from the one before it. The one exception
		// is the first line after a pruned file, which may follow a gap:
		// a later seq, chained to a line the copy never read.
		if have && (e.Seq != prev.Seq+1 || e.Prev != prev.Hash) && !(pruned && e.Seq > prev.Seq+1) {
			return lines, false, false, &CopyMismatch{File: t.file, Offset: off,
				Reason: fmt.Sprintf("seq %d (prev %s) does not chain on from the copy's seq %d (%s)", e.Seq, e.Prev, prev.Seq, prev.Hash)}
		}
		off += int64(len(raw)) + 1
		l := Line{File: t.file, Offset: off, Seq: e.Seq, Hash: lineHash(raw), Prev: e.Prev, Raw: raw, Entry: e}
		lines = append(lines, l)
		prev, have, pruned = l, true, false
	}
	return lines, false, true, nil
}

// apply hands lines to the store in batches and advances past each batch
// it accepted.
func (t *Tailer) apply(ctx context.Context, lines []Line) (int, error) {
	done := 0
	for len(lines) > 0 {
		n := min(len(lines), t.batch)
		b := CopyBatch{Lines: lines[:n]}
		if t.have && t.pruned && b.Lines[0].Seq != t.pos.Seq+1 {
			b.Gap = &Gap{AfterSeq: t.pos.Seq, NextSeq: b.Lines[0].Seq, AfterFile: t.pos.File}
		}
		if err := t.store.Apply(ctx, b); err != nil {
			return done, err
		}
		last := b.Lines[n-1]
		t.have, t.pos, t.pruned = true, last, false
		t.file, t.off = last.File, last.Offset
		if b.Gap != nil {
			t.setStatus(func(s *CopyStatus) { s.Gaps++ })
		}
		done += n
		lines = lines[n:]
	}
	return done, nil
}

// nextFile is the first journal file after t.file, and whether t.file
// itself is gone (pruned).
func (t *Tailer) nextFile() (next string, gone bool, err error) {
	files, err := listFiles(t.dir)
	if err != nil {
		return "", false, err
	}
	gone = true
	for _, f := range files {
		if f == t.file {
			gone = false
		}
		if next == "" && fileKey(f) > fileKey(t.file) {
			next = f
		}
	}
	return next, gone, nil
}

// LineHash is the chain hash of one line's bytes (without its newline):
// what the next line's Prev holds, and what a copy or an anchor keeps.
func LineHash(line []byte) string { return lineHash(line) }
