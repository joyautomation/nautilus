package runtime

import (
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/retain"
)

// Substitutes in the retain store (#707 PR 3a): a single controller's
// restart, a takeover from a shared store, the ID high-water mark, the
// last-seen wall time, __Substitute_Fault, and the leadership re-check
// before an expiry.

// newRetainSubRig is newExpiryRig with a retain store, on the clock clk
// (shared by the "processes" of one test, as the controller's clocks are
// across its restart), with these substitutable inputs.
func newRetainSubRig(t *testing.T, coord Coordinator, store retain.Store, clk *subClock, substitutable ...string) (*Runtime, *hookDriver) {
	t.Helper()
	if substitutable == nil {
		substitutable = []string{"Level", "Flow"}
	}
	d := &hookDriver{fieldDriver: &fieldDriver{in: nio.Values{"Level": 10.0, "Flow": 3.0}}}
	rt, err := New(Options{
		Program: expiryProgram, Driver: d, Clock: clk, Coordinator: coord, Retain: store,
		Tags: []TagDef{
			Input("Level"), Input("Flow"),
			State("SawFlag", false), State("SawExpired", false), State("Permit", false), State("Mirror", 0.0),
		},
		Substitutable: substitutable,
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.mono = clk.monoNow
	return rt, d
}

func noon() *subClock {
	return &subClock{wall: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC), mono: 1000 * time.Hour}
}

// save is one saver tick, as Run's saver would make it.
func save(t *testing.T, rt *Runtime) {
	t.Helper()
	rt.saveRetained(nil)
}

// collect gathers every event the runtime reports, from now on (and any
// queued before a first listener).
func collect(rt *Runtime) func() []SubstituteEvent {
	ch := make(chan SubstituteEvent, 64)
	rt.OnSubstituteEvent(func(e SubstituteEvent) { ch <- e })
	return func() []SubstituteEvent {
		var out []SubstituteEvent
		for {
			select {
			case e := <-ch:
				out = append(out, e)
			case <-time.After(200 * time.Millisecond):
				return out
			}
		}
	}
}

func hasEvent(evs []SubstituteEvent, kind, reason string) bool {
	for _, e := range evs {
		if e.Kind == kind && e.Reason == reason {
			return true
		}
	}
	return false
}

// A restart within the granted time brings every entry back as it was:
// value, who, ack state, its ID, and the remainder from until (the time
// down counted), and the first scan's logic runs on it.
func TestSubstitutesSurviveARestart(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	a := subFor(t, rt, "Level", 95, time.Hour, true)
	rt.AckSubstitutes([]uint64{a.ID}, "sup")
	b := subFor(t, rt, "Flow", 7, 2*time.Hour, false)
	clk.advance(10 * time.Minute)
	save(t, rt)

	clk.advance(10 * time.Minute) // down for 10 min
	rt2, _ := newRetainSubRig(t, nil, store, clk)
	rt2.LoadRetained()
	// Level and Flow have no initial value, so they exist from the first
	// read: the entries wait for it, and apply before the first logic.
	rt2.Scan()
	if got := rt2.Tags().Real("Mirror"); got != 95 || !rt2.Tags().Bool("SawFlag") || !rt2.Tags().Bool("Permit") {
		t.Fatalf("first scan: Mirror %v SawFlag %v Permit %v; want the logic on the substitute, flagged, acked",
			got, rt2.Tags().Bool("SawFlag"), rt2.Tags().Bool("Permit"))
	}
	l := rt2.Substitutes()
	if len(l) != 2 {
		t.Fatalf("after the restart: %+v; want both entries", l)
	}
	want := map[string]struct {
		id         uint64
		left       time.Duration
		by         string
		ack, acked bool
	}{
		"Level": {a.ID, 40 * time.Minute, "op", true, true},
		"Flow":  {b.ID, 100 * time.Minute, "op", false, false},
	}
	for _, s := range l {
		w := want[s.Name]
		if s.ID != w.id || s.RemainingMs != w.left.Milliseconds() || s.By != w.by || s.AckOnStart != w.ack || s.Acked != w.acked {
			t.Errorf("%s: %+v; want ID %d, %v left (downtime counted), by %s, ack %v/%v", s.Name, s, w.id, w.left, w.by, w.ack, w.acked)
		}
	}
	clk.advance(40 * time.Minute)
	rt2.Scan()
	if rt2.Tags().Real("Level") != 10 || len(rt2.Substitutes()) != 1 {
		t.Fatal("Level did not expire at its granted hour after the restart")
	}
}

// The restart rule: the remainder is until − now on the wall clock, so
// time down counts; and a clock that went backwards across the restart
// (before the start, or before the last time the controller saw it)
// expires the entry, audited as expired-while-down, with the pulse.
func TestSubstituteRestartRemainder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted time.Duration
		ran     time.Duration // wall time from the start to the last save
		restart time.Duration // wall clock at the restart, from the start
		left    time.Duration // 0: expired
	}{
		{"back within the time", 8 * time.Hour, 3 * time.Hour, 3*time.Hour + 10*time.Minute, 4*time.Hour + 50*time.Minute},
		{"down past until: downtime counts", time.Hour, 10 * time.Minute, 2 * time.Hour, 0},
		{"the clock reads 12:30 at a true 15:00", 8 * time.Hour, 3 * time.Hour, 30 * time.Minute, 0},
		{"the clock went back past the start", 8 * time.Hour, time.Hour, -time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, clk := &fakeStore{}, noon()
			rt, _ := newRetainSubRig(t, nil, store, clk)
			rt.LoadRetained()
			rt.Scan()
			subFor(t, rt, "Level", 95, tc.granted, false)
			clk.advance(tc.ran)
			save(t, rt)
			clk.step(tc.restart - tc.ran)
			rt2, _ := newRetainSubRig(t, nil, store, clk)
			rt2.LoadRetained()
			events := collect(rt2) // registered after the load, as the server is
			rt2.Scan()
			l := rt2.Substitutes()
			if tc.left > 0 {
				if len(l) != 1 || l[0].RemainingMs != tc.left.Milliseconds() {
					t.Fatalf("%+v; want %v left", l, tc.left)
				}
				return
			}
			if len(l) != 0 || rt2.Tags().Real("Level") != 10 || rt2.Tags().Real("Mirror") != 10 {
				t.Fatalf("%+v, Level %v: want it expired at the load and the live value in the first scan", l, rt2.Tags().Real("Level"))
			}
			if !rt2.Tags().Bool("SawExpired") {
				t.Fatal("no __Substitute_Expired pulse on the first scan after an expiry at the load")
			}
			if evs := events(); !hasEvent(evs, SubstituteEventExpire, SubstituteReasonExpiredWhileDown) {
				t.Fatalf("events %+v; want expire/expired-while-down, delivered to a listener registered after the load", evs)
			}
		})
	}
}

