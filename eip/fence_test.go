package eip

import (
	"context"
	"errors"
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
	d.SetSendGate(func() (bool, time.Duration) { return true, 300 * time.Millisecond })
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
		d.SetSendGate(func() (bool, time.Duration) { return g.open, g.budget })
		if _, _, err := d.send(context.Background(), d.term); !errors.Is(err, errGateClosed) {
			t.Errorf("gate %v with %s left: %v; want errGateClosed (not sent)", g.open, g.budget, err)
		}
	}
}
