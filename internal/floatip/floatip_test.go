package floatip

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeOps struct {
	mu     sync.Mutex
	calls  []string
	addErr error
	// applyErr: Add puts the address on and still reports an error (a lost reply)
	applyErr error
	// onArp runs on each announcement
	onArp  func()
	delErr error
	on     bool
}

func (f *fakeOps) rec(s string) { f.calls = append(f.calls, s) }
func (f *fakeOps) Add(Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("add")
	if f.addErr != nil {
		return f.addErr
	}
	if f.applyErr != nil {
		f.on = true
		return f.applyErr
	}
	f.on = true
	return nil
}
func (f *fakeOps) Del(Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rec("del")
	if f.delErr != nil {
		return f.delErr
	}
	f.on = false
	return nil
}
func (f *fakeOps) Announce(Config) error {
	f.mu.Lock()
	f.rec("arp")
	fn := f.onArp
	f.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}
func (f *fakeOps) log() []string { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.calls) }
func (f *fakeOps) present() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

func cfg(t *testing.T) Config {
	c, err := Parse("10.0.0.50/24", "eth0")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParse(t *testing.T) {
	for _, bad := range [][2]string{{"10.0.0.50", "eth0"}, {"fe80::1/64", "eth0"}, {"nonsense", "eth0"}, {"10.0.0.50/24", ""}, {"10.0.0.50/24", "an-interface-name-too-long"},
		{"0.0.0.0/0", "eth0"}, {"127.0.0.1/8", "eth0"}, {"224.0.0.1/24", "eth0"}, {"10.0.0.0/24", "eth0"}, {"10.0.0.255/24", "eth0"}, {"169.254.1.1/16", "eth0"}} {
		if _, err := Parse(bad[0], bad[1]); err == nil {
			t.Errorf("Parse(%q, %q) accepted", bad[0], bad[1])
		}
	}
}

// The address is added first and announced after, three times; dropping
// removes it once and a second drop is harmless.
func TestHoldAddsThenAnnounces(t *testing.T) {
	ops := &fakeOps{}
	h := New(cfg(t), ops)
	if err := h.Hold(); err != nil {
		t.Fatal(err)
	}
	if got := ops.log(); !slices.Equal(got, []string{"add", "arp", "arp", "arp"}) {
		t.Errorf("calls %v, want add then three announcements", got)
	}
	if err := h.Hold(); err != nil || len(ops.log()) != 4 {
		t.Errorf("a second Hold did something: %v", ops.log())
	}
	if err := h.Drop(); err != nil || h.Held() || ops.present() {
		t.Errorf("drop: err %v held %v present %v", err, h.Held(), ops.present())
	}
}

func TestAFailedAddIsNotHeldAndIsRetried(t *testing.T) {
	ops := &fakeOps{addErr: errors.New("operation not permitted")}
	h := New(cfg(t), ops)
	if err := h.Hold(); err == nil || h.Held() {
		t.Fatalf("a failed add: err %v held %v", err, h.Held())
	}
	ops.mu.Lock()
	ops.addErr = nil
	ops.mu.Unlock()
	if err := h.Hold(); err != nil || !h.Held() || !ops.present() {
		t.Error("the retry did not take the address")
	}
}

func TestAFailedDropStaysHeldAndIsRetried(t *testing.T) {
	ops := &fakeOps{}
	h := New(cfg(t), ops)
	h.Hold()
	ops.mu.Lock()
	ops.delErr = errors.New("busy")
	ops.mu.Unlock()
	if err := h.Drop(); err == nil || !h.Held() {
		t.Fatalf("a failed drop: err %v held %v (it must keep trying)", err, h.Held())
	}
}

// Watch follows the gate: nothing while it is closed (a spare, a copy held
// by a duplicate mark, a standby), the address after it opens, gone after it
// closes. Leftovers from a killed process are cleared at the start.
func TestWatchFollowsTheGate(t *testing.T) {
	ops := &fakeOps{on: true} // a leftover address
	h := New(cfg(t), ops)
	var mu sync.Mutex
	open := false
	gate := func() bool { mu.Lock(); defer mu.Unlock(); return open }
	set := func(v bool) { mu.Lock(); open = v; mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Watch(ctx, gate, 2*time.Millisecond); close(done) }()
	waitUntil(t, "the leftover to be cleared", func() bool { return !ops.present() })
	time.Sleep(20 * time.Millisecond)
	if h.Held() || ops.present() || slices.Contains(ops.log(), "add") {
		t.Fatal("a closed gate holds the address: a spare or held copy would answer for it")
	}
	set(true)
	waitUntil(t, "the address after the gate opens", func() bool { return h.Held() && ops.present() })
	set(false)
	waitUntil(t, "the address to go when the gate closes", func() bool { return !h.Held() && !ops.present() })
	set(true)
	waitUntil(t, "the address again", h.Held)
	cancel()
	<-done
	if !h.Held() {
		t.Error("Watch dropped on exit: the caller drops, after Watch returned and before it releases the lease")
	}
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fast(h *Holder) *Holder {
	h.Backoff, h.Refresh, h.Reconcile = 4*time.Millisecond, 20*time.Millisecond, 10*time.Millisecond
	return h
}

// An add whose reply was lost may still have put the address on: it is not
// "held", yet a shut gate must take it off (the leak a stalled kernel reply
// would leave on a live process).
func TestAnAddWhoseOutcomeIsUnknownIsUndoneWhenTheGateShuts(t *testing.T) {
	ops := &fakeOps{applyErr: errors.New("no acknowledgement")}
	h := fast(New(cfg(t), ops))
	var mu sync.Mutex
	open := true
	gate := func() bool { mu.Lock(); defer mu.Unlock(); return open }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Watch(ctx, gate, time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, "the address (applied despite the error)", ops.present)
	if h.Held() {
		t.Fatal("an add that reported an error is held")
	}
	mu.Lock()
	open = false
	mu.Unlock()
	waitUntil(t, "the unknown-outcome address to be removed", func() bool { return !ops.present() })
}

// A gate that shuts while Hold is announcing (a stall between the add and
// the announcements) takes the address back before it announces: a fenced
// controller must not announce after the new leader.
func TestTheGateIsLookedAtBetweenTheAddAndEachAnnouncement(t *testing.T) {
	ops := &fakeOps{}
	h := New(cfg(t), ops)
	var mu sync.Mutex
	open := true
	h.gate = func() bool { mu.Lock(); defer mu.Unlock(); return open }
	ops.onArp = func() { mu.Lock(); open = false; mu.Unlock() } // shuts after the first
	if err := h.Hold(); err != nil {
		t.Fatal(err)
	}
	if got := ops.log(); !slices.Equal(got, []string{"add", "arp", "del"}) {
		t.Errorf("calls %v, want add, one announcement, then del", got)
	}
	if h.Held() || ops.present() {
		t.Errorf("held %v present %v after the gate shut mid-announce", h.Held(), ops.present())
	}
}

// A shut gate keeps removing the address, even when nothing is believed held.
func TestAShutGateKeepsRemovingTheAddress(t *testing.T) {
	ops := &fakeOps{}
	h := fast(New(cfg(t), ops))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Watch(ctx, func() bool { return false }, time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, "the startup clear", func() bool { return len(ops.log()) >= 1 })
	ops.mu.Lock()
	ops.on = true // something put it back behind the holder's back
	ops.mu.Unlock()
	waitUntil(t, "the periodic reconcile to remove it", func() bool { return !ops.present() })
}

// An open gate puts the address back if something removed it, and announces
// again.
func TestAnOpenGateRestoresAndReannounces(t *testing.T) {
	ops := &fakeOps{}
	h := fast(New(cfg(t), ops))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Watch(ctx, func() bool { return true }, time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, "the address", ops.present)
	ops.mu.Lock()
	ops.on = false // removed from under the leader
	n := len(ops.calls)
	ops.mu.Unlock()
	waitUntil(t, "the refresh to put it back", ops.present)
	waitUntil(t, "a fresh announcement", func() bool { return len(ops.log()) > n+1 && slices.Contains(ops.log()[n:], "arp") })
}

// A failing add backs off instead of retrying every tick.
func TestAFailingAddBacksOff(t *testing.T) {
	ops := &fakeOps{addErr: errors.New("operation not permitted")}
	h := New(cfg(t), ops)
	h.Backoff = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Watch(ctx, func() bool { return true }, time.Millisecond); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if n := len(ops.log()); n > 2 {
		t.Errorf("%d add attempts in 100 ms with a 200 ms backoff", n)
	}
}
