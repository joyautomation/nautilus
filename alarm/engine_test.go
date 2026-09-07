package alarm

import (
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// clock is a stopped clock that only moves when a test says so — the same
// shape the acceptance harness's VirtualClock has, and the reason the
// engine takes Now as a function rather than calling time.Now.
type clock struct{ t time.Time }

func newClock() *clock { return &clock{t: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time               { return c.t }
func (c *clock) advance(d time.Duration)      { c.t = c.t.Add(d) }
func (c *clock) at(d time.Duration) time.Time { return c.t.Add(d) }

// tags is a stand-in for the tag store: the engine only ever asks it to
// resolve a path.
type tags map[string]any

func (m tags) read(p string) (any, bool) { v, ok := m[p]; return v, ok }

// rig is one engine plus the clock and tags driving it.
type rig struct {
	*Engine
	clk  *clock
	tags tags
	ring *RingJournal
}

func newRig(t *testing.T, defs ...Def) *rig {
	t.Helper()
	clk, tg, ring := newClock(), tags{}, NewRing(100)
	e, err := New(Options{Defs: defs, Read: tg.read, Now: clk.now, Journal: ring})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	return &rig{Engine: e, clk: clk, tags: tg, ring: ring}
}

// def is a definition with the package's policy defaults, which is what a
// YAML entry with nothing but a tag decodes to.
func def(id, tag string) Def {
	return Def{ID: id, Tag: tag, Name: id, Priority: Medium,
		AckRequired: true, AutoClear: true, Shelvable: true}
}

func (r *rig) state(t *testing.T, id string) State {
	t.Helper()
	for _, rec := range r.Records() {
		if rec.ID == id {
			return rec.State
		}
	}
	t.Fatalf("no alarm %q", id)
	return Normal
}

// kinds is the journal as a list of event kinds, oldest first — the shape
// the acceptance harness's `journal: [active, ack, rtn]` asserts.
func (r *rig) kinds(t *testing.T) []string {
	t.Helper()
	evs, err := r.Journal(time.Time{}, time.Time{}, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(evs))
	for i, e := range evs {
		out[len(evs)-1-i] = e.Kind // Query is newest first
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestLifecycleActiveAckRTN is the ordinary path an operator sees.
func TestLifecycleActiveAckRTN(t *testing.T) {
	r := newRig(t, def("HH", "FIT.HH"))

	r.tags["FIT.HH"] = false
	r.Evaluate()
	eq(t, "state", r.state(t, "HH"), Normal)

	r.tags["FIT.HH"] = true
	r.Evaluate()
	eq(t, "state", r.state(t, "HH"), UnackActive)
	eq(t, "count", r.Records()[0].Count, 1)

	n, err := r.Ack([]string{"HH"}, "rchon")
	if err != nil || n != 1 {
		t.Fatalf("Ack = %d, %v", n, err)
	}
	eq(t, "state", r.state(t, "HH"), AckActive)
	eq(t, "ackBy", r.Records()[0].AckBy, "rchon")

	r.tags["FIT.HH"] = false
	r.Evaluate()
	eq(t, "state", r.state(t, "HH"), Normal)
	eq(t, "journal", strings.Join(r.kinds(t), ","), "active,ack,rtn")
}

// TestOnDelayQualifiesAgainstTheClock walks a five-minute on-delay on a
// stopped clock, which is the whole reason Now is injected.
func TestOnDelayQualifiesAgainstTheClock(t *testing.T) {
	d := def("HH", "FIT.HH")
	d.OnDelay = 5 * time.Minute
	r := newRig(t, d)

	r.tags["FIT.HH"] = true
	r.Evaluate()
	eq(t, "state at t+0", r.state(t, "HH"), Normal)

	r.clk.advance(4 * time.Minute)
	r.Evaluate()
	eq(t, "state at t+4m", r.state(t, "HH"), Normal)

	r.clk.advance(time.Minute + time.Second)
	r.Evaluate()
	eq(t, "state at t+5m1s", r.state(t, "HH"), UnackActive)

	// A bit that drops before the delay elapses never qualifies at all.
	r2 := newRig(t, d)
	r2.tags["FIT.HH"] = true
	r2.Evaluate()
	r2.clk.advance(4 * time.Minute)
	r2.tags["FIT.HH"] = false
	r2.Evaluate()
	r2.clk.advance(10 * time.Minute)
	r2.Evaluate()
	eq(t, "transient state", r2.state(t, "HH"), Normal)
	eq(t, "transient journal", len(r2.kinds(t)), 0)
}

func TestOffDelayHoldsTheAlarmUp(t *testing.T) {
	d := def("HH", "FIT.HH")
	d.OffDelay = 30 * time.Second
	r := newRig(t, d)

	r.tags["FIT.HH"] = true
	r.Evaluate()
	eq(t, "state", r.state(t, "HH"), UnackActive)

	r.tags["FIT.HH"] = false
	r.Evaluate()
	eq(t, "state right after the bit drops", r.state(t, "HH"), UnackActive)

	r.clk.advance(31 * time.Second)
	r.Evaluate()
	eq(t, "state after the off-delay", r.state(t, "HH"), UnackRTN)
}

// TestStateMachineTable walks the transition table for the four policy
// combinations that matter.
func TestStateMachineTable(t *testing.T) {
	cases := []struct {
		name             string
		ack, clear       bool
		steps            []bool // condition per evaluation
		ackAfter         int    // ack after this many steps; -1 = never
		wantState        State
		wantJournalKinds string
	}{
		{
			name: "ack-required, auto-clear: the ordinary alarm",
			ack:  true, clear: true, steps: []bool{true, false}, ackAfter: -1,
			wantState: UnackRTN, wantJournalKinds: "active,rtn",
		},
		{
			name: "annunciate-only collapses unack-active to ack-active",
			ack:  false, clear: true, steps: []bool{true}, ackAfter: -1,
			wantState: AckActive, wantJournalKinds: "active",
		},
		{
			name: "annunciate-only returns straight to normal",
			ack:  false, clear: true, steps: []bool{true, false}, ackAfter: -1,
			wantState: Normal, wantJournalKinds: "active,rtn",
		},
		{
			name: "latched: acked, then cleared, still wants an ack",
			ack:  true, clear: false, steps: []bool{true, false}, ackAfter: 1,
			wantState: UnackRTN, wantJournalKinds: "active,ack,rtn",
		},
		{
			name: "auto-clear: acked, then cleared, is done",
			ack:  true, clear: true, steps: []bool{true, false}, ackAfter: 1,
			wantState: Normal, wantJournalKinds: "active,ack,rtn",
		},
		{
			name: "unack-RTN re-activating counts a second activation",
			ack:  true, clear: true, steps: []bool{true, false, true}, ackAfter: -1,
			wantState: UnackActive, wantJournalKinds: "active,rtn,active",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := def("A", "A")
			d.AckRequired, d.AutoClear = c.ack, c.clear
			r := newRig(t, d)
			for i, cond := range c.steps {
				r.tags["A"] = cond
				r.Evaluate()
				r.clk.advance(time.Second)
				if i+1 == c.ackAfter {
					if _, err := r.Ack([]string{"A"}, "op"); err != nil {
						t.Fatal(err)
					}
				}
			}
			eq(t, "state", r.state(t, "A"), c.wantState)
			eq(t, "journal", strings.Join(r.kinds(t), ","), c.wantJournalKinds)
		})
	}
}

func TestUnackRTNRequiresAnAck(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	r.tags["A"] = false
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), UnackRTN)
	eq(t, "still on the active list", len(r.Active()), 1)

	if _, err := r.Ack([]string{"A"}, "op"); err != nil {
		t.Fatal(err)
	}
	eq(t, "state after ack", r.state(t, "A"), Normal)
	eq(t, "off the active list", len(r.Active()), 0)
}

func TestShelveExpires(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), UnackActive)

	if err := r.Shelve("A", r.clk.at(15*time.Minute), "op"); err != nil {
		t.Fatal(err)
	}
	eq(t, "state", r.state(t, "A"), Shelved)
	eq(t, "summary shelved", r.Summary().Shelved, 1)
	eq(t, "summary active", r.Summary().Active, 0)

	r.clk.advance(14 * time.Minute)
	r.Evaluate()
	eq(t, "state before expiry", r.state(t, "A"), Shelved)

	r.clk.advance(2 * time.Minute)
	r.Evaluate()
	eq(t, "state after expiry", r.state(t, "A"), UnackActive)
	eq(t, "journal", strings.Join(r.kinds(t), ","), "active,shelve,unshelve")
}

