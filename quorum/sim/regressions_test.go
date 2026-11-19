package sim

import (
	"testing"
	"time"
)

// Named regression cases: seeded schedules the runner found, shrunk and
// committed (test plan §3.2), each judged by every oracle (checksFor).

// The two counted voters livelock (the yield): seeds 275 and 1653 of the
// default 2,000, shrunk. Seed 275: the witness is wiped (its new
// incarnation isn't counted before the handshake PR) and A is down 18 s;
// when A is back, A and B each promise themselves and refuse the other.
// Seed 1653: both controllers lose their files, so neither counts the
// witness (since #710 B2 that waits for the operator: a Recommission at
// 27 s, then the pair must elect). Both fail I6 (no leader after the heal) without
// Voter.WouldYield/Candidate.Yield. Seeds 540 and 1068 failed the same
// way first, and pass even without the yield since campaigns observe the
// peer's terms (Candidate.Observe) and retries move off the look grid.
var livelockCases = map[string]Options{
	"seed275": {Seed: 275, Delay: 2985580, Jitter: 1754305, Fsync: 381118, InPlace: true,
		Rates: [nNodes]PPM{997368, 1002584, 1002386}, Start: [3]time.Duration{3759068977, 1372222162, 3882453785}, Until: 58413300107,
		Faults: []Fault{
			{Kind: Crash, At: 21445059081, For: 17968241026, Node: A},
			{Kind: Wipe, At: 10764040395, For: 7199531444, Node: W},
		}},
	"seed1653": {Seed: 1653, Delay: 2919491, Jitter: 1674237, Fsync: 4400320, InPlace: true,
		Rates: [nNodes]PPM{996934, 999597, 998332}, Start: [3]time.Duration{1873736067, 648631022, 1286627556}, Until: 45590035827,
		Faults: []Fault{
			{Kind: Wipe, At: 11619435332, For: 311686393, Node: A},
			{Kind: Wipe, At: 6506519627, For: 17361366765, Node: B},
			{Kind: Recommission, At: 27 * sec, Node: A, Value: 1000},
		}},
}

func TestRegressionTwoVoterLivelock(t *testing.T) {
	for name, o := range livelockCases {
		t.Run(name, func(t *testing.T) {
			if hasOperator(o) {
				operatorRecovers(t, o)
				return
			}
			for _, v := range judged(t, Run(o), checksFor(o)) {
				t.Error(v)
			}
		})
	}
}

func hasOperator(o Options) bool {
	for _, f := range o.Faults {
		if f.Kind == Recommission {
			return true
		}
	}
	return false
}

// operatorRecovers runs o, in which both controllers lose their files
// (the two-of-three case since #710 B2: the witness may have missed terms
// the pair led on its own), with one Recommission fault, the operator's
// command. The pair must wait for it: no adoption while both are
// uncommissioned, and both raise the alarm. Every safety oracle holds (I1,
// I2, I4, the lease, the ledger; liveness is excused as the operator's
// case). After the command both re-adopt the witness, and a controller
// leads at the end.
func operatorRecovers(t *testing.T, o Options) *Record {
	t.Helper()
	opAt := time.Duration(-1)
	for _, f := range o.Faults {
		if f.Kind == Recommission {
			opAt = f.At
		}
	}
	rec := Run(o)
	defer trace(t, rec)
	if _, both := rec.twoOfThree(); !both || opAt < 0 {
		t.Fatalf("not both controllers uncommissioned at once with an operator (both %v, operator at %v)", both, opAt)
	}
	for _, v := range Check(rec, checksFor(o)) {
		t.Error(v)
	}
	var lost [2]time.Duration
	for _, s := range rec.starts {
		if (s.node == A || s.node == B) && !s.readable && s.at < opAt {
			lost[s.node-A] = s.at
		}
	}
	bothFrom := max(lost[0], lost[1])
	alarmed := map[Addr]bool{}
	for _, r := range rec.Recommissions {
		alarmed[r.Node] = true
	}
	for _, a := range rec.Adoptions {
		if a.At >= bothFrom && a.At < opAt {
			t.Errorf("adopted before the operator's command: %+v", a)
		}
	}
	inc := witnessIncAt(rec, W, o.Until)
	for _, a := range []Addr{A, B} {
		if !alarmed[a] {
			t.Errorf("%v raised no re-commission alarm", a)
		}
		if !rec.countsAt(a, inc, o.Until) {
			t.Errorf("%v does not count the witness at the end", a)
		}
	}
	leads := false
	for _, iv := range rec.Gates() {
		leads = leads || iv.To >= rec.until
	}
	if !leads {
		t.Error("nobody leads at the end")
	}
	return rec
}

