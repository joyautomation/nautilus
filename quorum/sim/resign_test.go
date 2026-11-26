package sim

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// A planned switchover's RESIGN (#710 d1) in the simulator, under the
// oracles: I1, I2, I4, the lease, the ledger (a taken RESIGN ends that
// promise), I3's resign case and "resign" (a voter takes a RESIGN only
// for the boot's latest leadership).

// resignRun runs a pair with A leading from the start (B starts later),
// the operator's switchover asked of both controllers at each of at (the
// one that leads resigns), and extra faults.
func resignRun(seed uint64, at []time.Duration, extra []Fault, until time.Duration) Options {
	o := Options{Seed: seed, Delay: 1 * ms, Jitter: 2 * ms, Fsync: 2 * ms, Until: until, Trace: true,
		Start: [3]time.Duration{0, 5 * sec, 0}}
	for _, t := range at {
		o.Faults = append(o.Faults, Fault{Kind: Resign, At: t, Node: A}, Fault{Kind: Resign, At: t, Node: B})
	}
	o.Faults = append(o.Faults, extra...)
	return o
}

// firstLead is when the other controller than from first leads at or
// after t, and in what epoch; 0 when it never does.
func firstLead(rec *Record, from Addr, t time.Duration) (time.Duration, uint64) {
	for _, ls := range rec.LeadStarts {
		if ls.Node != from && ls.At >= t {
			return ls.At, ls.Epoch
		}
	}
	return 0, 0
}

// Clean links: every switchover hands the lead to the standby within
// lease/4 of the resign, hot, and back again, five times over 100 seeds.
func TestSimResignHandsOverHotWithinAQuarterLease(t *testing.T) {
	const lease = 2 * sec
	var worst time.Duration
	for seed := uint64(1); seed <= 100; seed++ {
		var at []time.Duration
		for i := 0; i < 5; i++ {
			at = append(at, 12*sec+time.Duration(i)*3*lease)
		}
		rec := Run(resignRun(seed, at, nil, 12*sec+16*lease))
		clean(t, rec, Checks{MaxTakeovers: -1})
		if len(rec.Resigns) != len(at) {
			t.Fatalf("seed %d: %d resigns, want %d", seed, len(rec.Resigns), len(at))
		}
		for _, rs := range rec.Resigns {
			when, epoch := firstLead(rec, rs.Node, rs.At)
			if when == 0 || when-rs.At > lease/4 {
				t.Fatalf("seed %d: after %v resigned at %v, the other led at %v", seed, rs.Node, rs.At, when)
			}
			worst = max(worst, when-rs.At)
			hot := false
			for _, tk := range rec.Takeovers {
				if tk.At == when && tk.Epoch == epoch {
					hot = tk.Hot
					if !hot {
						t.Fatalf("seed %d: the switchover at %v went cold: %s", seed, rs.At, tk.Why)
					}
				}
			}
			if !hot {
				t.Fatalf("seed %d: no hot takeover judged for the switchover at %v", seed, rs.At)
			}
		}
	}
	t.Logf("slowest handover: %v (lease/4 = %v)", worst, lease/4)
}

// Link faults around the switchover: the RESIGN or the campaign after it
// lost, delayed, duplicated or replayed, on any link. The oracles hold,
// and the pair has a leader again within the fallback (the lease, then
// the old leader's hold).
func TestSimResignUnderLinkFaults(t *testing.T) {
	const lease = 2 * sec
	const at = 12 * sec
	links := [][2]Addr{{A, W}, {W, A}, {A, B}, {B, A}, {B, W}, {W, B}}
	fallbacks := 0
	for seed := uint64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewPCG(seed, 710))
		var extra []Fault
		for k := 0; k < 1+r.IntN(2); k++ {
			l := links[r.IntN(len(links))]
			f := Fault{From: l[0], To: l[1], At: at - lease/2 + time.Duration(r.Int64N(int64(lease))), For: time.Duration(r.Int64N(int64(lease)))}
			switch r.IntN(4) {
			case 0:
				f.Kind, f.Value = Loss, int64(30+r.IntN(71))
			case 1:
				f.Kind, f.Value = Delay, r.Int64N(int64(lease/2))
			case 2:
				f.Kind, f.Value = Duplicate, r.Int64N(int64(lease))
			default:
				f.Kind, f.Value = Replay, r.Int64N(int64(2*lease))
			}
			extra = append(extra, f)
		}
		rec := Run(resignRun(seed, []time.Duration{at}, extra, at+12*lease))
		last := at
		for _, f := range extra {
			last = max(last, f.At+f.For)
		}
		vs := judged(t, rec, Checks{MaxTakeovers: -1, HealAt: last, Bound: 4 * lease})
		if len(vs) > 0 {
			t.Errorf("seed %d, faults %#v:", seed, extra)
			for _, v := range vs {
				t.Error(v)
			}
			t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
			t.FailNow()
		}
		if len(rec.Resigns) == 1 {
			if when, _ := firstLead(rec, rec.Resigns[0].Node, at); when == 0 || when-at > lease/4 {
				fallbacks++
			}
		}
	}
	t.Logf("%d of 300 faulted switchovers fell back to the lease or the hold", fallbacks)
}

