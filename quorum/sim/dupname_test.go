package sim

import (
	"testing"
	"time"
)

// #1029 step 1: a name heard under two live boots gets no new grant; a
// candidate counts no grant from a duplicated voter; a voter relays a
// name it sees duplicated; a standby probes its peer as well as the
// witness. Each schedule below is one where that mechanism alone stands
// between the pair and two leaders: red when only it is off (found by a
// search over richCloneRun, then shrunk).

func pinned(t *testing.T, o Options) {
	t.Helper()
	rec := Run(o)
	if vs := Check(rec, cloneChecks(o)); len(vs) > 0 {
		t.Fatalf("%v (of %d)", vs[0], len(vs))
	}
}

// A clone of A boots while A is cut from W (one way) and B restarts: B's
// restarted voter sees the clone and A's relayed promise, and only its
// relay of "A is duplicated" stops W granting the clone (dup_not_relayed).
func TestDupRelayStopsTheWitnessGrantingAClone(t *testing.T) {
	pinned(t, Options{Seed: 10750, Delay: 4978687, Jitter: 4365439, Fsync: 3805602, InPlace: false,
		Rates: [nNodes]PPM{1004806, 1001662, 998209}, Start: [3]time.Duration{2531777082, 1960862456, 2457881270}, Until: 35914611305,
		Faults: []Fault{
			{Kind: Clone, At: 11221168112, For: 4621234432, Node: CA},
			{Kind: Cut, At: 10580376673, For: 6334234632, From: A, To: W},
			{Kind: Crash, At: 11525945757, For: 519484630, Node: B},
		}})
}

// A clone of A booted with A, then B–W and A→W cut: the clone wins W's
// promise before W hears both boots, and the running-promise rule renews
// it. Only B's candidate discounting A's (duplicated) voter keeps B from
// leading on {B, A's voter} beside it (dup_voter_counts).
func TestDuplicatedVoterBacksNoLease(t *testing.T) {
	pinned(t, Options{Seed: 4285, Delay: 4546141, Jitter: 3794424, Fsync: 3219668, InPlace: true,
		Rates: [nNodes]PPM{998221, 999120, 1003276}, Start: [3]time.Duration{1708162146, 2733108908, 3074328383}, Until: 43001814536,
		Faults: []Fault{
			{Kind: Clone, At: 1708162146, For: 7779068777, Node: CA},
			{Kind: Cut, At: 5215607855, For: 3356174238, From: A, To: W},
			{Kind: Cut, At: 5036118560, For: 4178025362, From: B, To: W, Both: true},
		}})
}

// A clone of B booted with B, then B→W and A–W cut. The real B, a standby
// cut from W, is heard by A only through its probes of its peer; without
// them no voter sees B's two boots (standby_probes_witness_only), and
// without the refusal a voter that does still grants the clone
// (dup_name_ignored).
func TestStandbyProbesItsPeerSoTheCloneIsSeen(t *testing.T) {
	pinned(t, Options{Seed: 3, Delay: 780446, Jitter: 108322, Fsync: 1114318, InPlace: false,
		Rates: [nNodes]PPM{1002082, 1000701, 998520}, Start: [3]time.Duration{1112996176, 513604735, 3178196971}, Until: 38885497571,
		Faults: []Fault{
			{Kind: Clone, At: 513604735, For: 19371892836, Node: CB},
			{Kind: Cut, At: 4678592333, For: 8817199499, From: B, To: W},
			{Kind: Cut, At: 7972957521, For: 11620032166, From: A, To: W, Both: true},
		}})
}

// leaderAt runs o until t and returns who holds the gate then.
func leaderAt(t *testing.T, o Options, at time.Duration) Addr {
	t.Helper()
	pre := o
	pre.Faults, pre.Until = nil, at
	gates := Run(pre).Gates()
	if len(gates) == 0 || gates[len(gates)-1].To < at {
		t.Fatalf("seed %d: no leader at %v", o.Seed, at)
	}
	return gates[len(gates)-1].Node
}

// A clone of the settled leader boots with every link up. The leader
// keeps its lease the whole time (a running promise to its boot still
// renews): a clone costs redundancy, never a takeover of a healthy leader
// (dup_refuses_incumbent hands the lease over).
func TestCloneOfTheLeaderCostsNoTakeover(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		o := randomRun(seed, profClone)
		at := 8 * sec
		lead := leaderAt(t, o, at)
		c := map[Addr]Addr{A: CA, B: CB}[lead]
		o.Faults = []Fault{{Kind: Clone, At: at, For: 8 * lease, Node: c}}
		o.Until = at + 8*lease
		for _, g := range Run(o).Gates() {
			if g.From >= at && g.Node != lead {
				t.Fatalf("seed %d: %v led from %v while a clone of the leader %v ran", seed, g.Node, g.From, lead)
			}
		}
	}
}

// A running clone costs redundancy, not the leader: whoever the clone
// copies, and whatever restarts or hangs meanwhile, the pair is never
// without a leader for longer than a takeover (bound) while the clone
// runs, and has one again once a takeover's time has passed.
func TestARunningCloneCostsRedundancyNotTheLeader(t *testing.T) {
	for _, mode := range []string{"original", "peer", "unhealthy"} {
		for seed := uint64(1); seed <= 100; seed++ {
			o := cloneRace(seed, mode)
			clone := o.Faults[0]
			gates := Run(o).Gates()
			for _, g := range gaps(gates, clone.At+bound, clone.At+clone.For) {
				if g.To-g.From > bound {
					t.Fatalf("%s seed %d: no leader for %v (from %v) while the clone ran", mode, seed, g.To-g.From, g.From)
				}
			}
		}
	}
}

