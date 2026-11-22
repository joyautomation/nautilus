package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/floatip"
	"github.com/joyautomation/nautilus/internal/project"
)

// countingOps is a kernel that only counts who holds the address.
type countingOps struct{ on atomic.Bool }

func (o *countingOps) Add(floatip.Config) error      { o.on.Store(true); return nil }
func (o *countingOps) Del(floatip.Config) error      { o.on.Store(false); return nil }
func (o *countingOps) Announce(floatip.Config) error { return nil }

// On a real pair (two boxes and a witness) the floating address follows the
// write gate: the leader holds it, never both at once across a handover, and
// a controller made a spare by a copy of its own name never holds it (#716
// slice 3, naut-redundancy's asks).
func TestFloatingIPFollowsTheGateOfARealPair(t *testing.T) {
	r := newBoxRigAPI(t)
	c, err := floatip.Parse("10.0.0.50/24", "eth0")
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]*countingOps{"a": {}, "b": {}}
	start := func(gate func() bool, o *countingOps) (*floatip.Holder, func()) {
		h := floatip.New(c, o)
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); h.Watch(ctx, gate, 2*time.Millisecond) }()
		return h, func() { cancel(); wg.Wait() }
	}
	// the electors are resolved once, here: the rig's node table is not safe
	// to read while it starts and stops boxes
	elA, elB := r.el("a"), r.el("b")
	_, stopA := start(elA.IsLeader, ops["a"])
	_, stopB := start(elB.IsLeader, ops["b"])
	t.Cleanup(stopB)
	stopAOnce := sync.OnceFunc(stopA)
	t.Cleanup(stopAOnce)
	// Sample both at once, all the time: two holders at one instant is the
	// overlap this feature must not create on an orderly handover.
	var both atomic.Int32
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() {
		for sctx.Err() == nil {
			if ops["a"].on.Load() && ops["b"].on.Load() {
				both.Add(1)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	r.handLeadTo("a")
	pairWait(t, "a to hold the address", 10*time.Second, func() bool { return ops["a"].on.Load() })
	if ops["b"].on.Load() {
		t.Error("the standby holds the address")
	}
	r.handLeadTo("b")
	pairWait(t, "b to hold the address", 10*time.Second, func() bool { return ops["b"].on.Load() && !ops["a"].on.Load() })
	if n := both.Load(); n != 0 {
		t.Errorf("both controllers held the address at once in %d samples across an orderly handover", n)
	}

	// A copy of a's name makes the real a a spare: its gate stays closed, so
	// it never takes the address, however long it waits.
	stopAOnce() // its node is about to go away
	r.nodes["a"].crash()
	r.cloneDiskEnv("a", "box-a")
	time.Sleep(2 * time.Second)
	r.restartOn("a", "box-a", r.env["a"], 0)
	spareOps := &countingOps{}
	spareHold, stopSpare := start(r.el("a").IsLeader, spareOps)
	t.Cleanup(stopSpare)
	pairWait(t, "the real a to go spare", 10*time.Second, func() bool { return r.el("a").Spare() })
	time.Sleep(500 * time.Millisecond)
	if spareOps.on.Load() || spareHold.Held() {
		t.Error("a spare holds the floating address")
	}
}

// recordingOps is a kernel that records the order of what it was asked.
type recordingOps struct {
	mu    sync.Mutex
	calls []string
	on    bool
}

func (o *recordingOps) rec(s string, on bool) {
	o.mu.Lock()
	o.calls = append(o.calls, s)
	o.on = on
	o.mu.Unlock()
}
func (o *recordingOps) Add(floatip.Config) error { o.rec("add", true); return nil }
func (o *recordingOps) Del(floatip.Config) error { o.rec("del", false); return nil }
func (o *recordingOps) Announce(floatip.Config) error {
	o.mu.Lock()
	o.calls = append(o.calls, "arp")
	o.mu.Unlock()
	return nil
}
func (o *recordingOps) state() (bool, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.on, append([]string(nil), o.calls...)
}

type gateFunc func() bool

func (g gateFunc) IsLeader() bool { return g() }

// startFloatingIP's stop ends the watcher and then drops the address: a
// leader that is stopping in order does not hand the lease over still holding
// it, and no tick can add it again behind the drop. Without the key, or
// outside a pair, nothing starts.
func TestStartFloatingIPStopDropsTheAddress(t *testing.T) {
	proj := &project.Project{Redundancy: &project.RedundancyConfig{
		Peers:      map[string]string{"a": "127.0.0.1:1", "b": "127.0.0.1:2"},
		FloatingIP: &project.FloatingIPConfig{Address: "10.0.0.50/24", Interface: "eth0"},
	}}
	ops := &recordingOps{}
	stop := startFloatingIP(proj, gateFunc(func() bool { return true }), ops)
	if stop == nil {
		t.Fatal("no floating IP started")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if on, _ := ops.state(); on {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the leader never took the address")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	on, calls := ops.state()
	if on || calls[len(calls)-1] != "del" {
		t.Errorf("after stop: present %v, calls %v: the address must be dropped last", on, calls)
	}
	time.Sleep(30 * time.Millisecond) // no tick may add it back behind the drop
	if on, _ := ops.state(); on {
		t.Error("the address came back after stop")
	}
	if startFloatingIP(&project.Project{}, gateFunc(func() bool { return true }), ops) != nil {
		t.Error("a floating IP started without redundancy")
	}
}
