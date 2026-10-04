package runtime

import (
	"math"
	"math/bits"
)

// Lateness is one task's wake-up timing: how far each scan started after
// the instant its period said it should. Scan TIME (ScanStats.LastMs and
// the 2 ms Histogram) answers "how long does a scan take"; lateness
// answers "does it start when it should" — the soft-real-time question,
// and the one a fast task on a general-purpose kernel actually loses.
//
// A sample is how late the scan STARTED against its slot: Run schedules
// slot n at start + n·period, and the sample is start time − slot time,
// never negative. When Scan or ScanTask is driven from outside Run (tests,
// a custom scheduler) there is no slot, and the sample falls back to
// period − target — the interval since the previous scan minus the
// configured one, where an early scan lands in the first bucket. Every
// field is cumulative since the runtime started — the numbers a report
// quotes ("over a 30-minute run, p99.9 was …"), not a moving view, which
// the Periods sparkline already gives.
//
// Percentiles are read off a log-linear histogram with 32 buckets per
// octave above 1 µs (about 3 % resolution) and reported as the upper edge
// of the bucket the percentile falls in, so a quoted p99 is a bound, not
// an interpolation. Histogram is the coarser 1-2-5 rollup for a chart:
// bucket i counts samples in [BucketsUs[i-1], BucketsUs[i]) µs, bucket 0
// everything below 1 µs (including early), and the last bucket everything
// at or above the final edge.
type Lateness struct {
	// ThresholdMs is the lateness that makes a scan Late — Options.
	// LateThreshold, or a tenth of the task's target when unset.
	ThresholdMs float64 `json:"thresholdMs"`
	// Late counts scans whose period exceeded target + ThresholdMs.
	Late uint64 `json:"late"`
	// Overruns counts scans whose EXECUTION exceeded the target period: the
	// next tick was already due before this scan finished. A ticker drops
	// the ticks that pile up, so an overrun is also one or more scans that
	// never happened.
	Overruns uint64 `json:"overruns"`
	// Missed counts scan slots the loop skipped because it was more than a
	// whole period behind schedule (after an overrun, or a stall): the
	// scans that should have happened and did not. Run keeps the schedule
	// absolute, so lateness never accumulates — it is paid once, here.
	Missed uint64  `json:"missed"`
	LastUs float64 `json:"lastUs"` // the latest sample, µs
	MaxUs  float64 `json:"maxUs"`  // worst lateness seen, µs
	P50Us  float64 `json:"p50Us"`
	P99Us  float64 `json:"p99Us"`
	P999Us float64 `json:"p999Us"`
	// Histogram is the 1-2-5 log-spaced rollup, cumulative; BucketsUs are
	// its edges (so a chart never hard-codes them).
	Histogram []int     `json:"histogram"`
	BucketsUs []float64 `json:"bucketsUs"`
}

// lateBucketsUs are the coarse histogram's edges: 1-2-5 per decade from
// 1 µs to 100 ms. 16 edges → 17 buckets.
var lateBucketsUs = [...]float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000, 50000, 100000}

// Fine histogram geometry: bucket 0 is < 1 µs; above that each octave
// [2^k, 2^(k+1)) µs splits into fineSub linear buckets. fineOctaves
// octaves reach 2^30 µs ≈ 18 min, beyond anything a scan loop will see;
// anything larger lands in the last bucket.
const (
	fineSub     = 32
	fineOctaves = 30
	fineBuckets = 1 + fineOctaves*fineSub
)

// lateTracker is the per-task accumulator behind Lateness. Not safe for
// concurrent use: the main task records under Runtime.mu, each additional
// task under its taskRun.mu — the same locks that guard the stats they
// ride in.
type lateTracker struct {
	thresholdUs float64
	late        uint64
	overruns    uint64
	missed      uint64
	lastUs      float64
	maxUs       float64
	n           uint64
	fine        [fineBuckets]uint64
	coarse      [len(lateBucketsUs) + 1]int
}

// record folds one scan in: how late it started, in µs, and whether its
// execution overran the period.
func (l *lateTracker) record(lateUs float64, overrun bool) {
	l.lastUs = lateUs
	l.n++
	if lateUs > l.maxUs {
		l.maxUs = lateUs
	}
	if lateUs > l.thresholdUs {
		l.late++
	}
	if overrun {
		l.overruns++
	}
	l.fine[fineIndex(lateUs)]++
	l.coarse[coarseIndex(lateUs)]++
}

// fineIndex maps a sample in µs to its log-linear bucket.
func fineIndex(us float64) int {
	if us < 1 || math.IsNaN(us) {
		return 0
	}
	if us >= float64(uint64(1)<<fineOctaves) {
		return fineBuckets - 1
	}
	// bits.Len64 of the integer part gives the octave: 1 ≤ v < 2 → 0,
	// 2 ≤ v < 4 → 1, and so on. The fractional position inside the octave
	// picks the linear sub-bucket.
	octave := bits.Len64(uint64(us)) - 1
	lo := float64(uint64(1) << octave)
	sub := int((us - lo) / lo * fineSub)
	if sub >= fineSub {
		sub = fineSub - 1
	}
	return 1 + octave*fineSub + sub
}

// fineUpper is the upper edge, in µs, of fine bucket i — what a percentile
// that lands in i is reported as.
func fineUpper(i int) float64 {
	if i <= 0 {
		return 1
	}
	i--
	octave, sub := i/fineSub, i%fineSub
	lo := float64(uint64(1) << octave)
	return lo + lo*float64(sub+1)/fineSub
}

// coarseIndex maps a sample to its 1-2-5 bucket.
func coarseIndex(us float64) int {
	i := 0
	for i < len(lateBucketsUs) && us >= lateBucketsUs[i] {
		i++
	}
	return i
}

// percentiles reads p50, p99 and p99.9 off the fine histogram in one
// pass. With no samples all three are 0.
func (l *lateTracker) percentiles() (p50, p99, p999 float64) {
	if l.n == 0 {
		return 0, 0, 0
	}
	// The k-th sample (1-based) for each percentile, by nearest-rank.
	k50 := uint64(math.Ceil(0.5 * float64(l.n)))
	k99 := uint64(math.Ceil(0.99 * float64(l.n)))
	k999 := uint64(math.Ceil(0.999 * float64(l.n)))
	var cum uint64
	got := 0
	for i := 0; i < fineBuckets && got < 3; i++ {
		cum += l.fine[i]
		if p50 == 0 && cum >= k50 {
			p50, got = fineUpper(i), got+1
		}
		if p99 == 0 && cum >= k99 {
			p99, got = fineUpper(i), got+1
		}
		if p999 == 0 && cum >= k999 {
			p999, got = fineUpper(i), got+1
		}
	}
	return p50, p99, p999
}

// snapshot renders the tracker as the wire struct.
func (l *lateTracker) snapshot() Lateness {
	p50, p99, p999 := l.percentiles()
	return Lateness{
		ThresholdMs: l.thresholdUs / 1000,
		Late:        l.late,
		Overruns:    l.overruns,
		Missed:      l.missed,
		LastUs:      l.lastUs,
		MaxUs:       l.maxUs,
		P50Us:       p50,
		P99Us:       p99,
		P999Us:      p999,
		Histogram:   append([]int(nil), l.coarse[:]...),
		BucketsUs:   append([]float64(nil), lateBucketsUs[:]...),
	}
}

// lateThresholdS resolves a task's late threshold: the configured one, or
// a tenth of the target.
func lateThresholdS(configuredS, targetS float64) float64 {
	if configuredS > 0 {
		return configuredS
	}
	return targetS / 10
}
