package server

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/internal/gopcuatest"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// A device-shaped address space over a plant's tags: a string-id tree in
// ns=1 (P+F's shape) and numeric ids in ns=3 (SICK's), ns=2 a namespace
// the device lists and the map does not use.
func mappedFixture(t *testing.T) (*runtime.Runtime, *Server, *opcua.Client) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Scan:    20 * time.Millisecond,
		Tags: []runtime.TagDef{
			runtime.State("M_Port1_SioIn", false),
			runtime.State("M_Port1_Pdi", int64(0)),
			runtime.Setpoint("M_Port1_Pdo", int64(0)),
			runtime.State("M_Port1_Status", "OPERATE"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &Mapped{
		Namespaces: []string{"urn:test:iolm", "urn:test:unused", "urn:test:numeric"},
		Product:    &Product{Manufacturer: "Test Maker", Name: "Test IOLM", URI: "urn:test:iolm:product", Version: "1.2.3"},
		Nodes: []MappedNode{
			{NodeID: "ns=1;s=IOLM", Name: "IOLM"},
			{NodeID: "ns=1;s=IOLM/Port 1", Parent: "ns=1;s=IOLM", Name: "Port 1"},
			{NodeID: "ns=1;s=IOLM/Port 1/SIO Input", Parent: "ns=1;s=IOLM/Port 1", Name: "SIO Input", Tag: "M_Port1_SioIn", Desc: "SIO input state"},
			{NodeID: "ns=1;s=IOLM/Port 1/Status", Parent: "ns=1;s=IOLM/Port 1", Name: "Status", Tag: "M_Port1_Status"},
			{NodeID: "ns=1;s=IOLM/Port 1/PDO", Parent: "ns=1;s=IOLM/Port 1", Name: "PDO", Tag: "M_Port1_Pdo"},
			{NodeID: "ns=3;i=8012", Parent: "ns=1;s=IOLM/Port 1", Name: "Pin4", Tag: "M_Port1_Pdi"},
		},
	}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "virtual", Sweep: 20 * time.Millisecond,
		Mapped: m, Writable: []string{"M_Port1_Pdo"}, AnonymousWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	go rt.Run(ctx)
	c, err := opcua.NewClient(s.URL(), opcua.SecurityMode(ua.MessageSecurityModeNone), opcua.AuthAnonymous(), opcua.AutoReconnect(false))
	if err != nil {
		t.Fatal(err)
	}
	if err := gopcuatest.Connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		c.Close(cctx)
	})
	return rt, s, c
}

func readNode(t *testing.T, c *opcua.Client, nid string, attr ua.AttributeID) *ua.DataValue {
	t.Helper()
	resp, err := c.Read(context.Background(), &ua.ReadRequest{NodesToRead: []*ua.ReadValueID{{NodeID: ua.MustParseNodeID(nid), AttributeID: attr}}})
	if err != nil {
		t.Fatalf("read %s: %v", nid, err)
	}
	return resp.Results[0]
}

