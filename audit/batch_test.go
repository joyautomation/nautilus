package audit

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func batch(n int, prefix string) []Entry {
	es := make([]Entry, n)
	for i := range es {
		es[i] = Entry{User: "user:op1", Source: SourceOPCUA, Action: "tag.write", Target: fmt.Sprintf("%s%d", prefix, i), Result: ResultOK}
	}
	return es
}

// A batch is written in one write and synced once, whatever its size, and
// its lines are numbered and chained one by one, as Records would be: the
// journal verifies, and a Record after it chains on.
func TestRecordBatchSyncsOnce(t *testing.T) {
	dir := t.TempDir()
	j := open(t, dir, &clock{t0()}, FileOptions{})
	write(t, j, "tag.write", "before")
	syncs, writes := 0, 0
	j.sync = func(f *os.File) error { syncs++; return f.Sync() }
	j.write = func(f *os.File, b []byte) (int, error) { writes++; return f.Write(b) }
	got, err := j.RecordBatch(batch(1000, "T"))
	if err != nil {
		t.Fatal(err)
	}
	if syncs != 1 || writes != 1 {
		t.Errorf("a 1000-entry batch: %d syncs, %d writes, want 1 and 1", syncs, writes)
	}
	if len(got) != 1000 || got[0].Seq != 2 || got[999].Seq != 1001 || got[0].ID == "" {
		t.Errorf("the batch's entries: %d, seq %d..%d, id %q", len(got), got[0].Seq, got[len(got)-1].Seq, got[0].ID)
	}
	write(t, j, "tag.write", "after")
	if r := mustVerify(t, dir); r.Entries != 1002 || r.HeadSeq != 1002 {
		t.Errorf("verify: %d entries, head seq %d, want 1002", r.Entries, r.HeadSeq)
	}
	if got, _ := j.RecordBatch(nil); got != nil {
		t.Errorf("an empty batch: %v", got)
	}
}

// A batch whose write or fsync fails leaves nothing: the file is cut back
// to the line before it, as a unit, and the next entry chains from there.
func TestAFailedBatchIsCutBackWhole(t *testing.T) {
	for _, fail := range []string{"short write", "fsync"} {
		dir := t.TempDir()
		j := open(t, dir, &clock{t0()}, FileOptions{})
		write(t, j, "tag.write", "A")
		if fail == "short write" {
			j.write = func(f *os.File, b []byte) (int, error) {
				n, _ := f.Write(b[:len(b)*2/3])
				return n, errors.New("ENOSPC")
			}
		} else {
			j.sync = func(*os.File) error { return errors.New("EIO") }
			j.write = (*os.File).Write
		}
		calls := 0
		realSync := (*os.File).Sync
		if fail == "fsync" {
			// The batch's sync fails; undo's own sync succeeds.
			j.sync = func(f *os.File) error {
				if calls++; calls == 1 {
					return errors.New("EIO")
				}
				return realSync(f)
			}
		}
		if _, err := j.RecordBatch(batch(50, "B")); err == nil {
			t.Fatalf("%s: RecordBatch reported success", fail)
		}
		j.write, j.sync = (*os.File).Write, (*os.File).Sync
		write(t, j, "tag.write", "C")
		ls := lines(t, dir)
		if len(ls) != 2 || strings.Contains(strings.Join(ls, ""), `"target":"B`) {
			t.Errorf("%s: lines:\n%s\nwant A and C only", fail, strings.Join(ls, "\n"))
		}
		mustVerify(t, dir)
	}
}