// The pure rule, with last-seen: a restart whose clock reads earlier than
// the last time the controller saw it expires, however much time until
// leaves; zero last-seen (a store from before it was kept) is not used.
func TestSubstituteRemainderLastSeen(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	until := t0.Add(8 * time.Hour)
	seen := t0.Add(3 * time.Hour)
	if r := substituteRemainder(t0, until, 8*time.Hour, t0.Add(30*time.Minute), seen, remainRestart); !r.expired {
		t.Errorf("clock at 12:30 after last seen at 15:00: %+v; want expired", r)
	}
	if r := substituteRemainder(t0, until, 8*time.Hour, seen, seen, remainRestart); r.expired || r.left != 5*time.Hour {
		t.Errorf("clock exactly at last seen: %+v; want 5 h left", r)
	}
	if r := substituteRemainder(t0, until, 8*time.Hour, t0.Add(30*time.Minute), time.Time{}, remainRestart); r.expired || r.left != 7*time.Hour+30*time.Minute {
		t.Errorf("no last seen: %+v; want until − now", r)
	}
	if r := substituteRemainder(t0, until, 8*time.Hour, t0.Add(30*time.Minute), seen, remainTakeover); r.expired {
		t.Errorf("takeover mode ignores last seen (another replica's clock): %+v", r)
	}
}

