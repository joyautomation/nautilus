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
func (f *fakeOps) Announce(Config) error { f.mu.Lock(); defer f.mu.Unlock(); f.rec("arp"); return nil }
func (f *fakeOps) log() []string         { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.calls) }
func (f *fakeOps) present() bool         { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

func cfg(t *testing.T) Config {
	c, err := Parse("10.0.0.50/24", "eth0")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestParse(t *testing.T) {
	for _, bad := range [][2]string{{"10.0.0.50", "eth0"}, {"fe80::1/64", "eth0"}, {"nonsense", "eth0"}, {"10.0.0.50/24", ""}, {"10.0.0.50/24", "an-interface-name-too-long"}} {
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
