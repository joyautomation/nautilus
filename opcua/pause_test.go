//go:build quorumtest

package opcua

import (
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	nio "github.com/joyautomation/nautilus/io"
)

// #1047 review (4), the deadline timer: a send stalled right before its
// system call past its budget (test plan §3.4, point 3) has the session's
// socket reset BY THE TIMER at the deadline (fence.ArmAbort), not only
// after the write comes back. ArmAbort(nil, ...) turns it red.
// (go test -tags quorumtest -run Pause ./opcua/)
func TestOPCUAPauseBeforeSendTheTimerResets(t *testing.T) {
	ts := startServer(t, freePort(t))
	d := startPaired(t, ts.url, manifest(2), func() (bool, time.Duration, uint64) { return true, 300 * time.Millisecond, 1 })
	waitFor(t, "first poll", func() bool { _, ok := read(t, d)["Speed"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 0.0}) // baseline
	fence.Pause(fence.RightBeforeSend, 600*time.Millisecond)
	t.Cleanup(func() { fence.Pause(fence.RightBeforeSend, 0) })
	_ = d.WriteOutputs(nio.Values{"Setpoint": 5.0})
	waitFor(t, "the timer's reset at the deadline", func() bool { return d.timerAborts.Load() > 0 })
	fence.Pause(fence.RightBeforeSend, 0)
	if got := ts.get(t, "Setpoint"); got != 0.0 {
		t.Fatalf("Setpoint = %v; a write stalled past its deadline still landed", got)
	}
}
