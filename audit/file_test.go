package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func t0() time.Time { return time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC) }

func open(t *testing.T, dir string, c *clock, opts FileOptions) *FileJournal {
	t.Helper()
	opts.Now = c.now
	j, err := OpenFile(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func write(t *testing.T, j *FileJournal, action, target string) Entry {
	t.Helper()
	e, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: action, Target: target, After: 55.0, Result: ResultOK})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// lines reads every line of the journal, oldest file first.
func lines(t *testing.T, dir string) []string {
	t.Helper()
	files, err := listFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		data, _ := os.ReadFile(filepath.Join(dir, f))
		for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

func mustVerify(t *testing.T, dir string) Report {
	t.Helper()
	r, err := Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() {
		t.Fatalf("verify: %v", r.Break)
	}
	return r
}

func TestRecordChainsEachLineToTheOneBefore(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	a := write(t, j, "tag.write", "SpeedSP")
	b := write(t, j, "alarm.ack", "HiTemp")
	if a.ID == "" || a.TS.IsZero() || a.Prev != "" {
		t.Fatalf("first entry: id %q ts %v prev %q; want an id, a time, no prev", a.ID, a.TS, a.Prev)
	}
	ls := lines(t, dir)
	if len(ls) != 2 {
		t.Fatalf("%d lines, want 2", len(ls))
	}
	if b.Prev != lineHash([]byte(ls[0])) {
		t.Errorf("second prev %q, want the hash of the first line", b.Prev)
	}
	r := mustVerify(t, dir)
	head, seq := j.Head()
	if r.Entries != 2 || r.Head != lineHash([]byte(ls[1])) || head != r.Head || seq != 2 || r.HeadSeq != 2 || a.Seq != 1 || b.Seq != 2 {
		t.Errorf("report %+v, journal head %q seq %d, entry seqs %d %d", r, head, seq, a.Seq, b.Seq)
	}
}

// Reopening continues the chain from the last line on disk, so a restart
// does not start a second chain that Verify would take for a removal.
func TestReopenContinuesTheChain(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "A")
	j.Close()
	j2 := open(t, dir, c, FileOptions{})
	e := write(t, j2, "tag.write", "B")
	if ls := lines(t, dir); e.Prev != lineHash([]byte(ls[0])) {
		t.Fatalf("after reopen prev %q, want the hash of the line before the restart", e.Prev)
	}
	mustVerify(t, dir)
}

// A crash mid-write leaves bytes after the last newline. OpenFile cuts
// exactly those, never a complete line, records the cut (length and hash)
// as the next entry, and chains on.
func TestOpenCutsATornTailAndRecordsIt(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "A")
	write(t, j, "tag.write", "B")
	j.Close()
	files, _ := listFiles(dir)
	p := filepath.Join(dir, files[0])
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"01TORN","ts":"2026-`)
	f.Close()
	if r, _ := Verify(dir); !r.OK() || !r.Torn {
		t.Fatalf("a torn tail: ok %v torn %v, want ok and torn", r.OK(), r.Torn)
	}
	j2 := open(t, dir, c, FileOptions{})
	e := write(t, j2, "tag.write", "C")
	ls := lines(t, dir)
	if len(ls) != 4 || strings.Contains(strings.Join(ls, "\n"), "01TORN") {
		t.Fatalf("lines after reopen:\n%s", strings.Join(ls, "\n"))
	}
	var drop Entry
	json.Unmarshal([]byte(ls[2]), &drop)
	if drop.Action != "audit.tail_dropped" || drop.Detail["bytes"] != float64(26) || drop.Prev != lineHash([]byte(ls[1])) {
		t.Errorf("third line %s, want the recorded cut of 26 bytes chained to B", ls[2])
	}
	if e.Prev != lineHash([]byte(ls[2])) || e.Seq != 4 {
		t.Errorf("C: prev %q seq %d, want chained to the cut record, seq 4", e.Prev, e.Seq)
	}
	r := mustVerify(t, dir)
	if len(r.TailDrops) != 1 || r.TailDrops[0].Bytes != 26 {
		t.Errorf("verify tail drops %+v, want the one cut of 26 bytes", r.TailDrops)
	}
}