// #976: two controllers that both lost their state files used to form a
// majority without the witness and grant terms below one already led, so
// the fencing token went backwards (I2: "B wrote term 2 after term 5").
// Seeds 208 and 1458 of the default 2,000 (before the generator could
// draw two lost controller files), shrunk. H2 had the pair re-commission
// on its own with the witness; since #710 B2 it waits for the operator
// (the witness may have missed terms the pair led on its own), then
// re-adopts the witness at the operator's floor (operatorRecovers). Red
// under both_lost_recommission.
func TestRegression976TwoLostControllerFiles(t *testing.T) {
	for name, o := range map[string]Options{
		"seed208": {Seed: 208, Delay: 530818, Jitter: 175320, Fsync: 4337184, InPlace: true,
			Rates: [nNodes]PPM{997866, 1004508, 999726}, Start: [3]time.Duration{581011620, 1369048054, 3402530163}, Until: 41517713503,
			Faults: []Fault{
				{Kind: Wipe, At: 14052161408, For: 6057402149, Node: A},
				{Kind: Wipe, At: 17222489667, For: 5295223836, Node: B},
				{Kind: Recommission, At: 25 * sec, Node: A, Value: 1000},
			}},
		"seed1458": {Seed: 1458, Delay: 220113, Jitter: 34053, Fsync: 3982614, InPlace: false,
			Rates: [nNodes]PPM{1000797, 997917, 1000919}, Start: [3]time.Duration{1080345728, 2642599916, 602681865}, Until: 57840622381,
			Faults: []Fault{
				{Kind: Wipe, At: 14547006072, For: 6742578343, Node: B},
				{Kind: Wipe, At: 20570756019, For: 3834675396, Node: A},
				{Kind: Recommission, At: 27 * sec, Node: B, Value: 1000},
			}},
	} {
		t.Run(name, func(t *testing.T) { operatorRecovers(t, o) })
	}
}

// The two-of-three exclusion (Record.TwoOfThreeLost) has teeth: it fires
// on a controller's file lost with the witness rebuilt before it
// re-commissions, on both controllers' files lost at once (#710 B2), and
// on seeds whose pair counted a second copy that went away while a
// controller was uncommissioned; never on one lost voter alone, nor on
// the switch fault alone. A case it doesn't excuse is held
// to every oracle.
func TestTwoOfThreeLostTeeth(t *testing.T) {
	base := func(fs ...Fault) Options {
		return Options{Seed: 11, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 50 * sec,
			Start: [3]time.Duration{0, 4 * sec, 0}, Faults: fs}
	}
	for _, c := range []struct {
		name string
		o    Options
		want bool
	}{
		{"A and W lost", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: A}, Fault{Kind: Wipe, At: 9 * sec, For: 2 * sec, Node: W}), true},
		{"A and B lost, witness intact", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: A}, Fault{Kind: Wipe, At: 9 * sec, For: 2 * sec, Node: B}), true},
		{"A lost alone", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: A}), false},
		{"B lost alone", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: B}), false},
		{"W rebuilt alone", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: W}), false},
		{"A then W, A back first", base(Fault{Kind: Wipe, At: 8 * sec, For: 2 * sec, Node: A}, Fault{Kind: Wipe, At: 20 * sec, For: 2 * sec, Node: W}), false},
		{"switch fault alone", base(
			Fault{Kind: Start, At: 0, For: 20 * sec, Node: W2},
			Fault{Kind: Mirror, At: 8 * sec, For: 12 * sec, From: A, To: W, Node: W2},
			Fault{Kind: Cut, At: 8 * sec, For: 12 * sec, From: A, To: W, Both: true}), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := Run(c.o)
			if got := rec.TwoOfThreeLost(); got != c.want {
				t.Fatalf("TwoOfThreeLost = %v, want %v", got, c.want)
			}
			if !c.want {
				for _, v := range Check(rec, checksFor(c.o)) {
					t.Error(v)
				}
			}
			if c.name == "switch fault alone" && len(rec.Adoptions) != 4 {
				t.Errorf("want the pair to switch to the copy and back (4 adoptions), got %+v", rec.Adoptions)
			}
		})
	}
	// The default range's, and three of the first 200,000 with the handshake
	// that are excused only since the rule looks at the peer's committed
	// incarnation (a peer pending a switch counts nothing, yet the lost
	// controller can only re-adopt what it committed: dead here).
	for _, seed := range []uint64{686, 966, 1158, 1235, 1272, 1411, 1482, 1492, 1713, 45754, 56148, 70430} {
		if o := randomRun(seed, profDefault); !Run(o).TwoOfThreeLost() {
			t.Errorf("seed %d: no longer judged two-of-three", seed)
		}
	}
}

