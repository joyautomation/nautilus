package sim

import (
	"testing"
	"time"
)

// The 20 schedules the first 200,000-seed nightly failed on (#998), as
// named regression cases, by class (test plan §3.3).

func nightly(t *testing.T, seeds []uint64, run func(t *testing.T, o Options)) {
	t.Helper()
	for _, k := range seeds {
		o, ok := nightlySeeds[k]
		if !ok {
			t.Fatalf("seed %d has no schedule", k)
		}
		t.Run("seed"+itoa(k), func(t *testing.T) { run(t, o) })
	}
}

func itoa(k uint64) string {
	if k == 0 {
		return "0"
	}
	var b []byte
	for ; k > 0; k /= 10 {
		b = append([]byte{byte('0' + k%10)}, b...)
	}
	return string(b)
}

// Class 1, a liveness bug: a voter the majority needs fsyncs in 1.6–2.2 s
// (lease 2 s), so a won campaign's grants leave less of the lease than a
// look, and it lapsed before its first renewal: takeovers flapped (I7).
// Fixed by renewing at once on winning (Candidate.OnReply); each is red
// with the renew_on_look mutation.
func TestNightlySlowFsyncWinner(t *testing.T) {
	nightly(t, []uint64{52814, 129636, 142433, 162345, 176221, 184642, 193654}, func(t *testing.T, o Options) {
		for _, v := range judged(t, Run(o), checksFor(o)) {
			t.Error(v)
		}
	})
}

// Seed 57197 was class 1 too, but B's file is lost (7.1–19.0 s) and the
// witness wiped (from 18.7 s) before B re-commissions: the two-of-three
// case, which needs an operator (design §3.1, §7). Its liveness oracles
// are excused; the safety ones (I1, the lease, the ledger) still hold.
func TestNightlyTwoOfThree(t *testing.T) {
	nightly(t, []uint64{57197}, func(t *testing.T, o Options) {
		rec := Run(o)
		if !rec.TwoOfThreeLost() {
			t.Fatal("not the two-of-three case: move it back to its class")
		}
		for _, v := range Check(rec, checksFor(o)) {
			t.Error(v)
		}
	})
}

// Classes 3 and 4: a node lost its state file, wiped (30099 54172 84269)
// or torn by a crash during the witness's slow in-place write (21198).
// Before the handshake, steady state was judged on A and B alone: a rebuilt
// witness wasn't adopted, and a controller that lost its file lost the
// incarnation it counts (#976). Now the pair re-adopts, so the witness must
// back the leader in steady state too (checksFor's voters, with no
// exclusion), and none of these is the two-of-three case (judged).
func TestNightlyLostFileSteady(t *testing.T) {
	nightly(t, []uint64{21198, 30099, 54172, 84269}, func(t *testing.T, o Options) {
		rec := Run(o)
		if !rec.lostFile() {
			t.Fatal("no node lost its file: the case proves nothing")
		}
		for _, v := range judged(t, rec, checksFor(o)) {
			t.Error(v)
		}
	})
}

// Class 2, beyond the documented rule (lease ≥ 4 × the worst one-way
// vote-link delay): the witness wiped (so the pair depends on A–B) and an
// A–B delay of 0.86–0.96 s each way, lease/2.2. Takeovers may flap there
// (F21, measured); every other oracle, safety and steady state included,
// still holds. The generator no longer draws such delays
// (TestGeneratorDelayWithinRule).
func TestNightlyBeyondRuleDelay(t *testing.T) {
	nightly(t, []uint64{14523, 38969, 47591, 63039, 89414, 153106, 157046}, func(t *testing.T, o Options) {
		beyond := false
		for _, f := range o.Faults {
			if f.Kind == Delay && o.Delay+o.Jitter+time.Duration(f.Value) > lease/4 {
				beyond = true
			}
		}
		if !beyond {
			t.Fatal("no delay beyond lease/4: the case is misfiled")
		}
		c := checksFor(o)
		c.Exclude = []string{"I7"}
		for _, v := range judged(t, Run(o), c) {
			t.Error(v)
		}
	})
}

