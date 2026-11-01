package server

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	opcserver "github.com/joyautomation/nautilus/opcua/internal/uaserver"
	"github.com/joyautomation/nautilus/runtime"
)

// F-37 (#434): a String holding "" is sent as an empty String (length 0),
// not a null one (length -1). Part 6 §5.2.2.4 encodes the two apart, and a
// client that decodes them apart (asyncua, .NET, Java) read null where the
// project held "", the value the HTTP API reported and the one written.
// gopcua's encoder writes -1 for every "", and its own decoder reads 0 and
// -1 alike, so a gopcua client cannot see the difference: these tests look
// at the bytes.

// emptyStringsRuntime holds a String and an array of Strings, both with an
// empty value in them.
func emptyStringsRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n  Str1 : STRING;\n  StrArr : ARRAY[0..1] OF STRING;\n  Full : STRING;\nEND_VAR\nEND_PROGRAM\n",
		Driver:  nio.NewMemory(),
		Scan:    time.Second,
		Tags: []runtime.TagDef{
			runtime.State("Str1", ""),
			runtime.State("StrArr", []any{"", "b"}),
			runtime.State("Full", "hello"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// The encodings a client must see: the Variant's mask (String, and the
// array bit for an array), then each String as an Int32 length and its
// bytes, length 0 for "".
var (
	wireEmpty   = []byte{0x0c, 0, 0, 0, 0}
	wireArray   = []byte{0x8c, 2, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'b'}
	wireFull    = []byte{0x0c, 5, 0, 0, 0, 'h', 'e', 'l', 'l', 'o'}
	wireNullStr = []byte{0x0c, 0xff, 0xff, 0xff, 0xff}
)

// encodedValue is dv's Variant as gopcua puts it on the wire.
func encodedValue(t *testing.T, dv *ua.DataValue) []byte {
	t.Helper()
	if dv == nil || dv.Value == nil {
		t.Fatalf("no value: %v", dv)
	}
	b, err := dv.Value.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEmptyStringIsReadAsEmptyNotNull(t *testing.T) {
	rt := emptyStringsRuntime(t)
	s, _ := startServer(t, rt)
	req := &ua.ReadRequest{RequestHeader: &ua.RequestHeader{}, NodesToRead: []*ua.ReadValueID{
		{NodeID: s.NodeID("Str1"), AttributeID: ua.AttributeIDValue},
		{NodeID: s.NodeID("StrArr"), AttributeID: ua.AttributeIDValue},
		{NodeID: s.NodeID("Full"), AttributeID: ua.AttributeIDValue},
		{NodeID: s.NodeID("StrArr"), AttributeID: ua.AttributeIDValue, IndexRange: "0"},
	}}
	resp, err := readAttributes(s.srv, s.ns, req)
	if err != nil {
		t.Fatal(err)
	}
	res := resp.(*ua.ReadResponse).Results
	for i, want := range [][]byte{wireEmpty, wireArray, wireFull, {0x8c, 1, 0, 0, 0, 0, 0, 0, 0}} {
		if got := encodedValue(t, res[i]); !bytes.Equal(got, want) {
			t.Errorf("%s: encoded % x, want % x", req.NodesToRead[i].NodeID, got, want)
		}
	}
	// What the namespace hands our own code is still the value itself: only
	// the result on its way to the wire changes.
	if v := s.ns.Attribute(s.NodeID("Str1"), ua.AttributeIDValue).Value.Value(); v != "" {
		t.Errorf("Str1 in the namespace is %#v, want \"\"", v)
	}
}

// The trick works against the gopcua this is built with; if an upgrade
// breaks it, the server falls back to null for "" and this fails.
func TestWireStringSurvivesTheEncoder(t *testing.T) {
	if !wireStringsWork() {
		t.Fatal("the empty-String encoding no longer works against this gopcua: \"\" goes out as null again (wirestring.go)")
	}
	for _, tc := range []struct {
		v    any
		want []byte
	}{
		{"", wireEmpty},
		{[]string{"", "b"}, wireArray},
		{"hello", wireFull},
	} {
		v := ua.MustVariant(tc.v)
		got, err := wireVariant(v).Encode()
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("%#v: encoded % x (%v), want % x", tc.v, got, err, tc.want)
		}
		if !reflect.DeepEqual(v.Value(), tc.v) {
			t.Errorf("%#v: the original Variant now holds %#v", tc.v, v.Value())
		}
	}
	// Values with no "" in them, and values of other types, are passed on
	// as they are, not copied.
	for _, v := range []any{"x", []string{"a", "b"}, []string{}, 3.5, int32(0), []byte{}} {
		in := ua.MustVariant(v)
		if out := wireVariant(in); out != in {
			t.Errorf("%#v was copied", v)
		}
	}
	// Without the trick the encoder writes null: the case this is for.
	if got, _ := ua.MustVariant("").Encode(); !bytes.Equal(got, wireNullStr) {
		t.Errorf("gopcua now encodes \"\" as % x; wirestring.go may no longer be needed", got)
	}
}

func TestEmptyStringIsNotifiedAsEmptyNotNull(t *testing.T) {
	rt := emptyStringsRuntime(t)
	s, _ := startServer(t, rt)
	for _, tc := range []struct {
		addr string
		want []byte
	}{{"Str1", wireEmpty}, {"StrArr", wireArray}, {"Full", wireFull}} {
		sub := opcserver.NewSubscription()
		nid := s.NodeID(tc.addr)
		item := &opcserver.MonitoredItem{ID: 1, Sub: sub, Mode: ua.MonitoringModeReporting, Req: &ua.MonitoredItemCreateRequest{
			ItemToMonitor:       &ua.ReadValueID{NodeID: nid, AttributeID: ua.AttributeIDValue},
			MonitoringMode:      ua.MonitoringModeReporting,
			RequestedParameters: &ua.MonitoringParameters{ClientHandle: 7, SamplingInterval: -1},
		}}
		mon := monitoredNode{key: nid.String(), nid: nid, items: []watched{{item: item, mode: ua.MonitoringModeReporting}}}
		if !s.notify(mon, map[string]reading{}, false, map[*opcserver.MonitoredItem]*itemState{}, time.Now()) {
			t.Fatalf("%s: notify owed a value it could not send", tc.addr)
		}
		select {
		case n := <-sub.NotifyChannel:
			if got := encodedValue(t, n.Value); !bytes.Equal(got, tc.want) {
				t.Errorf("%s: notified % x, want % x", tc.addr, got, tc.want)
			}
		default:
			t.Fatalf("%s: nothing notified", tc.addr)
		}
	}
}

// A Read of a node outside the project namespace whose value is "" is sent
// as "" too, and the node itself is left as it was.
func TestEmptyStringOfAServerNodeIsReadAsEmpty(t *testing.T) {
	rt := emptyStringsRuntime(t)
	s, _ := startServer(t, rt)
	nid := ua.NewNumericNodeID(0, id.Server_ServerStatus_BuildInfo_ManufacturerName)
	node := s.srv.Node(nid)
	if node == nil {
		t.Skip("no BuildInfo.ManufacturerName node")
	}
	node.SetAttribute(ua.AttributeIDValue, opcserver.DataValueFromValue(""))
	req := &ua.ReadRequest{RequestHeader: &ua.RequestHeader{}, NodesToRead: []*ua.ReadValueID{{NodeID: nid, AttributeID: ua.AttributeIDValue}}}
	for range 2 { // twice: the first must not have changed what the node holds
		resp, err := readAttributes(s.srv, s.ns, req)
		if err != nil {
			t.Fatal(err)
		}
		if got := encodedValue(t, resp.(*ua.ReadResponse).Results[0]); !bytes.Equal(got, wireEmpty) {
			t.Errorf("ManufacturerName \"\": encoded % x, want % x", got, wireEmpty)
		}
	}
	space, _ := s.srv.Namespace(0)
	if v := space.Attribute(nid, ua.AttributeIDValue).Value.Value(); v != "" {
		t.Errorf("the node now holds %#v, want \"\"", v)
	}
}
