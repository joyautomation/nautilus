package runtime

import (
	"math"
	"testing"
	"time"
)

// Every fine bucket's upper edge must exceed its lower neighbour's, and a
// value just under an edge must land in the bucket below the one a value
// at the edge lands in — the histogram is only a histogram if the index
// and the edge agree.
func TestFineBucketsAreMonotonic(t *testing.T) {
	prev := 0.0
	for i := 0; i < fineBuckets; i++ {
		up := fineUpper(i)
		if up <= prev {
			t.Fatalf("bucket %d: upper %g not above previous %g", i, up, prev)
		}
		prev = up
	}
	for i := 1; i < fineBuckets-1; i++ {
		up := fineUpper(i)
		if got := fineIndex(math.Nextafter(up, 0)); got != i {
			t.Errorf("value just under %g: bucket %d, want %d", up, got, i)
		}
		if got := fineIndex(up); got != i+1 {
			t.Errorf("value at %g: bucket %d, want %d", up, got, i+1)
		}
	}
	// Below 1 µs, early, NaN: bucket 0. Absurdly large: the last bucket.
	for _, v := range []float64{0, 0.5, -3, math.NaN()} {
		if fineIndex(v) != 0 {
			t.Errorf("fineIndex(%v) = %d, want 0", v, fineIndex(v))
		}
	}
	if fineIndex(1e12) != fineBuckets-1 {
		t.Errorf("fineIndex(1e12) = %d, want %d", fineIndex(1e12), fineBuckets-1)
	}
}

func TestFineResolution(t *testing.T) {
	// 32 buckets per octave: the widest relative bucket is the first in
	// each octave, 1/32 ≈ 3.1 % of its lower edge.
	for _, i := range []int{1, 1 + fineSub, 1 + 10*fineSub, fineBuckets - 2} {
		lo, hi := fineUpper(i-1), fineUpper(i)
		if rel := (hi - lo) / lo; rel > 1.0/fineSub+1e-9 {
			t.Errorf("bucket %d: relative width %.4f > %.4f", i, rel, 1.0/fineSub)
		}
	}
}

func TestCoarseIndex(t *testing.T) {
	cases := map[float64]int{
		-5: 0, 0: 0, 0.9: 0, 1: 1, 1.9: 1, 2: 2, 4.9: 2, 5: 3,
		999: 9, 1000: 10, 99999: 15, 100000: 16, 5e6: 16,
	}
	for v, want := range cases {
		if got := coarseIndex(v); got != want {
			t.Errorf("coarseIndex(%g) = %d, want %d", v, got, want)
		}
	}
}

func TestLateTrackerCountsAndPercentiles(t *testing.T) {
	var l lateTracker
	l.thresholdUs = 100 // late beyond 100 µs
	target := 0.001     // 1 ms task
	// 1000 scans: 990 on time (period 1.06 ms, 60 µs late), 9 at 300 µs
	// late, 1 at 30 ms late with an execution overrun. Sample values sit
	// mid-bucket so float cancellation in period − target cannot tip one
	// across an edge.
	for i := 0; i < 990; i++ {
		l.record(target+60e-6, target, 200e-6)
	}
	for i := 0; i < 9; i++ {
		l.record(target+300e-6, target, 200e-6)
	}
	l.record(target+30e-3, target, 29e-3)
	// And one early sample, which must count as a scan but not as late.
	l.record(target-40e-6, target, 200e-6)

	if l.n != 1001 {
		t.Fatalf("n = %d, want 1001", l.n)
	}
	if l.late != 10 {
		t.Errorf("late = %d, want 10", l.late)
	}
	if l.overruns != 1 {
		t.Errorf("overruns = %d, want 1", l.overruns)
	}
	if math.Abs(l.maxUs-30000) > 1e-6 {
		t.Errorf("maxUs = %g, want 30000", l.maxUs)
	}
	if math.Abs(l.lastUs+40) > 1e-6 {
		t.Errorf("lastUs = %g, want -40", l.lastUs)
	}
	p50, p99, p999 := l.percentiles()
	// Percentiles are bucket upper edges: at most one bucket (≤ 3.2 %)
	// above the true value, never below it.
	check := func(name string, got, true float64) {
		t.Helper()
		if got < true || got > true*(1+1.0/fineSub)+1e-9 {
			t.Errorf("%s = %g, want within [%g, %g]", name, got, true, true*(1+1.0/fineSub))
		}
	}
	check("p50", p50, 60)
	check("p99", p99, 60) // 990 of 1001 are at 60 µs: the 991st sample is still 60
	check("p999", p999, 300)
	if l.coarse[0] != 1 || l.coarse[6] != 990 || l.coarse[8] != 9 || l.coarse[14] != 1 {
		t.Errorf("coarse histogram = %v", l.coarse)
	}

	s := l.snapshot()
	if s.Late != 10 || s.Overruns != 1 || s.ThresholdMs != 0.1 || len(s.Histogram) != 17 || len(s.BucketsUs) != 16 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestLateTrackerEmpty(t *testing.T) {
	var l lateTracker
	p50, p99, p999 := l.percentiles()
	if p50 != 0 || p99 != 0 || p999 != 0 {
		t.Errorf("empty percentiles = %g %g %g, want zeros", p50, p99, p999)
	}
}

func TestLateThresholdDefaults(t *testing.T) {
	if got := lateThresholdS(0, 0.1); got != 0.01 {
		t.Errorf("default for 100 ms = %g, want 0.01", got)
	}
	if got := lateThresholdS(0.005, 0.1); got != 0.005 {
		t.Errorf("configured 5 ms = %g", got)
	}
}

// Under a virtual clock every period is exactly the target, so lateness
// stays at zero and the acceptance path is untouched by the tracker.
func TestLatenessUnderVirtualClock(t *testing.T) {
	clk := &stepClock{t: time.Unix(0, 0)}
	r, err := New(Options{
		Program: "PROGRAM Main\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM",
		Scan:    10 * time.Millisecond,
		Clock:   clk,
		Tasks: []Task{{
			Name: "fast", Scan: time.Millisecond, LateThreshold: 50 * time.Microsecond,
			Program: "PROGRAM Fast\nVAR y : INT; END_VAR\ny := y + 1;\nEND_PROGRAM",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		clk.t = clk.t.Add(time.Millisecond)
		if err := r.ScanTask("fast"); err != nil {
			t.Fatal(err)
		}
		if i%10 == 9 {
			r.Scan()
		}
	}
	s := r.Stats()
	if s.Lateness.ThresholdMs != 1 {
		t.Errorf("main threshold = %g ms, want 1 (a tenth of 10 ms)", s.Lateness.ThresholdMs)
	}
	if s.Lateness.Late != 0 || s.Lateness.MaxUs != 0 || s.Lateness.Overruns != 0 {
		t.Errorf("main lateness under virtual time = %+v", s.Lateness)
	}
	if len(s.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(s.Tasks))
	}
	ft := s.Tasks[0].Lateness
	if ft.ThresholdMs != 0.05 {
		t.Errorf("task threshold = %g ms, want 0.05", ft.ThresholdMs)
	}
	if ft.Late != 0 || ft.MaxUs != 0 {
		t.Errorf("task lateness under virtual time = %+v", ft)
	}
	// 50 scans, the first is unmeasured (no previous): 49 samples, all in
	// the first bucket.
	if ft.Histogram[0] != 49 {
		t.Errorf("task histogram = %v, want 49 in bucket 0", ft.Histogram)
	}
}

type stepClock struct{ t time.Time }

func (c *stepClock) Now() time.Time { return c.t }
