//go:build !js

package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// wideWait widens the same-tick wait for a test, so a step's timing on a
// loaded runner never decides it (Fable M2 on d1e).
func wideWait(t *testing.T) {
	old := sameTickWait
	sameTickWait = 300 * time.Millisecond
	t.Cleanup(func() { sameTickWait = old })
}

// A takeover edge waits a moment, in the same tick, for the steps it
// starts (#710 d1e): the image flush and the operator-intent catch-up each
// used to cost a whole tick of the fastest task, measured as most of a
// planned switchover's standby side.

// A flush that finishes within the wait is taken on the edge's first
// tick: one scan call takes over. flush_next_tick turns it red.
func TestATakeoverTakesAQuickFlushInItsFirstTick(t *testing.T) {
	wideWait(t)
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	take := r.flush
	r.flush = func() (*HotImage, error) {
		time.Sleep(2 * time.Millisecond) // a flush's fsync
		return take()
	}
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	r.stby.Scan()
	if term, lead := r.stby.LeadingTerm(); !lead || term != 4 {
		t.Fatal("a flush that finished within the same-tick wait was left to the next tick")
	}
	if !r.stby.LastTakeover().Hot {
		t.Fatalf("takeover %+v, want hot", r.stby.LastTakeover())
	}
}

// A slow flush is still left to later ticks, as before: the edge's tick
// waits only its bound, and the takeover goes on once the flush is done.
func TestASlowFlushStillFallsBackToTheNextTicks(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	take := r.flush
	release := make(chan struct{})
	r.flush = func() (*HotImage, error) {
		<-release
		return take()
	}
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	t0 := time.Now()
	r.stby.Scan()
	if took := time.Since(t0); took > sameTickWait+500*time.Millisecond {
		t.Fatalf("the edge's tick waited %s for a slow flush, want about %s", took, sameTickWait)
	}
	if _, lead := r.stby.LeadingTerm(); lead {
		t.Fatal("took over before the flush finished")
	}
	close(release)
	r.takeOver(t)
	if !r.stby.LastTakeover().Hot {
		t.Fatalf("takeover %+v, want hot", r.stby.LastTakeover())
	}
}

// The operator-intent catch-up likewise: one that finishes within the
// wait is taken on the edge's first tick. catchup_next_tick turns it red.
func TestATakeoverTakesAQuickCatchUpInItsFirstTick(t *testing.T) {
	wideWait(t)
	clk := noon()
	coord := lead(false)
	rt, _ := newRetainSubRig(t, coord, nil, clk)
	rt.Scan()
	release := make(chan struct{})
	time.AfterFunc(2*time.Millisecond, func() { close(release) })
	rt.SetIntentLog(&fakeLog{block: release}, 5*time.Second)
	coord.set(true)
	rt.Scan()
	if m := rt.Tags().Real("Mirror"); m == 0 {
		t.Fatal("a catch-up that finished within the same-tick wait was left to the next tick")
	}
}

// While one loop's tick waits on the takeover edge (holding leadMu), the
// other loops' ticks answer "do not scan" at once rather than queueing on
// the lock, so a fast task misses no slot to the wait (naut-lead, d1e).
// edge_wait_blocks_others turns it red.
func TestOtherTasksDoNotWaitOnATakeoverEdge(t *testing.T) {
	wideWait(t) // wide, so a queued tick is plain to see
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	take := r.flush
	release := make(chan struct{})
	r.flush = func() (*HotImage, error) {
		<-release
		return take()
	}
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	done := make(chan struct{})
	go func() { r.stby.Scan(); close(done) }() // the main task's tick: the edge, waiting
	for deadline := time.Now().Add(5 * time.Second); !r.stby.edgeWait.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the takeover edge never waited for its flush")
		}
	}
	t0 := time.Now()
	if err := r.stby.ScanTask("aux"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(t0); took > 100*time.Millisecond {
		t.Fatalf("the aux task's tick took %s: it queued behind the takeover edge's wait", took)
	}
	close(release)
	<-done
	r.takeOver(t)
}

