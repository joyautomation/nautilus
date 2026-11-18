package audit

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An fsync that fails leaves the line on disk with the head unmoved: the
// file is cut back so the next entry does not read as a tamper break.
func TestAFailedSyncIsCutBack(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	fail := true
	j.sync = func(f *os.File) error {
		if fail {
			fail = false
			return errors.New("EIO")
		}
		return f.Sync()
	}
	if _, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "B", Result: ResultOK}); err == nil {
		t.Fatal("Record reported success after a failed fsync")
	}
	write(t, j, "tag.write", "C")
	ls := lines(t, dir)
	if len(ls) != 2 || strings.Contains(ls[1], `"target":"B"`) {
		t.Fatalf("lines:\n%s\nwant A and C only", strings.Join(ls, "\n"))
	}
	mustVerify(t, dir)
}

// A short write (a full disk) leaves a partial line; it is cut back too,
// so the next entry is not joined onto it.
func TestAShortWriteIsCutBack(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "A")
	j.write = func(f *os.File, b []byte) (int, error) {
		n, _ := f.Write(b[:len(b)/2])
		return n, errors.New("ENOSPC")
	}
	if _, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "B", Result: ResultOK}); err == nil {
		t.Fatal("Record reported success after a short write")
	}
	j.write = (*os.File).Write
	write(t, j, "tag.write", "C")
	if ls := lines(t, dir); len(ls) != 2 {
		t.Fatalf("lines:\n%s\nwant A and C only", strings.Join(ls, "\n"))
	}
	mustVerify(t, dir)
}

// When even the cut-back fails, the journal refuses every entry until it
// is reopened, rather than chain past a line it could not remove.
func TestAFailedCutBackRefusesUntilReopened(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0()}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "A")
	j.sync = func(*os.File) error { return errors.New("EIO") }
	j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "B", Result: ResultOK})
	j.sync = (*os.File).Sync
	if _, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "C", Result: ResultOK}); err == nil || !strings.Contains(err.Error(), "until reopened") {
		t.Fatalf("after a failed cut-back: %v, want refused until reopened", err)
	}
	j.Close()
	j2 := open(t, dir, c, FileOptions{})
	write(t, j2, "tag.write", "D")
	mustVerify(t, dir)
}

// Files follow write order, not entry time: an entry stamped before the
// current file's day (out of order across midnight, or after the clock
// steps back) goes in the current file, and the chain holds, across a
// reopen too.
func TestAFilesDayNeverGoesBack(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Date(2026, 11, 19, 0, 0, 1, 0, time.UTC)}
	j := open(t, dir, c, FileOptions{})
	write(t, j, "tag.write", "AfterMidnight")
	if _, err := j.Record(Entry{TS: time.Date(2026, 11, 18, 23, 59, 59, 0, time.UTC), User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "BeforeMidnight", Result: ResultOK}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	c.t = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) // NTP stepped back two days
	j2 := open(t, dir, c, FileOptions{})
	write(t, j2, "tag.write", "AfterStepBack")
	j2.Close()
	files, _ := listFiles(dir)
	if strings.Join(files, ",") != "audit-20261119.jsonl" {
		t.Fatalf("files %v, want one: the day the journal was first written", files)
	}
	if r := mustVerify(t, dir); r.Entries != 3 || r.HeadSeq != 3 {
		t.Errorf("report %+v", r)
	}
}

// A REAL can hold NaN or ±Inf; the entry still records, with them as
// strings, at any depth.
func TestNonFiniteValuesAreRecordedAsStrings(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	e, err := j.Record(Entry{User: "user:op1", Source: SourceHMI, Action: "tag.write", Target: "FlowPV",
		Before: math.NaN(), After: map[string]any{"hi": math.Inf(1), "lo": []any{float32(math.Inf(-1)), 1.5}},
		Result: ResultOK, Detail: map[string]any{"scale": math.NaN()}})
	if err != nil {
		t.Fatalf("Record with NaN/Inf: %v", err)
	}
	ls := lines(t, dir)
	for _, want := range []string{`"before":"NaN"`, `"hi":"+Inf"`, `"lo":["-Inf",1.5]`, `"scale":"NaN"`} {
		if !strings.Contains(ls[0], want) {
			t.Errorf("line lacks %s:\n%s", want, ls[0])
		}
	}
	if e.Before != "NaN" {
		t.Errorf("returned before %v", e.Before)
	}
	mustVerify(t, dir)
}

// chain writes entries to one file with a correct hash chain but the Seqs
// given: what a writer bug, or a forger who can hash, would leave.
func chain(t *testing.T, dir, name string, es []Entry) {
	t.Helper()
	prev := ""
	var out []string
	for _, e := range es {
		e.Prev = prev
		b, _ := json.Marshal(e)
		out = append(out, string(b))
		prev = lineHash(b)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func ent(seq uint64, action string) Entry {
	return Entry{ID: NewID(t0()), Seq: seq, TS: t0(), User: "user:op1", Source: SourceHMI, Action: action, Result: ResultOK}
}

// Seq steps by one from 1; a gap, a repeat or a wrong start is a break
// even where the hashes chain.
func TestVerifyChecksSeq(t *testing.T) {
	for name, seqs := range map[string][]uint64{
		"a gap":         {1, 2, 4},
		"a repeat":      {1, 2, 2},
		"a wrong start": {2, 3, 4},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			var es []Entry
			for _, s := range seqs {
				es = append(es, ent(s, "tag.write"))
			}
			chain(t, dir, "audit-20261118.jsonl", es)
			r, _ := Verify(dir)
			if r.OK() || !strings.Contains(r.Break.Reason, "seq") {
				t.Errorf("seqs %v: %v, want a seq break", seqs, r.Break)
			}
		})
	}
}

// A journal that starts mid-chain must start at the Seq after the one the
// audit.prune entry recorded: removing a further file and keeping the
// prune record does not pass.
func TestVerifyChecksThePrunedSeq(t *testing.T) {
	start := "sha256:" + strings.Repeat("ab", 32)
	for _, tc := range []struct {
		recorded uint64
		ok       bool
	}{{4, true}, {3, false}} {
		dir := t.TempDir()
		first := ent(5, "tag.write")
		prune := ent(6, "audit.prune")
		prune.Detail = map[string]any{"last": start, "seq": tc.recorded}
		// The first line chains to the pruned file's last hash.
		b, _ := json.Marshal(func() Entry { e := first; e.Prev = start; return e }())
		prune.Prev = lineHash(b)
		p, _ := json.Marshal(prune)
		os.WriteFile(filepath.Join(dir, "audit-20261120.jsonl"), []byte(string(b)+"\n"+string(p)+"\n"), 0o600)
		r, _ := Verify(dir)
		if r.OK() != tc.ok {
			t.Errorf("prune records seq %d, journal starts at 5: ok %v (%v), want %v", tc.recorded, r.OK(), r.Break, tc.ok)
		}
	}
}