// Removing the last newline turns the last whole entry into a "torn tail".
// OpenFile still cuts it, but the cut is on the record, and verify lists it,
// so the entry does not vanish without a trace.
func TestARemovedLastNewlineIsOnTheRecord(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "A")
	gone := write(t, j, "force.set", "Pump1")
	j.Close()
	files, _ := listFiles(dir)
	p := filepath.Join(dir, files[0])
	data, _ := os.ReadFile(p)
	os.WriteFile(p, data[:len(data)-1], 0o600)
	j2 := open(t, dir, c, FileOptions{})
	j2.Close()
	r := mustVerify(t, dir)
	if len(r.TailDrops) != 1 {
		t.Fatalf("tail drops %+v, want the removed entry's cut recorded", r.TailDrops)
	}
	line, _ := json.Marshal(gone)
	if r.TailDrops[0].Bytes != len(line) {
		t.Errorf("cut %d bytes, want the whole entry's %d", r.TailDrops[0].Bytes, len(line))
	}
}

// Every entry is synced to disk before Record returns.
func TestRecordSyncsEveryEntry(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	syncs := 0
	j.sync = func(f *os.File) error { syncs++; return f.Sync() }
	for i := 0; i < 3; i++ {
		write(t, j, "tag.write", "A")
	}
	if syncs != 3 {
		t.Errorf("%d syncs for 3 entries, want 3", syncs)
	}
}

// A new day and a full file both start a new file, and the chain runs on
// across them.
func TestRotationKeepsTheChain(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{MaxSize: 300})
	for i := 0; i < 4; i++ {
		write(t, j, "tag.write", "A") // each line is over 150 bytes: two per file
	}
	c.t = c.t.Add(24 * time.Hour)
	e := write(t, j, "tag.write", "NextDay")
	files, _ := listFiles(dir)
	want := []string{"audit-20261118.jsonl", "audit-20261118-1.jsonl", "audit-20261119.jsonl"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files %v, want %v", files, want)
	}
	ls := lines(t, dir)
	if e.Prev != lineHash([]byte(ls[3])) {
		t.Errorf("first line of the new day chains to %q, want the last line of the day before", e.Prev)
	}
	if r := mustVerify(t, dir); r.Files != 3 || r.Entries != 5 {
		t.Errorf("report %+v", r)
	}
	// And a restart on the rotated set continues from the newest file.
	j.Close()
	j2 := open(t, dir, c, FileOptions{MaxSize: 300})
	write(t, j2, "tag.write", "AfterRestart")
	mustVerify(t, dir)
}

// Pruning removes old files, records each removal with the removed file's
// last hash, and the journal still verifies; it never removes the current
// file.
func TestPruneRecordsWhereTheChainNowStarts(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{Keep: 48 * time.Hour})
	for d := 0; d < 4; d++ {
		write(t, j, "tag.write", "Day")
		c.t = c.t.Add(24 * time.Hour)
	}
	write(t, j, "tag.write", "Today")
	removed, err := j.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "audit-20261118.jsonl,audit-20261119.jsonl" {
		t.Fatalf("removed %v", removed)
	}
	r := mustVerify(t, dir)
	if r.Start == "" {
		t.Errorf("after pruning the chain should start mid-way, at a recorded hash")
	}
	var prunes int
	for _, l := range lines(t, dir) {
		var e Entry
		json.Unmarshal([]byte(l), &e)
		if e.Action == "audit.prune" {
			prunes++
		}
	}
	if prunes != 2 {
		t.Errorf("%d audit.prune entries, want 2", prunes)
	}
}

// Concurrent writers still produce one unbroken chain.
func TestConcurrentRecordsKeepOneChain(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{MaxSize: 4096})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if _, err := j.Record(Entry{User: "user:op", Source: SourceAPI, Action: "tag.write", Result: ResultOK}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if r := mustVerify(t, dir); r.Entries != 200 {
		t.Errorf("%d entries, want 200", r.Entries)
	}
}

func TestNewIDIsAULIDThatSortsByTime(t *testing.T) {
	seen := map[string]bool{}
	var ids []string
	for i := 0; i < 50; i++ {
		id := NewID(t0().Add(time.Duration(i) * time.Millisecond))
		if len(id) != 26 || strings.Trim(id, crockford) != "" || seen[id] {
			t.Fatalf("id %q: want 26 Crockford characters, unique", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("ids out of time order: %v", ids)
	}
	// The time part is the standard ULID encoding: 2026-11-18T14:00:00Z.
	if got := NewID(t0())[:10]; got != "01M7QFA3R0" {
		t.Errorf("time part %q, want 01M7QFA3R0", got)
	}
}

func TestRingKeepsTheLastNNewestFirst(t *testing.T) {
	r := NewRing(3)
	for _, a := range []string{"a", "b", "c", "d"} {
		r.Record(Entry{Action: a})
	}
	var got []string
	for _, e := range r.Recent() {
		got = append(got, e.Action)
	}
	if strings.Join(got, "") != "dcb" {
		t.Errorf("recent %v, want d c b", got)
	}
}