// The real pair's flush asks LeadingTerm and carries the image into the
// retain store (CarryRetained) while the edge's tick waits for it under
// leadMu: neither may wait on that lock, or the flush could never finish
// inside the wait (Fable M1 on d1e). leading_term_locks turns it red.
func TestAFlushThatCarriesAndAsksTheLeadershipIsNotHeldByTheWait(t *testing.T) {
	wideWait(t)
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}, Stream: &retain.StreamPos{Term: 5, Seq: 3}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	took := make(chan time.Duration, 1)
	rt.SetTakeoverFlush(func() (*HotImage, error) {
		t0 := time.Now()
		rt.LeadingTerm()
		if _, err := rt.CarryRetained(context.Background(), []byte(`{"tags":{"SP":9}}`), 5, 4); err != nil {
			t.Errorf("the flush's carry: %v", err)
		}
		took <- time.Since(t0)
		return nil, nil
	})
	runWith(t, rt)
	coord.term.Store(6)
	coord.lead.Store(true)
	select {
	case d := <-took:
		if d > 150*time.Millisecond {
			t.Fatalf("the flush took %s: it waited on the lock the edge's tick held while waiting for it", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no takeover flush")
	}
}

// The lease is lost while the edge's tick waits for its flush: the tick
// samples the lease again and does not take over on a sample from before
// the wait (Fable S1 on d1e). wait_keeps_lead_sample turns it red.
func TestALeaseLostDuringTheWaitTakesNothingOver(t *testing.T) {
	wideWait(t)
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	take := r.flush
	r.flush = func() (*HotImage, error) {
		r.coord.lead.Store(false) // the lease went while the flush ran
		return take()
	}
	r.coord.term.Store(4)
	r.coord.lead.Store(true)
	r.stby.Scan()
	if _, lead := r.stby.LeadingTerm(); lead {
		t.Fatal("took over on a lease sampled before the wait, after it was lost")
	}
}

// A switchover of an earlier leadership asked while a new leadership's
// takeover runs (between its clearing the switching state and publishing
// its term) is refused: it reads the leadership under leadMu, not the
// snapshot that still shows the old term, so it cannot stop the new
// leadership's scans (Fable, d1e). beginswitch_reads_snapshot turns it
// red.
func TestASwitchoverOfAnOldTermDuringATakeoverIsRefused(t *testing.T) {
	r := newHotRig(t, redMain, redMain)
	r.stream(t, 1)
	r.takeOver(t) // leads term 4
	got := make(chan error, 1)
	r.store.mu2.Lock()
	r.store.onLoad = func() {
		go func() { got <- r.stby.BeginSwitch(context.Background(), 4) }()
		time.Sleep(30 * time.Millisecond) // the switchover runs while this takeover does
	}
	r.store.mu2.Unlock()
	r.coord.term.Store(6) // a new leadership the gate never closed for
	deadline := time.Now().Add(10 * time.Second)
	for {
		r.sc.step(100 * time.Millisecond)
		r.stby.Scan()
		if term, lead := r.stby.LeadingTerm(); lead && term == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never led term 6")
		}
	}
	r.store.mu2.Lock()
	r.store.onLoad = nil
	r.store.mu2.Unlock()
	if err := <-got; err == nil {
		t.Fatal("a switchover of term 4 began during term 6's takeover")
	}
	if r.stby.Switching() != 0 {
		t.Fatal("term 6's scans are stopped by a switchover of term 4")
	}
}

