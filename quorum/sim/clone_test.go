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

// TestCloneRaceSeeds runs the clone races #1079 left open and #1029
// step 1 closes: a MAC-spoofed clone running while the lease changes
// hands, with no other fault, in four cases:
//   - start: the clone booted together with its original (a clone made
//     before commissioning, or a site-wide power cycle with a clone);
//   - original: a settled pair with a clone running, and the original
//     restarts (a deploy update, a reboot);
//   - peer: the same, and the original's peer restarts;
//   - unhealthy: the same, and the leader's scans hang for one to four
//     leases (its candidate stops renewing; its voter keeps answering).
//
// Then the rich clone schedules (richCloneRun): any of those, plus cuts,
// restarts, unhealthy leaders, delays and loss. Every oracle but I7 on
// (cloneChecks). Before step 1, two leaders in 172, 184, 17 and 227 of
// 10,000 runs of the four cases. QUORUM_CLONE_RACE_SEEDS=<n> runs n per
// case (default 300; the nightly runs 20,000).
func TestCloneRaceSeeds(t *testing.T) {
	n := 300
	if v, err := strconv.Atoi(os.Getenv("QUORUM_CLONE_RACE_SEEDS")); err == nil && v > 0 {
		n = v
	}
	for _, mode := range []string{"start", "original", "peer", "unhealthy", "rich"} {
		failing := 0
		for seed := uint64(1); seed <= uint64(n); seed++ {
			o := cloneRace(seed, mode)
			vs := Check(Run(o), cloneChecks(o))
			if len(vs) == 0 {
				continue
			}
			failing++
			if failing <= 3 {
				t.Errorf("%s seed %d: %v\n  schedule: %#v", mode, seed, vs[0], o)
			}
		}
		t.Logf("clone race %s: %d of %d failing", mode, failing, n)
	}
}

// richCloneRun draws a clone schedule with extra faults: a clone of A or
// B booted with its original or later, and one to three of: a cut (one
// way or both), a restart of any member, an unhealthy controller, a delay
// within the rule, or loss. It never isolates the original while its
// clone runs: that split is outside the claim, and measured on its own.
func richCloneRun(seed uint64) Options {
	o := randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x7a7a7a))
	dur := func(lo, hi time.Duration) time.Duration { return lo + time.Duration(r.Int64N(int64(hi-lo)+1)) }
	c := []Addr{CA, CB}[r.IntN(2)]
	at := []time.Duration{o.Start[original(c)], dur(4*sec, 12*sec)}[r.IntN(2)]
	o.Faults = []Fault{{Kind: Clone, At: at, For: dur(2*lease, 15*lease), Node: c}}
	for range 1 + r.IntN(3) {
		f := Fault{At: dur(4*sec, 14*sec), For: dur(lease/4, 6*lease)}
		switch r.IntN(6) {
		case 0, 1:
			l := voteLinks[r.IntN(3)]
			f.Kind, f.From, f.To, f.Both = Cut, l.x, l.y, r.IntN(2) == 0
		case 2:
			f.Kind, f.Node, f.For = Crash, []Addr{A, B, W}[r.IntN(3)], dur(50*ms, 2*lease)
		case 3:
			f.Kind, f.Node = Unhealthy, []Addr{A, B}[r.IntN(2)]
		case 4:
			l := voteLinks[r.IntN(3)]
			f.Kind, f.From, f.To, f.Both, f.Value = Delay, l.x, l.y, true, int64(dur(ms, lease/8))
		case 5:
			l := voteLinks[r.IntN(3)]
			f.Kind, f.From, f.To, f.Both, f.Value = Loss, l.x, l.y, true, int64(1+r.IntN(20))
		}
		o.Faults = append(o.Faults, f)
	}
	// Within the claim: no split separates the clone from its original. A
	// cut or restart that would leave the original unable to reach both
	// other voters while the clone runs becomes a delay instead
	// (TestCloneSplitNoTwoLeaders is that case).
	for i := 1; i < len(o.Faults); i++ {
		if isolatesWithClone(o, i) {
			orig := original(c)
			o.Faults[i] = Fault{Kind: Delay, At: o.Faults[i].At, For: o.Faults[i].For, From: orig, To: W, Both: true, Value: int64(dur(ms, lease/8))}
		}
	}
	o.Until = healAt(o) + bound + 6*lease
	return o
}

