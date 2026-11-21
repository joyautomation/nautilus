package sim

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// #1029 3c-3c-1b: a proposal is taken only by the boot it addresses, and
// a refused one goes again at once when the State that refused it first
// names the peer's boot. The witness-switch families must not change:
// QUORUM_MEASURE_SWITCH=<seeds> prints, per family, how many runs switch
// (both controllers adopt the rebuilt witness), the time from the
// rebuilt witness's start to the second adoption, and the oracles'
// violations. Run it on main for the before.
func TestMeasureSwitch(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_SWITCH"))
	if n == 0 {
		t.Skip("set QUORUM_MEASURE_SWITCH=<seeds> to measure")
	}
	for _, loss := range []int64{0, 10, 30} {
		var switched, bad int
		var lat []time.Duration
		for seed := uint64(1); seed <= uint64(n); seed++ {
			r := rand.New(rand.NewPCG(seed, 0x53574348))
			at := 8*sec + time.Duration(r.Int64N(int64(4*sec)))
			o := Options{Seed: seed, Delay: time.Duration(200+r.IntN(4800)) * time.Microsecond, Jitter: ms / 2, Fsync: ms,
				Until: at + 20*sec, Start: [3]time.Duration{0, time.Duration(r.Int64N(int64(4 * sec))), 0},
				Faults: []Fault{{Kind: Wipe, At: at, For: 500 * ms, Node: W}}}
			if loss > 0 {
				o.Faults = append(o.Faults, Fault{Kind: Loss, At: at, For: 20 * sec, From: A, To: B, Both: true, Value: loss})
			}
			rec := Run(o)
			j := witnessIncAt(rec, W, o.Until)
			var done []time.Duration
			for _, a := range rec.Adoptions {
				if a.Inc == j {
					done = append(done, a.At)
				}
			}
			if a, b := adoptedBy(rec, A), adoptedBy(rec, B); len(a) > 0 && len(b) > 0 && a[len(a)-1] == j && b[len(b)-1] == j {
				switched++
				lat = append(lat, slices.Max(done)-(at+500*ms))
			}
			if len(Check(rec, checksFor(o))) > 0 {
				bad++
			}
		}
		slices.Sort(lat)
		var p50, p99, mx time.Duration
		if len(lat) > 0 {
			p50, p99, mx = lat[len(lat)/2], lat[len(lat)*99/100], lat[len(lat)-1]
		}
		t.Log(fmt.Sprintf("A-B loss %2d%%: switched %d/%d, to the second adoption p50 %v p99 %v max %v, runs with a violation %d",
			loss, switched, n, p50.Round(ms), p99.Round(ms), mx.Round(ms), bad))
	}
}
