package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// fakeKeeper is an IOKeeper whose restore a test releases.
type fakeKeeper struct {
	mu       sync.Mutex
	keep     *retain.IOState
	current  bool
	release  chan struct{}
	restores atomic.Int32
	got      *retain.IOState
	// deaf ignores ctx: a restore that does not end when told to.
	deaf bool
}

func (k *fakeKeeper) RetainedIO() *retain.IOState {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.keep
}

func (k *fakeKeeper) IOCurrent(*retain.IOState) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.current
}

func (k *fakeKeeper) RestoreIO(ctx context.Context, st *retain.IOState) error {
	k.restores.Add(1)
	k.mu.Lock()
	k.got = st
	k.mu.Unlock()
	if k.deaf {
		<-k.release
		return nil
	}
	select {
	case <-k.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// keepTerm is a Termed Coordinator a test flips.
type keepTerm struct {
	lead atomic.Bool
	term atomic.Uint64
}

func (f *keepTerm) IsLeader() bool { return f.lead.Load() }
func (f *keepTerm) Term() uint64   { return f.term.Load() }

func keptIO() *retain.IOState {
	return &retain.IOState{Units: map[string]retain.IOUnit{"modbus/DEV_A": {Base: "b", Hash: "h"}}}
}

func scanned(rt *Runtime) int64 { v, _ := rt.Tags().ReadGlobal("Count"); return v.I }

// A leadership edge re-applies the kept I/O configuration before the first
// scan runs any logic: the gate holds, the loop keeps ticking, and the
// restore is started once (#706).
func TestTakeoverHoldsForTheIORestore(t *testing.T) {
	store := &fakeStore{state: retain.State{IO: keptIO()}}
	rt := newRetained(t, store, nil)
	k := &fakeKeeper{release: make(chan struct{})}
	rt.SetIO(k)
	for i := 0; i < 100 && k.restores.Load() == 0; i++ {
		rt.Scan() // held: the restore runs off the scan
		time.Sleep(time.Millisecond)
	}
	rt.Scan()
	rt.Scan()
	if n := scanned(rt); n != 0 {
		t.Fatalf("%d scans ran logic while the I/O restore was under way, want none", n)
	}
	if n := k.restores.Load(); n != 1 {
		t.Fatalf("restore started %d times, want once", n)
	}
	k.mu.Lock()
	got := k.got
	k.mu.Unlock()
	if got == nil || got.Units["modbus/DEV_A"].Hash != "h" {
		t.Fatalf("restored %+v, want the store's IO", got)
	}
	close(k.release)
	deadline := time.Now().Add(2 * time.Second)
	for scanned(rt) == 0 && time.Now().Before(deadline) {
		rt.Scan()
		time.Sleep(time.Millisecond)
	}
	if scanned(rt) == 0 {
		t.Fatal("no scan ran once the restore was done")
	}
}

// What runs is already what the store keeps (a standby that followed it):
// no restore, no hold.
func TestTakeoverNoIOHoldWhenCurrent(t *testing.T) {
	store := &fakeStore{state: retain.State{IO: keptIO()}}
	rt := newRetained(t, store, nil)
	k := &fakeKeeper{current: true}
	rt.SetIO(k)
	rt.Scan()
	if n := scanned(rt); n != 1 {
		t.Fatalf("the first scan ran logic %d times, want once: nothing to re-apply", n)
	}
	if n := k.restores.Load(); n != 0 {
		t.Fatalf("restore started %d times, want none", n)
	}
}

// A restore that does not finish holds the gate no longer than
// takeoverIOWait; the scans go on after it.
func TestTakeoverIOHoldIsBounded(t *testing.T) {
	saved := takeoverIOWait
	takeoverIOWait = 50 * time.Millisecond
	t.Cleanup(func() { takeoverIOWait = saved })
	store := &fakeStore{state: retain.State{IO: keptIO()}}
	rt := newRetained(t, store, nil)
	k := &fakeKeeper{release: make(chan struct{}), deaf: true}
	t.Cleanup(func() { close(k.release) })
	rt.SetIO(k)
	rt.Scan()
	if scanned(rt) != 0 {
		t.Fatal("scanned before the wait")
	}
	time.Sleep(60 * time.Millisecond)
	rt.Scan()
	if n := scanned(rt); n != 1 {
		t.Fatalf("%d scans after the wait, want the scan to go on", n)
	}
}

// A new term is a takeover, even with no step-down seen between: the kept
// I/O configuration is asked for again. The same leadership resumed (the
// gate closed and opened in one term) is not: nothing is re-applied.
func TestResumeDoesNotRestoreIO(t *testing.T) {
	store := &fakeStore{state: retain.State{IO: keptIO()}}
	coord := &keepTerm{}
	coord.lead.Store(true)
	coord.term.Store(1)
	rt := newRetained(t, store, coord)
	k := &fakeKeeper{release: make(chan struct{})}
	close(k.release)
	rt.SetIO(k)
	for i := 0; i < 50 && scanned(rt) == 0; i++ {
		rt.Scan()
		time.Sleep(time.Millisecond)
	}
	if k.restores.Load() != 1 || scanned(rt) == 0 {
		t.Fatalf("restores %d, scans %d after the first takeover", k.restores.Load(), scanned(rt))
	}
	coord.term.Store(2) // lost and regained between two scans
	for i := 0; i < 50 && k.restores.Load() < 2; i++ {
		rt.Scan()
		time.Sleep(time.Millisecond)
	}
	if n := k.restores.Load(); n != 2 {
		t.Fatalf("restore started %d times after a new term, want twice", n)
	}
	for i := 0; i < 5; i++ {
		rt.Scan() // the new term's takeover runs
	}
	coord.lead.Store(false)
	rt.Scan()
	coord.lead.Store(true)
	for i := 0; i < 20; i++ {
		rt.Scan() // a restore asked for here would have started by now
		time.Sleep(time.Millisecond)
	}
	if n := k.restores.Load(); n != 2 {
		t.Fatalf("restore started %d times after a resume, want still twice", n)
	}
}

// SaveRetained saves now and says why it did not.
func TestSaveRetained(t *testing.T) {
	ctx := context.Background()
	if err := newRetained(t, nil, nil).SaveRetained(ctx); !errors.Is(err, ErrNoRetainStore) {
		t.Fatalf("no store: err %v, want ErrNoRetainStore", err)
	}
	run := func(t *testing.T, store *fakeStore, coord Coordinator) (*Runtime, *fakeKeeper) {
		rt := newRetained(t, store, coord)
		k := &fakeKeeper{current: true, keep: keptIO()}
		rt.SetIO(k)
		c, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { rt.Run(c); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		return rt, k
	}
	t.Run("a standby", func(t *testing.T) {
		rt, _ := run(t, &fakeStore{}, &flag{})
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := rt.SaveRetained(c); !errors.Is(err, ErrNotLeading) {
			t.Fatalf("err %v, want ErrNotLeading", err)
		}
	})
	t.Run("the leader", func(t *testing.T) {
		store := &fakeStore{}
		rt, _ := run(t, store, nil)
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		var err error
		for err = rt.SaveRetained(c); errors.Is(err, ErrNotLeading); err = rt.SaveRetained(c) {
			time.Sleep(5 * time.Millisecond) // until the first scan has taken over
		}
		if err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		got := store.state.IO
		store.mu.Unlock()
		if got == nil || got.Units["modbus/DEV_A"].Hash != "h" {
			t.Fatalf("store io %+v, want the keeper's, saved on asking", got)
		}
		store.mu.Lock()
		store.fail = true
		store.mu.Unlock()
		if err := rt.SaveRetained(c); err == nil {
			// Nothing changed since the last save: nothing to write, so
			// the failing store is not asked. Change something.
			rt.Tags().Set("SP", 71.0)
			if err := rt.SaveRetained(c); err == nil || err.Error() != "store down" {
				t.Fatalf("err %v, want the store's", err)
			}
		}
	})
}

// loadCounter counts a store's loads.
type loadCounter struct {
	*fakeStore
	loads atomic.Int32
}

func (c *loadCounter) Load() (retain.State, error) {
	c.loads.Add(1)
	return c.fakeStore.Load()
}

// A takeover with a keeper reads the store once: what the edge read for
// the I/O check is what takeover loads (#710's takeover bound).
func TestTakeoverReadsTheStoreOnce(t *testing.T) {
	store := &loadCounter{fakeStore: &fakeStore{state: retain.State{IO: keptIO(), Tags: map[string]any{"SP": 72.5}}}}
	coord := &keepTerm{}
	rt := newRetained(t, store, coord)
	rt.SetIO(&fakeKeeper{current: true})
	rt.Scan() // standby
	coord.term.Store(1)
	coord.lead.Store(true)
	rt.Scan()
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("the takeover read the store %d times, want once", n)
	}
	if got := rt.Tags().Real("SP"); got != 72.5 {
		t.Fatalf("SP = %v, want the store's 72.5: takeover loaded what the edge read", got)
	}
}