// isolatesWithClone reports whether fault i, with the others, leaves the
// clone's original unable to reach both its peer and the witness (a cut
// of the link, or the other end down) at some instant while the clone
// runs.
func isolatesWithClone(o Options, i int) bool {
	c := o.Faults[0]
	orig := original(c.Node)
	cuts := func(f Fault, x, y Addr) bool {
		return (f.Kind == Cut && ((f.From == x && f.To == y) || (f.From == y && f.To == x))) ||
			(f.Kind == Crash && f.Node == y)
	}
	f := o.Faults[i]
	for j, g := range o.Faults[1:] {
		if j+1 == i {
			continue
		}
		for _, pair := range [][2]Fault{{f, g}, {g, f}} {
			a, b := pair[0], pair[1]
			if !(cuts(a, orig, peerOf(orig)) && cuts(b, orig, W)) {
				continue
			}
			from := max(a.At, b.At, c.At)
			to := min(a.At+a.For, b.At+b.For, c.At+c.For)
			if from < to {
				return true
			}
		}
	}
	return false
}

// cloneRace is seed's network, disk and clocks with a clone of A or B
// campaigning beside its original: booted with it (start), or running
// when the original (original) or its peer (peer) restarts, or the leader
// goes unhealthy (unhealthy); "rich" is richCloneRun.
func cloneRace(seed uint64, mode string) Options {
	if mode == "rich" {
		return richCloneRun(seed)
	}
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
	if mode == "unhealthy" {
		pre := o
		pre.Faults = []Fault{{Kind: Clone, At: at, For: 10 * lease, Node: c}}
		pre.Until = crash
		gates := Run(pre).Gates()
		leader := A
		if len(gates) > 0 {
			leader = gates[len(gates)-1].Node
		}
		o.Faults = []Fault{
			pre.Faults[0],
			{Kind: Unhealthy, At: crash, For: lease + time.Duration(r.Int64N(int64(3*lease))), Node: leader},
		}
		o.Until = at + 10*lease + bound + 6*lease
		return o
	}
	o.Faults = []Fault{
		{Kind: Clone, At: at, For: 10 * lease, Node: c},
		{Kind: Crash, At: crash, For: 50*ms + time.Duration(r.Int64N(int64(lease))), Node: down},
	}
	o.Until = at + 10*lease + bound + 6*lease
	return o
}

// splitShapes are the cuts that keep a voter from hearing both copies of
// a name while a clone runs, the case #1029 step 1 left open:
//   - The two splits: the original with the witness and the clone with
//     the peer ({A, W} | {B, clone}), or the mirror ({A, B} | {clone, W}).
//     Once the other boot ages out (a lease), no voter on either side
//     sees two, and each side holds two of the three names, the
//     duplicated voter counting on both.
//   - The #1094 review's shapes (a clone of A, cuts from 8 s + 2-5
//     leases for 8 leases): a voter forgets a boot after a lease of
//     silence, so the overlap can also start when a split HEALS, the
//     first copy back getting a grant: {A-B, clone-B, clone-W, B-W},
//     {A-W, clone-B, clone-W, B-W}, {A-B, A-W, clone-B}, {A-W, clone-B}.
func splitShapes() []struct {
	name string
	run  func(seed uint64) Options
} {
	out := []struct {
		name string
		run  func(seed uint64) Options
	}{
		{"original with the witness", func(seed uint64) Options { return cloneSplit(seed, false) }},
		{"original with its peer", func(seed uint64) Options { return cloneSplit(seed, true) }},
	}
	for _, sh := range reviewSplitShapes {
		cuts := sh.cuts
		out = append(out, struct {
			name string
			run  func(seed uint64) Options
		}{"clone of A, cuts " + sh.name, func(seed uint64) Options { return reviewSplit(seed, cuts) }})
	}
	return out
}

