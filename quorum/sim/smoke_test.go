package sim

import (
	"testing"
	"time"
)

func TestSmokeNoFaults(t *testing.T) {
	rec := Run(Options{Seed: 1, Delay: time.Millisecond, Fsync: time.Millisecond, Until: 20 * time.Second, Trace: true})
	g := rec.Gates()
	for _, iv := range g {
		t.Log(iv)
	}
	for _, v := range Check(rec, Checks{MaxTakeovers: 0}) {
		t.Error(v)
	}
	if len(g) == 0 {
		for _, l := range rec.Trace[:min(60, len(rec.Trace))] {
			t.Log(l)
		}
		t.Fatal("nobody led")
	}
}

// A skew that starts or heals while a clock is frozen takes effect at the
// thaw: the frozen clock stays stopped, then runs at the rate in force.
func TestSkewDuringFreeze(t *testing.T) {
	o := Options{Seed: 1, Delay: time.Millisecond, Fsync: time.Millisecond, Until: 20 * time.Second,
		Faults: []Fault{
			{Kind: Freeze, At: 4 * time.Second, For: 2 * time.Second, Node: W},
			{Kind: Skew, At: 3 * time.Second, For: 2 * time.Second, Node: W, Value: 1_005_000}, // heals frozen
			{Kind: Freeze, At: 8 * time.Second, For: 2 * time.Second, Node: B},
			{Kind: Skew, At: 9 * time.Second, Node: B, Value: 995_000}, // starts frozen
		}}
	rec := Run(o)
	if r := rec.clocks[W].Rate(); r != Perfect {
		t.Errorf("W runs at %d after the thaw, want %d (its skew healed while frozen)", r, Perfect)
	}
	if r := rec.clocks[B].Rate(); r != 995_000 {
		t.Errorf("B runs at %d after the thaw, want 995000 (its skew started while frozen)", r)
	}
}

// I6 holds over the whole span after heal + bound: a leaderless gap later
// on fails it, not only one at the bound itself.
func TestI6IsContinuous(t *testing.T) {
	s := time.Second
	gates := []Interval{{Node: A, Term: 1, From: 0, To: 10 * s}, {Node: B, Term: 2, From: 11 * s, To: 20 * s}}
	if vs := checkI6(gates, 2*s, 3*s, 20*s); len(vs) != 1 || vs[0].At != 10*s {
		t.Fatalf("a gap at 10–11 s: %v, want one I6 at 10 s", vs)
	}
	gates[1].From = 10 * s
	if vs := checkI6(gates, 2*s, 3*s, 20*s); len(vs) != 0 {
		t.Fatalf("no gap: %v", vs)
	}
}