func TestShelveIsSilentButStillTracks(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	if err := r.Shelve("A", r.clk.at(time.Hour), "op"); err != nil {
		t.Fatal(err)
	}
	// The condition clears and comes back while shelved: no events at all.
	r.tags["A"] = false
	r.Evaluate()
	r.tags["A"] = true
	r.Evaluate()
	eq(t, "journal", strings.Join(r.kinds(t), ","), "active,shelve")

	if err := r.Unshelve("A", "op"); err != nil {
		t.Fatal(err)
	}
	eq(t, "state", r.state(t, "A"), UnackActive)
}

func TestShelveRejectsThePastAndTheUnshelvable(t *testing.T) {
	d := def("A", "A")
	d.Shelvable = false
	r := newRig(t, d, def("B", "B"))

	if err := r.Shelve("A", r.clk.at(time.Hour), "op"); err == nil {
		t.Error("a non-shelvable alarm should refuse to shelve")
	}
	if err := r.Shelve("B", r.clk.at(-time.Minute), "op"); err == nil {
		t.Error("a shelf in the past should be refused — there is no permanent shelf")
	}
	if err := r.Shelve("nope", r.clk.at(time.Hour), "op"); err == nil {
		t.Error("an unknown id should be an error")
	}
	if err := r.Unshelve("B", "op"); err == nil {
		t.Error("unshelving something that is not shelved should be an error")
	}
}