// splitSeeds is how many seeds each split test runs per shape: 200, or
// QUORUM_CLONE_SPLIT_SEEDS (the nightly runs more).
func splitSeeds() uint64 {
	if v, err := strconv.Atoi(os.Getenv("QUORUM_CLONE_SPLIT_SEEDS")); err == nil && v > 0 {
		return uint64(v)
	}
	return 200
}

// Every voter that heard both copies running marks the name for good
// (#1029 3c), so a split that later separates them, or heals one copy
// back first, leaves no voter granting the name anew on either side: no
// two leaders in any shape, and every oracle but I7 holds (cloneChecks:
// a leader is back once the cut has healed and the clone is gone).
// Before the mark, two leaders in every run of both splits and in 126,
// 306, 58 and 13 of 2000 of the heal shapes. The cost, stated in the
// guide: during the split itself usually NOBODY leads (both splits and
// the two shapes that cut B from W). dup_mark_off turns it red.
func TestCloneSplitNoTwoLeaders(t *testing.T) {
	n := splitSeeds()
	for _, sh := range splitShapes() {
		failing := 0
		for seed := uint64(1); seed <= n; seed++ {
			o := sh.run(seed)
			if vs := Check(Run(o), cloneChecks(o)); len(vs) > 0 {
				failing++
				if failing <= 3 {
					t.Errorf("%s, seed %d: %v", sh.name, seed, vs[0])
				}
			}
		}
		if failing > 0 {
			t.Errorf("%s: %d of %d failing", sh.name, failing, n)
		}
	}
}

// A voter that restarts inside such a split forgets its marks (they are
// in memory). One restart can't give two leaders: each side of a split
// holds one of the two voters other than the clone's original, and only
// the side whose voter forgot can grant the name anew. Both restarting
// can: then the clone's own candidate is what keeps it quiet, holding on
// to the far voter's last "marked" (Candidate.NoteMark), which no reply
// can clear across the cut. Without that hold, two leaders in all but one
// of 1000 runs of each split. The case left open (both voters AND the
// clone restart inside one split) is why durable marks stay on the list
// before experimental is lifted. Since 3c-2 the copy goes spare about 3.4
// s after it starts, before these splits (two to five leases on), so the
// test also runs each split one to two seconds after the clone's start:
// after the mark, before the spare can decide, where the hold alone keeps
// the clone quiet (both restarting: two leaders in 200 of 200 runs under
// marked_not_told or marked_still_campaigns).
func TestCloneSplitVoterRestarts(t *testing.T) {
	n := splitSeeds()
	for _, early := range []bool{false, true} {
		for _, mirror := range []bool{false, true} {
			for _, who := range []string{"peer", "witness", "both"} {
				failing := 0
				for seed := uint64(1); seed <= n; seed++ {
					o := cloneSplit(seed, mirror)
					c := o.Faults[0].Node
					if early {
						r := rand.New(rand.NewPCG(seed, 0x65617279))
						at := o.Faults[0].At + sec + time.Duration(r.Int64N(int64(sec)))
						for i := range o.Faults {
							if o.Faults[i].Kind == Cut {
								o.Faults[i].At = at
							}
						}
					}
					cut := o.Faults[1].At
					peer := peerOf(original(c))
					r := rand.New(rand.NewPCG(seed, 0x72737472))
					down := cut + lease + time.Duration(r.Int64N(int64(4*lease)))
					switch who {
					case "peer":
						o.Faults = append(o.Faults, Fault{Kind: Crash, At: down, For: 100 * ms, Node: peer})
					case "witness":
						o.Faults = append(o.Faults, Fault{Kind: Crash, At: down, For: 100 * ms, Node: W})
					default:
						o.Faults = append(o.Faults,
							Fault{Kind: Crash, At: down, For: 100 * ms, Node: W},
							Fault{Kind: Crash, At: down + lease/3, For: 100 * ms, Node: peer})
					}
					if overlap(Check(Run(o), cloneChecks(o))) {
						failing++
						if failing <= 3 {
							t.Errorf("early %v, mirror %v, %s restarts, seed %d: two leaders", early, mirror, who, seed)
						}
					}
				}
				if failing > 0 {
					t.Errorf("early %v, mirror %v, %s restarts: two leaders in %d of %d", early, mirror, who, failing, n)
				}
			}
		}
	}
}