// Both controllers lose their files while a second copy answers one of
// them (the switch fault). In H2 each re-commissioned on what it heard,
// and seeds 35401, 139572 and 146342 of the first 200,000 stayed stuck
// after the copy was gone, until a controller proposed again (the rule
// removed in #710 B2). Now neither proposes anything: the pair waits for
// the operator, the copy can't move it, and after the command (once the
// copy is gone) both re-adopt the real witness.
func TestRegressionBothUncommissionedStray(t *testing.T) {
	for name, o := range map[string]Options{
		"seed35401": {Seed: 35401, Delay: 1734349, Jitter: 703406, Fsync: 400695, InPlace: true,
			Rates: [nNodes]PPM{1003418, 1001028, 995398}, Start: [3]time.Duration{451761835, 971478969, 3425851887}, Until: 124467335963,
			Faults: []Fault{
				{Kind: Wipe, At: 12136328099, For: 15438908815, Node: B},
				{Kind: Wipe, At: 21535120896, For: 12799759609, Node: A},
				{Kind: Start, At: 0 * ms, For: 35467335963, Node: W2},
				{Kind: Mirror, At: 21653476148, For: 13813859815, From: B, To: W, Node: W2},
				{Kind: Cut, At: 21653476148, For: 13813859815, From: B, To: W, Both: true},
				{Kind: Recommission, At: 40 * sec, Node: A, Value: 1000},
			}},
		"seed139572": {Seed: 139572, Delay: 4944065, Jitter: 652901, Fsync: 936567, InPlace: false,
			Rates: [nNodes]PPM{996831, 996109, 1003997}, Start: [3]time.Duration{2288505510, 3007843129, 755273771}, Until: 121354059941,
			Faults: []Fault{
				{Kind: Mirror, At: 19917563955, For: 12436495986, From: W, To: B, Node: A},
				{Kind: Wipe, At: 7535038091, For: 9322378364, Node: B},
				{Kind: Wipe, At: 9315714117, For: 9576523303, Node: A},
				{Kind: Start, At: 0 * ms, For: 26238161980, Node: W2},
				{Kind: Mirror, At: 16345460742, For: 9892701238, From: B, To: W, Node: W2},
				{Kind: Cut, At: 16345460742, For: 9892701238, From: B, To: W, Both: true},
				{Kind: Recommission, At: 35 * sec, Node: B, Value: 1000},
			}},
		"seed146342": {Seed: 146342, Delay: 2982664, Jitter: 189209, Fsync: 3564252, InPlace: false,
			Rates: [nNodes]PPM{998305, 1002086, 1001939}, Start: [3]time.Duration{339241559, 1635922891, 1842889787}, Until: 124494376401,
			Faults: []Fault{
				{Kind: Delay, At: 16659049759, For: 18835326642, From: A, To: W, Both: true, Value: 49642257},
				{Kind: Wipe, At: 19212060134, For: 7954512902, Node: A},
				{Kind: Wipe, At: 11761191196, For: 10783481477, Node: B},
				{Kind: Start, At: 0 * ms, For: 34277937521, Node: W2},
				{Kind: Mirror, At: 17944494985, For: 16333442536, From: A, To: W, Node: W2},
				{Kind: Cut, At: 17944494985, For: 16333442536, From: A, To: W, Both: true},
				{Kind: Recommission, At: 38 * sec, Node: A, Value: 1000},
			}},
	} {
		t.Run(name, func(t *testing.T) { operatorRecovers(t, o) })
	}
}

// #976's own rule: a controller that lost its file grants nothing until it
// re-commissions. Here the witness is cut from both controllers while
// both files are lost (seed 208's shape). Without the rule A and B elect
// each other on their own, below a term B already led: I2. With it, nobody
// leads until the operator's command, after the witness is back; then both
// re-adopt it.
func TestRegression976WitnessUnreachable(t *testing.T) {
	o := Options{Seed: 208, Delay: 530818, Jitter: 175320, Fsync: 4337184, InPlace: true,
		Rates: [nNodes]PPM{997866, 1004508, 999726}, Start: [3]time.Duration{581011620, 1369048054, 3402530163}, Until: 50 * sec,
		Faults: []Fault{
			{Kind: Wipe, At: 14052161408, For: 6057402149, Node: A},
			{Kind: Wipe, At: 17222489667, For: 5295223836, Node: B},
			{Kind: Cut, At: 13 * sec, For: 17 * sec, From: A, To: W, Both: true},
			{Kind: Cut, At: 13 * sec, For: 17 * sec, From: B, To: W, Both: true},
			{Kind: Recommission, At: 32 * sec, Node: A, Value: 1000},
		}}
	rec := operatorRecovers(t, o)
	for _, iv := range rec.Gates() {
		if iv.From > 23*sec && iv.From < 32*sec {
			t.Errorf("%v: a lost pair led before the operator's command", iv)
		}
	}
}