// A RESIGN of an earlier leadership of the same boot, arriving in the
// next: A's RESIGN to the witness is held up on the link (the witness
// never took it; B's voter does, and B wins on the two controllers'
// voters), B then resigns (A leads
// again, same boot, a higher epoch), and A's old RESIGN reaches the
// witness only then. The witness must refuse it: its promise backs A's
// new leadership: it has heard A's later campaign or renewals ("resign").
// (A copy of a RESIGN the witness already took
// is answered as a duplicate and changes nothing.) Two rules refuse it
// here, the epoch and the Seq gate (A campaigned and renewed after it), so
// no single mutation turns it red; each has its unit test.
func TestSimALateResignOfAnEarlierLeadershipIsRefused(t *testing.T) {
	const lease = 2 * sec
	t1, t2 := 12*sec, 12*sec+3*lease
	for seed := uint64(1); seed <= 20; seed++ {
		late := Fault{Kind: Delay, From: A, To: W, At: t1 - 5*ms, For: 10 * ms, Value: int64(t2 - t1 + lease/2)}
		rec := Run(resignRun(seed, []time.Duration{t1, t2}, []Fault{late}, t2+6*lease))
		clean(t, rec, Checks{MaxTakeovers: -1})
		if len(rec.Resigns) != 2 || rec.Resigns[0].Node != A || rec.Resigns[1].Node != B {
			t.Fatalf("seed %d: resigns %+v, want A then B", seed, rec.Resigns)
		}
		if rec.Resigns[0].Epoch >= lastEpochOf(rec, A) {
			t.Fatalf("seed %d: A led no later epoch than the one it resigned", seed)
		}
	}
}

func lastEpochOf(rec *Record, a Addr) uint64 {
	var e uint64
	for _, ls := range rec.LeadStarts {
		if ls.Node == a {
			e = max(e, ls.Epoch)
		}
	}
	return e
}

// An old copy of a RESIGN must not make a controller that just resigned
// campaign again (Fable on #1355): A hands the lead to B, B hands it back
// with B→A cut (so A's voter never takes B's RESIGN), and A's first
// RESIGN reaches B's voter again 20 ms after B resigned. That copy is a
// duplicate: B's voter answers it as taken but B does nothing, so B leads
// again only after its hold or once A led ("rejoin"). resign_dup_acts
// turns it red.
func TestSimAnOldResignCopyDoesNotWakeTheResigner(t *testing.T) {
	const lease = 2 * sec
	t1, t2 := 12*sec, 18*sec
	for seed := uint64(1); seed <= 20; seed++ {
		extra := []Fault{
			{Kind: Replay, From: A, To: B, At: t1, For: 300 * ms, Value: int64(t2 - t1 + 20*ms)},
			{Kind: Cut, From: B, To: A, At: t2 - 5*ms, For: 4 * lease},
		}
		rec := Run(resignRun(seed, []time.Duration{t1, t2}, extra, t2+6*lease))
		clean(t, rec, Checks{MaxTakeovers: -1})
		if len(rec.Resigns) != 2 || rec.Resigns[1].Node != B {
			t.Fatalf("seed %d: resigns %+v, want A then B", seed, rec.Resigns)
		}
	}
}

