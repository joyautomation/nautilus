package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is a CopyStore in memory, keyed by Seq as the Postgres one is.
type memStore struct {
	mu    sync.Mutex
	rows  []Line
	gaps  []Gap
	fail  error // the next Apply fails with it, then clears
	calls int
}

func (m *memStore) Last(context.Context) (Line, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.rows) == 0 {
		return Line{}, false, nil
	}
	l := m.rows[len(m.rows)-1]
	return Line{File: l.File, Offset: l.Offset, Seq: l.Seq, Hash: l.Hash}, true, nil
}

func (m *memStore) Apply(_ context.Context, b CopyBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.fail != nil {
		err := m.fail
		m.fail = nil
		return err
	}
	for _, l := range b.Lines {
		for _, r := range m.rows {
			if r.Seq == l.Seq {
				if r.Hash != l.Hash {
					return &CopyMismatch{File: l.File, Offset: l.Offset, Reason: "the copy holds another line as this seq"}
				}
				return fmt.Errorf("test store: seq %d applied twice", l.Seq)
			}
		}
	}
	if b.Gap != nil {
		m.gaps = append(m.gaps, *b.Gap)
	}
	m.rows = append(m.rows, b.Lines...)
	return nil
}

func (m *memStore) seqs() []uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []uint64
	for _, r := range m.rows {
		out = append(out, r.Seq)
	}
	return out
}

func unbounded() (string, uint64) { return "", math.MaxUint64 }

func step(t *testing.T, tl *Tailer) int {
	t.Helper()
	n, err := tl.Step(context.Background())
	if err != nil {
		t.Fatalf("Step: %v", err)
	}
	return n
}

func wantSeqs(t *testing.T, m *memStore, want ...uint64) {
	t.Helper()
	if got := fmt.Sprint(m.seqs()); got != fmt.Sprint(want) {
		t.Fatalf("copy holds seqs %s, want %v", got, want)
	}
}

// stepToStop steps a tailer that meets a mismatch while reading: the
// first Step only notes it (a read can race a cut-back), the second stops.
func stepToStop(t *testing.T, tl *Tailer) error {
	t.Helper()
	_, err := tl.Step(context.Background())
	var mm *CopyMismatch
	if err == nil || errors.As(err, &mm) || !strings.Contains(err.Error(), "rechecking") {
		t.Fatalf("first Step: %v, want a recheck", err)
	}
	if st := tl.Status(); st.State != CopyBehind {
		t.Fatalf("after the first Step: %+v, want behind", st)
	}
	_, err = tl.Step(context.Background())
	return err
}

func span(a, b uint64) []uint64 {
	var out []uint64
	for i := a; i <= b; i++ {
		out = append(out, i)
	}
	return out
}

// The copy holds every line, byte for byte, in write order, across size
// and day rotation; a second Step finds nothing new.
func TestTailCopiesEveryLineAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{MaxSize: 300})
	for i := 0; i < 4; i++ {
		write(t, j, "tag.write", "A")
	}
	c.t = c.t.Add(24 * time.Hour)
	write(t, j, "tag.write", "NextDay")
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	if n := step(t, tl); n != 5 {
		t.Fatalf("copied %d, want 5", n)
	}
	wantSeqs(t, m, span(1, 5)...)
	for i, l := range lines(t, dir) {
		if string(m.rows[i].Raw) != l || m.rows[i].Hash != lineHash([]byte(l)) {
			t.Errorf("row %d is not the file's line", i)
		}
	}
	if n := step(t, tl); n != 0 {
		t.Errorf("a second Step copied %d", n)
	}
	if st := tl.Status(); st.State != CopyCurrent || st.Copied != 5 || st.Head != 5 {
		t.Errorf("status %+v", st)
	}
}

// The copy never runs ahead of the journal's durable head.
func TestTailNeverCopiesPastTheDurableHead(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	for i := 0; i < 5; i++ {
		write(t, j, "tag.write", "A")
	}
	head := uint64(3)
	m := &memStore{}
	tl := NewTailer(dir, m, func() (string, uint64) { return "", head })
	step(t, tl)
	wantSeqs(t, m, 1, 2, 3)
	if st := tl.Status(); st.State != CopyCurrent {
		t.Errorf("status %+v, want current (it holds every durable line)", st)
	}
	head = 5
	step(t, tl)
	wantSeqs(t, m, span(1, 5)...)
}

