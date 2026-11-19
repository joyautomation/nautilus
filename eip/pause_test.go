//go:build quorumtest

package eip

import (
	"context"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Test plan §3.4 on EtherNet/IP, through the driver's own write path: a
// pause right before the send, longer than the send's budget, and neither
// a scalar nor a struct member reaches the controller (each request runs
// fenced, so the socket refuses a write past its deadline); with the pause
// gone, both go out. A write path that sent on the plain context would
// land them during the pause. (go test -tags quorumtest ./eip/)
func TestEIPPauseBeforeSendPastTheBudget(t *testing.T) {
	cmd := ir.Value{Kind: ir.TypeStruct, Fld: []ir.Value{
		{Kind: ir.TypeStruct, Fld: []ir.Value{ir.RealVal(9.75), ir.BoolVal(true)}},
		ir.IntVal(99),
	}}
	// One write per case: a failed send resets the session and puts the
	// rest back, so a second tag in the same push would never be tried.
	for _, c := range []struct {
		name, tag, leaf string
		v               any
		want            any
	}{
		{"a scalar", "SpeedCmd", "SpeedCmd", 7.25, 7.25},
		{"a struct member", "TRSCmd", "TRSCmd.Count", cmd, 99.0},
	} {
		t.Run(c.name, func(t *testing.T) {
			host, port, store := startEmulator(t)
			d, err := New(host, testManifest(), WithPort(port), WithScanRate(30*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			d.SetSendGate(func() (bool, time.Duration) { return true, 150 * time.Millisecond })
			d.Start(context.Background())
			t.Cleanup(d.Stop)
			waitFor(t, "connect", func() bool { return d.Health().Connected })

			fence.Pause(fence.RightBeforeSend, 400*time.Millisecond)
			t.Cleanup(func() { fence.Pause(fence.RightBeforeSend, 0) })
			if err := d.WriteOutputs(map[string]any{c.tag: c.v}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1500 * time.Millisecond)
			if _, v, _ := store.Resolve(c.leaf); v == c.want {
				t.Fatalf("%s reached the controller after its send deadline", c.name)
			}
			fence.Pause(fence.RightBeforeSend, 0)
			waitFor(t, c.name+" once nothing stalls it", func() bool {
				_, v, _ := store.Resolve(c.leaf)
				return v == c.want
			})
		})
	}
}
