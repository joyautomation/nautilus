package eip

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// #710 d1b: on a pair an EtherNet/IP request is counted for a planned
// switchover's drain from its look at the gate until its cancel (called
// once the request has returned), and a look that does not send (gate
// closed, the lease's term moved, or goOn's look alone) leaves nothing
// counted, so a drain neither misses a request nor waits forever.
func TestEIPEachRequestIsCountedForTheDrain(t *testing.T) {
	d, err := New("127.0.0.1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	var term atomic.Uint64
	term.Store(5)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return open.Load(), time.Second, term.Load() })
	d.look()
	d.wmu.Lock()
	flushTerm := d.term
	d.wmu.Unlock()

	_, cancel, err := d.send(context.Background(), flushTerm)
	if err != nil {
		t.Fatal(err)
	}
	if n := fence.InFlight(); n < 1 {
		t.Fatal("nothing in flight during a request; want it counted")
	}
	cancel()
	drainSettles(t, "after the request's cancel")
	if err := d.goOn(flushTerm); err != nil {
		t.Fatalf("goOn: %v", err)
	}
	drainSettles(t, "after goOn")
	open.Store(false)
	if _, _, err := d.send(context.Background(), flushTerm); err == nil {
		t.Fatal("a closed gate: the request was not refused")
	}
	drainSettles(t, "after a closed gate's refusal")
	open.Store(true)
	term.Store(9)
	if _, _, err := d.send(context.Background(), flushTerm); err == nil {
		t.Fatal("the term moved: the request was not refused")
	}
	drainSettles(t, "after the term moved")
}

// drainSettles waits for nothing to be counted in flight: exact counts
// would catch a send of a driver an earlier test left running.
func drainSettles(t *testing.T, what string) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); fence.InFlight() != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: %d still counted in flight; a drain would never end", what, fence.InFlight())
		}
	}
}