// reviewSplitShapes are the #1094 review's cut sets, for a clone of A.
var reviewSplitShapes = []struct {
	name string
	cuts [][2]Addr
}{
	{"{A-B, clone-B, clone-W, B-W}", [][2]Addr{{A, B}, {CA, B}, {CA, W}, {B, W}}},
	{"{A-W, clone-B, clone-W, B-W}", [][2]Addr{{A, W}, {CA, B}, {CA, W}, {B, W}}},
	{"{A-B, A-W, clone-B}", [][2]Addr{{A, B}, {A, W}, {CA, B}}},
	{"{A-W, clone-B}", [][2]Addr{{A, W}, {CA, B}}},
}

// reviewSplit is the #1094 review's recipe: seed's network, disk and
// clocks, a clone of A from 8 s for 12 leases, and cuts (both ways) from
// 8 s + 2-5 leases for 8 leases.
func reviewSplit(seed uint64, cuts [][2]Addr) Options {
	o := randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x1234))
	at := 8 * sec
	cut := at + 2*lease + time.Duration(r.Int64N(int64(3*lease)))
	o.Faults = []Fault{{Kind: Clone, At: at, For: 12 * lease, Node: CA}}
	for _, l := range cuts {
		o.Faults = append(o.Faults, Fault{Kind: Cut, At: cut, For: 8 * lease, From: l[0], To: l[1], Both: true})
	}
	o.Until = at + 12*lease + bound + 6*lease
	return o
}

// cloneSplit is seed's network, disk and clocks with a clone of A or B
// running from 8 s, then a split two to five leases later that puts the
// original with the witness and the clone with the peer (or, mirror, the
// original with its peer and the clone with the witness), for eight
// leases.
func cloneSplit(seed uint64, mirror bool) Options {
	o := randomRun(seed, profClone)
	r := rand.New(rand.NewPCG(seed, 0x73706c6974))
	at := 8 * sec
	c := []Addr{CA, CB}[r.IntN(2)]
	orig, peer := original(c), peerOf(original(c))
	cut := at + 2*lease + time.Duration(r.Int64N(int64(3*lease)))
	o.Faults = []Fault{
		{Kind: Clone, At: at, For: 12 * lease, Node: c},
		{Kind: Cut, At: cut, For: 8 * lease, From: orig, To: peer, Both: true},
		{Kind: Cut, At: cut, For: 8 * lease, From: peer, To: W, Both: true},
		{Kind: Cut, At: cut, For: 8 * lease, From: c, To: W, Both: true},
	}
	if mirror {
		o.Faults[1] = Fault{Kind: Cut, At: cut, For: 8 * lease, From: orig, To: W, Both: true}
		o.Faults[3] = Fault{Kind: Cut, At: cut, For: 8 * lease, From: c, To: peer, Both: true}
	}
	o.Until = at + 12*lease + bound + 6*lease
	return o
}

// swapAB mirrors a schedule's controllers: A and B, CA and CB.
func swapAB(a Addr) Addr {
	switch a {
	case A:
		return B
	case B:
		return A
	case CA:
		return CB
	case CB:
		return CA
	}
	return a
}

// restartShape is one split a running clone can meet, with the clone it
// runs: the six of splitShapes, the four heal shapes mirrored to a clone
// of B, and the #1113 review's two (a clone that reaches only the witness
// while its original loses the witness, and the mirror through the peer).
type restartShape struct {
	name string
	run  func(seed uint64) Options
}

