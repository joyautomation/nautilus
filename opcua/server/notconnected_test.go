package server

import (
	"testing"
	"time"

	"github.com/gopcua/opcua/ua"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// A protocol input that has never been delivered is seeded in the tag store
// (runtime.TagDef.Comms), so it has a node from the start — its zero, with
// the driver's NotConnected as Bad_NotConnected — where an unseeded input
// has none until its first value (#420).
func TestNeverDeliveredProtocolInputReadsBadNotConnected(t *testing.T) {
	mem := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL Flow : REAL; Out : REAL; END_VAR
Out := Flow;
END_PROGRAM`,
		Driver: mem,
		Scan:   20 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Input("Flow", runtime.Comms()),
			runtime.State("Out", 0.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mem.SetQuality("Flow", nio.NotConnected)
	s, c := startServer(t, rt)
	dv := read(t, c, s.NodeID("Flow"), ua.AttributeIDValue)
	if dv.Status != ua.StatusBadNotConnected {
		t.Errorf("Flow status %s, want Bad_NotConnected", dv.Status)
	}
	if v, ok := dv.Value.Value().(float64); !ok || v != 0 {
		t.Errorf("Flow value %v (%T), want the seeded 0", dv.Value.Value(), dv.Value.Value())
	}
}
