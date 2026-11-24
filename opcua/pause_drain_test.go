//go:build quorumtest

package opcua

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	nio "github.com/joyautomation/nautilus/io"
)

// A planned switchover's drain on the OPC UA client (#710 d1b): a write
// chunk that read the gate open stalls after the read or right before its
// system call; the gate closes behind it and fence.Drain runs. The write
// never reaches the server: the drain waits for it, and it does not go out
// (not started during the drain, or aborted in place by it).
// (go test -tags quorumtest -run Pause ./opcua/)
func TestOPCUAPauseAcrossCloseAndDrain(t *testing.T) {
	for _, pt := range []struct {
		name  string
		point fence.Point
	}{{"after the check", fence.AfterCheck}, {"right before the send", fence.RightBeforeSend}} {
		t.Run(pt.name, func(t *testing.T) {
			ts := startServer(t, freePort(t))
			d := startDriver(t, ts.url, manifest(2))
			d.SetWriteGate(func() bool { return true })
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
			waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
			_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
			waitFor(t, "the baseline", func() bool { return ts.get(t, "Setpoint") == 0.0 })
			time.Sleep(100 * time.Millisecond)

			fence.Pause(pt.point, 300*time.Millisecond)
			t.Cleanup(func() { fence.Pause(pt.point, 0) })
			armed.Store(true)
			_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0})
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
			fence.Pause(pt.point, 0)
			time.Sleep(600 * time.Millisecond)
			if got := ts.get(t, "Setpoint"); got == 5.0 {
				t.Fatal("the write reached the server across the close and the drain")
			}
		})
	}
}