func restartShapes() []restartShape {
	var out []restartShape
	for _, sh := range splitShapes() {
		out = append(out, restartShape{sh.name, sh.run})
	}
	for _, sh := range reviewSplitShapes {
		cuts := sh.cuts
		out = append(out, restartShape{"clone of B, cuts " + sh.name + " mirrored", func(seed uint64) Options {
			o := reviewSplit(seed, cuts)
			for i := range o.Faults {
				f := &o.Faults[i]
				f.Node, f.From, f.To = swapAB(f.Node), swapAB(f.From), swapAB(f.To)
			}
			return o
		}})
	}
	only := func(name string, viaPeer bool) restartShape {
		return restartShape{name, func(seed uint64) Options {
			o := cloneSplit(seed, !viaPeer)
			c := o.Faults[0].Node
			orig, peer := original(c), peerOf(original(c))
			at, cut := o.Faults[0].At, o.Faults[1].At
			never, lose := peer, W // the clone never reaches never; the original loses lose
			if viaPeer {
				never, lose = W, peer
			}
			o.Faults = []Fault{
				o.Faults[0],
				{Kind: Cut, At: at - lease, For: 14 * lease, From: c, To: never, Both: true},
				{Kind: Cut, At: cut, For: 8 * lease, From: orig, To: lose, Both: true},
			}
			return o
		}}
	}
	return append(out, only("clone reaches only the witness, the original loses it", false),
		only("clone reaches only the peer, the original loses it", true))
}

// withRestarts adds a restart of each member named, one to five leases
// into the split (the clone's original, the clone, the original's peer,
// the witness), a third of a lease apart.
func withRestarts(o Options, seed uint64, who ...string) Options {
	c := o.Faults[0].Node
	var cut time.Duration // the split: the latest cut to start
	for _, f := range o.Faults {
		if f.Kind == Cut {
			cut = max(cut, f.At)
		}
	}
	at := map[string]Addr{"original": original(c), "clone": c, "peer": peerOf(original(c)), "witness": W}
	r := rand.New(rand.NewPCG(seed, 0x72657374))
	down := cut + lease + time.Duration(r.Int64N(int64(4*lease)))
	for i, w := range who {
		o.Faults = append(o.Faults, Fault{Kind: Crash, At: down + time.Duration(i)*lease/3, For: 50*ms + time.Duration(r.Int64N(int64(lease/4))), Node: at[w]})
	}
	o.Until = max(o.Until, down+2*lease+bound+6*lease)
	return o
}

// restartSeeds is how many seeds each (shape, restart) cell runs: 100, or
// QUORUM_CLONE_RESTART_SEEDS.
func restartSeeds() uint64 {
	if v, err := strconv.Atoi(os.Getenv("QUORUM_CLONE_RESTART_SEEDS")); err == nil && v > 0 {
		return uint64(v)
	}
	return 100
}

// No SINGLE restart inside a split with a clone gives two leaders: every
// split shape (the six, the heal shapes mirrored to a clone of B, and the
// #1113 review's clone that reaches only one voter), each member restarted
// once inside the split, on the sim where the peer's requests reach the
// clone too. A restarted voter forgets its marks; what holds then:
//   - rule (i): a hold ends only on an unmarked word from the very voter
//     boot that set it, so the clone stays quiet though the voter that
//     marked it restarted and forgot (hold_released_by_any_boot);
//   - rule (ii): a held controller's voter makes no new grant to the other
//     controller, so the peer, restarted and forgetting its own mark,
//     can't lead on the clone's copy of the original's voter. No single
//     restart needs it alone (TestHeldVoterGrantsNothingInADoubleRestart
//     pins it).
func TestCloneSplitSingleRestarts(t *testing.T) {
	n := restartSeeds()
	for _, sh := range restartShapes() {
		for _, who := range []string{"original", "clone", "peer", "witness"} {
			failing := 0
			for seed := uint64(1); seed <= n; seed++ {
				o := withRestarts(sh.run(seed), seed, who)
				if overlap(Check(Run(o), cloneChecks(o))) {
					failing++
					if failing <= 2 {
						t.Errorf("%s, %s restarts, seed %d: two leaders", sh.name, who, seed)
					}
				}
			}
			if failing > 0 {
				t.Errorf("%s, %s restarts: two leaders in %d of %d", sh.name, who, failing, n)
			}
		}
	}
}