func TestAckingAShelvedAlarmAcksWhatIsUnderneath(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	if err := r.Shelve("A", r.clk.at(time.Hour), "op"); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Ack([]string{"A"}, "op"); err != nil || n != 1 {
		t.Fatalf("Ack = %d, %v", n, err)
	}
	if err := r.Unshelve("A", "op"); err != nil {
		t.Fatal(err)
	}
	eq(t, "state", r.state(t, "A"), AckActive)
}

// TestSuppressedWhenEnableFalse — the flood control for a site going dark.
func TestSuppressedWhenEnableFalse(t *testing.T) {
	d := def("A", "A")
	d.Enable = "RTU9__Online"
	r := newRig(t, d)

	r.tags["RTU9__Online"] = true
	r.tags["A"] = true
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), UnackActive)

	// The node dies. Host tags hold their last values, so A stays true —
	// suppression is what stops it annunciating forever.
	r.tags["RTU9__Online"] = false
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), Suppressed)
	eq(t, "summary", r.Summary().Suppressed, 1)
	eq(t, "active list", len(r.Active()), 0)

	r.tags["RTU9__Online"] = true
	r.Evaluate()
	eq(t, "state on return", r.state(t, "A"), UnackActive)
	eq(t, "journal", strings.Join(r.kinds(t), ","), "active,suppress,unsuppress,active")
}

// TestMissingTagSuppressesWithAReason — one dark site must not fault the
// engine, and "why" must be visible.
func TestMissingTagSuppressesWithAReason(t *testing.T) {
	r := newRig(t, def("A", "RTU99_NEVER_BIRTHED.HH"))
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), Suppressed)
	eq(t, "missing", r.Missing(), 1)
	if reason := r.Records()[0].Reason; !strings.Contains(reason, "RTU99_NEVER_BIRTHED.HH") {
		t.Errorf("reason = %q, want it to name the path", reason)
	}
}

func TestNonBoolConditionIsSuppressedNotFatal(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = "high"
	r.Evaluate()
	eq(t, "state", r.state(t, "A"), Suppressed)
	if reason := r.Records()[0].Reason; !strings.Contains(reason, "not a BOOL") {
		t.Errorf("reason = %q", reason)
	}
}

