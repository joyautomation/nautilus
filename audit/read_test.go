package audit

import (
	"fmt"
	"testing"
	"time"
)

// Scan streams every file in order, reaches past what a ring would hold,
// honours from, and stops when fn says so.
func TestScanReadsEveryFileInOrder(t *testing.T) {
	dir := t.TempDir()
	j, err := OpenFile(dir, FileOptions{MaxSize: 4 << 10}) // small files: many rotations
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	const n = 3000
	for i := 0; i < n; i++ {
		if _, err := j.Record(Entry{TS: base.Add(time.Duration(i) * time.Minute), User: "user:alice", Source: SourceAPI, Action: "tag.write", Target: fmt.Sprintf("T%d", i), Result: ResultOK}); err != nil {
			t.Fatal(err)
		}
	}
	j.Close()
	var got []string
	if err := Scan(dir, time.Time{}, func(e Entry) bool { got = append(got, e.Target); return true }); err != nil {
		t.Fatal(err)
	}
	if len(got) != n || got[0] != "T0" || got[n-1] != fmt.Sprintf("T%d", n-1) {
		t.Fatalf("%d entries, first %q; want %d in order", len(got), got[0], n)
	}
	cut := base.Add(2000 * time.Minute)
	got = nil
	_ = Scan(dir, cut, func(e Entry) bool { got = append(got, e.Target); return true })
	if len(got) != n-2000 || got[0] != "T2000" {
		t.Fatalf("from: %d entries, first %q; want %d from T2000", len(got), got[0], n-2000)
	}
	seen := 0
	_ = Scan(dir, time.Time{}, func(Entry) bool { seen++; return seen < 5 })
	if seen != 5 {
		t.Fatalf("stopped after %d; want 5", seen)
	}
}