// A line whose fsync fails is on disk until the journal cuts it back, and
// the next entry takes its seq. The tailer, bounded by the durable head,
// never copies it, so the next entry is not a mismatch.
func TestTailNeverCopiesALineWhoseSyncFails(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	// FileJournal.Head waits on the lock Record holds across the sync, so
	// the tailer inside the sync reads the head as it stood before.
	head := uint64(1)
	tl := NewTailer(dir, m, func() (string, uint64) { return "", head })
	step(t, tl)
	inside := -1
	j.sync = func(f *os.File) error {
		j.sync = (*os.File).Sync
		inside = step(t, tl) // line B is written and not yet synced
		return errors.New("EIO")
	}
	if _, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "B", Result: ResultOK}); err == nil {
		t.Fatal("Record succeeded after a failed sync")
	}
	if inside != 0 {
		t.Fatalf("the tailer copied %d lines while B was unsynced", inside)
	}
	write(t, j, "tag.write", "C")
	_, head = j.Head()
	step(t, tl)
	wantSeqs(t, m, 1, 2)
	if !strings.Contains(string(m.rows[1].Raw), `"target":"C"`) {
		t.Errorf("seq 2 in the copy is %s, want C", m.rows[1].Raw)
	}
}

// A restarted tailer goes on from the copy's newest line: nothing twice,
// nothing skipped.
func TestTailResumesWhereTheCopyLeftOff(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{MaxSize: 300})
	write(t, j, "tag.write", "A")
	write(t, j, "tag.write", "B")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	write(t, j, "tag.write", "C") // a new file (size rotation)
	c.t = c.t.Add(24 * time.Hour)
	write(t, j, "tag.write", "D")
	step(t, NewTailer(dir, m, j.Head))
	wantSeqs(t, m, 1, 2, 3, 4)
}

// The journal's line that the copy holds as its newest was changed: the
// tailer stops, and stays stopped, and the copy is not touched.
func TestTailStopsWhenACopiedLineWasChanged(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	write(t, j, "tag.write", "B")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	j.Close()
	path := filepath.Join(dir, "audit-20261118.jsonl")
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(data), `"target":"B"`, `"target":"X"`, 1)), 0o600)

	tl := NewTailer(dir, m, unbounded)
	_, err := tl.Step(context.Background())
	var mm *CopyMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Reason, "changed") {
		t.Fatalf("Step: %v, want a mismatch on the changed line", err)
	}
	if st := tl.Status(); st.State != CopyStopped || st.LastError == "" {
		t.Errorf("status %+v, want stopped with the reason", st)
	}
	calls := m.calls
	if _, err2 := tl.Step(context.Background()); err2 != err {
		t.Errorf("a stopped tailer stepped again: %v", err2)
	}
	if m.calls != calls {
		t.Error("a stopped tailer wrote to the copy")
	}
	wantSeqs(t, m, 1, 2)
}

// Lines the copy holds were cut off the end: the tailer stops.
func TestTailStopsWhenTheJournalWasCutShort(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	write(t, j, "tag.write", "B")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	j.Close()
	path := filepath.Join(dir, "audit-20261118.jsonl")
	ls := lines(t, dir)
	os.WriteFile(path, []byte(ls[0]+"\n"), 0o600)

	_, err := NewTailer(dir, m, unbounded).Step(context.Background())
	var mm *CopyMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Reason, "no line ending where the copy's newest line (seq 2) ended") {
		t.Fatalf("Step: %v, want a mismatch saying the file is too short", err)
	}
}

// The copy's newest file is gone and nothing follows it: the tailer
// stops rather than starting the copy over.
func TestTailStopsWhenTheJournalIsGone(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	j.Close()
	os.Remove(filepath.Join(dir, "audit-20261118.jsonl"))
	_, err := NewTailer(dir, m, unbounded).Step(context.Background())
	var mm *CopyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("Step: %v, want a mismatch", err)
	}
}

