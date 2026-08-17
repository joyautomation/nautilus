package leader

import (
	"context"
	"encoding/json"
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

	// raceGate, when armed, makes GETs on the lease block until raceWant of
	// them have arrived, so two racing electors are guaranteed to read the
	// same pre-steal snapshot before either one's PUT lands.
	raceGate chan struct{}
	raceWant int
	raceHits int
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

func (f *fakeAPI) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	if f.fail {
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
