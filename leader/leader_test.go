package leader

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/k8sapi"
)

// fakeAPI is a minimal in-memory stand-in for the coordination.k8s.io Lease
// API: one lease, guarded by a mutex, with a resourceVersion that bumps on
// every write and is enforced on PUT (a stale resourceVersion loses with
// 409) — the fidelity the CAS-race test below depends on.
type fakeAPI struct {
	mu    sync.Mutex
	ns    string
	name  string
	lease *lease
	rv    int
	fail  bool
	// failNext answers that many requests 500 before serving normally.
	failNext int

	// raceGate, when armed, makes GETs on the lease block until raceWant of
	// them have arrived, so two racing electors are guaranteed to read the
	// same pre-steal snapshot before either one's PUT lands.
	raceGate chan struct{}
	raceWant int
	raceHits int

	// holdGet, when armed, holds the next GET until it is closed (a slow
	// API call); heldGet is closed once that GET has arrived.
	holdGet chan struct{}
	heldGet chan struct{}
}

func newFakeAPI(ns, name string) *fakeAPI {
	return &fakeAPI{ns: ns, name: name}
}

func (f *fakeAPI) itemPath() string {
	return "/apis/coordination.k8s.io/v1/namespaces/" + f.ns + "/leases/" + f.name
}

func (f *fakeAPI) collectionPath() string {
	return "/apis/coordination.k8s.io/v1/namespaces/" + f.ns + "/leases"
}

func (f *fakeAPI) armRace(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raceGate = make(chan struct{})
	f.raceWant = n
	f.raceHits = 0
}

// armHoldGet holds the next GET until release is called; arrived is
// closed once it is held.
func (f *fakeAPI) armHoldGet() (arrived <-chan struct{}, release func()) {
	hold, held := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.holdGet, f.heldGet = hold, held
	f.mu.Unlock()
	var once sync.Once
	return held, func() { once.Do(func() { close(hold) }) }
}