// TestMeasureCloneSplitDoubleRestarts measures what rules (i) and (ii)
// leave open: two members restarting inside one split while the copy
// runs, each forgetting what it knew. The clear procedure is such a pair
// (the members that raised the alarm, then the marked controller), which
// is why it starts with stopping the copy. Logs the two-leader rate per
// pair over every shape; skipped unless QUORUM_MEASURE_DOUBLE_RESTART=<n>.
func TestMeasureCloneSplitDoubleRestarts(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_DOUBLE_RESTART"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_DOUBLE_RESTART=<runs per shape>")
	}
	members := []string{"original", "clone", "peer", "witness"}
	for i, a := range members {
		for _, b := range members[i+1:] {
			two, all := 0, 0
			for _, sh := range restartShapes() {
				for seed := uint64(1); seed <= uint64(n); seed++ {
					o := withRestarts(sh.run(seed), seed, a, b)
					all++
					if overlap(Check(Run(o), cloneChecks(o))) {
						two++
					}
				}
			}
			t.Logf("%s and %s restart inside the split: two leaders in %d of %d", a, b, two, all)
		}
	}
}

// Rule (ii) of the #1113 review where it alone matters: a clone that
// reaches only the peer, the original losing the peer, and then both the
// original and the peer restarting inside the split. Without the rule,
// the held clone's copy of the original's voter grants the restarted peer
// (which forgot its mark) a lease while the original leads with the
// witness: two leaders in these six seeds of 300 (held_voter_grants). No
// single-restart schedule needs the rule alone; this double restart does.
func TestHeldVoterGrantsNothingInADoubleRestart(t *testing.T) {
	var sh restartShape
	for _, x := range restartShapes() {
		if x.name == "clone reaches only the peer, the original loses it" {
			sh = x
		}
	}
	for _, seed := range []uint64{16, 41, 177, 199, 235, 289} {
		o := withRestarts(sh.run(seed), seed, "original", "peer")
		if vs := Check(Run(o), cloneChecks(o)); overlap(vs) {
			t.Errorf("seed %d: two leaders: %v", seed, vs[0])
		}
	}
}

// TestMeasureCloneEarlyCut measures the residual the mark can't close: a
// split that starts so soon after a clone does that no voter has yet heard
// both copies send past the one-way delay. Every split shape, the cut moved
// to a time after the clone's start drawn from each half-second bin, no
// restart; two-leader runs per bin. With one new frame past D per boot
// (#1113 re-check): 52, 21 and 1 of 3,600 in the first three bins, 0 from
// 1.5 s on, at a 2 s lease (two frames per boot gave 633, 39, 14 in the
// re-check's own sampling). Skipped unless
// QUORUM_MEASURE_EARLY_CUT=<seeds per shape>.
func TestMeasureCloneEarlyCut(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("QUORUM_MEASURE_EARLY_CUT"))
	if n <= 0 {
		t.Skip("set QUORUM_MEASURE_EARLY_CUT=<seeds per shape>")
	}
	for _, bin := range []time.Duration{0, sec / 2, sec, 3 * sec / 2, 2 * sec} {
		two, all := 0, 0
		for _, sh := range restartShapes() {
			for seed := uint64(1); seed <= uint64(n); seed++ {
				o := sh.run(seed)
				r := rand.New(rand.NewPCG(seed, 0x6561726c))
				var cut time.Duration
				for _, f := range o.Faults {
					if f.Kind == Cut {
						cut = max(cut, f.At)
					}
				}
				at := o.Faults[0].At + bin + time.Duration(r.Int64N(int64(sec/2)))
				for i := range o.Faults {
					if o.Faults[i].Kind == Cut && o.Faults[i].At == cut {
						o.Faults[i].At = at
					}
				}
				all++
				if overlap(Check(Run(o), cloneChecks(o))) {
					two++
				}
			}
		}
		t.Logf("split %v to %v after the clone starts: two leaders in %d of %d", bin, bin+sec/2, two, all)
	}
}
