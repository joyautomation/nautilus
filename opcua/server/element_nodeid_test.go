package server

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// An element of an array of scalars has a node id of its own,
// <tag>.<member>[i], though Browse does not list it: the array is one
// Variable holding an OPC UA array. The element node reads, takes its
// AccessLevel from opcua.writable like any scalar, and is written one
// element at a time. This is how MHJ PLC-Lab reaches the bytes of its
// connector (ns=1;s=PlcLabConnector.Inputs[17]), so the fixture is the
// connector type of examples/plclab, read from that example.
func elementFixture(t *testing.T, anonymous bool) *writeFixture {
	t.Helper()
	types, err := os.ReadFile("../../examples/plclab/types.st")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL PlcLabConnector : PlcLabConnectorData; END_VAR
END_PROGRAM`,
		Libraries: []string{string(types)},
		Driver:    nio.NewMemory(),
		Scan:      20 * time.Millisecond,
		Tags:      []runtime.TagDef{runtime.Typed("PlcLabConnector", runtime.RoleState, "PlcLabConnectorData")},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &writeFixture{rt: rt, log: &syncBuffer{}}
	s, err := New(rt, Options{Endpoint: "localhost:0", Name: "plclab", Sweep: 20 * time.Millisecond, AnonymousWrites: anonymous, Logger: slog.New(slog.NewTextHandler(f.log, nil)),
		Writable: []string{"PlcLabConnector.Inputs", "PlcLabConnector.AreaInputs", "PlcLabConnector.AreaOutputs"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	f.s = s
	f.anon = f.connectAnonymous(t)
	return f
}

func TestElementNodeIDsRead(t *testing.T) {
	f := elementFixture(t, true)
	if err := f.rt.Tags().SetPath("PlcLabConnector.Inputs[17]", 0xA5); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.Tags().SetPath("PlcLabConnector.AreaOutputs[3].IoOfs", 12); err != nil {
		t.Fatal(err)
	}
	nid := ua.NewStringNodeID(1, "PlcLabConnector.Inputs[17]")
	if dv := read(t, f.anon, nid, ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != uint8(0xA5) {
		t.Errorf("Inputs[17] = %v (%T) %s, want Byte 165", dv.Value.Value(), dv.Value.Value(), dv.Status)
	}
	for attr, want := range map[ua.AttributeID]any{
		ua.AttributeIDNodeClass:  int32(ua.NodeClassVariable),
		ua.AttributeIDDataType:   ua.NewNumericNodeID(0, id.Byte),
		ua.AttributeIDValueRank:  int32(-1),
		ua.AttributeIDBrowseName: &ua.QualifiedName{NamespaceIndex: 1, Name: "Inputs[17]"},
	} {
		got := read(t, f.anon, nid, attr).Value.Value()
		switch w := want.(type) {
		case *ua.NodeID:
			if g, ok := got.(*ua.NodeID); !ok || g.String() != w.String() {
				t.Errorf("Inputs[17] attribute %d = %v, want %v", attr, got, w)
			}
		case *ua.QualifiedName:
			if g, ok := got.(*ua.QualifiedName); !ok || *g != *w {
				t.Errorf("Inputs[17] attribute %d = %v, want %v", attr, got, w)
			}
		default:
			if got != w {
				t.Errorf("Inputs[17] attribute %d = %v, want %v", attr, got, w)
			}
		}
	}
	// A WORD member of an element of an array of structs reads as UInt16;
	// a WORD member of the connector itself carries its declared default.
	if dv := read(t, f.anon, ua.NewStringNodeID(1, "PlcLabConnector.AreaOutputs[3].IoOfs"), ua.AttributeIDValue); dv.Value.Value() != uint16(12) {
		t.Errorf("AreaOutputs[3].IoOfs = %v (%T), want UInt16 12", dv.Value.Value(), dv.Value.Value())
	}
	if dv := read(t, f.anon, ua.NewStringNodeID(1, "PlcLabConnector.SIZE_I_ARRAY"), ua.AttributeIDValue); dv.Value.Value() != uint16(1024) {
		t.Errorf("SIZE_I_ARRAY = %v (%T), want UInt16 1024", dv.Value.Value(), dv.Value.Value())
	}
	// Past either bound there is no node.
	for _, addr := range []string{"PlcLabConnector.Inputs[1024]", "PlcLabConnector.Inputs[-1]", "PlcLabConnector.Inputs[x]"} {
		if dv := read(t, f.anon, ua.NewStringNodeID(1, addr), ua.AttributeIDValue); dv.Status != ua.StatusBadNodeIDUnknown {
			t.Errorf("%s: %s, want Bad_NodeIdUnknown", addr, dv.Status)
		}
	}
	// The array is one Variable: Browse lists no element under it.
	if refs := browse(t, f.anon, ua.NewStringNodeID(1, "PlcLabConnector.Inputs")); len(refs) != 0 {
		t.Errorf("Inputs browses to %v, want no children", keys(refs))
	}
	// Node ids name tags and members in any casing, elements included.
	if dv := read(t, f.anon, ua.NewStringNodeID(1, "plclabconnector.INPUTS[17]"), ua.AttributeIDValue); dv.Status != ua.StatusGood || dv.Value.Value() != uint8(0xA5) {
		t.Errorf("plclabconnector.INPUTS[17] = %v %s, want Byte 165", dv.Value.Value(), dv.Status)
	}
}

// AccessLevel on an element node follows opcua.writable, as on any scalar;
// UserAccessLevel follows it for a writer (an anonymous session, with
// anonymous-writes on) and is CurrentRead otherwise. The array node itself
// stays CurrentRead: a whole-array write is not supported.
func TestElementNodeIDsAccessLevels(t *testing.T) {
	const rw = uint8(ua.AccessLevelTypeCurrentRead | ua.AccessLevelTypeCurrentWrite)
	const ro = uint8(ua.AccessLevelTypeCurrentRead)
	for _, anonymous := range []bool{true, false} {
		f := elementFixture(t, anonymous)
		for addr, access := range map[string]uint8{
			"PlcLabConnector.Inputs[17]":             rw,
			"PlcLabConnector.AreaInputs[0].CntBytes": rw,
			"PlcLabConnector.AreaOutputs[9].IoOfs":   rw,
			"PlcLabConnector.Outputs[17]":            ro,
			"PlcLabConnector.SIZE_I_ARRAY":           ro,
			"PlcLabConnector.Inputs":                 ro,
		} {
			nid := ua.NewStringNodeID(1, addr)
			if got := read(t, f.anon, nid, ua.AttributeIDAccessLevel).Value.Value(); got != access {
				t.Errorf("anonymous-writes %v: %s AccessLevel %v, want %v", anonymous, addr, got, access)
			}
			want := access
			if !anonymous {
				want = ro
			}
			if got := read(t, f.anon, nid, ua.AttributeIDUserAccessLevel).Value.Value(); got != want {
				t.Errorf("anonymous-writes %v: %s UserAccessLevel %v, want %v", anonymous, addr, got, want)
			}
		}
	}
}

// An anonymous writer writes an element by its node id, one at a time, in
// any integer type the declared width holds; the rest of the array is
// untouched. What opcua.writable does not open, a value that does not fit,
// the whole array and an IndexRange are refused.
func TestElementNodeIDsAnonymousWrites(t *testing.T) {
	f := elementFixture(t, true)
	ranged := wv("PlcLabConnector.Inputs", []uint8{1, 2})
	ranged.IndexRange = "3:4"
	element := wv("PlcLabConnector.Inputs[5]", uint8(9))
	element.IndexRange = "0"
	cases := []struct {
		name  string
		item  *ua.WriteValue
		want  ua.StatusCode
		path  string
		value any
	}{
		{"a Byte element", wv("PlcLabConnector.Inputs[17]", uint8(200)), ua.StatusGood, "PlcLabConnector.Inputs[17]", int64(200)},
		{"an Int64 element (asyncua's plain int)", wv("PlcLabConnector.Inputs[0]", int64(3)), ua.StatusGood, "PlcLabConnector.Inputs[0]", int64(3)},
		{"another casing", wv("plclabconnector.inputs[1023]", uint8(7)), ua.StatusGood, "PlcLabConnector.Inputs[1023]", int64(7)},
		{"a UInt16 area field", wv("PlcLabConnector.AreaInputs[0].CntBytes", uint16(2)), ua.StatusGood, "PlcLabConnector.AreaInputs[0].CntBytes", int64(2)},
		{"past BYTE", wv("PlcLabConnector.Inputs[1]", int64(256)), ua.StatusBadOutOfRange, "PlcLabConnector.Inputs[1]", int64(0)},
		{"past WORD", wv("PlcLabConnector.AreaOutputs[0].Addr", int64(65536)), ua.StatusBadOutOfRange, "PlcLabConnector.AreaOutputs[0].Addr", int64(0)},
		{"a Double to a BYTE", wv("PlcLabConnector.Inputs[2]", 1.0), ua.StatusBadTypeMismatch, "PlcLabConnector.Inputs[2]", int64(0)},
		{"Outputs is not opened", wv("PlcLabConnector.Outputs[0]", uint8(1)), ua.StatusBadNotWritable, "PlcLabConnector.Outputs[0]", int64(0)},
		{"a size is not opened", wv("PlcLabConnector.SIZE_I_ARRAY", uint16(1)), ua.StatusBadNotWritable, "PlcLabConnector.SIZE_I_ARRAY", int64(1024)},
		{"the whole array", wv("PlcLabConnector.Inputs", make([]uint8, 1024)), ua.StatusBadWriteNotSupported, "PlcLabConnector.Inputs[17]", int64(200)},
		{"an IndexRange on the array", ranged, ua.StatusBadWriteNotSupported, "PlcLabConnector.Inputs[3]", int64(0)},
		{"an IndexRange on an element", element, ua.StatusBadWriteNotSupported, "PlcLabConnector.Inputs[5]", int64(0)},
		{"past the end", wv("PlcLabConnector.Inputs[1024]", uint8(1)), ua.StatusBadNodeIDUnknown, "", nil},
	}
	for _, c := range cases {
		if got := write(t, f.anon, c.item)[0]; got != c.want {
			t.Errorf("%s: %s, want %s", c.name, statusName(got), statusName(c.want))
		}
		if c.path != "" {
			if v := stored(t, f.rt, c.path); v != c.value {
				t.Errorf("%s: %s holds %v (%T), want %v", c.name, c.path, v, v, c.value)
			}
		}
	}
	// A landed element reads back as the client wrote it.
	if dv := read(t, f.anon, ua.NewStringNodeID(1, "PlcLabConnector.Inputs[17]"), ua.AttributeIDValue); dv.Value.Value() != uint8(200) {
		t.Errorf("Inputs[17] reads %v after the write", dv.Value.Value())
	}
	// Its neighbours are as they were.
	for _, addr := range []string{"PlcLabConnector.Inputs[16]", "PlcLabConnector.Inputs[18]"} {
		if v := stored(t, f.rt, addr); v != int64(0) {
			t.Errorf("%s = %v after a write to Inputs[17]", addr, v)
		}
	}
	// Several elements in one request land together.
	if got := write(t, f.anon, wv("PlcLabConnector.Inputs[40]", uint8(1)), wv("PlcLabConnector.Inputs[41]", uint8(2))); got[0] != ua.StatusGood || got[1] != ua.StatusGood {
		t.Errorf("two elements in one Write: %s, %s", statusName(got[0]), statusName(got[1]))
	}
	if a, b := stored(t, f.rt, "PlcLabConnector.Inputs[40]"), stored(t, f.rt, "PlcLabConnector.Inputs[41]"); a != int64(1) || b != int64(2) {
		t.Errorf("Inputs[40], [41] = %v, %v", a, b)
	}
}

// With anonymous-writes off, an element is refused like any tag.
func TestElementNodeIDsAnonymousWritesOff(t *testing.T) {
	f := elementFixture(t, false)
	if got := write(t, f.anon, wv("PlcLabConnector.Inputs[17]", uint8(1)))[0]; got != ua.StatusBadUserAccessDenied {
		t.Errorf("anonymous element write with the switch off: %s, want Bad_UserAccessDenied", statusName(got))
	}
	if v := stored(t, f.rt, "PlcLabConnector.Inputs[17]"); v != int64(0) {
		t.Errorf("Inputs[17] = %v after a refused write", v)
	}
}
