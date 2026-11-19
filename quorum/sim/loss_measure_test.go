package sim

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestMeasureSolePathLoss measures how often the leader's lease lapses
// when loss hits the sole vote path (test plan F20): the witness cut off
// from both controllers, so only A–B can carry a majority, and p% loss on
// A–B both ways for 10 leases (the generator's longest fault). Each p runs
// QUORUM_MEASURE_LOSS windows on seeded networks, disks and clocks drawn as
// randomRun draws them; it logs, per p, the windows in which the gate
// closed at least once, those in which the holder changed, and those in
// which it changed twice or more: more takeovers than the loss fault's one,
// an I7 breach. maxSoleLoss is the highest rate with none of those; skipped
// unless QUORUM_MEASURE_LOSS is set (20,000 takes about a minute).
//
//	QUORUM_MEASURE_LOSS=20000 go test ./quorum/sim -run '^TestMeasureSolePathLoss$' -v
func TestMeasureSolePathLoss(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_LOSS"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_LOSS=<windows per loss rate>")
	}
	for _, p := range []int64{1, 2, 3, 4, 5, 6, 8, 10, 15, 20, 27, 30} {
		lapsed, changed, twice := 0, 0, 0
		for seed := uint64(1); seed <= uint64(n); seed++ {
			o := solePathLoss(seed, p)
			from, to := o.Faults[2].At, o.Faults[2].At+o.Faults[2].For
			breaks, holders := lapses(Run(o).Gates(), from, to)
			if breaks > 0 {
				lapsed++
			}
			if holders > 0 {
				changed++
			}
			if holders > 1 {
				twice++
			}
		}
		t.Logf("loss %2d%%: of %d windows, %5d lapsed, %5d changed holder, %5d twice or more", p, n, lapsed, changed, twice)
	}
}

// solePathLoss is seed's network, disk and clocks with the witness cut off
// from both controllers and p% loss on A–B both ways for 10 leases, once A
// or B leads.
func solePathLoss(seed uint64, p int64) Options {
	o := randomRun(seed, profDefault)
	at := 8 * sec
	o.Faults = []Fault{
		{Kind: Cut, At: at - lease, For: 12 * lease, From: A, To: W, Both: true},
		{Kind: Cut, At: at - lease, For: 12 * lease, From: B, To: W, Both: true},
		{Kind: Loss, At: at, For: 10 * lease, From: A, To: B, Both: true, Value: p},
	}
	o.Until = at + 11*lease
	return o
}

// lapses counts, within [from, to), the gate closings that were followed by
// a reopening, and the holder changes.
func lapses(gates []Interval, from, to time.Duration) (breaks, holders int) {
	for i := 1; i < len(gates); i++ {
		if gates[i].From < from || gates[i].From >= to {
			continue
		}
		breaks++
		if gates[i].Node != gates[i-1].Node {
			holders++
		}
	}
	return breaks, holders
}
