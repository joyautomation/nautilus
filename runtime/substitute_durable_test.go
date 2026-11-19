package runtime

import (
	"context"
	"errors"
	"sync"
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

// A start the store does not take is refused and never applied: the
// entry it would have replaced runs on untouched (its ID, value and time
// left), and no event says otherwise.
func TestAStartThatCannotBeSavedIsNotApplied(t *testing.T) {
	store, clk := &fakeStore{}, noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	ctx := withSaver(t, rt)
	a, err := rt.SubstituteSaved(ctx, "Level", SubstituteRequest{Value: 95.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(rt)
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
		t.Fatalf("%+v; want entry %d untouched (95, 50 min left)", l, a.ID)
	}
	if rt.Tags().Real("Level") != 95 || rt.Tags().Real("Flow") != 3 {
		t.Fatalf("Level %v Flow %v; want 95 and the live 3", rt.Tags().Real("Level"), rt.Tags().Real("Flow"))
	}
	for _, e := range events() {
		if e.Kind != SubstituteEventStart || e.Entry.ID != a.ID { // a's own start, from the backlog
			t.Fatalf("event %+v; a refused start changes nothing", e)
		}
	}
}

// gateStore holds each armed Save until the test answers it, so a test
// can interleave requests exactly. Unarmed saves go straight through.
type gateStore struct {
	fakeStore
	gmu     sync.Mutex
	armed   []chan error
	entered chan retain.State
	closed  chan struct{}
}

func newGateStore() *gateStore {
	return &gateStore{entered: make(chan retain.State, 8), closed: make(chan struct{})}
}

// arm makes the next save wait for the answer sent on the returned channel
// (nil: the save succeeds).
func (g *gateStore) arm() chan error {
	ch := make(chan error)
	g.gmu.Lock()
	g.armed = append(g.armed, ch)
	g.gmu.Unlock()
	return ch
}

func (g *gateStore) Save(st retain.State) error {
	g.gmu.Lock()
	if len(g.armed) == 0 {
		g.gmu.Unlock()
		return g.fakeStore.Save(st)
	}
	ch := g.armed[0]
	g.armed = g.armed[1:]
	g.gmu.Unlock()
	g.entered <- st
	select {
	case err := <-ch:
		if err != nil {
			return err
		}
	case <-g.closed: // the test ended (failed) without answering
		return errors.New("test over")
	}
	return g.fakeStore.Save(st)
}

func (g *gateStore) entries() []retain.SubstituteRetain {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state.Substitutes == nil {
		return nil
	}
	return g.state.Substitutes.Entries
}

// handSaver answers SaveRetained requests one at a time, as the saver
// does, but with no tick: only requests save.
func handSaver(t *testing.T, rt *Runtime) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case req := <-rt.saveRequests():
				_, err := rt.saveRetainedErr(nil)
				req <- err
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

// recv takes one value from ch, failing the test after 5 s (a mutation
// that loses a save or an answer must fail, not hang).
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// send answers an armed save, failing after 5 s.
func send(t *testing.T, ch chan error, err error) {
	t.Helper()
	select {
	case ch <- err:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out answering a save nobody made")
	}
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *Runtime) pendingStarts() int {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	return len(r.subStarts)
}

type startResult struct {
	s   Substitution
	err error
}

func startAsync(rt *Runtime, addr string, v float64) chan startResult {
	ch := make(chan startResult, 1)
	go func() {
		s, err := rt.SubstituteSaved(context.Background(), addr, SubstituteRequest{Value: v, For: time.Hour, By: "op"})
		ch <- startResult{s, err}
	}()
	return ch
}

// gatedRig is a single controller on a gate store with a hand-driven
// saver, with entry 1 (Level 50) active and saved.
func gatedRig(t *testing.T) (*Runtime, *gateStore, Substitution) {
	t.Helper()
	store, clk := newGateStore(), noon()
	rt, _ := newRetainSubRig(t, nil, store, clk)
	rt.LoadRetained()
	rt.Scan()
	handSaver(t, rt)
	t.Cleanup(func() { close(store.closed) }) // runs before handSaver's cleanup
	e1, err := rt.SubstituteSaved(context.Background(), "Level", SubstituteRequest{Value: 50.0, For: time.Hour, By: "op"})
	if err != nil {
		t.Fatal(err)
	}
	return rt, store, e1
}

// Save, then apply: while a start's save is in flight nothing runs on it,
// and a start whose save fails never ran at all.
func TestAStartRunsOnlyAfterItsSave(t *testing.T) {
	rt, store, e1 := gatedRig(t)
	gA := store.arm()
	resA := startAsync(rt, "Level", 95)
	snap := recv(t, store.entered, "a save")
	if es := snap.Substitutes.Entries; len(es) != 1 || es[0].Value != 95.0 || es[0].ID == e1.ID {
		t.Fatalf("the save wrote %+v; want the pending start in place of entry %d", es, e1.ID)
	}
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 50 {
		t.Fatalf("Mirror = %v during the save; want 50: a start must not run before its save", got)
	}
	send(t, gA, errors.New("store down"))
	if r := recv(t, resA, "A's answer"); !errors.Is(r.err, ErrSubstituteNotSaved) {
		t.Fatalf("A: %v; want not saved", r.err)
	}
	rt.Scan()
	if got := rt.Tags().Real("Mirror"); got != 50 {
		t.Fatalf("Mirror = %v after the refusal; want 50", got)
	}
	waitFor(t, "the store back to the table", func() bool {
		es := store.entries()
		return len(es) == 1 && es[0].ID == e1.ID
	})
}

// Two starts on one address, interleaved: A (95) is saving when B (96) is
// reserved. The newer one wins whatever A's save does: A is refused, B
// replaces entry 1, and nothing reinstates an older entry over B.
func TestConcurrentStartsOnOneAddress(t *testing.T) {
	for _, aFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "A saved", true: "A not saved"}[aFails], func(t *testing.T) {
			rt, store, e1 := gatedRig(t)
			events := collect(rt)
			gA := store.arm()
			resA := startAsync(rt, "Level", 95)
			recv(t, store.entered, "a save")
			gB := store.arm()
			resB := startAsync(rt, "Level", 96)
			waitFor(t, "B reserved", func() bool { return rt.pendingStarts() == 2 })
			if aFails {
				send(t, gA, errors.New("store down"))
			} else {
				send(t, gA, nil)
			}
			ra := recv(t, resA, "A's answer")
			if aFails && !errors.Is(ra.err, ErrSubstituteNotSaved) || !aFails && !errors.Is(ra.err, ErrSubstituteSuperseded) {
				t.Fatalf("A: %v", ra.err)
			}
			snapB := recv(t, store.entered, "B's save")
			if es := snapB.Substitutes.Entries; len(es) != 1 || es[0].Value != 96.0 {
				t.Fatalf("B's save wrote %+v; want B in place of entry 1", es)
			}
			send(t, gB, nil)
			rb := recv(t, resB, "B's answer")
			if rb.err != nil {
				t.Fatalf("B: %v", rb.err)
			}
			l := rt.Substitutes()
			if len(l) != 1 || l[0].ID != rb.s.ID || l[0].Value != 96.0 || rt.Tags().Real("Level") != 96 {
				t.Fatalf("%+v, Level %v; want only B (96)", l, rt.Tags().Real("Level"))
			}
			waitFor(t, "the store to hold B alone", func() bool {
				es := store.entries()
				return len(es) == 1 && es[0].ID == rb.s.ID
			})
			for _, e := range events() {
				if e.Kind == SubstituteEventStart && e.Entry.ID != rb.s.ID && e.Entry.ID != e1.ID { // entry 1's own, from the backlog
					t.Fatalf("event %+v: only B started", e)
				}
				if e.Kind == SubstituteEventStop && e.Entry.ID != e1.ID {
					t.Fatalf("event %+v: only entry 1 ended (replaced by B)", e)
				}
			}
		})
	}
}

