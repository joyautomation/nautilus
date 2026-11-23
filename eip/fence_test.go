package eip

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// Design §3.4 on EtherNet/IP: every write request (a scalar, each member
// of a struct) looks at the gate itself and runs fenced, with the gate's
// budget as its deadline; closed, or with no budget left, it is not sent.
// send_no_deadline turns it red.
func TestEIPEachRequestRunsToItsSendDeadline(t *testing.T) {
	d, err := New("127.0.0.1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, 300 * time.Millisecond, 1 })
	start := time.Now()
	ctx, cancel, err := d.send(context.Background(), d.term)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok || !fence.Fenced(ctx) || dl.Before(start.Add(300*time.Millisecond)) || dl.After(time.Now().Add(300*time.Millisecond)) {
		t.Fatalf("deadline %v (%v), fenced %v; want fenced, 300 ms from the look", dl, ok, fence.Fenced(ctx))
	}
	for _, g := range []struct {
		open   bool
		budget time.Duration
	}{{false, time.Second}, {true, 0}} {
		d.SetSendGate(func() (bool, time.Duration, uint64) { return g.open, g.budget, 1 })
		if _, _, err := d.send(context.Background(), d.term); !errors.Is(err, errGateClosed) {
			t.Errorf("gate %v with %s left: %v; want errGateClosed (not sent)", g.open, g.budget, err)
		}
	}
}

// #710: a request's context carries the driver's name for the writes-cut
// counter, so the transport that resets its socket at the deadline can
// record the cut.
func TestEIPSendNamesTheDeviceForTheCutCounter(t *testing.T) {
	d, err := New("127.0.0.1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	d.SetCutLabel("plc7", false)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, 1 })
	ctx, cancel, err := d.send(context.Background(), d.term)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	fence.ResetCutsForTest()
	fence.CutSend(ctx, true)
	if got := fence.CutCounts(); len(got) != 1 || got["plc7"] != 1 {
		t.Fatalf("writes cut = %v, want plc7 1", got)
	}
}

// #1040 review: a flush that took its queue under one term of the lease
// sends none of it once the term has moved, even when no look saw the gate
// closed in between (lost and won again between two looks).
func TestEIPSendStopsWhenTheLeaseTermMoved(t *testing.T) {
	d, err := New("127.0.0.1", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	var term atomic.Uint64
	term.Store(5)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, time.Second, term.Load() })
	d.look() // term 5 seen
	d.wmu.Lock()
	flushTerm := d.term // what a flush records as it takes its queue
	d.wmu.Unlock()
	term.Store(9)
	if _, _, err := d.send(context.Background(), flushTerm); !errors.Is(err, errNewTerm) {
		t.Fatalf("send after the lease moved from term 5 to 9: %v; want errNewTerm (the queue is the previous leadership's)", err)
	}
}
