package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// The fixture covers every shape the namespace has a rule for: scalars of
// each kind (and a declared width), an enumeration, a UDT with a nested
// UDT and an array member, arrays of scalars, an array of UDTs, and a
// function-block instance.
const typesST = `
TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
TYPE Drive :
  STRUCT
    Speed : REAL;
    Amps  : REAL;
  END_STRUCT
END_TYPE
TYPE Pump :
  STRUCT
    Running : BOOL;
    Drive   : Drive;
    Hours   : ARRAY[1..3] OF DINT;
  END_STRUCT
END_TYPE
`

const programST = `
PROGRAM Main
VAR_EXTERNAL
  Running : BOOL;
  Count   : DINT;
  Level   : REAL;
  Label   : STRING;
  Elapsed : TIME;
  State   : Mode;
  P101    : Pump;
  Levels  : ARRAY[0..2] OF REAL;
  Pumps   : ARRAY[1..2] OF Pump;
END_VAR
VAR
  T1 : TON;
END_VAR
  T1(IN := Running, PT := T#5s);
END_PROGRAM
`

func newRuntime(t *testing.T) (*runtime.Runtime, *nio.Memory) {
	t.Helper()
	mem := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program:   programST,
		Libraries: []string{typesST},
		Driver:    mem,
		Scan:      20 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("Running", true),
			runtime.State("Count", int64(42)),
			runtime.State("Level", 3.5, runtime.Desc("Wet well level"), runtime.Unit("ft")),
			runtime.State("Label", "Pump 1"),
			runtime.Typed("Elapsed", runtime.RoleState, "TIME"),
			runtime.Typed("State", runtime.RoleState, "Mode"),
			runtime.Typed("P101", runtime.RoleState, "Pump"),
			runtime.State("Levels", []any{1.0, 2.0, 3.0}),
			runtime.Typed("Pumps", runtime.RoleState, "ARRAY[1..2] OF Pump"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt, mem
}

func startServer(t *testing.T, rt *runtime.Runtime) (*Server, *opcua.Client) {
	t.Helper()
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "lift-station", Sweep: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect %s: %v", s.URL(), err)
	}
	t.Cleanup(func() { c.Close(ctx) })
	return s, c
}

func browse(t *testing.T, c *opcua.Client, nid *ua.NodeID) map[string]*ua.ReferenceDescription {
	t.Helper()
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          nid,
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences),
			IncludeSubtypes: true,
			NodeClassMask:   uint32(ua.NodeClassAll),
			ResultMask:      uint32(ua.BrowseResultMaskAll),
		}},
	})
	if err != nil {
		t.Fatalf("browse %s: %v", nid, err)
	}
	if st := resp.Results[0].StatusCode; st != ua.StatusGood {
		t.Fatalf("browse %s: %s", nid, st)
	}
	out := map[string]*ua.ReferenceDescription{}
	for _, r := range resp.Results[0].References {
		out[r.BrowseName.Name] = r
	}
	return out
}

func read(t *testing.T, c *opcua.Client, nid *ua.NodeID, attr ua.AttributeID) *ua.DataValue {
	t.Helper()
	resp, err := c.Read(context.Background(), &ua.ReadRequest{
		NodesToRead:        []*ua.ReadValueID{{NodeID: nid, AttributeID: attr}},
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	})
	if err != nil {
		t.Fatalf("read %s: %v", nid, err)
	}
	return resp.Results[0]
}

func TestBrowseWalksTheStore(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)

	objects := browse(t, c, ua.NewNumericNodeID(0, id.ObjectsFolder))
	folder, ok := objects["lift-station"]
	if !ok {
		t.Fatalf("Objects has no lift-station folder: %v", keys(objects))
	}
	if folder.NodeClass != ua.NodeClassObject {
		t.Errorf("folder class %v", folder.NodeClass)
	}

	tags := browse(t, c, folder.NodeID.NodeID)
	for _, want := range []string{"Running", "Count", "Level", "Label", "Elapsed", "State", "P101", "Levels", "Pumps"} {
		if _, ok := tags[want]; !ok {
			t.Errorf("folder lacks %s: %v", want, keys(tags))
		}
	}
	for name, class := range map[string]ua.NodeClass{"Level": ua.NodeClassVariable, "Levels": ua.NodeClassVariable, "P101": ua.NodeClassObject, "Pumps": ua.NodeClassObject} {
		if got := tags[name].NodeClass; got != class {
			t.Errorf("%s class %v, want %v", name, got, class)
		}
	}
	if got := tags["P101"].NodeID.NodeID.String(); got != s.NodeID("P101").String() {
		t.Errorf("P101 node id %s", got)
	}

	// A UDT's members are child nodes, nested UDTs included.
	p101 := browse(t, c, s.NodeID("P101"))
	if len(p101) != 3 || p101["Drive"] == nil || p101["Hours"] == nil || p101["Running"] == nil {
		t.Fatalf("P101 members %v", keys(p101))
	}
	if p101["Drive"].NodeClass != ua.NodeClassObject || p101["Hours"].NodeClass != ua.NodeClassVariable {
		t.Errorf("Drive is %v, Hours is %v", p101["Drive"].NodeClass, p101["Hours"].NodeClass)
	}
	drive := browse(t, c, p101["Drive"].NodeID.NodeID)
	if drive["Speed"] == nil || drive["Speed"].NodeID.NodeID.StringID() != "P101.Drive.Speed" {
		t.Errorf("P101.Drive members %v", keys(drive))
	}

	// An array of UDTs is an object with one child per element, at the
	// declared lower bound.
	pumps := browse(t, c, s.NodeID("Pumps"))
	if pumps["Pumps[1]"] == nil || pumps["Pumps[2]"] == nil || pumps["Pumps[0]"] != nil {
		t.Errorf("Pumps elements %v", keys(pumps))
	}
	if el := browse(t, c, s.NodeID("Pumps[2]")); el["Drive"] == nil {
		t.Errorf("Pumps[2] members %v", keys(el))
	}

	// An unknown node is an error, not an empty folder.
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{{
		NodeID: s.NodeID("Nope"), BrowseDirection: ua.BrowseDirectionForward, ResultMask: uint32(ua.BrowseResultMaskAll)}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].StatusCode != ua.StatusBadNodeIDUnknown {
		t.Errorf("browse of an unknown node: %s", resp.Results[0].StatusCode)
	}
}

