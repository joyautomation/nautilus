package rationalize

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/alarm"
)

// liveRun drives a live engine over a random schedule of bounces and
// returns its journal, oldest first, and the time it stopped at. Each
// bounce is an activation and a return to normal one scan (100 ms)
// later, unacknowledged: a flap through unack-rtn, which counts.
func liveRun(t *testing.T, seed uint64, alarms int, span time.Duration, meanGap time.Duration) ([]alarm.Event, time.Time) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	now := t0
	vals := map[string]any{}
	defs := make([]alarm.Def, alarms)
	for i := range defs {
		id := fmt.Sprintf("A%d", i)
		defs[i] = alarm.Def{ID: id, Tag: id, Name: id, Priority: alarm.Medium, AckRequired: true, AutoClear: true, Shelvable: true,
			Equipment: fmt.Sprintf("E%d", i%3)}
		vals[id] = false
	}
	j := alarm.NewRing(1 << 16)
	e, err := alarm.New(alarm.Options{Defs: defs, Journal: j, Now: func() time.Time { return now },
		Read: func(p string) (any, bool) { v, ok := vals[p]; return v, ok }})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.Evaluate()
	end := t0.Add(span)
	const scan = 100 * time.Millisecond
	for now.Before(end) {
		// The next bounce: gaps from a scan to a few mean gaps, whole scans.
		gap := time.Duration(rng.ExpFloat64()*float64(meanGap)) / scan * scan
		next := now.Add(max(gap, 2*scan))
		for now.Before(next) && now.Before(end) {
			now = now.Add(scan)
			e.Evaluate()
		}
		id := defs[rng.IntN(alarms)].ID
		vals[id] = true
		e.Evaluate()
		now = now.Add(scan)
		vals[id] = false
		e.Evaluate()
	}
	// Run out every episode.
	for range 25 * 60 * 10 {
		now = now.Add(scan)
		e.Evaluate()
	}
	evs, err := j.Query(time.Time{}, time.Time{}, alarm.Filter{Limit: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	for i, k := 0, len(evs)-1; i < k; i, k = i+1, k-1 {
		evs[i], evs[k] = evs[k], evs[i]
	}
	return evs, now
}

// The live chatter rule finds, alarm by alarm, the episodes the report
// finds in the same journal: as many, beginning at the same instants.
func TestLiveChatterMatchesTheReport(t *testing.T) {
	for seed := range uint64(40) {
		evs, _ := liveRun(t, seed, 3, 20*time.Minute, 20*time.Second)
		rep := map[string]Chatter{}
		for _, c := range Chattering(Activations(evs), ChatterWindow, ChatterCount) {
			rep[c.ID] = c
		}
		live := map[string][]alarm.Event{}
		for _, e := range evs {
			if e.Kind == alarm.KindChatter {
				live[e.ID] = append(live[e.ID], e)
			}
		}
		for _, id := range []string{"A0", "A1", "A2"} {
			c, rows := rep[id], live[id]
			if c.Episodes != len(rows) {
				t.Fatalf("seed %d %s: report %d episodes, live %d", seed, id, c.Episodes, len(rows))
			}
			if len(rows) > 0 && rows[0].SinceMs != c.First.UnixMilli() {
				t.Fatalf("seed %d %s: report's first burst at %v, live's at %v", seed, id, c.First, time.UnixMilli(rows[0].SinceMs))
			}
		}
	}
}

// The report's sliding mode finds the floods the live engine journalled,
// with the same start, end, count and peak.
func TestSlidingFloodsMatchLive(t *testing.T) {
	floods := 0
	for seed := range uint64(30) {
		evs, stop := liveRun(t, seed, 20, 2*time.Hour, 50*time.Second)
		rep := SlidingFloods(Activations(evs), t0, stop.Add(time.Millisecond), FloodWindow, FloodThreshold)
		var starts, ends []alarm.Event
		for _, e := range evs {
			switch e.Kind {
			case alarm.KindFlood:
				starts = append(starts, e)
			case alarm.KindFloodEnd:
				ends = append(ends, e)
			}
		}
		if len(rep.Floods) != len(starts) || len(ends) != len(starts) {
			t.Fatalf("seed %d: report %d floods, live %d starts and %d ends", seed, len(rep.Floods), len(starts), len(ends))
		}
		for i, f := range rep.Floods {
			s, e := starts[i], ends[i]
			if f.Start.UnixMilli() != s.TS || f.End.UnixMilli() != e.TS || f.Count != e.Count || f.Peak != e.Peak || e.SinceMs != s.TS {
				t.Fatalf("seed %d flood %d: report %v..%v count %d peak %d; live %v..%v count %d peak %d",
					seed, i, f.Start, f.End, f.Count, f.Peak, time.UnixMilli(s.TS), time.UnixMilli(e.TS), e.Count, e.Peak)
			}
		}
		floods += len(rep.Floods)
	}
	if floods == 0 {
		t.Fatal("no seed made a flood: the test proves nothing")
	}
}