func (f *fakeAPI) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.fail || f.failNext > 0 {
		if f.failNext > 0 {
			f.failNext--
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == f.itemPath():
		f.handleGet(w)
	case r.Method == http.MethodPost && r.URL.Path == f.collectionPath():
		f.handlePost(w, r)
	case r.Method == http.MethodPut && r.URL.Path == f.itemPath():
		f.handlePut(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleGet waits at the race gate (if armed) before reading, so concurrent
// racers all observe the lease as it stood before any of them writes.
func (f *fakeAPI) handleGet(w http.ResponseWriter) {
	f.mu.Lock()
	hold, held := f.holdGet, f.heldGet
	f.holdGet, f.heldGet = nil, nil
	f.mu.Unlock()
	if hold != nil {
		close(held)
		<-hold
	}

	f.mu.Lock()
	gate := f.raceGate
	if gate != nil {
		f.raceHits++
		if f.raceHits >= f.raceWant {
			close(gate)
			f.raceGate = nil
		}
	}
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lease == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(f.lease)
}

func (f *fakeAPI) handlePost(w http.ResponseWriter, r *http.Request) {
	var l lease
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
	l.Metadata.ResourceVersion = strconv.Itoa(f.rv)
	f.lease = &l
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(f.lease)
}

func (f *fakeAPI) handlePut(w http.ResponseWriter, r *http.Request) {
	var l lease
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lease == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if l.Metadata.ResourceVersion != f.lease.Metadata.ResourceVersion {
		w.WriteHeader(http.StatusConflict)
		return
	}
	f.rv++
	l.Metadata.ResourceVersion = strconv.Itoa(f.rv)
	f.lease = &l
	_ = json.NewEncoder(w).Encode(f.lease)
}

// newTestElector builds a cluster-mode Elector against a fresh fakeAPI, with
// a lease duration shorter than production's 4s. Every test drives tick
// with explicit synthetic times, so shortening it is about wire-format
// realism (LeaseDurationSeconds), not about making the test faster.
func newTestElector(t *testing.T, srv *httptest.Server, identity, addr string) *Elector {
	t.Helper()
	c := &k8sapi.Client{Base: srv.URL, Namespace: "testns", HTTP: srv.Client()}
	e := newWithClient(c, "test-lease", identity, addr)
	e.duration = time.Second
	return e
}

func newFakeServer(t *testing.T) (*httptest.Server, *fakeAPI) {
	t.Helper()
	f := newFakeAPI("testns", "test-lease")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, f
}

func TestFreshClusterAcquire(t *testing.T) {
	srv, _ := newFakeServer(t)
	e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")

	e.tick(time.Now())

	if !e.IsLeader() {
		t.Fatal("expected leadership after first tick on an empty lease")
	}
	st := e.Status()
	if st.Mode != "cluster" || !st.IsLeader || st.Leader != "pod-a" || st.LeaderAddr != "10.0.0.1:8080" || st.Transitions != 1 {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestRenew(t *testing.T) {
	srv, fake := newFakeServer(t)
	e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")

	t0 := time.Now()
	e.tick(t0)
	firstRenew := fake.lease.Spec.RenewTime

	e.tick(t0.Add(300 * time.Millisecond))

	if !e.IsLeader() {
		t.Fatal("expected to remain leader across a renew")
	}
	if fake.lease.Spec.RenewTime == firstRenew {
		t.Fatal("expected renew to advance RenewTime")
	}
	if st := e.Status(); st.Transitions != 1 {
		t.Fatalf("renew should not bump transitions, got %d", st.Transitions)
	}
}

func TestFollowerThenSteal(t *testing.T) {
	srv, _ := newFakeServer(t)
	e1 := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	e2 := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")

	t0 := time.Now()
	e1.tick(t0)
	e1.tick(t0.Add(300 * time.Millisecond)) // renew, still well inside the lease

	e2.tick(t0.Add(300 * time.Millisecond))
	if e2.IsLeader() {
		t.Fatal("second elector should stay a follower while the first renews")
	}
	if st := e2.Status(); st.Leader != "pod-a" {
		t.Fatalf("follower should see pod-a as leader, got %q", st.Leader)
	}

	// e1 stops ticking; once its lease outlives the 1s duration, e2 steals it.
	e2.tick(t0.Add(1500 * time.Millisecond))
	if !e2.IsLeader() {
		t.Fatal("expected the follower to steal the expired lease")
	}
	if st := e2.Status(); st.Transitions != 2 {
		t.Fatalf("steal should bump transitions to 2, got %d", st.Transitions)
	}
}

func TestCASRace(t *testing.T) {
	srv, fake := newFakeServer(t)
	holder := newTestElector(t, srv, "pod-0", "10.0.0.0:8080")
	e1 := newTestElector(t, srv, "pod-1", "10.0.0.1:8080")
	e2 := newTestElector(t, srv, "pod-2", "10.0.0.2:8080")

	t0 := time.Now()
	holder.tick(t0) // establishes the lease so e1/e2 have something to steal

	tRace := t0.Add(1500 * time.Millisecond) // past the 1s duration
	fake.armRace(2)                          // both GETs must land before either PUT

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); e1.tick(tRace) }()
	go func() { defer wg.Done(); e2.tick(tRace) }()
	wg.Wait()

	if e1.IsLeader() == e2.IsLeader() {
		t.Fatalf("expected exactly one racer to win, got e1=%v e2=%v", e1.IsLeader(), e2.IsLeader())
	}
	if fake.lease.Spec.LeaseTransitions != 2 {
		t.Fatalf("expected exactly one steal to land, transitions=%d", fake.lease.Spec.LeaseTransitions)
	}
}

func TestRelease(t *testing.T) {
	srv, _ := newFakeServer(t)
	e1 := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	e2 := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")

	t0 := time.Now()
	e1.tick(t0)
	e1.Release()

	if e1.IsLeader() {
		t.Fatal("expected Release to drop leadership locally")
	}

	// A tiny time step, not a full 4s/1s wait — Release expires the lease
	// immediately so the very next tick can acquire it.
	e2.tick(t0.Add(10 * time.Millisecond))
	if !e2.IsLeader() {
		t.Fatal("expected the follower to acquire immediately after Release")
	}
	if st := e2.Status(); st.Transitions != 2 {
		t.Fatalf("expected transitions to bump on takeover, got %d", st.Transitions)
	}
}

func TestStepDown(t *testing.T) {
	srv, fake := newFakeServer(t)
	e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")

	t0 := time.Now()
	e.tick(t0)
	if !e.IsLeader() {
		t.Fatal("expected initial acquire to succeed")
	}

	fake.setFail(true)

	e.tick(t0.Add(500 * time.Millisecond))
	if !e.IsLeader() {
		t.Fatal("should not step down before the lease duration elapses")
	}

	e.tick(t0.Add(1500 * time.Millisecond))
	if e.IsLeader() {
		t.Fatal("expected step-down once the lease duration elapses without a successful renew")
	}
}

func TestStandalone(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	e, err := New("test-lease", "pod-a", "10.0.0.1:8080")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st := e.Status()
	if st.Mode != "standalone" || !st.IsLeader || !e.IsLeader() {
		t.Fatalf("expected always-leader standalone mode, got %+v", st)
	}

	done := make(chan struct{})
	go func() {
		e.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Run should return immediately in standalone mode")
	}

	e.Release() // no-op; must not panic or block
}

// Run makes its first decision at once, before its ticker's first tick,
// and Decided says so: a replica that acquires the lease is the leader
// from that moment, so `naut run` starts its drivers as a plain start
// (their start-up baseline kept, #606); one that finds the lease held is a
// standby from that moment. A standalone elector has decided from New.
func TestRunDecidesBeforeItsFirstTick(t *testing.T) {
	waitDecided := func(t *testing.T, e *Elector) {
		t.Helper()
		select {
		case <-e.Decided():
		case <-time.After(2 * time.Second):
			t.Fatal("no first decision: Run must decide before its ticker's first tick")
		}
	}
	// A long lease: the ticker's first tick is 10 s away, so only the
	// decision Run makes up front can close Decided in time.
	const lease = 40 * time.Second

	t.Run("acquires", func(t *testing.T) {
		srv, _ := newFakeServer(t)
		e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
		e.duration = lease
		select {
		case <-e.Decided():
			t.Fatal("a cluster elector has decided before Run")
		default:
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go e.Run(ctx)
		waitDecided(t, e)
		if !e.IsLeader() {
			t.Fatal("the first decision on an empty lease must be leader")
		}
	})

	t.Run("standby", func(t *testing.T) {
		srv, _ := newFakeServer(t)
		holder := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
		holder.duration = lease
		holder.tick(time.Now())
		e := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")
		e.duration = lease
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go e.Run(ctx)
		waitDecided(t, e)
		if e.IsLeader() {
			t.Fatal("a held, fresh lease must make the first decision standby")
		}
	})

	t.Run("standalone", func(t *testing.T) {
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
		e, err := New("test-lease", "pod-a", "")
		if err != nil {
			t.Fatal(err)
		}
		waitDecided(t, e)
	})
}

// A first tick that never reached the API (a blip: the Lease GET failed)
// decides nothing: Run tries again within its lease duration, and the
// first decision is the one the API gave. A replica that will lead is
// then a plain start, not a standby that commands every output a second
// later (#606).
func TestRunRetriesAFirstTickThatMissedTheAPI(t *testing.T) {
	srv, fake := newFakeServer(t)
	fake.failNext = 1
	e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	e.duration = 4 * time.Second // retries every 250 ms; the ticker's first tick is 1 s in
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	select {
	case <-e.Decided():
	case <-time.After(3 * time.Second):
		t.Fatal("no first decision")
	}
	if !e.IsLeader() {
		t.Fatal("a failed first GET was taken as the first decision (standby); the retry acquires an empty lease")
	}
}

// seedLease stands in for a Lease something other than this naut wrote (by
// hand, a GitOps manifest, another tool): held by holder, last renewed at
// renewed, with the given stored duration.
func (f *fakeAPI) seedLease(holder string, seconds int, renewed time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
	l := &lease{APIVersion: "coordination.k8s.io/v1", Kind: "Lease"}
	l.Metadata.Name, l.Metadata.Namespace = f.name, f.ns
	l.Metadata.ResourceVersion = strconv.Itoa(f.rv)
	l.Spec = leaseSpec{
		HolderIdentity: holder, LeaseDurationSeconds: seconds,
		AcquireTime: mtime(renewed), RenewTime: mtime(renewed), LeaseTransitions: 1,
	}
	f.lease = l
}

func (f *fakeAPI) storedSeconds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lease.Spec.LeaseDurationSeconds
}

// A renew writes this replica's duration over a long one stored by
// something else, so the Lease says what the holder's fence enforces (#635).
func TestRenewWritesOwnDuration(t *testing.T) {
	srv, fake := newFakeServer(t)
	e := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	e.duration = 4 * time.Second

	t0 := time.Now()
	fake.seedLease(e.holderID, 50, t0)
	e.tick(t0.Add(time.Second))

	if !e.IsLeader() {
		t.Fatal("expected the holder to renew and lead")
	}
	if got := fake.storedSeconds(); got != 4 {
		t.Fatalf("stored leaseDurationSeconds after renew = %d, want 4", got)
	}
}

// A takeover of a Lease stored long waits out the stored duration, then
// writes this replica's own (#635: the bench saw 50 s kept after takeover).
func TestTakeoverWritesOwnDuration(t *testing.T) {
	srv, fake := newFakeServer(t)
	e := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")
	e.duration = 4 * time.Second

	t0 := time.Now()
	fake.seedLease("pod-a|10.0.0.1:8080", 50, t0)

	e.tick(t0.Add(10 * time.Second))
	if e.IsLeader() {
		t.Fatal("a standby must not take a lease still inside its stored 50 s")
	}
	e.tick(t0.Add(51 * time.Second))
	if !e.IsLeader() {
		t.Fatal("expected the standby to take the lease once its stored duration lapsed")
	}
	if got := fake.storedSeconds(); got != 4 {
		t.Fatalf("stored leaseDurationSeconds after takeover = %d, want 4", got)
	}
}

// A Lease stored shorter than this replica's duration, or with none, must
// not let a standby take it from a live holder: expiry is judged by the
// longer of the two (#635).
func TestShortStoredDurationDoesNotCutAHolderShort(t *testing.T) {
	for _, stored := range []int{0, 1} {
		t.Run(strconv.Itoa(stored), func(t *testing.T) {
			srv, fake := newFakeServer(t)
			e := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")
			e.duration = 4 * time.Second

			t0 := time.Now()
			fake.seedLease("pod-a|10.0.0.1:8080", stored, t0)

			e.tick(t0.Add(3 * time.Second))
			if e.IsLeader() {
				t.Fatalf("stored %d s: standby took the lease inside its own 4 s duration", stored)
			}
			e.tick(t0.Add(5 * time.Second))
			if !e.IsLeader() {
				t.Fatalf("stored %d s: expected a takeover once the 4 s duration lapsed", stored)
			}
			if got := fake.storedSeconds(); got != 4 {
				t.Fatalf("stored leaseDurationSeconds after takeover = %d, want 4", got)
			}
		})
	}
}

func TestLeaseSecondsRoundsUp(t *testing.T) {
	e := &Elector{}
	for d, want := range map[time.Duration]int{
		4 * time.Second: 4, 1500 * time.Millisecond: 2, 100 * time.Millisecond: 1,
	} {
		e.duration = d
		if got := e.leaseSeconds(); got != want {
			t.Errorf("leaseSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}

// A stored duration too large for a time.Duration saturates (never lapses)
// rather than wrapping to a short or negative lease.
func TestHugeStoredDurationDoesNotWrap(t *testing.T) {
	e := &Elector{duration: 4 * time.Second}
	t0 := time.Now()
	l := &lease{Spec: leaseSpec{RenewTime: mtime(t0), LeaseDurationSeconds: math.MaxInt}}
	if e.expired(l, t0.Add(time.Hour)) {
		t.Fatal("a huge stored duration wrapped and expired the lease")
	}
}
