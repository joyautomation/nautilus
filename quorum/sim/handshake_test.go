package sim

import (
	"strings"
	"testing"
	"time"
)

// Named cases for the witness-incarnation handshake (design §3.1; test
// plan F15, F15b; #976, #710 H2). Each is judged by every oracle.

// adopted returns the incarnations n durably adopted, in order.
func adoptedBy(rec *Record, n Addr) []uint64 {
	var out []uint64
	for _, a := range rec.Adoptions {
		if a.Node == n {
			out = append(out, a.Inc)
		}
	}
	return out
}

// witnessIncAt returns the incarnation node w ran at its last start before t.
func witnessIncAt(rec *Record, w Addr, t time.Duration) uint64 {
	var inc uint64
	for _, s := range rec.starts {
		if s.node == w && s.at <= t {
			inc = s.inc
		}
	}
	return inc
}

func trace(t *testing.T, rec *Record) {
	t.Helper()
	if t.Failed() && rec.Trace != nil {
		t.Log("trace:\n" + strings.Join(rec.Trace, "\n"))
	}
}

// F15b, the switch case, a must-fail case for the quiet period. A leads on
// {A, W}. From 8 s B can't reach W but reaches a second copy W2 that has
// been up since 0 (its restart wait long over), and every request A sends
// B is lost while A's replies and handshake messages get through: B misses
// A's renewals, so its promise to A lapses and it campaigns, hearing W2,
// and it still hears A. B proposes W2's incarnation; A, still leading on
// grants from {A, W}, adopts it and acks. With the quiet period B counts
// W2 only one lease after its adoption is durable, when A's lease, which
// stopped resting on W when A adopted, is over. Without it B wins on
// {B, W2} while A's lease runs: I1. The DECISIVE guard asserts W2's grant
// reached B inside its quiet period when it would have made B's majority.
func TestMustFailSwitchQuietPeriod(t *testing.T) {
	o := Options{Seed: 3, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 30 * sec, Trace: true,
		Start: [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{
			{Kind: Start, At: 0, Node: W2},
			{Kind: Cut, At: 8 * sec, From: B, To: W, Both: true},
			{Kind: Mirror, At: 8 * sec, From: B, To: W, Node: W2},
			{Kind: Cut, At: 8 * sec, From: A, To: B, Value: CutRequests},
		}}
	rec := Run(o)
	defer trace(t, rec)
	j := witnessIncAt(rec, W2, o.Until)
	if a, b := adoptedBy(rec, A), adoptedBy(rec, B); len(a) == 0 || a[0] != j || len(b) == 0 || b[0] != j {
		t.Fatalf("the pair did not adopt W2's incarnation: A %v, B %v", a, b)
	}
	hostile(t, rec, "quiet period", B)
	clean(t, rec, Checks{MaxTakeovers: -1})
	if l := ledBy(rec); !l[A] || !l[B] {
		t.Fatalf("leaders %v: want A first, then B on {B, W2}", l)
	}
}

// The term floor (design §3.1). B leads a term A never saw: A is not yet
// up, B's campaigns climb while W restarts three times (each restart wait
// refuses them), and B leads term 17 on {B, W}. Every vote B sends A is
// lost from the start (requests and replies; the handshake gets through),
// so A learns no term of B's that way. W is then rebuilt (a new
// incarnation, promised 0), B's lease lapses, A comes up, and the pair
// adopts the rebuilt witness: B's proposal carries its floor, 17. B dies
// at 21 s, before its quiet period ends, so the new witness never hears a
// term of B's either. A then wins on {A, W′}: above 17 with the floor.
// Without it A campaigns from the terms it knows and leads term 2 after
// B's 17: I2 (the fencing token goes back).
func TestFloorCarriesTheUnseenTerm(t *testing.T) {
	o := Options{Seed: 5, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 40 * sec, Trace: true,
		Start: [3]time.Duration{20 * sec, 0, 0},
		Faults: []Fault{
			{Kind: Crash, At: 1900 * ms, For: 100 * ms, Node: W},
			{Kind: Crash, At: 3900 * ms, For: 100 * ms, Node: W},
			{Kind: Crash, At: 5900 * ms, For: 100 * ms, Node: W},
			{Kind: Wipe, At: 17 * sec, For: 500 * ms, Node: W},
			{Kind: Cut, At: 0, From: B, To: A, Value: CutRequests | CutReplies},
			{Kind: Crash, At: 21 * sec, Node: B},
		}}
	rec := Run(o)
	defer trace(t, rec)
	j := witnessIncAt(rec, W, o.Until)
	if a, b := adoptedBy(rec, A), adoptedBy(rec, B); len(a) != 1 || a[0] != j || len(b) != 1 || b[0] != j {
		t.Fatalf("the pair did not adopt the rebuilt witness before B died: A %v, B %v", a, b)
	}
	var bTop uint64
	aLed := false
	for _, iv := range rec.Gates() {
		switch {
		case iv.Node == B:
			bTop = max(bTop, iv.Term)
		case iv.Node == A && iv.From > 21*sec:
			aLed = true
		}
	}
	if bTop < 10 || !aLed {
		t.Fatalf("the case proves nothing: B's top term %d (want ≥ 10), A led after B died: %v", bTop, aLed)
	}
	clean(t, rec, Checks{MaxTakeovers: -1})
}

// F15: the witness loses its state and comes back as a new incarnation.
// Both controllers adopt it together, each counts it only one lease after
// its adoption is durable (the quiet period), and once settled the rebuilt
// witness backs the leader (steady state, every oracle).
func TestRebuiltWitnessAdoptedTogether(t *testing.T) {
	o := Options{Seed: 7, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 40 * sec, Trace: true,
		Start:  [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{{Kind: Wipe, At: 8 * sec, For: 500 * ms, Node: W}}}
	rec := Run(o)
	defer trace(t, rec)
	j := witnessIncAt(rec, W, o.Until)
	if j == rec.witnessInc {
		t.Fatal("the witness kept its incarnation: the case proves nothing")
	}
	for _, a := range rec.Adoptions {
		if a.Inc != j {
			t.Fatalf("%v adopted %d, not the rebuilt witness's %d", a.Node, a.Inc, j)
		}
		if rec.countsAt(a.Node, j, a.At+lease-ms) {
			t.Errorf("%v counts the rebuilt witness inside its quiet period (adopted at %v)", a.Node, a.At)
		}
		if !rec.countsAt(a.Node, j, a.At+lease+50*ms) {
			t.Errorf("%v does not count the rebuilt witness after its quiet period", a.Node)
		}
	}
	if a, b := adoptedBy(rec, A), adoptedBy(rec, B); len(a) != 1 || len(b) != 1 {
		t.Fatalf("want one adoption each: A %v, B %v", a, b)
	}
	clean(t, rec, checksFor(o))
}

// F15 with one controller down: the survivor can't adopt the rebuilt
// witness alone (design §3.1, the costs), so nobody leads until B is
// back; then both adopt it and every oracle holds.
func TestRebuiltWitnessNotAdoptedWithAControllerDown(t *testing.T) {
	o := Options{Seed: 7, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 50 * sec, Trace: true,
		Start: [3]time.Duration{0, 4 * sec, 0},
		Faults: []Fault{
			{Kind: Crash, At: 7 * sec, For: 18 * sec, Node: B},
			{Kind: Wipe, At: 8 * sec, For: 500 * ms, Node: W},
		}}
	rec := Run(o)
	defer trace(t, rec)
	for _, a := range rec.Adoptions {
		if a.At < 25*sec {
			t.Fatalf("%v adopted at %v with B down", a.Node, a.At)
		}
	}
	if a, b := adoptedBy(rec, A), adoptedBy(rec, B); len(a) != 1 || len(b) != 1 {
		t.Fatalf("not adopted once B was back: A %v, B %v", a, b)
	}
	for _, iv := range rec.Gates() {
		if iv.To > 11*sec && iv.From < 25*sec {
			t.Fatalf("%v: a leader on one counted voter while B was down", iv)
		}
	}
	clean(t, rec, checksFor(o))
}

// switchOnce is the switch fault alone: from 8 s A can't reach W but
// reaches a second copy W2, which stops at 20 s. The pair switches to W2's
// incarnation (generation 1) and back to W's (generation 2).
func switchOnce(extra ...Fault) Options {
	return Options{Seed: 11, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 50 * sec, Trace: true,
		Start: [3]time.Duration{0, 4 * sec, 0},
		Faults: append([]Fault{
			{Kind: Start, At: 0, For: 20 * sec, Node: W2},
			{Kind: Mirror, At: 8 * sec, For: 12 * sec, From: A, To: W, Node: W2},
			{Kind: Cut, At: 8 * sec, For: 12 * sec, From: A, To: W, Both: true}}, extra...)}
}

// A late proposal from the peer's earlier boot does not split the pair.
// A proposes W2's incarnation at generation 1; a copy of that proposal is
// replayed to B 15 s later, after A restarted and the pair went back to W
// at generation 2. B must refuse it (generations only rise). Taking it, B
// would count W2's incarnation while A counts W's: red under no_adopt_gen.
func TestLateEarlierBootProposalRefused(t *testing.T) {
	o := switchOnce(
		Fault{Kind: Replay, At: 10150 * ms, For: 100 * ms, From: A, To: B, Value: int64(15 * time.Second)},
		Fault{Kind: Crash, At: 22 * sec, For: 100 * ms, Node: A})
	rec := Run(o)
	defer trace(t, rec)
	if len(rec.StaleProposals) == 0 || rec.StaleProposals[0].Node != B {
		t.Fatalf("no late proposal reached B: %+v", rec.StaleProposals)
	}
	late := rec.StaleProposals[0].At
	var aBoot2 time.Duration
	for _, s := range rec.starts {
		if s.node == A && s.boot == 2 {
			aBoot2 = s.at
		}
	}
	if aBoot2 == 0 || aBoot2 > late {
		t.Fatalf("A did not restart before the late proposal arrived (boot 2 at %v, late at %v)", aBoot2, late)
	}
	w := witnessIncAt(rec, W, o.Until)
	for _, n := range []Addr{A, B} {
		if !rec.countsAt(n, w, o.Until) {
			t.Errorf("%v does not count the real witness at the end (adopted %v)", n, adoptedBy(rec, n))
		}
	}
	clean(t, rec, checksFor(o))
}

// The flapping stray (design §3.1, the switch hold-off). A second copy
// answers A for 4 s in every 8, while W is cut from A: without a hold-off
// the pair switches back and forth in every window. The hold-off doubles
// with each switch (1, 2, 4 … leases), so here it is soon longer than a
// window and the switches stop: a bound on adoptions like I7's on
// takeovers. Safety holds throughout; two incarnations are reported.
func TestFlappingStrayBounded(t *testing.T) {
	fs := []Fault{{Kind: Start, At: 0, Node: W2}}
	for k := range 12 {
		at := 8*sec + time.Duration(k)*8*sec
		fs = append(fs, Fault{Kind: Mirror, At: at, For: 4 * sec, From: A, To: W, Node: W2},
			Fault{Kind: Cut, At: at, For: 4 * sec, From: A, To: W, Both: true})
	}
	o := Options{Seed: 13, Delay: ms, Jitter: ms / 2, Fsync: ms, Until: 110 * sec, Trace: true,
		Start: [3]time.Duration{0, 4 * sec, 0}, Faults: fs}
	rec := Run(o)
	defer trace(t, rec)
	const maxAdoptions = 6 // three switches, by both controllers
	if n := len(rec.Adoptions); n == 0 || n > maxAdoptions {
		t.Errorf("%d adoptions over 12 windows, want 1–%d: %+v", n, maxAdoptions, rec.Adoptions)
	}
	if rec.TwoIncarnations == 0 {
		t.Error("two incarnations answered within a lease, and no event said so")
	}
	clean(t, rec, Checks{MaxTakeovers: -1})
}
