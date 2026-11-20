package sim

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"
)

// cloneFaults draws a clone schedule (#1029 layer 2), from its own stream:
// one vote link cut both ways for one to ten leases, and a clone of A or B
// booted from its original's disk around then (from two leases before
// the cut to six after) for two to fifteen leases. A third of the
// schedules add a delay within the documented rule or a clock skew within
// ρ. A MAC-spoofed clone brings a copy of its original's voter, so the
// pair has four voters under three names; the relayed promises must keep
// it from ever leading beside the original, whichever link is cut and
// whichever controller leads.
func cloneFaults(seed uint64, o Options) []Fault {
	r := rand.New(rand.NewPCG(seed, 0x636c6f6e65))
	dur := func(lo, hi time.Duration) time.Duration { return lo + time.Duration(r.Int64N(int64(hi-lo)+1)) }
	l := voteLinks[r.IntN(len(voteLinks))]
	cut := Fault{Kind: Cut, At: dur(6*sec, 6*sec+8*lease), For: dur(lease, 10*lease), From: l.x, To: l.y, Both: true}
	clone := Fault{Kind: Clone, At: max(6*sec, cut.At-2*lease+dur(0, 8*lease)), For: dur(2*lease, 15*lease), Node: []Addr{CA, CB}[r.IntN(2)]}
	fs := []Fault{cut, clone}
	switch r.IntN(6) {
	case 0:
		x := voteLinks[r.IntN(len(voteLinks))]
		fs = append(fs, Fault{Kind: Delay, At: dur(4*sec, 6*sec+8*lease), For: dur(lease, 10*lease), From: x.x, To: x.y, Both: true,
			Value: int64(dur(ms, max(ms, lease/4-o.Delay-o.Jitter)))})
	case 1:
		fs = append(fs, Fault{Kind: Skew, At: dur(4*sec, 6*sec+8*lease), For: dur(lease, 10*lease), Node: []Addr{A, B, W}[r.IntN(3)],
			Value: int64(Perfect + PPM(r.Int64N(10_001)-5_000))})
	}
	return fs
}

// TestCloneSeeds runs the clone schedules (#1029 layer 2): never two
// leaders, every oracle but I7 on (cloneChecks), and a leader again within
// the bound once the cut and the clone are gone. QUORUM_CLONE_SEEDS sets the range (as
// QUORUM_SEEDS does for TestSeeds); relay_ignored, relay_voter_rule_off
// and standby_no_probe each turn it red.
func TestCloneSeeds(t *testing.T) {
	env := func(k string) string {
		if k == "QUORUM_SEEDS" {
			return os.Getenv("QUORUM_CLONE_SEEDS")
		}
		return os.Getenv(k)
	}
	runSeeds(t, seedRun{test: "TestCloneSeeds", p: profClone, env: env, checks: cloneChecks})
}

// cloneChecks are checksFor without I7: while a clone runs, the lease may
// pass between the original and its clone (never both at once, I1), and
// the operator is told (redundancy.duplicate-member). I6 and the steady
// state still hold from when the cut and the clone are gone.
func cloneChecks(o Options) Checks {
	c := checksFor(o)
	c.Exclude = append(c.Exclude, "I7")
	return c
}

// relayTakeoverBound is the takeover bound once the voters relay their
// promises (design §4.5 with its R term): the standby campaigns up to
// lease + ρ·lease + lease/4 after the last renewal it saw (C), and the
// witness may still hold the standby's own voter's relayed promise to the
// dead leader for up to MaxRho·lease past its end (rule (b)), refusing
// that campaign once; the retry comes half a look plus the jitter later,
// at most 3·lease/8. Plus lease/40 for the vote rounds: these links are
// 0.2–5 ms (randomRun); a slow link adds a round trip (design §4.5, R).
const relayTakeoverBound = lease + lease/100 + lease/4 + lease/10 + 3*lease/8 + lease/40