// A stop of entry 1 while a start replacing it is saving: the stop is the
// newer change and wins; the start is refused, and the address goes back
// to its live value.
func TestAStopDuringAPendingStart(t *testing.T) {
	rt, store, e1 := gatedRig(t)
	gA := store.arm()
	resA := startAsync(rt, "Level", 95)
	recv(t, store.entered, "a save")
	stopped := make(chan bool, 1)
	go func() {
		_, ok, kept, _ := rt.RestoreSaved(context.Background(), e1.ID, "op")
		stopped <- ok && kept
	}()
	waitFor(t, "the stop to take effect", func() bool { return len(rt.Substitutes()) == 0 })
	send(t, gA, nil)
	if r := recv(t, resA, "A's answer"); !errors.Is(r.err, ErrSubstituteSuperseded) {
		t.Fatalf("A: %v; want it superseded by the stop", r.err)
	}
	if !recv(t, stopped, "the stop's answer") {
		t.Fatal("the stop was not kept")
	}
	rt.Scan()
	if l := rt.Substitutes(); len(l) != 0 || rt.Tags().Real("Mirror") != 10 {
		t.Fatalf("%+v, Mirror %v; want nothing active and the live value", l, rt.Tags().Real("Mirror"))
	}
	waitFor(t, "the store to hold nothing", func() bool { return len(store.entries()) == 0 })
}

// A crash between a start's save and its apply: the store holds it, so
// the restart runs it, though its request was never answered (outcome
// unknown, as documented).
func TestACrashBetweenSaveAndApply(t *testing.T) {
	rt, store, _ := gatedRig(t)
	gA := store.arm()
	resA := startAsync(rt, "Level", 95)
	snap := recv(t, store.entered, "a save") // the store is about to hold this; the process dies here
	clk := noon()
	rt2, _ := newRetainSubRig(t, nil, &fakeStore{state: snap}, clk)
	rt2.LoadRetained()
	rt2.Scan()
	if l := rt2.Substitutes(); len(l) != 1 || l[0].Value != 95.0 || rt2.Tags().Real("Mirror") != 95 {
		t.Fatalf("%+v; want the saved start running after the restart", l)
	}
	send(t, gA, nil)
	recv(t, resA, "A's answer")
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