// A power cycle of the whole pair, with no clone, elects within the
// takeover bound, and no voter counts a duplicate: a real restart's hold
// outlasts its old boot.
func TestPowerCycleLeadsWithinTheBoundAndRaisesNoDuplicate(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		o := randomRun(seed, profClone)
		at := 8*sec + time.Duration(seed)*lease/23
		o.Faults = []Fault{
			{Kind: Crash, At: at, For: 300 * ms, Node: A},
			{Kind: Crash, At: at, For: 300 * ms, Node: B},
			{Kind: Crash, At: at, For: 300 * ms, Node: W},
		}
		o.Until = at + 300*ms + bound + 4*lease
		rec := Run(o)
		for _, a := range []Addr{A, B, W} {
			if n := rec.duplicates[a]; n != 0 {
				t.Fatalf("seed %d: %v counted %d duplicates after a plain power cycle", seed, a, n)
			}
		}
		for _, g := range gaps(rec.Gates(), at+300*ms+bound+lease, o.Until) {
			t.Fatalf("seed %d: no leader over %v after a power cycle", seed, g)
		}
	}
}

// gaps returns the stretches of [from, to) no gate covers.
func gaps(gates []Interval, from, to time.Duration) []Interval {
	var out []Interval
	at := from
	for _, g := range gates {
		if g.To <= at || g.From >= to {
			continue
		}
		if g.From > at {
			out = append(out, Interval{From: at, To: g.From})
		}
		at = max(at, g.To)
	}
	if at < to {
		out = append(out, Interval{From: at, To: to})
	}
	return out
}

// A single controller restarting, with no clone, on links as slow as the
// rule allows: no voter counts a duplicate (its old boot, alive only in a
// relayed promise, refuses the new boot's first campaign at most in that
// promise's margin, where nothing is counted: a relayed promise counts a
// conflict only while it certainly runs, receipt + left - lease/4), and a
// leader is back within the bound. The restart takes at least a one-way
// delay, as a process restart does; one faster than the vote link's
// delay can still be counted once, for a promise granted to its old boot
// after it died (a 3c item, with the sticky duplicate's evidence rule).
func TestASingleRestartRaisesNoDuplicate(t *testing.T) {
	for seed := uint64(1); seed <= 200; seed++ {
		o := randomRun(seed, profClone)
		o.Delay, o.Jitter = lease/5, lease/40 // one way, just under the lease/4 rule
		at := 8*sec + time.Duration(seed)*lease/29
		who := []Addr{A, B}[seed%2]
		o.Faults = []Fault{{Kind: Crash, At: at, For: o.Delay + o.Jitter + time.Duration(seed)*ms, Node: who}}
		o.Until = at + bound + 6*lease
		rec := Run(o)
		for _, a := range []Addr{A, B, W} {
			if n := rec.duplicates[a]; n != 0 {
				t.Fatalf("seed %d: %v counted %d duplicates after %v restarted, with no clone", seed, a, n, who)
			}
		}
		for _, g := range gaps(rec.Gates(), at+bound, o.Until) {
			t.Fatalf("seed %d: no leader over %v after %v restarted", seed, g, who)
		}
	}
}

// cloneStopBound is how soon a real controller leads once the clone is
// stopped, the operator's fix: the clone's boot stays live a lease after
// its last frame, a heard "duplicated" up to a lease and the margin, then
// a takeover (bound).
const cloneStopBound = 2*lease + lease/10 + bound

// Stopping the clone brings a leader back within cloneStopBound, whatever
// the clone race left: the operator's action the alarm asks for.
func TestStoppingTheCloneBringsALeaderBack(t *testing.T) {
	var worst time.Duration
	for _, mode := range []string{"start", "original", "peer", "unhealthy", "rich"} {
		for seed := uint64(1); seed <= 200; seed++ {
			o := cloneRace(seed, mode)
			stop := o.Faults[0].At + o.Faults[0].For
			if heal := healAt(o); heal > stop {
				continue // another fault outlasts the clone: the bound is I6's then
			}
			o.Until = stop + cloneStopBound + 2*lease
			gates := Run(o).Gates()
			back := time.Duration(-1)
			for _, g := range gates {
				if g.To > stop && g.Node != CA && g.Node != CB {
					back = max(g.From, stop) - stop
					break
				}
			}
			if back < 0 || back > cloneStopBound {
				t.Fatalf("%s seed %d: a real controller led %v after the clone stopped (bound %v)", mode, seed, back, cloneStopBound)
			}
			worst = max(worst, back)
		}
	}
	t.Logf("a real controller leads within %v of the clone stopping (bound %v = %.1f leases)", worst, cloneStopBound, float64(cloneStopBound)/float64(lease))
}

// A clone of B reaches A during an A–B cut and wins term 30 there; its
// promise runs out, the cut heals, and the real B asks A for term 30.
// Granting it would give one term to two processes (I4): a term once
// granted to one boot of a name goes to no other (same_term_other_boot).
func TestATermGoesToOneBootOfAName(t *testing.T) {
	pinned(t, Options{Seed: 8657, Delay: 203896, Jitter: 138993, Fsync: 628442, InPlace: false,
		Rates: [nNodes]PPM{995580, 1002135, 1000392}, Start: [3]time.Duration{1801308744, 1698880241, 664275251}, Until: 37700850472,
		Faults: []Fault{
			{Kind: Clone, At: 1698880241, For: 14825025687, Node: CB},
			{Kind: Cut, At: 10409914751, For: 3257641725, From: A, To: W, Both: true},
			{Kind: Delay, At: 9267688312, For: 702532041, From: B, To: W, Both: true, Value: 68955457},
			{Kind: Cut, At: 9467916718, For: 9232933754, From: A, To: B, Both: true},
		}})
}