// An entry's ID is never issued twice, across a restart too: the
// high-water mark is retained even when no entry is active, a new start
// takes the next ID, an ended ID supplied again is refused, and a stale
// stop naming it ends nothing.
func TestSubstituteIDsAreNeverReused(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	a := subFor(t, rt, "Level", 95, time.Hour, false)
	b := subFor(t, rt, "Flow", 7, time.Hour, false)
	rt.Restore(a.ID, "op")
	rt.Restore(b.ID, "op")
	save(t, rt)
	if st := store.state.Substitutes; st == nil || st.HighID != b.ID || len(st.Entries) != 0 || st.LastSeenMs != 0 {
		t.Fatalf("retained %+v; want the high-water mark %d, no entries, no last-seen", st, b.ID)
	}
	rt2, _ := newRetainSubRig(t, nil, store, clk)
	rt2.LoadRetained()
	rt2.Scan()
	c := subFor(t, rt2, "Level", 96, time.Hour, false)
	if c.ID <= b.ID {
		t.Fatalf("the first start after the restart got ID %d; IDs up to %d were issued before it", c.ID, b.ID)
	}
	if _, ok := rt2.Restore(a.ID, "late"); ok {
		t.Fatal("a stale stop of an ended ID ended something")
	}
	if _, err := rt2.Substitute("Flow", SubstituteRequest{Value: 1.0, For: time.Hour, By: "op", ID: b.ID}); err == nil || !strings.Contains(err.Error(), "never reused") {
		t.Fatalf("an ended ID supplied again: %v; want it refused", err)
	}
	if l := rt2.Substitutes(); len(l) != 1 || l[0].ID != c.ID {
		t.Fatalf("after the refusals: %+v; want only the new entry", l)
	}
}

// Last-seen is written only while a substitute runs, rounded down to 30 s,
// so a running substitute costs one store write per 30 s, not one per
// saver tick, and an idle controller none.
func TestSubstituteLastSeenCadence(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	var last []byte
	tick := func() { last = rt.saveRetained(last) }
	tick()
	idle := store.saves
	clk.advance(5 * time.Minute)
	tick()
	if store.saves != idle {
		t.Fatalf("%d writes with no substitute and nothing changed; want none", store.saves-idle)
	}
	subFor(t, rt, "Level", 95, time.Hour, false)
	tick()
	base := store.saves
	for i := 0; i < 14; i++ { // 28 s of 2 s ticks: the same 30 s step
		clk.advance(2 * time.Second)
		tick()
	}
	if store.saves != base {
		t.Fatalf("%d writes inside one 30 s step; want none", store.saves-base)
	}
	clk.advance(2 * time.Second)
	tick()
	if store.saves != base+1 {
		t.Fatalf("%d writes on crossing the 30 s step; want one", store.saves-base)
	}
	if ls := store.state.Substitutes.LastSeenMs; ls%lastSeenStep.Milliseconds() != 0 || ls > clk.Now().UnixMilli() {
		t.Fatalf("last-seen %d is not the clock rounded down to 30 s", ls)
	}
}