// A line after the copy's newest one that does not chain on from it (one
// was removed before the copy read it): the tailer stops.
func TestTailStopsOnABrokenChainAfterTheCopy(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	write(t, j, "tag.write", "B")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	write(t, j, "tag.write", "C")
	write(t, j, "tag.write", "D")
	j.Close()
	ls := lines(t, dir)
	os.WriteFile(filepath.Join(dir, "audit-20261118.jsonl"), []byte(ls[0]+"\n"+ls[1]+"\n"+ls[3]+"\n"), 0o600)

	tl := NewTailer(dir, m, unbounded)
	err := stepToStop(t, tl)
	var mm *CopyMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Reason, "does not chain") {
		t.Fatalf("Step: %v, want a chain mismatch", err)
	}
	wantSeqs(t, m, 1, 2)
}

// A line chained to the copy's newest line but numbered past it is a
// mismatch too: seq gaps are only allowed across a pruned file.
func TestTailStopsOnASeqJumpWithinTheJournal(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	j.Close()
	ls := lines(t, dir)
	e := Entry{Seq: 3, Prev: lineHash([]byte(ls[0])), User: "user:op1", Source: SourceHMI, Action: "tag.write", Result: ResultOK, TS: t0()}
	b, _ := json.Marshal(e)
	f, _ := os.OpenFile(filepath.Join(dir, "audit-20261118.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.Write(append(b, '\n'))
	f.Close()
	err := stepToStop(t, NewTailer(dir, m, unbounded))
	var mm *CopyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("Step: %v, want a mismatch", err)
	}
}

// The journal pruned files the copy had not read (it was down longer
// than keep-days): the copy records the gap and goes on.
func TestTailRecordsAGapWhenUnreadFilesWerePruned(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{Keep: 24 * time.Hour})
	write(t, j, "tag.write", "Day1") // seq 1
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	for d := 0; d < 3; d++ {
		c.t = c.t.Add(24 * time.Hour)
		write(t, j, "tag.write", "Day") // seqs 2, 3, 4
	}
	if removed, err := j.Prune(); err != nil || len(removed) != 2 {
		t.Fatalf("prune: %v %v", removed, err) // day 1 and day 2: seq 2 never copied
	}
	tl := NewTailer(dir, m, j.Head)
	step(t, tl)
	wantSeqs(t, m, 1, 3, 4, 5, 6) // 5 and 6 are the two audit.prune entries
	if len(m.gaps) != 1 || m.gaps[0] != (Gap{AfterSeq: 1, NextSeq: 3, AfterFile: "audit-20261118.jsonl"}) {
		t.Errorf("gaps %+v, want one after seq 1, before seq 3", m.gaps)
	}
	if st := tl.Status(); st.Gaps != 1 || st.State != CopyCurrent {
		t.Errorf("status %+v", st)
	}
}

// Pruning only files the copy already holds is no gap.
func TestTailPruningCopiedFilesIsNoGap(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{Keep: 48 * time.Hour})
	m := &memStore{}
	for d := 0; d < 3; d++ {
		write(t, j, "tag.write", "Day")
		c.t = c.t.Add(24 * time.Hour)
	}
	step(t, NewTailer(dir, m, j.Head))
	if _, err := j.Prune(); err != nil {
		t.Fatal(err)
	}
	write(t, j, "tag.write", "Today")
	step(t, NewTailer(dir, m, j.Head))
	if len(m.gaps) != 0 {
		t.Errorf("gaps %+v, want none", m.gaps)
	}
	wantSeqs(t, m, span(1, 5)...)
}

// A pruned file whose lines were all copied, followed by a line that does
// not chain from the copy's newest line, is still a mismatch: a prune is
// no excuse for a broken chain at the same seq.
func TestTailAPruneDoesNotExcuseABrokenChain(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "Day1") // seq 1
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	j.Close()
	os.Remove(filepath.Join(dir, "audit-20261118.jsonl"))
	e := Entry{Seq: 2, Prev: "sha256:forged", User: "user:op1", Source: SourceHMI, Action: "tag.write", Result: ResultOK, TS: t0()}
	b, _ := json.Marshal(e)
	os.WriteFile(filepath.Join(dir, "audit-20261119.jsonl"), append(b, '\n'), 0o600)
	err := stepToStop(t, NewTailer(dir, m, unbounded))
	var mm *CopyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("Step: %v, want a mismatch", err)
	}
}