// TestReadsWhatTheTagStoreReturns covers every shape Tags.ReadPath hands
// back — plain Go leaves for scalars, an ir.Value for anything else — so
// wiring the engine to the real store is `Read: tags.ReadPath` and nothing
// more.
func TestReadsWhatTheTagStoreReturns(t *testing.T) {
	cases := []struct {
		name string
		v    any
		want State
	}{
		{"plain bool", true, UnackActive},
		{"plain bool false", false, Normal},
		{"float64 leaf", float64(1), UnackActive},
		{"float64 zero", float64(0), Normal},
		{"int64 leaf", int64(1), UnackActive},
		{"ir bool", ir.BoolVal(true), UnackActive},
		{"ir real zero", ir.RealVal(0), Normal},
		{"string leaf", "high", Suppressed},
		{"whole struct", ir.Value{Kind: ir.TypeStruct, Struct: analogInput}, Suppressed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, def("A", "A"))
			r.tags["A"] = c.v
			r.Evaluate()
			eq(t, "state", r.state(t, "A"), c.want)
		})
	}
}

// TestSummaryRevBumpsOnlyOnChange is the contract the SSE frame depends on:
// an HMI refetches when Rev moves and only then.
func TestSummaryRevBumpsOnlyOnChange(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = false
	r.Evaluate()
	rev0 := r.Summary().Rev

	for i := 0; i < 5; i++ {
		r.clk.advance(time.Second)
		r.Evaluate()
	}
	eq(t, "rev after five quiet scans", r.Summary().Rev, rev0)

	r.tags["A"] = true
	r.Evaluate()
	rev1 := r.Summary().Rev
	if rev1 <= rev0 {
		t.Fatalf("rev did not bump on activation: %d -> %d", rev0, rev1)
	}
	r.Evaluate()
	eq(t, "rev while nothing moves", r.Summary().Rev, rev1)

	if _, err := r.Ack(nil, "op"); err != nil {
		t.Fatal(err)
	}
	if r.Summary().Rev <= rev1 {
		t.Fatal("rev did not bump on ack")
	}
}

func TestSummaryCounts(t *testing.T) {
	crit, low := def("C", "C"), def("L", "L")
	crit.Priority, low.Priority = Critical, Low
	shelvedDef := def("S", "S")
	r := newRig(t, crit, low, shelvedDef)

	r.tags["C"], r.tags["L"], r.tags["S"] = true, true, true
	r.Evaluate()
	if err := r.Shelve("S", r.clk.at(time.Hour), "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Ack([]string{"L"}, "op"); err != nil {
		t.Fatal(err)
	}

	s := r.Summary()
	eq(t, "active", s.Active, 2)
	eq(t, "unacked", s.Unacked, 1)
	eq(t, "shelved", s.Shelved, 1)
	eq(t, "worst", s.Worst, Critical)
	eq(t, "byPriority critical", s.ByPriority["critical"], 1)
	eq(t, "byPriority low", s.ByPriority["low"], 1)
	if s.Newest == nil || s.Newest.ID != "C" {
		t.Fatalf("newest = %+v, want the unacked critical", s.Newest)
	}
}

// TestActiveSortsWorstThenNewest is the alarm table's order, computed once
// here rather than in every client.
func TestActiveSortsWorstThenNewest(t *testing.T) {
	a, b, c := def("a", "a"), def("b", "b"), def("c", "c")
	a.Priority, b.Priority, c.Priority = Low, Critical, Critical
	r := newRig(t, a, b, c)

	r.tags["b"] = true
	r.Evaluate()
	r.clk.advance(time.Minute)
	r.tags["c"] = true
	r.Evaluate()
	r.clk.advance(time.Minute)
	r.tags["a"] = true
	r.Evaluate()

	got := make([]string, 0, 3)
	for _, rec := range r.Active() {
		got = append(got, rec.ID)
	}
	eq(t, "order", strings.Join(got, ","), "c,b,a")
}