// A takeover from a shared store (a coordinator): the store's set
// replaces this replica's table. An entry only here (the leader since
// ended it) stops, superseded; one only in the store (another replica
// started it) applies, with its remainder from until; the program's first
// scan runs on the store's set.
func TestTakeoverFromAStoreReplacesTheTable(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	coord := &flag{lead: true}
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	mine := subFor(t, rt, "Level", 95, time.Hour, false)
	save(t, rt)
	coord.lead = false
	rt.Scan()

	// Meanwhile the other replica led: it ended "mine" and started one on
	// Flow 10 min ago.
	clk.advance(30 * time.Minute)
	other := retain.SubstituteRetain{
		ID: mine.ID + 1, Name: "Flow", Value: 7.0, By: "op2",
		StartMs: clk.Now().Add(-10 * time.Minute).UnixMilli(), UntilMs: clk.Now().Add(50 * time.Minute).UnixMilli(),
		GrantedMs: time.Hour.Milliseconds(),
	}
	store.mu.Lock()
	store.state.Substitutes = &retain.SubstituteState{Entries: []retain.SubstituteRetain{other}, HighID: other.ID}
	store.mu.Unlock()
	events := collect(rt)

	coord.lead = true
	rt.Scan() // takeover
	l := rt.Substitutes()
	if len(l) != 1 || l[0].ID != other.ID || l[0].Name != "Flow" || l[0].RemainingMs != (50*time.Minute).Milliseconds() {
		t.Fatalf("after the takeover: %+v; want only the store's Flow entry, 50 min left", l)
	}
	if rt.Tags().Real("Level") != 10 || rt.Tags().Real("Mirror") != 10 || rt.Tags().Bool("SawFlag") {
		t.Fatalf("Level %v Mirror %v: the first scan ran on an entry the store no longer holds", rt.Tags().Real("Level"), rt.Tags().Real("Mirror"))
	}
	if rt.Tags().Real("Flow") != 7 {
		t.Fatalf("Flow = %v; want the store's substitute 7", rt.Tags().Real("Flow"))
	}
	if evs := events(); !hasEvent(evs, SubstituteEventStop, SubstituteReasonSuperseded) || hasEvent(evs, SubstituteEventExpire, SubstituteReasonExpired) {
		t.Fatalf("events %+v; want a superseded stop and no expiry", evs)
	}
	if c := subFor(t, rt, "Level", 96, time.Hour, false); c.ID <= other.ID {
		t.Fatalf("the new leader's start got ID %d; the store's high-water mark is %d", c.ID, other.ID)
	}
}

// An entry from the store whose until lies more than the skew beyond the
// granted duration (this replica's clock is behind) is capped and raises
// __Substitute_Fault, which is not latched: it clears, with an event,
// when the entry ends.
func TestSubstituteFaultFollowsTheEntry(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	coord := &flag{lead: false}
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	e := retain.SubstituteRetain{
		ID: 5, Name: "Level", Value: 95.0, By: "op",
		StartMs: clk.Now().Add(time.Hour).UnixMilli(), UntilMs: clk.Now().Add(2 * time.Hour).UnixMilli(),
		GrantedMs: time.Hour.Milliseconds(),
	}
	store.state.Substitutes = &retain.SubstituteState{Entries: []retain.SubstituteRetain{e}, HighID: 5}
	events := collect(rt)
	coord.lead = true
	rt.Scan()
	if l := rt.Substitutes(); len(l) != 1 || l[0].RemainingMs != time.Hour.Milliseconds() || l[0].Fault != SubstituteReasonClockCapped {
		t.Fatalf("%+v; want capped at the granted hour, fault clock-capped", l)
	}
	if !rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault is false with a capped entry")
	}
	if _, ok := rt.Restore(5, "op"); !ok {
		t.Fatal("Restore of the capped entry reported none")
	}
	if rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault still true after the faulted entry ended")
	}
	evs := events()
	if !hasEvent(evs, SubstituteEventFault, SubstituteReasonClockCapped) || !hasEvent(evs, SubstituteEventFaultClear, SubstituteReasonClockCapped) {
		t.Fatalf("events %+v; want the fault and its clearing", evs)
	}
}

