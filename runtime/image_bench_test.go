package runtime

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// bigImageMain is a large frame: 20 000 REALs in a table, 2 000 timers, 500
// structs, for guard (a)'s per-leaf cost of the scan's copy.
const bigImageMain = `TYPE Rec : STRUCT a : REAL; b : INT; c : BOOL; d : TIME; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL Run : BOOL; Setpoint : REAL; Out : REAL; END_VAR
VAR
    tbl : ARRAY[0..19999] OF REAL;
    tons : ARRAY[0..1999] OF TON;
    recs : ARRAY[0..499] OF Rec;
    i : INT;
END_VAR
i := (i + 1) MOD 2000;
tbl[i] := Setpoint;
tons[i](IN := Run, PT := T#5s);
recs[i MOD 500].a := Setpoint;
Out := tbl[0];
END_PROGRAM`

// copyRatioLimit is guard (a)'s bound on the scan's copy, relative to the
// machine it runs on: the extra a scan pays to copy its frame, against a
// plain copy of the same leaves (one slice copy of as many ir.Leaf) timed
// beside it. A walk that copies values only is about 5 times the plain
// copy (i9-14900K, also pinned to 2 cores: 3.7 ns a leaf), and a shared CI
// runner has measured it above 12 (#1120), so the bound sits well clear.
// It catches a copy that does far more work per leaf without allocating
// (a search per leaf over even 200 neighbours is over 400 times). A copy
// that allocates per leaf (reflection, JSON, a pointer per leaf: 30 to 35
// times here) is the exact guard's: TestImageCopyAddsNoAllocationToTheScan.
const copyRatioLimit = 25

// BenchmarkImageCopyInScan is the extra a scan pays to copy its frame, per
// leaf, on a large frame; it fails over copyRatioLimit times a plain copy
// of the same leaves. Each side is the fastest of several rounds, so one
// preempted round (CI runs this once) does not decide it.
func BenchmarkImageCopyInScan(b *testing.B) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	o := imgOptions(clk, bigImageMain)
	o.Tasks = nil
	rt, err := New(o)
	if err != nil {
		b.Fatal(err)
	}
	var img Image
	rt.RequestImage()
	rt.Scan()
	rt.TakeImage(&img)
	leaves := len(img.Tasks[0].Leaves)
	const n, rounds = 100, 7
	fastest := func(f func()) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range rounds {
			t0 := time.Now()
			for range n {
				f()
			}
			if d := time.Since(t0) / n; d < best {
				best = d
			}
		}
		return best
	}
	scan := func() { rt.Scan() }
	copied := func() {
		rt.prog.requestImage()
		rt.Scan()
		rt.prog.takeImage(MainTaskName, &img.Tasks[0])
	}
	src := append([]ir.Leaf(nil), img.Tasks[0].Leaves...)
	dst := make([]ir.Leaf, len(src))
	plainCopy := func() { copy(dst, src) }
	b.ResetTimer()
	var extra, plain time.Duration
	for range b.N {
		extra = max(fastest(copied)-fastest(scan), 0)
		plain = max(fastest(plainCopy), time.Nanosecond)
	}
	b.StopTimer()
	ratio := float64(extra) / float64(plain)
	b.ReportMetric(float64(leaves), "leaves")
	b.ReportMetric(float64(extra.Nanoseconds()), "copy-ns/scan")
	b.ReportMetric(float64(extra.Nanoseconds())/float64(leaves), "copy-ns/leaf")
	b.ReportMetric(ratio, "x-plain-copy")
	if ratio > copyRatioLimit {
		b.Fatalf("the scan's copy costs %v for %d leaves, %.1f times a plain copy of them (%v), above guard (a)'s %d", extra, leaves, ratio, plain, copyRatioLimit)
	}
}

// BenchmarkImageEncode is the stream goroutine's cost for the same frame.
func BenchmarkImageEncode(b *testing.B) {
	clk := &imgClock{t: time.Unix(1_700_000_000, 0)}
	o := imgOptions(clk, bigImageMain)
	o.Tasks = nil
	rt, err := New(o)
	if err != nil {
		b.Fatal(err)
	}
	var img Image
	rt.RequestImage()
	rt.Scan()
	rt.TakeImage(&img)
	buf := mustEncode(b, nil, &img)
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for range b.N {
		buf, _ = EncodeImage(buf[:0], &img)
	}
	b.ReportMetric(float64(len(buf)), "image-bytes")
}