func TestReadsCarryTypesQualityAndTimestamps(t *testing.T) {
	rt, mem := newRuntime(t)
	s, c := startServer(t, rt)
	rt.Tags().Set("Elapsed", ir.TimeVal(1500))
	rt.Tags().Set("State", "Run")
	if err := rt.Tags().SetPath("P101.Drive.Speed", 1750.5); err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().SetPath("P101.Hours[2]", 7); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		addr     string
		dataType uint32
		value    any
	}{
		{"Running", id.Boolean, true},
		{"Count", id.Int32, int32(42)},
		{"Level", id.Double, 3.5},
		{"Label", id.String, "Pump 1"},
		{"Elapsed", id.Duration, 1500.0},
		{"State", id.Int32, int32(10)},
		{"P101.Drive.Speed", id.Double, 1750.5},
		{"P101.Running", id.Boolean, false},
		{"p101.drive.speed", id.Double, 1750.5}, // IEC names are case-insensitive
	}
	for _, tc := range cases {
		dv := read(t, c, s.NodeID(tc.addr), ua.AttributeIDValue)
		if dv.Status != ua.StatusGood {
			t.Errorf("%s: status %s", tc.addr, dv.Status)
			continue
		}
		if got := dv.Value.Value(); got != tc.value {
			t.Errorf("%s = %v (%T), want %v (%T)", tc.addr, got, got, tc.value, tc.value)
		}
		dt := read(t, c, s.NodeID(tc.addr), ua.AttributeIDDataType)
		if got := dt.Value.Value().(*ua.NodeID).IntID(); got != tc.dataType {
			t.Errorf("%s: DataType i=%d, want i=%d", tc.addr, got, tc.dataType)
		}
		if dv.SourceTimestamp.IsZero() || dv.ServerTimestamp.IsZero() {
			t.Errorf("%s: timestamps %v / %v", tc.addr, dv.SourceTimestamp, dv.ServerTimestamp)
		}
	}

	// Arrays of scalars are OPC UA arrays of the element type.
	levels := read(t, c, s.NodeID("Levels"), ua.AttributeIDValue)
	if got, ok := levels.Value.Value().([]float64); !ok || len(got) != 3 || got[2] != 3.0 {
		t.Errorf("Levels = %v (%T)", levels.Value.Value(), levels.Value.Value())
	}
	if rank := read(t, c, s.NodeID("Levels"), ua.AttributeIDValueRank).Value.Value(); rank != int32(1) {
		t.Errorf("Levels ValueRank %v", rank)
	}
	hours := read(t, c, s.NodeID("P101.Hours"), ua.AttributeIDValue)
	if got, ok := hours.Value.Value().([]int32); !ok || len(got) != 3 || got[1] != 7 {
		t.Errorf("P101.Hours = %v (%T)", hours.Value.Value(), hours.Value.Value())
	}

	// Description carries the tag model's desc and unit.
	desc := read(t, c, s.NodeID("Level"), ua.AttributeIDDescription).Value.Value().(*ua.LocalizedText)
	if desc.Text != "Wet well level [ft]" {
		t.Errorf("Level description %q", desc.Text)
	}
	// Read-only, and says so.
	if al := read(t, c, s.NodeID("Level"), ua.AttributeIDAccessLevel).Value.Value(); al != uint8(ua.AccessLevelTypeCurrentRead) {
		t.Errorf("AccessLevel %v", al)
	}
	// An object has no Value.
	if st := read(t, c, s.NodeID("P101"), ua.AttributeIDValue).Status; st != ua.StatusBadAttributeIDInvalid {
		t.Errorf("P101 value: %s", st)
	}
	if st := read(t, c, s.NodeID("Nope"), ua.AttributeIDValue).Status; st != ua.StatusBadNodeIDUnknown {
		t.Errorf("Nope value: %s", st)
	}

	// Quality rides as the status code: a stale source is Uncertain, a
	// bad one Bad, a never-delivered one Bad_NotConnected — and a member
	// is as good as its tag.
	mem.SetQuality("Level", nio.Stale)
	mem.SetQuality("Count", nio.Bad)
	mem.SetQuality("P101", nio.NotConnected)
	for addr, want := range map[string]ua.StatusCode{
		"Level":            ua.StatusUncertainLastUsableValue,
		"Count":            ua.StatusBadDeviceFailure,
		"P101.Drive.Speed": ua.StatusBadNotConnected,
		"Label":            ua.StatusGood,
	} {
		if got := read(t, c, s.NodeID(addr), ua.AttributeIDValue).Status; got != want {
			t.Errorf("%s status %s, want %s", addr, got, want)
		}
	}

	// SourceTimestamp moves when the value does, and not otherwise.
	first := read(t, c, s.NodeID("Label"), ua.AttributeIDValue).SourceTimestamp
	time.Sleep(30 * time.Millisecond)
	if again := read(t, c, s.NodeID("Label"), ua.AttributeIDValue).SourceTimestamp; !again.Equal(first) {
		t.Errorf("unchanged Label restamped %v → %v", first, again)
	}
	rt.Tags().Set("Label", "Pump 2")
	time.Sleep(30 * time.Millisecond)
	if later := read(t, c, s.NodeID("Label"), ua.AttributeIDValue).SourceTimestamp; !later.After(first) {
		t.Errorf("changed Label kept its stamp %v", later)
	}
}