func TestAckAllAndUnknownID(t *testing.T) {
	r := newRig(t, def("A", "A"), def("B", "B"))
	r.tags["A"], r.tags["B"] = true, true
	r.Evaluate()

	if _, err := r.Ack([]string{"A", "nope"}, "op"); err == nil {
		t.Fatal("an unknown id should be an error")
	}
	// ... and must not have applied the half it could.
	eq(t, "A untouched", r.state(t, "A"), UnackActive)

	n, err := r.Ack(nil, "op")
	if err != nil || n != 2 {
		t.Fatalf("Ack all = %d, %v", n, err)
	}
	eq(t, "unacked", r.Summary().Unacked, 0)

	n, _ = r.Ack([]string{"*"}, "op")
	eq(t, "acking an already-acked set changes nothing", n, 0)
}

// TestRetainedAckSurvivesRestart is the redundancy-correctness claim: a
// failover must not resurrect acked alarms as unacked.
func TestRetainedAckSurvivesRestart(t *testing.T) {
	r := newRig(t, def("A", "A"), def("B", "B"))
	r.tags["A"], r.tags["B"] = true, true
	r.Evaluate()
	if _, err := r.Ack([]string{"A"}, "rchon"); err != nil {
		t.Fatal(err)
	}
	if err := r.Shelve("B", r.clk.at(time.Hour), "rchon"); err != nil {
		t.Fatal(err)
	}

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %+v, want the acked and the shelved", snap)
	}
	if !snap["A"].Acked || snap["A"].AckBy != "rchon" {
		t.Errorf("snapshot A = %+v", snap["A"])
	}
	if snap["B"].ShelfUntilMs == 0 {
		t.Errorf("snapshot B = %+v", snap["B"])
	}

	// Restart: a fresh engine, the same clock, the field still asserting.
	clk2, tg2 := &clock{t: r.clk.t}, tags{"A": true, "B": true}
	e2, err := New(Options{
		Defs: []Def{def("A", "A"), def("B", "B")},
		Read: tg2.read, Now: clk2.now, Journal: NewRing(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	e2.Restore(snap)
	e2.Evaluate()

	recs := map[string]Record{}
	for _, rec := range e2.Records() {
		recs[rec.ID] = rec
	}
	eq(t, "A after restore", recs["A"].State, AckActive)
	eq(t, "A ackBy", recs["A"].AckBy, "rchon")
	eq(t, "B after restore", recs["B"].State, Shelved)
	eq(t, "unacked after restore", e2.Summary().Unacked, 0)

	// The retained ack applies to that one activation and no more: clear
	// the bit, re-assert it, and the alarm annunciates properly.
	tg2["A"] = false
	clk2.advance(time.Second)
	e2.Evaluate()
	tg2["A"] = true
	clk2.advance(time.Second)
	e2.Evaluate()
	eq(t, "A on re-activation", e2.Records()[0].State, UnackActive)
}

func TestRestoreIgnoresUnknownAndExpired(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.Restore(map[string]retain.AlarmRetain{
		"gone": {Acked: true},
		"A":    {ShelfUntilMs: r.clk.at(-time.Hour).UnixMilli(), ShelfBy: "op"},
	})
	r.tags["A"] = true
	r.Evaluate()
	eq(t, "an expired shelf should not apply", r.state(t, "A"), UnackActive)
}

func TestNewRejectsBadDefs(t *testing.T) {
	tg := tags{}
	cases := []struct {
		name string
		defs []Def
	}{
		{"no tag", []Def{{ID: "A"}}},
		{"duplicate id", []Def{{ID: "A", Tag: "A"}, {ID: "A", Tag: "B"}}},
	}
	for _, c := range cases {
		if _, err := New(Options{Defs: c.defs, Read: tg.read}); err == nil {
			t.Errorf("%s: New should have failed", c.name)
		}
	}
	if _, err := New(Options{Defs: []Def{def("A", "A")}}); err == nil {
		t.Error("New without Read should have failed")
	}
}

func TestRecordJSONShape(t *testing.T) {
	r := newRig(t, def("A", "A"))
	r.tags["A"] = true
	r.Evaluate()
	b := mustJSON(t, r.Active()[0])
	for _, want := range []string{`"id":"A"`, `"state":"unack-active"`, `"priority":"medium"`, `"cond":true`} {
		if !strings.Contains(b, want) {
			t.Errorf("Record JSON missing %s: %s", want, b)
		}
	}
}
