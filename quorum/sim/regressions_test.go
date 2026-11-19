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
// witness. Both fail I6 (no leader after the heal) without
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
		}},
}

func TestRegressionTwoVoterLivelock(t *testing.T) {
	for name, o := range livelockCases {
		t.Run(name, func(t *testing.T) {
			for _, v := range judged(t, Run(o), checksFor(o)) {
				t.Error(v)
			}
		})
	}
}

// #976, closed by the handshake: two controllers that both lost their
// state files used to form a majority without the witness and grant terms
// below one already led, so the fencing token went backwards (I2: "B wrote
// term 2 after term 5"). Seeds 208 and 1458 of the default 2,000 (before
// the generator could draw two lost controller files), shrunk. Each
// controller now re-commissions from its peer, or with the witness, at the
// term floor before it grants or campaigns. The witness stayed intact, so
// this is not the two-of-three case: every oracle is asserted, and both
// controllers must have re-adopted the witness.
func TestRegression976TwoLostControllerFiles(t *testing.T) {
	for name, o := range map[string]Options{
		"seed208": {Seed: 208, Delay: 530818, Jitter: 175320, Fsync: 4337184, InPlace: true,
			Rates: [nNodes]PPM{997866, 1004508, 999726}, Start: [3]time.Duration{581011620, 1369048054, 3402530163}, Until: 41517713503,
			Faults: []Fault{
				{Kind: Wipe, At: 14052161408, For: 6057402149, Node: A},
				{Kind: Wipe, At: 17222489667, For: 5295223836, Node: B},
			}},
		"seed1458": {Seed: 1458, Delay: 220113, Jitter: 34053, Fsync: 3982614, InPlace: false,
			Rates: [nNodes]PPM{1000797, 997917, 1000919}, Start: [3]time.Duration{1080345728, 2642599916, 602681865}, Until: 57840622381,
			Faults: []Fault{
				{Kind: Wipe, At: 14547006072, For: 6742578343, Node: B},
				{Kind: Wipe, At: 20570756019, For: 3834675396, Node: A},
			}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := Run(o)
			if rec.TwoOfThreeLost() {
				t.Fatal("judged the two-of-three case: the oracles would be excused")
			}
			lost := map[Addr]bool{}
			for _, s := range rec.starts {
				if !s.readable {
					lost[s.node] = true
				}
			}
			if !lost[A] || !lost[B] {
				t.Fatalf("both controllers must lose their files: %v", lost)
			}
			for _, a := range []Addr{A, B} {
				if !rec.countsAt(a, rec.witnessInc, o.Until) {
					t.Errorf("%v does not count the witness at the end", a)
				}
			}
			for _, v := range judged(t, rec, checksFor(o)) {
				t.Error(v)
			}
		})
	}
}

// The two-of-three exclusion (Record.TwoOfThreeLost) has teeth: it fires
// on a controller's file lost with the witness rebuilt before it
// re-commissions, and on seeds whose pair counted a second copy that went
// away while a controller was uncommissioned; never on one lost voter
// alone, nor on the switch fault alone. A case it doesn't excuse is held
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
// them (the switch fault): each re-commissions by proposing what it hears,
// A the real witness and B the copy, and each refuses the other's (an
// incarnation never heard, a lower generation). Seeds 35401, 139572 and
// 146342 of the first 200,000 with the handshake stayed stuck after the
// copy was gone (I6), until a controller proposes again when what it heard
// last is no longer what it pends (design §3.1, strays). Not the
// two-of-three case: the real witness kept its state. Red under
// stale_pending_kept.
func TestRegressionBothUncommissionedUnstick(t *testing.T) {
	for name, o := range map[string]Options{
		"seed35401": {Seed: 35401, Delay: 1734349, Jitter: 703406, Fsync: 400695, InPlace: true,
			Rates: [nNodes]PPM{1003418, 1001028, 995398}, Start: [3]time.Duration{451761835, 971478969, 3425851887}, Until: 124467335963,
			Faults: []Fault{
				{Kind: Wipe, At: 12136328099, For: 15438908815, Node: B},
				{Kind: Wipe, At: 21535120896, For: 12799759609, Node: A},
				{Kind: Start, At: 0 * ms, For: 35467335963, Node: W2},
				{Kind: Mirror, At: 21653476148, For: 13813859815, From: B, To: W, Node: W2},
				{Kind: Cut, At: 21653476148, For: 13813859815, From: B, To: W, Both: true},
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
			}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, v := range judged(t, Run(o), checksFor(o)) {
				t.Error(v)
			}
		})
	}
}

// #976's own rule: a controller that lost its file grants nothing until it
// re-commissions. With the witness reachable the pair re-commissions
// inside the restart wait and the rule never bites (the seeds above stay
// green without it), so here the witness is cut from both controllers
// while both files are lost (seed 208's shape). Without the rule A and B
// elect each other on their own, below a term B already led: I2. With it,
// nobody leads until the witness is back; then both re-adopt it.
func TestRegression976WitnessUnreachable(t *testing.T) {
	o := Options{Seed: 208, Delay: 530818, Jitter: 175320, Fsync: 4337184, InPlace: true,
		Rates: [nNodes]PPM{997866, 1004508, 999726}, Start: [3]time.Duration{581011620, 1369048054, 3402530163}, Until: 50 * sec,
		Faults: []Fault{
			{Kind: Wipe, At: 14052161408, For: 6057402149, Node: A},
			{Kind: Wipe, At: 17222489667, For: 5295223836, Node: B},
			{Kind: Cut, At: 13 * sec, For: 17 * sec, From: A, To: W, Both: true},
			{Kind: Cut, At: 13 * sec, For: 17 * sec, From: B, To: W, Both: true},
		}}
	rec := Run(o)
	for _, iv := range rec.Gates() {
		if iv.From > 23*sec && iv.From < 30*sec {
			t.Errorf("%v: a lost pair led with the witness unreachable", iv)
		}
	}
	for _, a := range []Addr{A, B} {
		if !rec.countsAt(a, rec.witnessInc, o.Until) {
			t.Errorf("%v does not count the witness at the end", a)
		}
	}
	for _, v := range judged(t, rec, checksFor(o)) {
		t.Error(v)
	}
}