// A carry during the takeover's catch-up wait is a late flush's: it waits
// on leadMu and finds the leadership taken, as before the snapshot, so it
// writes nothing over the store the takeover loaded (Fable, d1e).
// carry_snapshot_any_wait turns it red.
func TestACarryDuringTheCatchUpWaitIsRefusedOnceLeading(t *testing.T) {
	wideWait(t)
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}, Stream: &retain.StreamPos{Term: 5, Seq: 3}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	rt.SetTakeoverFlush(func() (*HotImage, error) { return nil, nil })
	release := make(chan struct{})
	rt.SetIntentLog(&fakeLog{block: release}, 5*time.Second)
	runWith(t, rt)
	coord.term.Store(6)
	coord.lead.Store(true)
	for deadline := time.Now().Add(5 * time.Second); !rt.edgeWait.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the takeover edge never waited for its catch-up")
		}
	}
	errc := make(chan error, 1)
	go func() {
		_, err := rt.CarryRetained(context.Background(), []byte(`{"tags":{"SP":9}}`), 5, 4)
		errc <- err
	}()
	time.Sleep(30 * time.Millisecond)
	close(release)
	if err := <-errc; !errors.Is(err, ErrLeadingNoCarry) {
		t.Fatalf("a carry during the catch-up wait: %v, want ErrLeadingNoCarry (it landed over the store the takeover loaded)", err)
	}
}

// A retain save that comes up while a takeover edge waits for its flush
// (the saver's tick, or a requested save) does not park the saver on
// leadMu: the flush's carry goes through the same saver goroutine and
// would wait the whole wait out behind it (CI on #1359). The save is
// skipped, as one before the takeover's load must be. save_waits_on_edge
// turns it red.
func TestASaveDuringTheFlushWaitDoesNotHoldTheCarry(t *testing.T) {
	wideWait(t)
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}, Stream: &retain.StreamPos{Term: 5, Seq: 3}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	took := make(chan time.Duration, 1)
	rt.SetTakeoverFlush(func() (*HotImage, error) {
		go func() { _ = rt.SaveRetained(context.Background()) }() // a save the saver takes first
		time.Sleep(20 * time.Millisecond)
		t0 := time.Now()
		if _, err := rt.CarryRetained(context.Background(), []byte(`{"tags":{"SP":9}}`), 5, 4); err != nil {
			t.Errorf("the flush's carry: %v", err)
		}
		took <- time.Since(t0)
		return nil, nil
	})
	runWith(t, rt)
	coord.term.Store(6)
	coord.lead.Store(true)
	select {
	case d := <-took:
		if d > 150*time.Millisecond {
			t.Fatalf("the carry waited %s behind a save parked on the lock the edge held", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no takeover flush")
	}
}

// A save that comes up as the flush starts (here the flush asks for one
// itself and waits for the answer) finds the edge's flags already up: the
// edge raises them before the flush runs, not when its tick starts to wait
// (CI on main after #1359: in that window a save parked on leadMu and held
// the carry for the whole wait). flush_wait_after_start turns it red.
func TestASaveAskedAsTheFlushStartsIsNotParked(t *testing.T) {
	wideWait(t)
	// The edge's tick is descheduled between starting the flush and its
	// wait, as on a loaded runner: the flush's save comes up in between.
	flushStartedHook = func() { time.Sleep(50 * time.Millisecond) }
	t.Cleanup(func() { flushStartedHook = nil })
	store := &fakeStore{state: retain.State{Tags: map[string]any{"SP": 1.0}, Stream: &retain.StreamPos{Term: 5, Seq: 3}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	took := make(chan time.Duration, 1)
	rt.SetTakeoverFlush(func() (*HotImage, error) {
		t0 := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rt.SaveRetained(ctx) // refused (not leading yet), or parked on leadMu
		took <- time.Since(t0)
		return nil, nil
	})
	runWith(t, rt)
	coord.term.Store(6)
	coord.lead.Store(true)
	select {
	case d := <-took:
		if d > 150*time.Millisecond {
			t.Fatalf("a save asked as the flush started took %s: it parked on the lock the edge held", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no takeover flush")
	}
}
