package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// Substitutes made durable before the API answers (#707 PR 3a, review item
// 1): a stopped substitute must never come back at a restart.

// withSaver runs the retain saver for the test, as Run does.
func withSaver(t *testing.T, rt *Runtime) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.retainSaver(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	c, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel2)
	return c
}

func setFail(s *fakeStore, b bool) {
	s.mu.Lock()
	s.fail = b
	s.mu.Unlock()
}

// The resurrection the review found: start, stop, then a hard kill before
// the saver's next tick. The stop was saved before it was answered, so the
// restart does not bring the substitute back.
func TestAStoppedSubstituteNeverComesBack(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	ctx := withSaver(t, rt)
	s, err := rt.SubstituteSaved(ctx, "Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, kept, err := rt.RestoreSaved(ctx, s.ID, "op"); !ok || !kept || err != nil {
		t.Fatalf("stop: ok %v kept %v err %v", ok, kept, err)
	}
	// Hard kill now: no further save.
	rt2, _ := newRetainSubRig(t, nil, store, clk)
	rt2.LoadRetained()
	rt2.Scan()
	if l := rt2.Substitutes(); len(l) != 0 || rt2.Tags().Real("Mirror") != 10 {
		t.Fatalf("%+v, Mirror %v: the stopped substitute came back", l, rt2.Tags().Real("Mirror"))
	}
}

// A start the store does not take is refused and undone; an entry it
// replaced is reinstated as it was (its ID and its remaining time).
func TestAStartThatCannotBeSavedIsUndone(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	ctx := withSaver(t, rt)
	events := collect(rt)
	a, err := rt.SubstituteSaved(ctx, "Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	clk.advance(10 * time.Minute)
	setFail(store, true)
	if _, err := rt.SubstituteSaved(ctx, "Level", SubstituteRequest{Value: 96.0, For: time.Hour, By: "op"}); !errors.Is(err, ErrSubstituteNotSaved) {
		t.Fatalf("a start the store refused: %v; want ErrSubstituteNotSaved", err)
	}
	if _, err := rt.SubstituteSaved(ctx, "Flow", SubstituteRequest{Value: 7.0, For: time.Hour, By: "op"}); !errors.Is(err, ErrSubstituteNotSaved) {
		t.Fatalf("a fresh start the store refused: %v", err)
	}
	l := rt.Substitutes()
	if len(l) != 1 || l[0].ID != a.ID || l[0].Value != 95.0 || l[0].RemainingMs != (50*time.Minute).Milliseconds() {
		t.Fatalf("%+v; want the replaced entry back as it was (ID %d, 95, 50 min left)", l, a.ID)
	}
	if rt.Tags().Real("Level") != 95 || rt.Tags().Real("Flow") != 3 {
		t.Fatalf("Level %v Flow %v; want 95 and the live 3", rt.Tags().Real("Level"), rt.Tags().Real("Flow"))
	}
	evs := events()
	if !hasEvent(evs, SubstituteEventStop, SubstituteReasonNotSaved) || !hasEvent(evs, SubstituteEventStart, SubstituteReasonRestored) {
		t.Fatalf("events %+v; want the undone start and the reinstated entry", evs)
	}
}

// A stop or acknowledgement the store does not take still stands live;
// it reports kept false and raises __Substitute_Fault until a save
// succeeds.
func TestAStopThatCannotBeSavedStandsAndFaults(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	ctx := withSaver(t, rt)
	a, _ := rt.SubstituteSaved(ctx, "Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op", AckOnStart: true})
	b, _ := rt.SubstituteSaved(ctx, "Flow", SubstituteRequest{Value: 7.0, For: time.Hour, By: "op"})
	events := collect(rt)
	setFail(store, true)
	if acked, kept, err := rt.AckSubstitutesSaved(ctx, []uint64{a.ID}, "sup"); len(acked) != 1 || kept || err == nil {
		t.Fatalf("ack: %d acked, kept %v, err %v; want it acked, not kept", len(acked), kept, err)
	}
	if _, ok, kept, err := rt.RestoreSaved(ctx, b.ID, "op"); !ok || kept || err == nil {
		t.Fatalf("stop: ok %v kept %v err %v; want it stopped, not kept", ok, kept, err)
	}
	if rt.Tags().Real("Flow") != 3 || !rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatalf("Flow %v, fault %v; want the stop live and the fault raised", rt.Tags().Real("Flow"), rt.Tags().Bool(SubstituteFaultTag))
	}
	setFail(store, false)
	if err := rt.SaveRetained(ctx); err != nil {
		t.Fatal(err)
	}
	if rt.Tags().Bool(SubstituteFaultTag) {
		t.Fatal("__Substitute_Fault still true after a save succeeded")
	}
	evs := events()
	if !hasEvent(evs, SubstituteEventFault, SubstituteReasonRetainSaveFailed) || !hasEvent(evs, SubstituteEventFaultClear, SubstituteReasonRetainSaveFailed) {
		t.Fatalf("events %+v; want the save fault and its clearing", evs)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if st := store.state.Substitutes; len(st.Entries) != 1 || st.Entries[0].ID != a.ID || !st.Entries[0].Acked {
		t.Fatalf("store %+v; want only Level, acknowledged", st)
	}
}

// With a keeper, a takeover reads the store once (#1010's edge snapshot),
// and the substitutes come from that same read.
func TestSubstituteTakeoverReadsTheStoreOnce(t *testing.T) {
	clk := noon()
	store := &loadCounter{fakeStore: &fakeStore{state: retain.State{IO: keptIO(), Substitutes: &retain.SubstituteState{
		Entries: []retain.SubstituteRetain{{ID: 3, Name: "Level", Value: 95.0, By: "op",
			StartMs: clk.Now().UnixMilli(), UntilMs: clk.Now().Add(time.Hour).UnixMilli(), GrantedMs: time.Hour.Milliseconds()}},
		HighID: 3,
	}}}}
	coord := &keepTerm{}
	rt, d := newRetainSubRig(t, coord, store, clk)
	_ = d
	rt.SetIO(&fakeKeeper{current: true})
	rt.Scan() // standby
	coord.term.Store(1)
	coord.lead.Store(true)
	rt.Scan()
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("the takeover read the store %d times, want once", n)
	}
	if l := rt.Substitutes(); len(l) != 1 || l[0].ID != 3 {
		t.Fatalf("%+v; want the store's entry from that one read", l)
	}
}
