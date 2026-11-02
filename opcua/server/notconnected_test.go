package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
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

// A local-I/O input (plcnext, optommp: not runtime.Comms) is not seeded, so
// the scan faults loudly on a read before its first delivery. Its node is
// there all the same, from the start, typed as declared, with no value and
// Bad_NotConnected; a subscriber hears it go Good when the value arrives.
// It had no node at all until then, and a client that browsed at connect
// never saw it (#420). An input nothing types has no shape to show and
// stays absent.
func TestNeverDeliveredLocalInputHasANode(t *testing.T) {
	mem := nio.NewMemory()
	pump := runtime.Input("P1")
	pump.Type = "Pump"
	rt, err := runtime.New(runtime.Options{
		Program: `TYPE Pump : STRUCT Run : BOOL; Amps : REAL; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL Level : REAL; Out : REAL; END_VAR
Out := Level;
END_PROGRAM`,
		Driver: mem,
		Scan:   20 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.Input("Level"), // typed by the program
			pump,                   // typed by type:
			runtime.Input("Loose"), // typed by nothing
			runtime.State("Out", 0.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, c := startServer(t, rt)
	// The loud fault is the runtime's, and stays: a scan that reads Level
	// before its first delivery errors.
	rt.Scan()
	if st := rt.Stats(); st.LogicErrors == 0 {
		t.Fatal("a scan reading an undelivered local input did not fault")
	}

	folder := browse(t, c, ua.NewNumericNodeID(s.ns.id, folderID))
	for _, name := range []string{"Level", "P1"} {
		if folder[name] == nil {
			t.Errorf("no %s under the project folder before its first delivery", name)
		}
	}
	if folder["Loose"] != nil {
		t.Error("an input nothing types has a node")
	}
	if dv := read(t, c, s.NodeID("Loose"), ua.AttributeIDValue); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("Loose: %s, want Bad_NodeIdUnknown", dv.Status)
	}

	dv := read(t, c, s.NodeID("Level"), ua.AttributeIDValue)
	if dv.Status != ua.StatusBadNotConnected || hasValue(dv) {
		t.Errorf("Level before delivery: %s, value %v; want Bad_NotConnected and no value", dv.Status, dv.Value)
	}
	if dt := read(t, c, s.NodeID("Level"), ua.AttributeIDDataType); !hasValue(dt) || dt.Value.NodeID() == nil || dt.Value.NodeID().IntID() != id.Double {
		t.Errorf("Level DataType %v, want Double", dt.Value)
	}
	if nc := read(t, c, s.NodeID("Level"), ua.AttributeIDNodeClass); !hasValue(nc) || nc.Value.Value() != int32(ua.NodeClassVariable) {
		t.Errorf("Level NodeClass %v, want Variable", nc.Value)
	}
	members := browse(t, c, s.NodeID("P1"))
	if members["Run"] == nil || members["Amps"] == nil {
		t.Errorf("P1's members before delivery: %v, want Run and Amps", keys(members))
	}
	if dv := read(t, c, s.NodeID("P1.Amps"), ua.AttributeIDValue); dv.Status != ua.StatusBadNotConnected || hasValue(dv) {
		t.Errorf("P1.Amps before delivery: %s, value %v", dv.Status, dv.Value)
	}

	// A subscriber hears Bad_NotConnected first, then the delivered value.
	ctx := context.Background()
	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1))
	if err != nil || resp.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("monitor Level before delivery: %v %v", err, resp)
	}
	first := next(t, notify)
	if first.Status != ua.StatusBadNotConnected || hasValue(first) {
		t.Errorf("first notification %s, want Bad_NotConnected", first.Status)
	}

	if err := mem.WriteOutputs(nio.Values{"Level": 4.5}); err != nil {
		t.Fatal(err)
	}
	rt.Scan()
	got := next(t, notify)
	if got.Status != ua.StatusGood || got.Value == nil || got.Value.Value() != 4.5 {
		t.Errorf("after delivery: %s %v, want Good 4.5", got.Status, got.Value)
	}
	if dv := read(t, c, s.NodeID("Level"), ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != 4.5 {
		t.Errorf("Level after delivery: %s %v", dv.Status, dv.Value)
	}
}

// next is the next value notification on notify, within two seconds.
func next(t *testing.T, notify <-chan *opcua.PublishNotificationData) *ua.DataValue {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case n := <-notify:
			if dc, ok := n.Value.(*ua.DataChangeNotification); ok && len(dc.MonitoredItems) > 0 {
				return dc.MonitoredItems[len(dc.MonitoredItems)-1].Value
			}
		case <-deadline:
			t.Fatal("no notification in 2 s")
		}
	}
}

// hasValue says whether dv carries a value; gopcua decodes an absent one as
// an empty Variant.
func hasValue(dv *ua.DataValue) bool {
	return dv.EncodingMask&ua.DataValueValue != 0 && dv.Value != nil && dv.Value.Value() != nil
}