func TestMappedAddressSpace(t *testing.T) {
	rt, _, c := mappedFixture(t)
	ctx := context.Background()

	// The namespace array: the device's, at its indices, then ours.
	ns := readNode(t, c, "i=2255", ua.AttributeIDValue)
	uris, _ := ns.Value.Value().([]string)
	if len(uris) < 5 || uris[1] != "urn:test:iolm" || uris[2] != "urn:test:unused" || uris[3] != "urn:test:numeric" {
		t.Fatalf("NamespaceArray = %v", uris)
	}

	// Objects organizes the device's tree, not the project folder.
	objs := browse(t, c, ua.NewNumericNodeID(0, id.ObjectsFolder))
	if _, ok := objs["IOLM"]; !ok {
		t.Fatalf("Objects does not organize IOLM: %v", keys(objs))
	}
	if _, ok := objs["virtual"]; ok {
		t.Error("the project folder is still under Objects")
	}
	port := browse(t, c, ua.MustParseNodeID("ns=1;s=IOLM/Port 1"))
	for _, want := range []string{"SIO Input", "Status", "PDO", "Pin4"} {
		if _, ok := port[want]; !ok {
			t.Errorf("Port 1 has no %s: %v", want, keys(port))
		}
	}
	if r := port["Pin4"]; r != nil && r.NodeID.NodeID.String() != "ns=3;i=8012" {
		t.Errorf("Pin4 is %s, want ns=3;i=8012", r.NodeID.NodeID)
	}

	// A browse path resolves to the device's node id.
	tr, err := send[*ua.TranslateBrowsePathsToNodeIDsResponse](t, c, &ua.TranslateBrowsePathsToNodeIDsRequest{RequestHeader: &ua.RequestHeader{}, BrowsePaths: []*ua.BrowsePath{{
		StartingNode: ua.NewNumericNodeID(0, id.ObjectsFolder),
		RelativePath: &ua.RelativePath{Elements: []*ua.RelativePathElement{
			{TargetName: &ua.QualifiedName{NamespaceIndex: 1, Name: "IOLM"}, ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true},
			{TargetName: &ua.QualifiedName{NamespaceIndex: 1, Name: "Port 1"}, ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true},
			{TargetName: &ua.QualifiedName{NamespaceIndex: 1, Name: "SIO Input"}, ReferenceTypeID: ua.NewNumericNodeID(0, id.HierarchicalReferences), IncludeSubtypes: true},
		}},
	}}})
	if tr == nil || tr.Results[0].StatusCode != ua.StatusGood || len(tr.Results[0].Targets) != 1 ||
		tr.Results[0].Targets[0].TargetID.NodeID.String() != "ns=1;s=IOLM/Port 1/SIO Input" {
		t.Fatalf("translate: %v %+v", err, tr)
	}

	// Values, names and types come from the tag; names from the map.
	if dv := readNode(t, c, "ns=1;s=IOLM/Port 1/Status", ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != "OPERATE" {
		t.Errorf("Status = %v %v", dv.Value.Value(), dv.Status)
	}
	if dv := readNode(t, c, "ns=3;i=8012", ua.AttributeIDBrowseName); dv.Value.Value().(*ua.QualifiedName).Name != "Pin4" {
		t.Errorf("BrowseName = %v", dv.Value.Value())
	}
	if dv := readNode(t, c, "ns=1;s=IOLM/Port 1/SIO Input", ua.AttributeIDDescription); dv.Value.Value().(*ua.LocalizedText).Text != "SIO input state" {
		t.Errorf("Description = %v", dv.Value.Value())
	}
	if dv := readNode(t, c, "ns=1;s=IOLM", ua.AttributeIDNodeClass); dv.Value.Value() != int32(ua.NodeClassObject) {
		t.Errorf("IOLM NodeClass = %v", dv.Value.Value())
	}
	if dv := readNode(t, c, "ns=1;s=IOLM/Port 1/PDO", ua.AttributeIDAccessLevel); dv.Value.Value() != uint8(3) {
		t.Errorf("PDO AccessLevel = %v, want 3", dv.Value.Value())
	}
	if dv := readNode(t, c, "ns=1;s=IOLM/Port 1/Status", ua.AttributeIDAccessLevel); dv.Value.Value() != uint8(1) {
		t.Errorf("Status AccessLevel = %v, want 1", dv.Value.Value())
	}
	if dv := readNode(t, c, "ns=1;s=IOLM/Nope", ua.AttributeIDValue); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("unknown node: %s", dv.Status)
	}

	// The device introduces itself as the device.
	if dv := readNode(t, c, ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ProductName).String(), ua.AttributeIDValue); dv.Value.Value() != "Test IOLM" {
		t.Errorf("ProductName = %v", dv.Value.Value())
	}
	if dv := readNode(t, c, ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ManufacturerName).String(), ua.AttributeIDValue); dv.Value.Value() != "Test Maker" {
		t.Errorf("ManufacturerName = %v", dv.Value.Value())
	}

	// A write to the writable node lands in the tag; the read-only one is refused.
	w, err := c.Write(ctx, &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{
		{NodeID: ua.MustParseNodeID("ns=1;s=IOLM/Port 1/PDO"), AttributeID: ua.AttributeIDValue,
			Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(int32(7))}},
		{NodeID: ua.MustParseNodeID("ns=1;s=IOLM/Port 1/Status"), AttributeID: ua.AttributeIDValue,
			Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant("X")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Results[0] != ua.StatusGood {
		t.Errorf("write PDO: %s", w.Results[0])
	}
	if w.Results[1] != ua.StatusBadNotWritable {
		t.Errorf("write Status: %s, want BadNotWritable", w.Results[1])
	}
	if v, _ := rt.Tags().ReadPath("M_Port1_Pdo"); v != int64(7) {
		t.Errorf("M_Port1_Pdo = %v after the write", v)
	}

	// A change in the plant reaches a monitored mapped node.
	notify := make(chan *opcua.PublishNotificationData, 64)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 20 * time.Millisecond}, notify)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel(ctx)
	mr, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth,
		opcua.NewMonitoredItemCreateRequestWithDefaults(ua.MustParseNodeID("ns=3;i=8012"), ua.AttributeIDValue, 1))
	if err != nil || mr.Results[0].StatusCode != ua.StatusGood {
		t.Fatalf("monitor: %v %+v", err, mr)
	}
	rt.Tags().Set("M_Port1_Pdi", int64(286))
	deadline := time.After(3 * time.Second)
	for {
		select {
		case n := <-notify:
			if dc, ok := n.Value.(*ua.DataChangeNotification); ok {
				for _, it := range dc.MonitoredItems {
					if it.Value.Value != nil && it.Value.Value.Value() == int64(286) {
						return
					}
				}
			}
		case <-deadline:
			t.Fatal("the change to M_Port1_Pdi never reached ns=3;i=8012")
		}
	}
}

