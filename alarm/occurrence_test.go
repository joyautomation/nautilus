package alarm

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

func (r *rig) rec(t *testing.T, id string) Record {
	t.Helper()
	rec, ok := r.Lookup(id)
	if !ok {
		t.Fatalf("no alarm %q", id)
	}
	return rec
}

// events is the journal oldest first.
func (r *rig) events(t *testing.T) []Event {
	t.Helper()
	evs, err := r.Journal(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]Event, len(evs))
	for i, e := range evs {
		out[len(evs)-1-i] = e
	}
	return out
}

var occShape = regexp.MustCompile(`^[0-9a-f]{16}$`)

// An occurrence runs from an activation out of normal to the next return
// to normal: a flap through unack-rtn is the same one (one wait, one
// ack), a return to normal ends it, and every event names it.
func TestOccurrenceRunsNormalToNormal(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	first := r.rec(t, "A").Occurrence
	if !occShape.MatchString(first) {
		t.Fatalf("occurrence %q: want 16 lowercase hex", first)
	}
	// Flap: unack-rtn and back to unack-active.
	r.tags["A"] = false
	r.clk.advance(time.Second)
	r.Evaluate()
	eq(t, "state after rtn", r.state(t, "A"), UnackRTN)
	r.tags["A"] = true
	r.clk.advance(time.Second)
	r.Evaluate()
	eq(t, "state after flap", r.state(t, "A"), UnackActive)
	eq(t, "occurrence after a flap", r.rec(t, "A").Occurrence, first)
	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	eq(t, "occurrence after ack", r.rec(t, "A").Occurrence, first)
	// Back to normal, then a new activation: a new occurrence.
	r.tags["A"] = false
	r.clk.advance(time.Second)
	r.Evaluate()
	eq(t, "state after ack + rtn", r.state(t, "A"), Normal)
	r.tags["A"] = true
	r.clk.advance(time.Second)
	r.Evaluate()
	second := r.rec(t, "A").Occurrence
	if second == first || !occShape.MatchString(second) {
		t.Fatalf("occurrence after normal = %q, want a new one (first %q)", second, first)
	}
	evs := r.events(t)
	for i, e := range evs {
		want := first
		if i == len(evs)-1 {
			want = second
		}
		if e.Occurrence != want {
			t.Errorf("event %d %s: occurrence %q, want %q", i, e.Kind, e.Occurrence, want)
		}
	}
}

// An ack that names an occurrence is refused by any other, and changes
// nothing; an id it does not name is acked as before.
func TestAckNamingAnotherOccurrenceIsRefused(t *testing.T) {
	r := newRig(t, def("A", "A"), def("B", "B"))
	r.tags["A"], r.tags["B"] = true, true
	r.Evaluate()
	old := r.rec(t, "A").Occurrence
	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	r.tags["A"] = false
	r.clk.advance(time.Second)
	r.Evaluate()
	r.tags["A"] = true
	r.clk.advance(time.Second)
	r.Evaluate()
	cur := r.rec(t, "A").Occurrence

	_, err := r.AckIDsAt([]string{"A", "B"}, map[string]string{"A": old}, "op")
	if !errors.Is(err, ErrOtherOccurrence) {
		t.Fatalf("ack of the old occurrence: err %v, want ErrOtherOccurrence", err)
	}
	eq(t, "A after refused ack", r.state(t, "A"), UnackActive)
	eq(t, "B after refused ack (nothing half-applied)", r.state(t, "B"), UnackActive)
	if _, err := r.AckTargets([]string{"A"}, map[string]string{"A": old}); !errors.Is(err, ErrOtherOccurrence) {
		t.Fatalf("AckTargets of the old occurrence: err %v, want ErrOtherOccurrence", err)
	}

	tg, err := r.AckTargets(nil, nil)
	if err != nil || len(tg) != 2 || tg[0].Occurrence == "" || tg[0].ActiveMs == 0 {
		t.Fatalf("AckTargets(all) = %+v, %v", tg, err)
	}
	acked, err := r.AckIDsAt([]string{"A", "B"}, map[string]string{"A": cur}, "op")
	if err != nil || len(acked) != 2 {
		t.Fatalf("ack of the current occurrence: %v, %v", acked, err)
	}
	if evs := r.events(t); evs[len(evs)-1].Occurrence == "" {
		t.Error("the ack row names no occurrence")
	}
}

// restart builds a fresh engine on the same clock from snap, as naut run
// does after a restart.
func restart(t *testing.T, r *rig, snap map[string]retain.AlarmRetain, grace time.Duration, defs ...Def) *rig {
	t.Helper()
	clk, tg, ring := &clock{t: r.clk.t}, tags{}, NewRing(100)
	e, err := New(Options{Defs: defs, Read: tg.read, Now: clk.now, Journal: ring, CarryGrace: grace})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	e.Restore(snap)
	return &rig{Engine: e, clk: clk, tags: tg, ring: ring}
}

