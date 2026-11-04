package leader

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The lease needs live scans (#621). These tests drive electors against
// the fake Lease API with explicit times, a tick (duration/4 = 250 ms with
// the tests' 1 s lease) at a time; the health of the first group is a
// stub, of the second a real runtime with a driver whose read can hang.

// stubHealth is a health func a test flips between live and stuck.
type stubHealth struct{ stuck atomic.Bool }

func (s *stubHealth) health(time.Time, time.Duration) error {
	if s.stuck.Load() {
		return errors.New("task main: a scan has not finished within 250ms")
	}
	return nil
}

const testTick = 250 * time.Millisecond // newTestElector's 1 s lease / 4

// A leader whose health says its scans are stuck stops renewing: the
// lease keeps its last renew time, the leader gives up its gate on its
// first tick more than half a lease after it (before anyone can take the
// lease), and the standby takes over once the lease lapses.
func TestUnhealthyLeaderStopsRenewing(t *testing.T) {
	srv, fake := newFakeServer(t)
	a := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	b := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")
	h := &stubHealth{}
	a.SetHealth(h.health)

	t0 := time.Now()
	a.tick(t0)
	b.tick(t0)
	if !a.IsLeader() || b.IsLeader() {
		t.Fatalf("setup: a=%v b=%v", a.IsLeader(), b.IsLeader())
	}
	h.stuck.Store(true)
	lastRenew := fake.lease.Spec.RenewTime
	a.tick(t0.Add(testTick))
	if fake.lease.Spec.RenewTime != lastRenew {
		t.Fatal("a leader with stuck scans renewed its lease")
	}
	if !a.IsLeader() {
		t.Fatal("one tick of stuck scans closed the gate: an occasional slow scan would flap it")
	}
	a.tick(t0.Add(3 * testTick))
	b.tick(t0.Add(3 * testTick))
	if a.IsLeader() || b.IsLeader() {
		t.Fatalf("three quarters of a lease after the last renew: a=%v b=%v, want the hung leader down and the lease not yet lapsed", a.IsLeader(), b.IsLeader())
	}
	a.tick(t0.Add(time.Second + testTick))
	b.tick(t0.Add(time.Second + testTick))
	if a.IsLeader() || !b.IsLeader() {
		t.Fatalf("a lease duration and a tick after the last renew: a=%v b=%v, want the standby leading", a.IsLeader(), b.IsLeader())
	}
}

// A replica whose scans are stuck does not acquire: not an empty lease,
// not an expired one. A broken replica must not take over.
func TestUnhealthyReplicaDoesNotAcquire(t *testing.T) {
	srv, _ := newFakeServer(t)
	a := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	h := &stubHealth{}
	h.stuck.Store(true)
	a.SetHealth(h.health)

	t0 := time.Now()
	a.tick(t0)
	if a.IsLeader() {
		t.Fatal("a replica with stuck scans created the lease")
	}

	b := newTestElector(t, srv, "pod-b", "10.0.0.2:8080")
	b.tick(t0)                      // b claims it, then dies
	a.tick(t0.Add(2 * time.Second)) // b's lease has long lapsed
	if a.IsLeader() {
		t.Fatal("a replica with stuck scans took over an expired lease")
	}

	h.stuck.Store(false)
	a.tick(t0.Add(2*time.Second + testTick))
	if !a.IsLeader() {
		t.Fatal("once its scans are live again the replica contends as before")
	}
}

// The change of health is logged once each way, naming the reason, not
// once a tick.
func TestHealthChangeIsLoggedOnce(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv, _ := newFakeServer(t)
	a := newTestElector(t, srv, "pod-a", "10.0.0.1:8080")
	h := &stubHealth{}
	a.SetHealth(h.health)
	t0 := time.Now()
	a.tick(t0)
	h.stuck.Store(true)
	for i := 1; i <= 6; i++ {
		a.tick(t0.Add(time.Duration(i) * testTick))
	}
	h.stuck.Store(false)
	for i := 7; i <= 9; i++ {
		a.tick(t0.Add(time.Duration(i) * testTick))
	}
	out := buf.String()
	if n := strings.Count(out, "level=ERROR"); n != 1 {
		t.Fatalf("%d error records, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "task main") {
		t.Fatalf("the error does not name the stuck task:\n%s", out)
	}
	if n := strings.Count(out, "scans are completing again"); n != 1 {
		t.Fatalf("%d recovery records, want 1:\n%s", n, out)
	}
}

// A standalone elector has no lease and no one to hand over to: with a
// health func, it closes its gate a lease duration after its scans stopped
// completing (a driver's keep-alive then stops masking the hang from the
// device's watchdog), and opens it again once they complete. Without one
// it is always the leader (TestStandalone).
func TestStandaloneGateClosesWhileScansHang(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	e, err := New("test-lease", "pod-a", "")
	if err != nil {
		t.Fatal(err)
	}
	e.duration = time.Second
	h := &stubHealth{}
	e.SetHealth(h.health)

	t0 := time.Now()
	e.standaloneTick(t0)
	h.stuck.Store(true)
	e.standaloneTick(t0.Add(time.Second))
	if !e.IsLeader() {
		t.Fatal("the gate closed before a lease duration without live scans")
	}
	e.standaloneTick(t0.Add(time.Second + testTick))
	if e.IsLeader() {
		t.Fatal("a standalone replica whose scans are hung keeps its gate open")
	}
	h.stuck.Store(false)
	e.standaloneTick(t0.Add(time.Second + 2*testTick))
	if !e.IsLeader() {
		t.Fatal("the gate stays closed once scans complete again")
	}

	// Run ticks on its own when there is a health func: a fresh elector,
	// as this one's last renew is in the test's future.
	e, err = New("test-lease", "pod-a", "")
	if err != nil {
		t.Fatal(err)
	}
	e.duration = 40 * time.Millisecond
	e.SetHealth(h.health)
	h.stuck.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.IsLeader() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if e.IsLeader() {
		t.Fatal("standalone Run never closed the gate on hung scans")
	}
}

// ---- with a real runtime ----

// hangDriver is a driver whose next read blocks until the test lets it go:
// a scan stuck in a driver call. It counts output pushes.
type hangDriver struct {
	mu     sync.Mutex
	hang   chan struct{}
	hung   chan struct{}
	writes atomic.Int64
}

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

// replica is one pod: an elector, and a runtime that answers to it and
// whose liveness it asks.
type replica struct {
	el  *Elector
	rt  *runtime.Runtime
	drv *hangDriver
}

func newReplica(t *testing.T, e *Elector, scan time.Duration, tasks ...runtime.Task) *replica {
	t.Helper()
	drv := &hangDriver{}
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Counter
VAR_EXTERNAL Out : DINT; END_VAR
Out := Out + 1;
END_PROGRAM`,
		Driver:      drv,
		Outputs:     []string{"Out"},
		Seed:        nio.Values{"Out": int64(0)},
		Scan:        scan,
		Tasks:       tasks,
		Coordinator: e,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.SetHealth(rt.Liveness)
	return &replica{el: e, rt: rt, drv: drv}
}

// run starts the runtime's loops until the test ends; release functions
// run first in the cleanup so a blocked read cannot keep Run from
// returning.
func (r *replica) run(t *testing.T, release ...func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.rt.Run(ctx); close(done) }()
	t.Cleanup(func() {
		for _, f := range release {
			f()
		}
		cancel()
		<-done
	})
}

// progress waits until the runtime has completed two more scans, so a look
// at its liveness after it sees a new scan whatever time the test passes:
// the tests' times run ahead of the wall clock, and only a scan that has
// not finished between two looks is on trial.
func (r *replica) progress(t *testing.T) {
	t.Helper()
	want := r.rt.Stats().Count + 2
	deadline := time.Now().Add(5 * time.Second)
	for r.rt.Stats().Count < want {
		if time.Now().After(deadline) {
			t.Fatal("the runtime is not scanning")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitFor(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

// hungLeader sets up pod-a leading with its scan hung in a driver read,
// and pod-b a healthy standby whose runtime idles (a standby's scans gate
// out, so its period does not matter; an hour keeps it between scans).
func hungLeader(t *testing.T) (a, b *replica, t0 time.Time, release func()) {
	t.Helper()
	srv, _ := newFakeServer(t)
	a = newReplica(t, newTestElector(t, srv, "pod-a", "10.0.0.1:8080"), 10*time.Millisecond)
	b = newReplica(t, newTestElector(t, srv, "pod-b", "10.0.0.2:8080"), time.Hour)
	t0 = time.Now()
	a.el.tick(t0) // decides before its first scan: the start-up grace
	b.el.tick(t0)
	if !a.el.IsLeader() || b.el.IsLeader() {
		t.Fatalf("setup: a=%v b=%v", a.el.IsLeader(), b.el.IsLeader())
	}
	hung, release := a.drv.arm()
	a.run(t, release)
	b.run(t)
	waitFor(t, hung, "pod-a's read to block")
	return a, b, t0, release
}

// A leader whose scan hangs in a driver read stops renewing and loses the
// lease, and the standby takes over: within the scan's stall bound (a
// tick late at most, since it is seen a tick at a time) plus a lease
// duration plus a tick. The two never lead at once, and when the hung
// scan finally returns it writes nothing: the standby has taken over.
func TestAHungLeaderHandsOverToTheStandby(t *testing.T) {
	a, b, t0, release := hungLeader(t)
	const lease = time.Second
	bound := runtime.StallBound(10*time.Millisecond, lease/2) // 250 ms
	// The hung scan is first seen at the first tick; stuck a bound after
	// (rounded up to a tick); the lease lapses a duration after the last
	// renew; the standby sees it on the tick after.
	limit := testTick + bound + testTick + lease + testTick
	took := time.Duration(0)
	for now := t0.Add(testTick); now.Sub(t0) <= 3*time.Second; now = now.Add(testTick) {
		a.el.tick(now)
		b.el.tick(now)
		if a.el.IsLeader() && b.el.IsLeader() {
			t.Fatalf("at +%s both replicas lead", now.Sub(t0))
		}
		if b.el.IsLeader() && took == 0 {
			took = now.Sub(t0)
		}
	}
	if took == 0 {
		t.Fatal("the standby never took over from a leader whose scan is hung")
	}
	if took > limit {
		t.Fatalf("the standby took over %s after the hang, want within %s", took, limit)
	}
	if a.el.IsLeader() {
		t.Fatal("the hung replica still leads")
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for a.rt.Stats().Count < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.rt.Stats().Count < 1 {
		t.Fatal("the hung scan never finished")
	}
	if n := a.drv.writes.Load(); n != 0 {
		t.Fatalf("the hung scan pushed outputs after it lost the lease (%d pushes)", n)
	}
}

// A replica whose scan is still hung does not take the lease back: when
// the leader that replaced it dies, it stays a standby until its scans
// complete, then contends as before.
func TestAHungReplicaDoesNotTakeTheLeaseBack(t *testing.T) {
	a, b, t0, release := hungLeader(t)
	now := t0
	for i := 0; i < 12 && !b.el.IsLeader(); i++ {
		now = now.Add(testTick)
		a.el.tick(now)
		b.el.tick(now)
	}
	if !b.el.IsLeader() {
		t.Fatal("setup: the standby never took over from the hung leader")
	}

	// pod-b dies: no more ticks from it. Its lease lapses.
	for i := 0; i < 12; i++ {
		now = now.Add(testTick)
		a.el.tick(now)
		if a.el.IsLeader() {
			t.Fatalf("a replica whose scan is still hung took the lease %s after the other stopped renewing", time.Duration(i+1)*testTick)
		}
	}

	release()
	// Its scans gate out now (a standby), so wait for liveness itself:
	// the hung scan has finished once a look sees its count move.
	deadline := time.Now().Add(5 * time.Second)
	for a.rt.Liveness(now, time.Second/2) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the released scan never finished")
		}
		time.Sleep(time.Millisecond)
	}
	now = now.Add(testTick)
	a.el.tick(now)
	if !a.el.IsLeader() {
		t.Fatal("once its scans complete the replica takes the lapsed lease")
	}
}

// A healthy leader with a scan that is slow now and then keeps the lease:
// one scan stuck for most of a second (two ticks seen stuck past its
// bound) skips two renews, and the gate stays open.
func TestASlowScanKeepsTheLease(t *testing.T) {
	srv, _ := newFakeServer(t)
	a := newReplica(t, newTestElector(t, srv, "pod-a", "10.0.0.1:8080"), 10*time.Millisecond)
	b := newReplica(t, newTestElector(t, srv, "pod-b", "10.0.0.2:8080"), time.Hour)
	t0 := time.Now()
	a.el.tick(t0)
	b.el.tick(t0)
	a.run(t)
	b.run(t)
	a.progress(t)

	now := t0
	step := func() {
		t.Helper()
		now = now.Add(testTick)
		a.el.tick(now)
		b.el.tick(now)
		if !a.el.IsLeader() || b.el.IsLeader() {
			t.Fatalf("at +%s: a=%v b=%v, want the leader to keep the lease", now.Sub(t0), a.el.IsLeader(), b.el.IsLeader())
		}
	}
	for range 4 {
		a.progress(t)
		step()
	}
	hung, release := a.drv.arm()
	t.Cleanup(release)
	waitFor(t, hung, "the slow read")
	for range 4 { // first seen, live at the bound, then stuck for two ticks
		step()
	}
	release()
	for range 8 {
		a.progress(t)
		step()
	}
}

// A task whose period is longer than the lease does not make the lease
// flap: between its scans it sleeps, and a sleeping task is live.
func TestASlowTaskDoesNotFlapTheLease(t *testing.T) {
	srv, _ := newFakeServer(t)
	a := newReplica(t, newTestElector(t, srv, "pod-a", "10.0.0.1:8080"), 5*time.Millisecond, runtime.Task{
		Name:    "reports",
		Program: `PROGRAM Reports VAR n : DINT; END_VAR n := n + 1; END_PROGRAM`,
		Scan:    10 * time.Second, // ten leases
	})
	b := newReplica(t, newTestElector(t, srv, "pod-b", "10.0.0.2:8080"), time.Hour)
	t0 := time.Now()
	a.el.tick(t0)
	b.el.tick(t0)
	a.run(t)
	b.run(t)
	now := t0
	for i := 0; i < 60; i++ { // 15 s: one and a half of the slow task's periods
		a.progress(t)
		now = now.Add(testTick)
		a.el.tick(now)
		b.el.tick(now)
		if !a.el.IsLeader() || b.el.IsLeader() {
			t.Fatalf("at +%s: a=%v b=%v, want the leader to keep the lease", now.Sub(t0), a.el.IsLeader(), b.el.IsLeader())
		}
	}
}

// A leader that decided before its first scan (the start-up grace) but
// whose scan loops never start does not hold the lease for good: after
// the grace (twice the lease) it stops renewing and the standby takes
// over.
func TestANeverScanningLeaderLosesTheLease(t *testing.T) {
	srv, _ := newFakeServer(t)
	a := newReplica(t, newTestElector(t, srv, "pod-a", "10.0.0.1:8080"), 10*time.Millisecond)
	b := newReplica(t, newTestElector(t, srv, "pod-b", "10.0.0.2:8080"), time.Hour)
	b.run(t) // a's runtime is never run
	t0 := time.Now()
	a.el.tick(t0)
	b.el.tick(t0)
	if !a.el.IsLeader() {
		t.Fatal("the first decision, before any scan, must be able to lead")
	}
	now := t0
	took := time.Duration(0)
	for now.Sub(t0) < 6*time.Second {
		now = now.Add(testTick)
		a.el.tick(now)
		b.el.tick(now)
		if b.el.IsLeader() && took == 0 {
			took = now.Sub(t0)
		}
	}
	if took == 0 || a.el.IsLeader() {
		t.Fatalf("a leader whose scans never started kept the lease (standby took over at %s)", took)
	}
	// grace (2 s) + lease (1 s) + a tick
	if limit := 2*time.Second + time.Second + 2*testTick; took > limit {
		t.Fatalf("the standby took over at +%s, want within %s", took, limit)
	}
}
