package runtime_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/retain"
	"github.com/joyautomation/nautilus/runtime"
)

// hangDriver is a driver whose next read can be made to block until the
// test lets it go: a scan stuck in a driver call, the hung-leader case
// (#621). It counts the output pushes it is handed.
type hangDriver struct {
	mu     sync.Mutex
	hang   chan struct{} // non-nil: the next read blocks until it is closed
	hung   chan struct{} // closed once that read is blocked
	writes atomic.Int64
}

// arm makes the next read block. hung is closed once it is; release lets
// it go (and may be called more than once).
func (d *hangDriver) arm() (hung <-chan struct{}, release func()) {
	h, s := make(chan struct{}), make(chan struct{})
	d.mu.Lock()
	d.hang, d.hung = h, s
	d.mu.Unlock()
	var once sync.Once
	return s, func() { once.Do(func() { close(h) }) }
}

func (d *hangDriver) ReadInputs() (nio.Values, error) {
	d.mu.Lock()
	h, s := d.hang, d.hung
	d.hang, d.hung = nil, nil
	d.mu.Unlock()
	if h != nil {
		close(s)
		<-h
	}
	return nio.Values{}, nil
}

func (d *hangDriver) WriteOutputs(nio.Values) error {
	d.writes.Add(1)
	return nil
}

// leadFlag is a Coordinator a test flips while Run's goroutines read it.
type leadFlag struct{ lead atomic.Bool }

func (f *leadFlag) IsLeader() bool { return f.lead.Load() }

const countProgram = `PROGRAM Counter
VAR_EXTERNAL Out : DINT; END_VAR
Out := Out + 1;
END_PROGRAM`

func newHangRuntime(t *testing.T, drv nio.Driver, coord runtime.Coordinator, scan time.Duration, tasks ...runtime.Task) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:     countProgram,
		Driver:      drv,
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Out": int64(0)},
		Scan:        scan,
		Tasks:       tasks,
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// startRun runs rt until the test ends; the cleanup releases first, so a
// read still blocked cannot keep Run from returning.
func startRun(t *testing.T, rt *runtime.Runtime, release ...func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		for _, r := range release {
			r()
		}
		cancel()
		<-done
	})
}

func waitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

