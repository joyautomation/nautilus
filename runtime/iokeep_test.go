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
	deaf       bool
	unreadable error
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

func (k *fakeKeeper) IOUnreadable(err error) {
	k.mu.Lock()
	k.unreadable = err
	k.mu.Unlock()
}

func (k *fakeKeeper) IOSaved(*retain.IOState) {}

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
	saved, grace := takeoverIOWait, ioGrace
	takeoverIOWait, ioGrace = 30*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { takeoverIOWait, ioGrace = saved, grace })
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

// conflictStore refuses its first save as a changed store would.
type conflictStore struct {
	*fakeStore
	conflicts atomic.Int32
}

func (c *conflictStore) Save(s retain.State) error {
	if c.conflicts.Add(1) == 1 {
		return retain.ErrConflict
	}
	return c.fakeStore.Save(s)
}

// slowSave saves after a delay.
type slowSave struct {
	*fakeStore
	delay time.Duration
}

func (s *slowSave) Save(st retain.State) error {
	time.Sleep(s.delay)
	return s.fakeStore.Save(st)
}

// SaveRetained writes over a store another writer changed (once), and its
// answer is the store's once the saver has taken the request, however long
// the store takes: a definite result for an API handler.
func TestSaveRetainedConflictAndDefinite(t *testing.T) {
	run := func(t *testing.T, store retain.Store) *Runtime {
		rt := newRetained(t, store, nil)
		rt.SetIO(&fakeKeeper{current: true, keep: keptIO()})
		c, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { rt.Run(c); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
		deadline := time.Now().Add(2 * time.Second)
		for scanned(rt) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		return rt
	}
	t.Run("a conflict is written over once", func(t *testing.T) {
		store := &conflictStore{fakeStore: &fakeStore{}}
		rt := run(t, store)
		if err := rt.SaveRetained(context.Background()); err != nil {
			t.Fatalf("err %v, want the retry to land", err)
		}
		store.mu.Lock()
		got := store.state.IO
		store.mu.Unlock()
		if got == nil {
			t.Fatal("nothing saved")
		}
	})
	t.Run("a slow store is waited for", func(t *testing.T) {
		store := &slowSave{fakeStore: &fakeStore{}, delay: 200 * time.Millisecond}
		rt := run(t, store)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := rt.SaveRetained(ctx)
		store.mu.Lock()
		got := store.state.IO
		store.mu.Unlock()
		// The saver was idle, so it took the request at once: the answer is
		// the store's, after the store's 200 ms, not ctx's 50 ms.
		if err != nil || got == nil {
			t.Fatalf("err %v, stored %v: want nil once the store holds it", err, got != nil)
		}
	})
}

// failingLoads fails its first n loads.
type failingLoads struct {
	*fakeStore
	n atomic.Int32
}

func (f *failingLoads) Load() (retain.State, error) {
	if f.n.Add(-1) >= 0 {
		return retain.State{}, errors.New("store down")
	}
	return f.fakeStore.Load()
}

// A store read that fails at the edge is retried; one that keeps failing
// is reported to the keeper, and the scan goes on (#1010 re-review).
func TestTakeoverRetriesAStoreRead(t *testing.T) {
	saved, retry := ioLoadTries, ioLoadRetry
	ioLoadTries, ioLoadRetry = 3, 10*time.Millisecond
	t.Cleanup(func() { ioLoadTries, ioLoadRetry = saved, retry })
	scanUntil := func(rt *Runtime, cond func() bool) {
		for i := 0; i < 200 && !cond(); i++ {
			rt.Scan()
			time.Sleep(time.Millisecond)
		}
	}
	t.Run("a read that fails once", func(t *testing.T) {
		store := &failingLoads{fakeStore: &fakeStore{state: retain.State{IO: keptIO()}}}
		store.n.Store(1)
		rt := newRetained(t, store, nil)
		k := &fakeKeeper{release: make(chan struct{})}
		close(k.release)
		rt.SetIO(k)
		scanUntil(rt, func() bool { return scanned(rt) > 0 })
		if k.restores.Load() != 1 || k.unreadable != nil {
			t.Fatalf("restores %d, unreadable %v: want the retry to read the store and restore", k.restores.Load(), k.unreadable)
		}
	})
	t.Run("a store that cannot be read", func(t *testing.T) {
		store := &failingLoads{fakeStore: &fakeStore{state: retain.State{IO: keptIO()}}}
		store.n.Store(100)
		rt := newRetained(t, store, nil)
		k := &fakeKeeper{}
		rt.SetIO(k)
		scanUntil(rt, func() bool { return scanned(rt) > 0 })
		k.mu.Lock()
		unreadable := k.unreadable
		k.mu.Unlock()
		if scanned(rt) == 0 || unreadable == nil || k.restores.Load() != 0 {
			t.Fatalf("scans %d, unreadable %v, restores %d: want the keeper told and the scan going on", scanned(rt), unreadable, k.restores.Load())
		}
	})
}