// A resign hold ended early (an EndHold: the resigner said yes to its
// peer's hand-back, #710 d1d) at a random moment after a switchover, under
// random link faults: the resigner may campaign at once, against a peer
// taking over, and no oracle breaks (I1-I4, the lease, the ledger, rejoin).
func TestSimAnEarlyHoldEndIsSafe(t *testing.T) {
	const lease = 2 * sec
	const at = 12 * sec
	links := [][2]Addr{{A, W}, {W, A}, {A, B}, {B, A}, {B, W}, {W, B}}
	ended, early := 0, 0
	for seed := uint64(1); seed <= 200; seed++ {
		r := rand.New(rand.NewPCG(seed, 7104))
		// Mostly within 30 ms of the resign: the hold ends while
		// the peer is still taking over, the case a hand-back makes.
		end := at + time.Duration(r.Int64N(int64(30*ms)))
		if seed%4 == 0 {
			end = at + time.Duration(r.Int64N(int64(2*lease)))
		}
		extra := []Fault{{Kind: EndHold, At: end, Node: A}, {Kind: EndHold, At: end, Node: B}}
		last := end
		for k := 0; k < r.IntN(3); k++ {
			l := links[r.IntN(len(links))]
			f := Fault{From: l[0], To: l[1], At: at - lease/2 + time.Duration(r.Int64N(int64(lease))), For: time.Duration(r.Int64N(int64(lease)))}
			switch r.IntN(3) {
			case 0:
				f.Kind, f.Value = Loss, int64(30+r.IntN(71))
			case 1:
				f.Kind, f.Value = Delay, r.Int64N(int64(lease/2))
			default:
				f.Kind, f.Value = Cut, 0
			}
			extra = append(extra, f)
			last = max(last, f.At+f.For)
		}
		rec := Run(resignRun(seed, []time.Duration{at}, extra, at+12*lease))
		ended += len(rec.HoldEnds)
		if len(rec.HoldEnds) > 0 && len(rec.Resigns) > 0 {
			if peer, _ := firstLead(rec, rec.Resigns[0].Node, at); peer == 0 || rec.HoldEnds[0].At < peer {
				early++ // the hold ended before the peer led: the resigner may campaign against its takeover
			}
		}
		vs := judged(t, rec, Checks{MaxTakeovers: -1, HealAt: last, Bound: 4 * lease})
		if len(vs) > 0 {
			t.Errorf("seed %d, faults %#v:", seed, extra)
			for _, v := range vs {
				t.Error(v)
			}
			t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
			t.FailNow()
		}
	}
	if ended < 50 {
		t.Fatalf("only %d of 200 runs ended a resign hold early: the fault is not exercised", ended)
	}
	t.Logf("%d of 200 runs ended a resign hold early, %d before the peer led", ended, early)
	if early < 40 {
		t.Fatalf("only %d of 200 runs ended the hold before the peer led: the case a hand-back makes is not exercised", early)
	}
}

// The rejoin oracle judges a resign hold where it runs: on the resigner's
// own clock, in its own process (#710 d1d). A resigner whose clock runs
// fast, or that restarts, may lead again sooner than two leases of the
// simulation's time, with its peer cut off, and that is no violation.
func TestSimRejoinJudgesTheHoldOnTheResignersClockAndBoot(t *testing.T) {
	const lease = 2 * sec
	const at = 12 * sec
	cutB := []Fault{{Kind: Crash, Node: B, At: at + ms, For: 10 * lease}} // the peer goes down just after the resign
	for _, c := range []struct {
		name  string
		extra []Fault
	}{
		{"a fast clock", []Fault{{Kind: Skew, Node: A, At: at - lease, For: 10 * lease, Value: 1_009_000}}}, // 0.9% fast, inside rho
		{"a restart", []Fault{{Kind: Crash, Node: A, At: at + lease/4, For: lease / 4}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := Run(resignRun(1, []time.Duration{at}, append(append([]Fault{}, cutB...), c.extra...), at+8*lease))
			if len(rec.Resigns) != 1 || rec.Resigns[0].Node != A {
				t.Fatalf("resigns %+v, want A's", rec.Resigns)
			}
			again, _ := firstLeadOf(rec, A, at)
			if again == 0 || again-at >= 2*lease {
				t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
				t.Fatalf("A led again at %v after its resign, want sooner than two leases of simulation time: the case proves nothing", again-at)
			}
			for _, v := range Check(rec, Checks{MaxTakeovers: -1, HealAt: at + 10*lease, Bound: 4 * lease}) {
				if v.Oracle == "rejoin" {
					t.Errorf("%v", v)
				}
			}
		})
	}
}

// firstLeadOf is when controller n first leads after t, and in what epoch.
func firstLeadOf(rec *Record, n Addr, t time.Duration) (time.Duration, uint64) {
	for _, ls := range rec.LeadStarts {
		if ls.Node == n && ls.At > t {
			return ls.At, ls.Epoch
		}
	}
	return 0, 0
}