// The store is unreachable: nothing advances, the status says so, and the
// next Step copies everything once.
func TestTailCatchesUpAfterAStoreOutage(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	step(t, tl)
	write(t, j, "tag.write", "B")
	write(t, j, "tag.write", "C")
	m.fail = errors.New("connection refused")
	if _, err := tl.Step(context.Background()); err == nil {
		t.Fatal("Step reported success while the store failed")
	}
	if st := tl.Status(); st.State != CopyBehind || st.Copied != 1 || st.Head != 3 || !strings.Contains(st.LastError, "refused") {
		t.Errorf("status %+v", st)
	}
	step(t, tl)
	wantSeqs(t, m, 1, 2, 3)
	if st := tl.Status(); st.State != CopyCurrent || st.LastError != "" {
		t.Errorf("status %+v, want current with the error cleared", st)
	}
}

// An outage before the first read: the tailer finds its place once the
// store answers.
func TestTailResumesAfterTheStoreFirstAnswers(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	write(t, j, "tag.write", "B")
	down := &downStore{memStore: m, down: true}
	tl := NewTailer(dir, down, j.Head)
	if _, err := tl.Step(context.Background()); err == nil {
		t.Fatal("Step succeeded against a store that is down")
	}
	down.down = false
	step(t, tl)
	wantSeqs(t, m, 1, 2)
}

type downStore struct {
	*memStore
	down bool
}

func (d *downStore) Last(ctx context.Context) (Line, bool, error) {
	if d.down {
		return Line{}, false, errors.New("connection refused")
	}
	return d.memStore.Last(ctx)
}

// A store that says the copy disagrees stops the tailer.
func TestTailStopsOnAStoreMismatch(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{fail: &CopyMismatch{Reason: "the copy holds another line as this seq"}}
	tl := NewTailer(dir, m, j.Head)
	if _, err := tl.Step(context.Background()); err == nil {
		t.Fatal("Step succeeded")
	}
	if st := tl.Status(); st.State != CopyStopped {
		t.Errorf("status %+v, want stopped", st)
	}
	if _, err := tl.Step(context.Background()); err == nil {
		t.Error("a stopped tailer stepped again")
	}
	wantSeqs(t, m)
}

// A partial last line (a write in progress, or a crash) is not copied
// until it is complete.
func TestTailWaitsForAPartialLine(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	j.Close()
	ls := lines(t, dir)
	e := Entry{Seq: 2, Prev: lineHash([]byte(ls[0])), User: "user:op1", Source: SourceHMI, Action: "tag.write", Result: ResultOK, TS: t0()}
	b, _ := json.Marshal(e)
	path := filepath.Join(dir, "audit-20261118.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	defer f.Close()
	f.Write(b[:10])
	m := &memStore{}
	tl := NewTailer(dir, m, unbounded)
	step(t, tl)
	wantSeqs(t, m, 1)
	f.Write(append(b[10:], '\n'))
	step(t, tl)
	wantSeqs(t, m, 1, 2)
}

// A line lands in a file after the tailer read to its end and before it
// saw the next file: it reads the old file once more before moving on.
func TestTailReadsARotatedFileToItsEnd(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{MaxSize: 300})
	write(t, j, "tag.write", "A") // ~160 bytes: room for one more
	m := &memStore{}
	// The live head: B and C are past the head the Step began with, and
	// durable once the later file exists.
	tl := NewTailer(dir, m, j.Head)
	once := true
	tl.afterRead = func() {
		if once {
			once = false
			write(t, j, "tag.write", "B") // the first file
			write(t, j, "tag.write", "C") // rotates to the second
		}
	}
	step(t, tl)
	wantSeqs(t, m, 1, 2, 3)
	if files, _ := listFiles(dir); len(files) != 2 {
		t.Fatalf("files %v, want two (the test needs a rotation)", files)
	}
}

// Batches are applied in order, and each advances the copy.
func TestTailAppliesInBatches(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	for i := 0; i < 7; i++ {
		write(t, j, "tag.write", "A")
	}
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	tl.batch = 3
	step(t, tl)
	wantSeqs(t, m, span(1, 7)...)
	if m.calls != 3 {
		t.Errorf("%d batches, want 3", m.calls)
	}
}