func TestMappedRejectsBadMaps(t *testing.T) {
	rt, _ := newRuntime(t)
	for name, m := range map[string]*Mapped{
		"namespace out of range": {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "ns=2;s=X", Name: "X"}}},
		"ns 0":                   {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "i=5000", Name: "X"}}},
		"parent after child":     {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "ns=1;s=B", Parent: "ns=1;s=A", Name: "B"}, {NodeID: "ns=1;s=A", Name: "A"}}},
		"variable parent":        {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "ns=1;s=A", Name: "A", Tag: "Count"}, {NodeID: "ns=1;s=B", Parent: "ns=1;s=A", Name: "B"}}},
		"twice":                  {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "ns=1;s=A", Name: "A"}, {NodeID: "ns=1;s=A", Name: "A"}}},
		"no name":                {Namespaces: []string{"a"}, Nodes: []MappedNode{{NodeID: "ns=1;s=A"}}},
	} {
		if _, err := New(rt, Options{Endpoint: "localhost:0", Mapped: m}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestForeignAsyncuaMapped is the mapped address space seen by asyncua:
// somebody else's decoder on the namespace array, the browse, the numeric
// node in another namespace, the subscription and the writes. Gated on
// NAUTILUS_OPCUA_PYTHON like TestForeignAsyncuaClient.
func TestForeignAsyncuaMapped(t *testing.T) {
	python := os.Getenv("NAUTILUS_OPCUA_PYTHON")
	if python == "" {
		t.Skip("set NAUTILUS_OPCUA_PYTHON=/path/to/python (with asyncua installed) to run the foreign-stack test")
	}
	rt, s, _ := mappedFixture(t)
	script, err := filepath.Abs(filepath.Join("testdata", "foreign", "asyncua_mapped.py"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", script, s.URL())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	next := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			cmd.Wait()
			t.Fatalf("asyncua client ended early (%v); stderr:\n%s", cmd.ProcessState, stderr.String())
		}
		var m map[string]any
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			t.Fatalf("asyncua client printed %q: %v", lines.Text(), err)
		}
		return m
	}
	if ready := next(); ready["ready"] != true {
		t.Fatalf("expected the ready line, got %v", ready)
	}
	rt.Tags().Set("M_Port1_Pdi", int64(286))
	r := next()
	cmd.Wait()

	ns, _ := r["namespaces"].([]any)
	if len(ns) < 4 || ns[1] != "urn:test:iolm" || ns[3] != "urn:test:numeric" {
		t.Errorf("namespaces = %v", ns)
	}
	objs, _ := r["objects"].(map[string]any)
	if objs["IOLM"] != "ns=1;s=IOLM" {
		t.Errorf("Objects children = %v", objs)
	}
	if r["translated"] != "ns=1;s=IOLM/Port 1/SIO Input" {
		t.Errorf("translated = %v", r["translated"])
	}
	if p, _ := r["pin4"].(map[string]any); p["status"] != "Good" || p["value"] != float64(0) {
		t.Errorf("pin4 = %v", r["pin4"])
	}
	if r["pin4_name"] != "Pin4" {
		t.Errorf("pin4 browse name = %v", r["pin4_name"])
	}
	if r["changed_to"] != float64(286) {
		t.Errorf("subscription heard %v, want 286", r["changed_to"])
	}
	if r["pdo"] != float64(7) {
		t.Errorf("PDO after the write = %v", r["pdo"])
	}
	if r["status_write"] != "BadNotWritable" {
		t.Errorf("write to Status: %v, want BadNotWritable", r["status_write"])
	}
}