// Beyond the documented loss rule (test plan F20): seed 260567 of an extra
// sample on #999 wipes the witness (so A–B is the sole vote path) and drops
// 27% of A–B both ways for 6.6 leases. A renewal round then fails about
// half the time and takeovers flap (I7): measured, not guaranteed, above
// maxSoleLoss. Every other oracle, safety and steady state included, still
// holds. The generator no longer draws such loss (TestGeneratorLossWithinRule).
func TestNightlyBeyondRuleLoss(t *testing.T) {
	nightly(t, []uint64{260567}, func(t *testing.T, o Options) {
		beyond := false
		for i, f := range o.Faults {
			if f.Kind == Loss && f.Value > maxSoleLoss && solePath(o, i) {
				beyond = true
			}
		}
		if !beyond {
			t.Fatal("no loss beyond maxSoleLoss on a sole vote path: the case is misfiled")
		}
		rec := Run(o)
		all := checksFor(o)
		for _, v := range judged(t, rec, all) {
			if v.Oracle == "I7" {
				t.Logf("measured: %v", v)
			}
		}
		c := all
		c.Exclude = []string{"I7"}
		for _, v := range judged(t, rec, c) {
			t.Error(v)
		}
	})
}

// Seed 61535 failed steady state on main because a crash during the
// witness's slow in-place write tore its file (class 4). Renewing at once
// shifts the run's timing so the crash no longer lands mid-write; with the
// renew_on_look mutation (main's timing) the file tears again and the
// lost-file rule carries it. Either way every oracle holds.
func TestNightlyTornWitnessTimingShifted(t *testing.T) {
	nightly(t, []uint64{61535}, func(t *testing.T, o Options) {
		for _, v := range judged(t, Run(o), checksFor(o)) {
			t.Error(v)
		}
	})
}

// The generator keeps every vote-link delay within the documented rule:
// base + jitter + fault ≤ lease/4.
func TestGeneratorDelayWithinRule(t *testing.T) {
	for seed := uint64(1); seed <= 20000; seed++ {
		o := randomRun(seed, profDefault)
		for _, f := range o.Faults {
			if f.Kind == Delay && o.Delay+o.Jitter+time.Duration(f.Value) > lease/4 {
				t.Fatalf("seed %d draws %v + %v + %v > lease/4", seed, o.Delay, o.Jitter, time.Duration(f.Value))
			}
		}
	}
}

// The generator keeps loss on a link that may be the sole vote path within
// the documented rule (maxSoleLoss), and still draws loss up to 30% where
// the third voter stays counted.
func TestGeneratorLossWithinRule(t *testing.T) {
	high := 0
	for seed := uint64(1); seed <= 20000; seed++ {
		o := randomRun(seed, profDefault)
		for i, f := range o.Faults {
			if f.Kind != Loss {
				continue
			}
			if solePath(o, i) && f.Value > maxSoleLoss {
				t.Fatalf("seed %d draws %d%% loss on a sole vote path: %#v", seed, f.Value, f)
			}
			if f.Value > maxSoleLoss {
				high++
			}
		}
	}
	if high == 0 {
		t.Fatal("no loss above maxSoleLoss drawn at all: the cap is too wide")
	}
}

// solePath judges a loss by the third voter's faults.
func TestSolePath(t *testing.T) {
	loss := Fault{Kind: Loss, From: A, To: B, Both: true, Value: 27}
	for _, c := range []struct {
		other Fault
		want  bool
	}{
		{Fault{Kind: Wipe, Node: W}, true},
		{Fault{Kind: Crash, Node: W}, true},
		{Fault{Kind: SlowDisk, Node: W}, true},
		{Fault{Kind: Cut, From: B, To: W}, true},
		{Fault{Kind: Loss, From: W, To: A}, true},
		{Fault{Kind: Crash, Node: A}, false},
		{Fault{Kind: Skew, Node: W}, false},
		{Fault{Kind: Duplicate, From: A, To: W}, false},
		{Fault{Kind: Start, Node: W2}, false},
	} {
		o := Options{Faults: []Fault{loss, c.other}}
		if got := solePath(o, 0); got != c.want {
			t.Errorf("A–B loss with %#v: sole path %v, want %v", c.other, got, c.want)
		}
	}
	o := Options{Faults: []Fault{{Kind: Loss, From: A, To: W, Value: 9}, {Kind: Crash, Node: B}}}
	if !solePath(o, 0) {
		t.Error("A–W loss with B down: not judged a sole path")
	}
}