// A restart continues the occurrences whose condition the field
// re-asserts: same ID, activation and wait (escalation does not restart
// at level 1), the ack with it, no on-delay, and no new `active` row.
func TestRestartContinuesTheOccurrence(t *testing.T) {
	slow := def("S", "S")
	slow.OnDelay = 5 * time.Minute
	defs := []Def{def("U", "U"), def("K", "K"), slow}
	r := newRig(t, defs...)
	r.tags["U"], r.tags["K"], r.tags["S"] = true, true, true
	r.Evaluate()
	r.clk.advance(5 * time.Minute)
	r.Evaluate()
	if _, err := r.Ack([]string{"K"}, "op"); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(10 * time.Minute)
	before := map[string]Record{"U": r.rec(t, "U"), "K": r.rec(t, "K"), "S": r.rec(t, "S")}
	snap := r.Snapshot()
	if snap["U"].Occurrence == "" || snap["U"].WaitMs == 0 || snap["K"].WaitMs != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}

	r2 := restart(t, r, snap, 0, defs...)
	r2.tags["U"], r2.tags["K"], r2.tags["S"] = true, true, true
	r2.Evaluate()
	for id, b := range before {
		a := r2.rec(t, id)
		eq(t, id+" state", a.State, b.State)
		eq(t, id+" occurrence", a.Occurrence, b.Occurrence)
		eq(t, id+" activeMs", a.ActiveMs, b.ActiveMs)
		eq(t, id+" waitMs", a.WaitMs, b.WaitMs)
		eq(t, id+" ackBy", a.AckBy, b.AckBy)
	}
	if k := r2.kinds(t); len(k) != 0 {
		t.Errorf("journal after a continued restart = %v, want nothing", k)
	}
	if r2.Summary().Rev == 0 {
		t.Error("a continued occurrence bumps no Rev: an HMI would not refetch")
	}
}

// The field not re-asserting ends the carried occurrence: with no grace
// (a pair) the first false reading, and the ack goes with it.
func TestRestartFalseEndsTheOccurrence(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	old := r.rec(t, "A").Occurrence
	r2 := restart(t, r, r.Snapshot(), 0, def("A", "A"))
	r2.tags["A"] = false
	r2.Evaluate()
	evs := r2.events(t)
	if len(evs) != 1 || evs[0].Kind != "system" || evs[0].Occurrence != old ||
		!strings.Contains(evs[0].Name, "ack of A by op not carried") {
		t.Fatalf("journal after the carried ack ended = %+v; want one system note naming it", evs)
	}
	r2.tags["A"] = true
	r2.clk.advance(time.Second)
	r2.Evaluate()
	a := r2.rec(t, "A")
	eq(t, "state", a.State, UnackActive)
	if a.Occurrence == old {
		t.Error("a new activation continued the old occurrence")
	}
}

// Inside a standalone controller's grace a false reading may be a
// driver's default: the occurrence may still continue, but never
// acknowledged. After the grace a false reading ends it.
func TestRestartGraceCarriesNoAck(t *testing.T) {
	r := newRig(t, def("A", "A"), def("B", "B"))
	r.tags["A"], r.tags["B"] = true, true
	r.Evaluate()
	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	r.clk.advance(time.Minute)
	snap := r.Snapshot()
	oldA, oldB := r.rec(t, "A"), r.rec(t, "B")

	r2 := restart(t, r, snap, time.Minute, def("A", "A"), def("B", "B"))
	r2.tags["A"], r2.tags["B"] = false, false
	r2.Evaluate()
	eq(t, "A held reads normal", r2.state(t, "A"), Normal)
	r2.clk.advance(10 * time.Second)
	r2.tags["A"] = true
	r2.Evaluate()
	a := r2.rec(t, "A")
	eq(t, "A after false then true inside the grace", a.State, UnackActive)
	eq(t, "A occurrence", a.Occurrence, oldA.Occurrence)
	eq(t, "A waits from now (it was acked)", a.WaitMs, r2.clk.t.UnixMilli())

	r2.clk.advance(time.Minute) // past the grace, B still false
	r2.Evaluate()
	r2.tags["B"] = true
	r2.clk.advance(time.Second)
	r2.Evaluate()
	if b := r2.rec(t, "B"); b.Occurrence == oldB.Occurrence {
		t.Error("B continued after a false reading past the grace")
	}
}