// A backlog longer than one read is copied in pieces, all of it, in
// order: a long outage never holds a whole file in memory.
func TestTailReadsALongBacklogInPieces(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	for i := 0; i < 10; i++ {
		write(t, j, "tag.write", "A")
	}
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	tl.maxRead, tl.batch = 3, 2
	if n := step(t, tl); n != 10 {
		t.Fatalf("copied %d, want 10", n)
	}
	wantSeqs(t, m, span(1, 10)...)
}

// The good lines before a bad one are copied before the copy stops: they
// are the evidence.
func TestTailCopiesTheGoodLinesBeforeABadOne(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	step(t, NewTailer(dir, m, j.Head))
	for _, x := range []string{"B", "C", "D"} {
		write(t, j, "tag.write", x)
	}
	j.Close()
	path := filepath.Join(dir, "audit-20261118.jsonl")
	ls := lines(t, dir)
	// Edit C: it still chains on from B, but D no longer chains on from it.
	os.WriteFile(path, []byte(strings.Join([]string{ls[0], ls[1], strings.Replace(ls[2], `"target":"C"`, `"target":"Y"`, 1), ls[3]}, "\n")+"\n"), 0o600)
	err := stepToStop(t, NewTailer(dir, m, unbounded))
	var mm *CopyMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("Step: %v, want a mismatch", err)
	}
	wantSeqs(t, m, 1, 2, 3) // A, B and the edited C (which still chains on); D does not chain on from it
}

// A mismatch seen once while reading, and gone at the next Step (a read
// that raced a cut-back), does not stop the copy.
func TestTailRechecksAMismatchBeforeStopping(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	j.Close()
	path := filepath.Join(dir, "audit-20261118.jsonl")
	good, _ := os.ReadFile(path)
	os.WriteFile(path, append(append([]byte(nil), good...), []byte("{\"spliced\n")...), 0o600)
	m := &memStore{}
	tl := NewTailer(dir, m, func() (string, uint64) { return "", 1 })
	if _, err := tl.Step(context.Background()); err == nil || !strings.Contains(err.Error(), "rechecking") {
		t.Fatalf("Step: %v, want a recheck", err)
	}
	os.WriteFile(path, good, 0o600) // the cut-back finished
	step(t, tl)
	if st := tl.Status(); st.State != CopyCurrent {
		t.Errorf("status %+v, want current", st)
	}
	wantSeqs(t, m, 1)
}

// A rotated file that ends in a partial line is damage, not a write in
// progress: the copy stops rather than waiting on it for ever.
func TestTailStopsOnAPartialLineInARotatedFile(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "A")
	c.t = c.t.Add(24 * time.Hour)
	write(t, j, "tag.write", "B")
	f, _ := os.OpenFile(filepath.Join(dir, "audit-20261118.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte(`{"seq":`))
	f.Close()
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	_, err := tl.Step(context.Background())
	var mm *CopyMismatch
	if !errors.As(err, &mm) || !strings.Contains(mm.Reason, "later file follows it") {
		t.Fatalf("Step: %v, want a mismatch on the rotated file", err)
	}
	wantSeqs(t, m, 1)
}

// A copy whose newest row names something that is not a journal file
// (a row someone wrote by hand) stops the copy; it is never read.
func TestTailStopsOnARowThatNamesNoJournalFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../etc/passwd", "x", "audit-2026.jsonl"} {
		m := &memStore{rows: []Line{{File: name, Offset: 10, Seq: 3, Hash: "sha256:x"}}}
		_, err := NewTailer(dir, m, unbounded).Step(context.Background())
		var mm *CopyMismatch
		if !errors.As(err, &mm) || !strings.Contains(mm.Reason, "not one of the journal's") {
			t.Errorf("%q: %v, want a mismatch", name, err)
		}
	}
}

// A store call cut short because the controller is stopping is not an
// outage: the status stays as it was.
func TestTailStoppingIsNotAnOutage(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	m := &memStore{}
	tl := NewTailer(dir, m, j.Head)
	step(t, tl)
	write(t, j, "tag.write", "B")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.fail = context.Canceled
	if _, err := tl.Step(ctx); err == nil {
		t.Fatal("Step succeeded")
	}
	if st := tl.Status(); st.State != CopyCurrent || st.LastError != "" {
		t.Errorf("status %+v, want it unchanged (current)", st)
	}
}