// A store that cannot be loaded at a takeover leaves the table as it is
// and raises __Substitute_Fault (retain-load-failed) until a load
// succeeds; both edges are events.
func TestSubstituteFaultOnAFailedLoad(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	coord := &flag{lead: true}
	rt, _ := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	s := subFor(t, rt, "Level", 95, time.Hour, false)
	save(t, rt)
	coord.lead = false
	rt.Scan()
	events := collect(rt)
	store.mu.Lock()
	store.fail = true
	store.mu.Unlock()
	coord.lead = true
	rt.Scan()
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != s.ID {
		t.Fatalf("after a failed load: %+v; want the table kept", l)
	}
	if !rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault is false after the store failed to load")
	}
	store.mu.Lock()
	store.fail = false
	store.mu.Unlock()
	coord.lead = false
	rt.Scan()
	coord.lead = true
	rt.Scan()
	if rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault still true after a load succeeded")
	}
	evs := events()
	if !hasEvent(evs, SubstituteEventFault, SubstituteReasonRetainLoadFailed) || !hasEvent(evs, SubstituteEventFaultClear, SubstituteReasonRetainLoadFailed) {
		t.Fatalf("events %+v; want the load fault and its clearing", evs)
	}
}

// A retained entry the project no longer allows (the deploy dropped its
// rule, or its value no longer fits) ends at the load as an expiry,
// refused-at-load, with the pulse: never restored, never dropped silently.
func TestRetainedSubstituteRefusedAtLoad(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Hour, false)
	subFor(t, rt, "Flow", 7, time.Hour, false)
	save(t, rt)
	store.state.Substitutes.Entries[0].Value = "not a number" // Level's entry (ID 1)

	rt2, _ := newRetainSubRig(t, nil, store, clk, "Level") // Flow's rule is gone
	rt2.LoadRetained()
	events := collect(rt2)
	rt2.Scan()
	if l := rt2.Substitutes(); len(l) != 0 {
		t.Fatalf("%+v; want both refused", l)
	}
	if rt2.Tags().Real("Level") != 10 || rt2.Tags().Real("Flow") != 3 || !rt2.Tags().Bool("SawExpired") {
		t.Fatal("want the live values and the expiry pulse on the first scan")
	}
	n := 0
	for _, e := range events() {
		if e.Kind == SubstituteEventExpire && e.Reason == SubstituteReasonRefusedAtLoad {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d refused-at-load expiries; want 2", n)
	}
}

// The expiry step runs after the read, which may have hung past the
// lease: a replica that lost the lease meanwhile leaves the entry for the
// leader (#621), however late it is.
func TestSubstituteExpiryRechecksLeadership(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	coord := &flag{lead: true}
	rt, d := newRetainSubRig(t, coord, store, clk)
	rt.Scan()
	subFor(t, rt, "Level", 95, time.Minute, false)
	d.mu.Lock()
	d.onRead = func() { // the read hangs past the deadline and the lease
		clk.advance(time.Hour)
		coord.lead = false
	}
	d.mu.Unlock()
	rt.Scan()
	if n := rt.Tags().SubstituteCount(); n != 1 {
		t.Fatal("a replica that lost the lease during the read expired a substitute")
	}
}

// A retained entry whose input has no value until its first read waits
// for it. Meanwhile a save keeps it, and a stop ends it: it never applies
// after someone ended it.
func TestPendingRetainedSubstitute(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	a := subFor(t, rt, "Level", 95, time.Hour, false)
	b := subFor(t, rt, "Flow", 7, time.Hour, false)
	save(t, rt)

	rt2, _ := newRetainSubRig(t, nil, store, clk)
	rt2.LoadRetained()
	rt2.leadMu.Lock()
	rt2.leading = true // as after the first takeover, before the read lands
	rt2.leadMu.Unlock()
	save(t, rt2)
	if st := store.state.Substitutes; st == nil || len(st.Entries) != 2 {
		t.Fatalf("saved %+v while the entries waited for their inputs; want both kept", st)
	}
	if _, ok := rt2.Restore(b.ID, "op"); !ok {
		t.Fatal("Restore of a waiting entry reported none")
	}
	rt2.Scan()
	if l := rt2.Substitutes(); len(l) != 1 || l[0].ID != a.ID || rt2.Tags().Real("Flow") != 3 {
		t.Fatalf("%+v, Flow %v: want only Level's entry; the stopped one never applies", l, rt2.Tags().Real("Flow"))
	}
}