// An unreadable condition (a remote site not yet birthed) holds the
// carried occurrence, ack included, up to the hold; enable false ends it.
func TestRestartUnreadableHoldsEnableFalseEnds(t *testing.T) {
	en := def("E", "E")
	en.Enable = "en"
	defs := []Def{def("R", "R"), def("L", "L"), en}
	r := newRig(t, defs...)
	r.tags["R"], r.tags["L"], r.tags["E"], r.tags["en"] = true, true, true, true
	r.Evaluate()
	if _, err := r.Ack(nil, "op"); err != nil {
		t.Fatal(err)
	}
	snap := r.Snapshot()
	old := map[string]string{"R": r.rec(t, "R").Occurrence, "L": r.rec(t, "L").Occurrence, "E": r.rec(t, "E").Occurrence}

	r2 := restart(t, r, snap, 0, defs...)
	r2.tags["en"], r2.tags["E"] = false, true
	r2.Evaluate() // R, L missing; E disabled
	r2.clk.advance(time.Minute)
	r2.tags["R"] = true
	r2.Evaluate()
	rr := r2.rec(t, "R")
	eq(t, "R after a minute unreadable", rr.State, AckActive)
	eq(t, "R occurrence", rr.Occurrence, old["R"])

	r2.clk.advance(DefaultCarryHold) // L still missing, past the hold
	r2.Evaluate()
	r2.tags["L"], r2.tags["en"] = true, true
	r2.clk.advance(time.Second)
	r2.Evaluate()
	if l := r2.rec(t, "L"); l.State != UnackActive || l.Occurrence == old["L"] {
		t.Errorf("L past the hold = %s %q, want a new unacked occurrence", l.State, l.Occurrence)
	}
	if e := r2.rec(t, "E"); e.State != UnackActive || e.Occurrence == old["E"] {
		t.Errorf("E after enable false = %s %q, want a new unacked occurrence", e.State, e.Occurrence)
	}
}

// A shelved alarm continues under its shelf; a second restart before the
// condition was read keeps the carried occurrence.
func TestRestartShelvedAndTwice(t *testing.T) {
	r := newRig(t, def("A", "A"), def("B", "B"))
	r.tags["A"], r.tags["B"] = true, true
	r.Evaluate()
	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	if err := r.Shelve("A", r.clk.at(time.Hour), "op"); err != nil {
		t.Fatal(err)
	}
	oldA, oldB := r.rec(t, "A").Occurrence, r.rec(t, "B")
	r2 := restart(t, r, r.Snapshot(), 0, def("A", "A"), def("B", "B"))
	r2.tags["A"] = true
	r2.Evaluate() // B missing: still held
	r3 := restart(t, r2, r2.Snapshot(), 0, def("A", "A"), def("B", "B"))
	r3.tags["A"], r3.tags["B"] = true, true
	r3.Evaluate()
	eq(t, "A shelved", r3.state(t, "A"), Shelved)
	if err := r3.Unshelve("A", "op"); err != nil {
		t.Fatal(err)
	}
	a := r3.rec(t, "A")
	eq(t, "A unshelved", a.State, AckActive)
	eq(t, "A occurrence", a.Occurrence, oldA)
	b := r3.rec(t, "B")
	eq(t, "B occurrence after two restarts", b.Occurrence, oldB.Occurrence)
	eq(t, "B wait after two restarts", b.WaitMs, oldB.WaitMs)
}

// A retained ack written before occurrences applies to the first
// activation, as it always did.
func TestLegacyRetainedAck(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.Restore(map[string]retain.AlarmRetain{"A": {Acked: true, AckBy: "op", AckMs: r.clk.t.UnixMilli()}})
	r.tags["A"] = false
	r.Evaluate()
	r.tags["A"] = true
	r.clk.advance(time.Second)
	r.Evaluate()
	eq(t, "legacy ack", r.state(t, "A"), AckActive)
}

func TestNotifiedCarriesTheMessagesOccurrence(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	r.Notified("A", Notice{Level: 1, To: "op", Via: "log", Result: NotifySent, Occurrence: "0123456789abcdef"})
	evs := r.events(t)
	eq(t, "notify occurrence", evs[len(evs)-1].Occurrence, "0123456789abcdef")
}

// A committed ack (a pair's log) on an occurrence the engine did not
// continue: adopted only if the alarm has read true since the restore,
// without a false reading or an end; else refused.
func TestCommittedAckAdoptsTheStandingOccurrence(t *testing.T) {
	act := newClock().t.Add(-time.Hour).UnixMilli()
	for _, tc := range []struct {
		name  string
		reads []bool // the condition at each evaluation since the restore
		adopt bool
	}{
		{"stood throughout", []bool{true, true}, true},
		{"false first", []bool{false, true}, false},
		{"dropped and back", []bool{true, false, true}, false},
	} {
		r := newRig(t, def("A", "A"))
		for _, v := range tc.reads {
			r.tags["A"] = v
			r.Evaluate()
			r.clk.advance(time.Second)
		}
		cur := r.rec(t, "A").Occurrence
		ok, err := r.AckCommitted("A", "7777777777777777", act, "op")
		a := r.rec(t, "A")
		if tc.adopt {
			if !ok || err != nil || a.State != AckActive || a.Occurrence != "7777777777777777" || a.ActiveMs != act {
				t.Errorf("%s: %v %v, %s on %s since %d; want acked on the committed occurrence", tc.name, ok, err, a.State, a.Occurrence, a.ActiveMs)
			}
			continue
		}
		if !errors.Is(err, ErrOtherOccurrence) || a.State != UnackActive || a.Occurrence != cur {
			t.Errorf("%s: %v, %s on %s; want refused, unacked, on its own occurrence", tc.name, err, a.State, a.Occurrence)
		}
	}
}