// waitScans waits until the main task has completed at least n scans.
func waitScans(t *testing.T, rt *runtime.Runtime, n uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for rt.Stats().Count < n {
		if time.Now().After(deadline) {
			t.Fatalf("main task completed %d scans, want %d", rt.Stats().Count, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A scan stuck in a driver read is not live once it has been in progress
// for longer than its bound, measured in the caller's time, and the error
// names the task. Once scans complete again, it is live.
func TestLivenessAScanHungInADriverReadIsStuck(t *testing.T) {
	const max = 2 * time.Second
	drv := &hangDriver{}
	hung, release := drv.arm()
	rt := newHangRuntime(t, drv, nil, 10*time.Millisecond)
	startRun(t, rt, release)
	waitClosed(t, hung, "the read to block")

	bound := runtime.StallBound(10*time.Millisecond, max)
	t0 := time.Now()
	if err := rt.Liveness(t0, max); err != nil {
		t.Fatalf("first look: %v", err)
	}
	if err := rt.Liveness(t0.Add(bound), max); err != nil {
		t.Fatalf("a scan in progress for exactly its bound is live: %v", err)
	}
	err := rt.Liveness(t0.Add(bound+time.Millisecond), max)
	if err == nil {
		t.Fatal("a scan blocked in ReadInputs past its bound reads as live")
	}
	if !strings.Contains(err.Error(), "task main") {
		t.Fatalf("the error does not name the stuck task: %v", err)
	}

	release()
	waitScans(t, rt, 2) // the hung scan finished and another began
	if err := rt.Liveness(t0.Add(time.Hour), max); err != nil {
		t.Fatalf("scans complete again but Liveness says %v", err)
	}
}

// A task whose period is longer than the lease is live between its scans,
// however long since the last one: it is sleeping to its next slot. Its
// bound is capped at max, so a hung scan of it is still caught well inside
// the lease.
func TestLivenessASlowTaskIsLiveBetweenScans(t *testing.T) {
	const max = 2 * time.Second
	if b := runtime.StallBound(10*time.Second, max); b != max {
		t.Fatalf("a 10 s task's bound is %s, want the cap %s", b, max)
	}
	if b := runtime.StallBound(time.Millisecond, max); b != max/2 {
		t.Fatalf("a 1 ms task's bound is %s, want the floor %s", b, max/2)
	}
	if b := runtime.StallBound(750*time.Millisecond, max); b != 1500*time.Millisecond {
		t.Fatalf("a 750 ms task's bound is %s, want twice its period", b)
	}

	rt := newHangRuntime(t, nil, nil, 10*time.Millisecond, runtime.Task{
		Name:    "reports",
		Program: `PROGRAM Reports VAR n : DINT; END_VAR n := n + 1; END_PROGRAM`,
		Scan:    10 * time.Second,
	})
	startRun(t, rt)
	waitScans(t, rt, 1)
	t0 := time.Now()
	for i := range 10 {
		// The main task completes a scan between two looks, so only the
		// slow task's sleep is on trial.
		waitScans(t, rt, rt.Stats().Count+2)
		if err := rt.Liveness(t0.Add(time.Duration(i)*7*time.Second), max); err != nil {
			t.Fatalf("at +%ds: %v", i*7, err)
		}
	}
}

// Before Run, Liveness answers live for a start-up grace from its first
// call (a leader decides before its first scan), then not live: a runtime
// whose loops never start cannot hold a lease. After Run returns it is not
// live either.
func TestLivenessStartupGraceAndStop(t *testing.T) {
	const max = 500 * time.Millisecond
	rt := newHangRuntime(t, nil, nil, 10*time.Millisecond)
	t0 := time.Now()
	if err := rt.Liveness(t0, max); err != nil {
		t.Fatalf("before Run, at the first look: %v", err)
	}
	if err := rt.Liveness(t0.Add(4*max), max); err != nil {
		t.Fatalf("before Run, inside the grace: %v", err)
	}
	if err := rt.Liveness(t0.Add(4*max+time.Millisecond), max); err == nil {
		t.Fatal("a runtime whose loops never started reads as live after the grace")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	waitScans(t, rt, 1)
	if err := rt.Liveness(time.Now(), max); err != nil {
		t.Fatalf("running: %v", err)
	}
	cancel()
	<-done
	if err := rt.Liveness(time.Now(), max); err == nil {
		t.Fatal("a runtime whose Run has returned reads as live")
	} else if !errors.Is(err, runtime.ErrStopped) || !errors.Is(err, context.Canceled) {
		t.Fatalf("an orderly stop reads as %v, want ErrStopped (a context.Canceled)", err)
	}
}

// A scan that hung in its read and resumes after this replica lost
// leadership does not push its outputs: a standby has taken over and is
// writing (#621). The next scan as leader pushes the whole set.
func TestAScanThatLostLeadershipWhileHungWritesNothing(t *testing.T) {
	drv := &hangDriver{}
	coord := &leadFlag{}
	coord.lead.Store(true)
	rt := newHangRuntime(t, drv, coord, time.Hour)

	rt.Scan()
	if n := drv.writes.Load(); n != 1 {
		t.Fatalf("first scan as leader pushed %d times, want 1", n)
	}

	hung, release := drv.arm()
	done := make(chan struct{})
	go func() { rt.Scan(); close(done) }()
	waitClosed(t, hung, "the read to block")
	coord.lead.Store(false) // the lease lapsed meanwhile
	release()
	waitClosed(t, done, "the hung scan to finish")
	if n := drv.writes.Load(); n != 1 {
		t.Fatalf("the scan that resumed after losing leadership pushed its outputs (%d pushes, want 1)", n)
	}

	coord.lead.Store(true)
	rt.Scan()
	if n := drv.writes.Load(); n != 2 {
		t.Fatalf("the next scan as leader pushed %d times in all, want 2", n)
	}
}

// The leadership check before the output push allocates nothing: a scan
// with a coordinator allocates no more than one without.
func TestScanWithACoordinatorAllocatesNothingMore(t *testing.T) {
	allocs := func(coord runtime.Coordinator) float64 {
		drv := nio.NewMemory()
		rt := newHangRuntime(t, drv, coord, time.Hour)
		rt.Scan()
		rt.Scan()
		return testing.AllocsPerRun(200, rt.Scan)
	}
	lead := &leadFlag{}
	lead.lead.Store(true)
	without, with := allocs(nil), allocs(lead)
	if with > without {
		t.Fatalf("a scan with a coordinator allocates %v, without %v", with, without)
	}
}

// termFlag is a Termed Coordinator a test flips: leadership and its term.
type termFlag struct {
	lead atomic.Bool
	term atomic.Uint64
}

func (f *termFlag) IsLeader() bool { return f.lead.Load() }
func (f *termFlag) Term() uint64   { return f.term.Load() }

const resumeProgram = `PROGRAM Counter
VAR n : DINT; END_VAR
VAR_EXTERNAL Out : DINT; Count : DINT; END_VAR
n := n + 1;
Count := n;
Out := 5;
END_PROGRAM`

// A gate that closes and opens again in the same term (scans stalled, then
// completed, and nobody else led) resumes: no takeover, so the program's
// frames carry on, and the drivers get every output once, changed or not.
// A new term is a takeover, also when the runtime never saw the gate close
// (lost and regained between two scans).
func TestAResumedTermDoesNotTakeOver(t *testing.T) {
	drv := &hangDriver{}
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	rt, err := runtime.New(runtime.Options{
		Program:     resumeProgram,
		Driver:      drv,
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Out": int64(0), "Count": int64(0)},
		Scan:        time.Hour,
		Coordinator: coord,
	})
	if err != nil {
		t.Fatal(err)
	}
	count := func() int64 {
		t.Helper()
		v, err := rt.Tags().ReadGlobal("Count")
		if err != nil {
			t.Fatal(err)
		}
		return v.I
	}
	for range 3 {
		rt.Scan()
	}
	if c, w := count(), drv.writes.Load(); c != 3 || w != 1 {
		t.Fatalf("setup: Count=%d after 3 scans, %d pushes of the unchanged output", c, w)
	}

	coord.lead.Store(false)
	rt.Scan()
	coord.lead.Store(true) // same term: nobody else led
	rt.Scan()
	if c := count(); c != 4 {
		t.Fatalf("Count = %d after the gate reopened in the same term, want 4: the frames were reset (a takeover)", c)
	}
	if w := drv.writes.Load(); w != 2 {
		t.Fatalf("%d pushes in all, want 2: the resumed scan sends every output once", w)
	}

	coord.term.Store(2) // lost and regained between two scans
	rt.Scan()
	if c := count(); c != 1 {
		t.Fatalf("Count = %d after a new term, want 1: a new term is a takeover", c)
	}
}

// countingStore is a retain store that counts its loads (each takeover
// loads it once) and saves nothing worth keeping.
type countingStore struct{ loads atomic.Int64 }

func (s *countingStore) Load() (retain.State, error) { s.loads.Add(1); return retain.State{}, nil }
func (s *countingStore) Save(retain.State) error     { return nil }
func (s *countingStore) Kind() string                { return "counting" }

// The dangerous mirror of TestAResumedTermDoesNotTakeOver: the gate closed
// and opens again in a NEW term (another replica led meanwhile). That is a
// takeover, never a resume: the retained state is reloaded and the frames
// reset, or this replica would carry on from state another leader has
// since moved past.
func TestANewTermAfterTheGateClosedIsATakeover(t *testing.T) {
	drv := &hangDriver{}
	store := &countingStore{}
	coord := &termFlag{}
	coord.lead.Store(true)
	coord.term.Store(1)
	rt, err := runtime.New(runtime.Options{
		Program:     resumeProgram,
		Driver:      drv,
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Out": int64(0), "Count": int64(0)},
		Scan:        time.Hour,
		Coordinator: coord,
		Retain:      store,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		rt.Scan()
	}
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("setup: %d retained loads after the first takeover, want 1", n)
	}

	coord.lead.Store(false)
	rt.Scan()
	coord.term.Store(2) // another replica led while this one's gate was closed
	coord.lead.Store(true)
	rt.Scan()
	v, err := rt.Tags().ReadGlobal("Count")
	if err != nil {
		t.Fatal(err)
	}
	if v.I != 1 {
		t.Fatalf("Count = %d after the gate opened in a new term, want 1: the frames were not reset (resumed instead of taking over)", v.I)
	}
	if n := store.loads.Load(); n != 2 {
		t.Fatalf("%d retained loads, want 2: the new term did not reload what the other leader saved", n)
	}
}