func TestSubscriptionHearsChanges(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)
	ctx := context.Background()

	notify := make(chan *opcua.PublishNotificationData, 16)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 50 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("Level"), ua.AttributeIDValue, 1),
		opcua.NewMonitoredItemCreateRequestWithDefaults(s.NodeID("P101.Drive.Speed"), ua.AttributeIDValue, 2),
	)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range res.Results {
		if r.StatusCode != ua.StatusGood {
			t.Fatalf("monitor item %d: %s", i, r.StatusCode)
		}
	}

	rt.Tags().Set("Level", ir.RealVal(9.25))
	if err := rt.Tags().SetPath("P101.Drive.Speed", 60.0); err != nil {
		t.Fatal(err)
	}
	got := map[uint32]any{}
	deadline := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case n := <-notify:
			if n.Error != nil {
				t.Fatal(n.Error)
			}
			dcn, ok := n.Value.(*ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range dcn.MonitoredItems {
				got[item.ClientHandle] = item.Value.Value.Value()
				if item.Value.Status != ua.StatusGood {
					t.Errorf("handle %d status %s", item.ClientHandle, item.Value.Status)
				}
			}
		case <-deadline:
			t.Fatalf("no data change after 5s; got %v", got)
		}
	}
	if got[1] != 9.25 || got[2] != 60.0 {
		t.Errorf("notified %v", got)
	}
	if st := s.Status(); st.Subscriptions != 1 || st.MonitoredItems != 2 || !st.Listening {
		t.Errorf("status %+v", st)
	}
}

func TestAnonymousWriteIsRefused(t *testing.T) {
	rt, _ := newRuntime(t)
	s, c := startServer(t, rt)

	resp, err := c.Write(context.Background(), &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{
		{NodeID: s.NodeID("Level"), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(99.0)}},
		{NodeID: s.NodeID("Level"), AttributeID: ua.AttributeIDDescription, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(text("x"))}},
		{NodeID: s.NodeID("Nope"), AttributeID: ua.AttributeIDValue, Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(1.0)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []ua.StatusCode{ua.StatusBadUserAccessDenied, ua.StatusBadNotWritable, ua.StatusBadNodeIDUnknown}
	for i, w := range want {
		if resp.Results[i] != w {
			t.Errorf("write %d: %s, want %s", i, resp.Results[i], w)
		}
	}
	if got := rt.Tags().Real("Level"); got != 3.5 {
		t.Errorf("the refused write landed: Level = %v", got)
	}
}

func TestParseEndpoint(t *testing.T) {
	good := map[string][2]any{
		"opc.tcp://localhost:4840": {"localhost", 4840},
		"0.0.0.0:4840":             {"0.0.0.0", 4840},
		"opc.tcp://[::1]:4841":     {"::1", 4841},
	}
	for ep, want := range good {
		h, p, err := ParseEndpoint(ep)
		if err != nil || h != want[0] || p != want[1] {
			t.Errorf("%s → %q %d %v", ep, h, p, err)
		}
	}
	for _, ep := range []string{"localhost", "http://localhost:4840", "opc.tcp://localhost:4840/path", ":4840", "localhost:70000", "localhost:abc"} {
		if _, _, err := ParseEndpoint(ep); err == nil || !strings.Contains(err.Error(), "opcua.endpoint") {
			t.Errorf("%s accepted (%v)", ep, err)
		}
	}
}

func keys(m map[string]*ua.ReferenceDescription) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
