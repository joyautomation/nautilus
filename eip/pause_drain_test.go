//go:build quorumtest

package eip

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// A planned switchover's drain on EtherNet/IP, through the driver's own
// write path (#710 d1b): a write that read the gate open stalls after the
// read or right before its system call; the gate closes behind it and
// fence.Drain runs. The write never reaches the controller, during the
// drain or after it: the drain waits for it, and it does not go out
// (not started during the drain, or its socket reset by it).
// (go test -tags quorumtest -run Pause ./eip/)
func TestEIPPauseAcrossCloseAndDrain(t *testing.T) {
	for _, pt := range []struct {
		name  string
		point fence.Point
	}{{"after the check", fence.AfterCheck}, {"right before the send", fence.RightBeforeSend}} {
		t.Run(pt.name, func(t *testing.T) {
			host, port, store := startEmulator(t)
			d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			var shut, armed atomic.Bool
			var once sync.Once
			opened := make(chan struct{})
			d.SetSendGate(func() (bool, time.Duration, uint64) {
				if shut.Load() {
					return false, 0, 1
				}
				if armed.Load() {
					once.Do(func() { close(opened) })
				}
				return true, 2 * time.Second, 1
			})
			d.Start(context.Background())
			t.Cleanup(d.Stop)
			waitFor(t, "connect", func() bool { return d.Health().Connected })

			fence.Pause(pt.point, 300*time.Millisecond)
			t.Cleanup(func() { fence.Pause(pt.point, 0) })
			armed.Store(true)
			if err := d.WriteOutputs(map[string]any{"SpeedCmd": 7.25}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-opened:
			case <-time.After(3 * time.Second):
				t.Fatal("the write never read the gate")
			}
			time.Sleep(30 * time.Millisecond) // inside the stall
			shut.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			start := time.Now()
			if err := fence.Drain(ctx, 0); err != nil {
				t.Fatalf("Drain: %v", err)
			}
			// Bounded by the stall, not the 2 s budget: what waits on the wire is cut.
			if took := time.Since(start); took > time.Second {
				t.Fatalf("the drain took %v; want it within the stall, not the send budget", took)
			}
			time.Sleep(600 * time.Millisecond)
			if _, v, _ := store.Resolve("SpeedCmd"); v == 7.25 {
				t.Fatal("the write reached the controller across the close and the drain")
			}
		})
	}
}