// The relayed promises cost a takeover at most the R term: a standby whose
// leader died for good leads within relayTakeoverBound of the death,
// though the witness heard, a moment before, that the standby's own voter
// was promised to that leader (a stale relay ends). It logs the worst
// takeover, and how many runs the stale relay refused a campaign in.
// retry_after_refusal_only turns it red: a refusal heard before its own
// grant then waits out a whole lease.
func TestStaleRelayEndsAndTheStandbyLeads(t *testing.T) {
	var worst time.Duration
	refused := 0
	const n = 300
	for seed := uint64(1); seed <= n; seed++ {
		o := randomRun(seed, profDefault)
		o.Faults = nil
		at := 8*sec + time.Duration(seed)*lease/37 // spread over the renewal phase
		o.Until = at
		gates := Run(o).Gates()
		if len(gates) == 0 || gates[len(gates)-1].To < at {
			t.Fatalf("seed %d: no leader at %v", seed, at)
		}
		dead := gates[len(gates)-1].Node
		o.Faults = []Fault{{Kind: Crash, At: at, Node: dead}}
		o.Until = at + 6*lease
		rec := Run(o)
		var took time.Duration = -1
		for _, g := range rec.Gates() {
			if g.Node != dead && g.From >= at {
				took = g.From - at
				break
			}
		}
		if took < 0 || took > relayTakeoverBound {
			t.Errorf("seed %d: %v died at %v; the other took over after %v (bound %v)", seed, dead, at, took, relayTakeoverBound)
		}
		worst = max(worst, took)
		if rec.relayRefused > 0 {
			refused++
		}
	}
	t.Logf("%d leader deaths: worst takeover %v (bound %v); a stale relay refused a campaign in %d", n, worst, relayTakeoverBound, refused)
}

// TestMeasureCloneAtStart measures the residual the relay leaves (#1029
// layer 2): a MAC-spoofed clone that campaigns at the same time as its
// original, with no promise held yet for the voters to relay, so the two
// can each win one of the other two voters in one election. Three cases,
// each with no other fault:
//   - start: the clone booted together with its original (a clone made
//     before commissioning, or a site-wide power cycle with a clone);
//   - original: a settled pair with a clone running, and the original
//     restarts (a deploy update, a reboot);
//   - peer: the same, and the original's peer restarts.
//
// It logs how many of n runs each had two leaders at once; skipped unless
// QUORUM_MEASURE_CLONE_START=<n>. The follow-up on #1029 must bring all
// three to 0.
//
//	QUORUM_MEASURE_CLONE_START=10000 go test ./quorum/sim -run '^TestMeasureCloneAtStart$' -v
func TestMeasureCloneAtStart(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_CLONE_START"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_CLONE_START=<runs>")
	}
	for _, mode := range []string{"start", "original", "peer"} {
		both := 0
		for seed := uint64(1); seed <= uint64(n); seed++ {
			if overlap(Check(Run(cloneRace(seed, mode)), cloneChecks(cloneRace(seed, mode)))) {
				both++
			}
		}
		t.Logf("a clone racing its original (%s): two leaders at once in %d of %d runs", mode, both, n)
	}
}

// cloneRace is seed's network, disk and clocks with a clone of A or B
// campaigning beside its original: booted with it (start), or running
// when the original (original) or its peer (peer) restarts.
func cloneRace(seed uint64, mode string) Options {
	o := randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x72616365))
	c := []Addr{CA, CB}[r.IntN(2)]
	if mode == "start" {
		o.Faults = []Fault{{Kind: Clone, At: o.Start[original(c)], For: 10 * lease, Node: c}}
		o.Until = o.Start[original(c)] + 12*lease
		return o
	}
	at := 6*sec + time.Duration(r.Int64N(int64(4*lease)))
	down := original(c)
	if mode == "peer" {
		down = peerOf(down)
	}
	crash := at + 2*lease + time.Duration(r.Int64N(int64(6*lease)))
	o.Faults = []Fault{
		{Kind: Clone, At: at, For: 10 * lease, Node: c},
		{Kind: Crash, At: crash, For: 50*ms + time.Duration(r.Int64N(int64(lease))), Node: down},
	}
	o.Until = at + 10*lease + bound + 6*lease
	return o
}
