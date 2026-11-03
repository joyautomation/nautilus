package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// enumTypesST has a sparse enumeration (values 0, 10, 11: EnumValues), a
// dense one (0, 1, 2: EnumStrings), a dense one declared out of value
// order, one no tag uses, and a struct holding the first two.
const enumTypesST = `
TYPE Mode : (Idle, Run := 10, Fault) := Idle; END_TYPE
TYPE Color : (Red, Green, Blue) := Green; END_TYPE
TYPE Order : (A := 2, B := 0, C := 1) := B; END_TYPE
TYPE Spare : (X, Y); END_TYPE
TYPE Unit :
  STRUCT
    Mode  : Mode;
    Shade : Color;
  END_STRUCT
END_TYPE
`

const enumProgramST = `
PROGRAM Main
VAR_EXTERNAL
  State : Mode;
  Shade : Color;
  U1    : Unit;
  Modes : ARRAY[1..3] OF Mode;
  Count : DINT;
  Seq   : Order;
END_VAR
END_PROGRAM
`

func enumServer(t *testing.T) (*runtime.Runtime, *Server, *opcua.Client) {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   enumProgramST,
		Libraries: []string{enumTypesST},
		Driver:    nio.NewMemory(),
		Scan:      time.Second,
		Tags: []runtime.TagDef{
			runtime.Typed("State", runtime.RoleSetpoint, "Mode"),
			runtime.Typed("Shade", runtime.RoleSetpoint, "Color"),
			runtime.Typed("U1", runtime.RoleSetpoint, "Unit"),
			runtime.Typed("Modes", runtime.RoleSetpoint, "ARRAY[1..3] OF Mode"),
			runtime.Typed("Count", runtime.RoleSetpoint, "DINT"),
			runtime.Typed("Seq", runtime.RoleSetpoint, "Order"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.Tags().Set("State", "Run")
	rt.Tags().Set("Shade", "Blue")
	if err := rt.Tags().SetPath("U1.Mode", "Fault"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Tags().SetPath("Modes[2]", "Run"); err != nil {
		t.Fatal(err)
	}
	s, c := startServer(t, rt)
	return rt, s, c
}

// nodeIDOf is a DataType read's NodeId, or the null NodeId when the read
// carried none, so a test fails rather than panics on a server without it.
func nodeIDOf(dv *ua.DataValue) *ua.NodeID {
	if nid, ok := val(dv).(*ua.NodeID); ok && nid != nil {
		return nid
	}
	return ua.NewTwoByteNodeID(0)
}

// val is a read's value, nil when it carried none.
func val(dv *ua.DataValue) any {
	if dv == nil || dv.Value == nil {
		return nil
	}
	return dv.Value.Value()
}

func typeNode(s *Server, name string) *ua.NodeID {
	return ua.NewStringNodeID(s.NamespaceIndex(), "type:"+name)
}

// browseAll is one node's references in both directions, of every type.
func browseAll(t *testing.T, c *opcua.Client, nid *ua.NodeID, refType uint32, dir ua.BrowseDirection) []*ua.ReferenceDescription {
	t.Helper()
	resp, err := c.Browse(context.Background(), &ua.BrowseRequest{NodesToBrowse: []*ua.BrowseDescription{{
		NodeID: nid, BrowseDirection: dir, ReferenceTypeID: ua.NewNumericNodeID(0, refType),
		IncludeSubtypes: true, ResultMask: uint32(ua.BrowseResultMaskAll),
	}}})
	if err != nil {
		t.Fatalf("browse %s: %v", nid, err)
	}
	if st := resp.Results[0].StatusCode; st != ua.StatusGood {
		t.Fatalf("browse %s: %s", nid, st)
	}
	return resp.Results[0].References
}

// Each IEC enumeration is a DataType node in the project namespace, a
// subtype of Enumeration found by browsing down from it (Part 3 §5.8.3),
// with its members in DataTypeDefinition and one property: EnumValues
// for the sparse Mode, EnumStrings for the dense Color (nautilus#425).
func TestEnumerationIsADataTypeUnderEnumeration(t *testing.T) {
	_, s, c := enumServer(t)

	subtypes := map[string]*ua.ReferenceDescription{}
	for _, r := range browseAll(t, c, ua.NewNumericNodeID(0, id.Enumeration), id.HasSubtype, ua.BrowseDirectionForward) {
		if r.NodeID.NodeID.Namespace() == s.NamespaceIndex() {
			subtypes[r.BrowseName.Name] = r
		}
	}
	// Spare is declared and used by no tag: a client still sees the type.
	if len(subtypes) != 4 || subtypes["Mode"] == nil || subtypes["Color"] == nil || subtypes["Order"] == nil || subtypes["Spare"] == nil {
		t.Fatalf("Enumeration's project subtypes: %v", subtypes)
	}
	// And under the hierarchical browse a client walks the Types tree with.
	if got := browse(t, c, ua.NewNumericNodeID(0, id.Enumeration))["Mode"]; got == nil {
		t.Error("a HierarchicalReferences browse of Enumeration lacks Mode")
	}

	for name, want := range map[string]struct {
		prop   string
		fields []ua.EnumField
	}{
		"Mode":  {"EnumValues", []ua.EnumField{{Name: "Idle", Value: 0}, {Name: "Run", Value: 10}, {Name: "Fault", Value: 11}}},
		"Color": {"EnumStrings", []ua.EnumField{{Name: "Red", Value: 0}, {Name: "Green", Value: 1}, {Name: "Blue", Value: 2}}},
	} {
		ref := subtypes[name]
		nid := typeNode(s, name)
		if !ref.NodeID.NodeID.Equal(nid) || ref.NodeClass != ua.NodeClassDataType {
			t.Errorf("%s: reference to %s class %v", name, ref.NodeID, ref.NodeClass)
		}
		if got := val(read(t, c, nid, ua.AttributeIDNodeClass)); got != int32(ua.NodeClassDataType) {
			t.Errorf("%s NodeClass %v", name, got)
		}
		if got, ok := val(read(t, c, nid, ua.AttributeIDBrowseName)).(*ua.QualifiedName); !ok || got.Name != name || got.NamespaceIndex != s.NamespaceIndex() {
			t.Errorf("%s BrowseName %v", name, got)
		}
		if got := val(read(t, c, nid, ua.AttributeIDIsAbstract)); got != false {
			t.Errorf("%s IsAbstract %v", name, got)
		}
		def, ok := val(read(t, c, nid, ua.AttributeIDDataTypeDefinition)).(*ua.ExtensionObject)
		if !ok {
			t.Fatalf("%s DataTypeDefinition is no ExtensionObject", name)
		}
		ed, ok := def.Value.(*ua.EnumDefinition)
		if !ok || len(ed.Fields) != len(want.fields) {
			t.Fatalf("%s DataTypeDefinition %#v", name, def.Value)
		}
		for i, f := range ed.Fields {
			if f.Name != want.fields[i].Name || f.Value != want.fields[i].Value || f.DisplayName.Text != want.fields[i].Name {
				t.Errorf("%s field %d: %s = %d (%q)", name, i, f.Name, f.Value, f.DisplayName.Text)
			}
		}

		// Its supertype, backwards, and exactly one property.
		up := browseAll(t, c, nid, id.HasSubtype, ua.BrowseDirectionInverse)
		if len(up) != 1 || up[0].NodeID.NodeID.IntID() != id.Enumeration || up[0].NodeID.NodeID.Namespace() != 0 {
			t.Errorf("%s supertype: %v", name, up)
		}
		props := browseAll(t, c, nid, id.HasProperty, ua.BrowseDirectionForward)
		if len(props) != 1 || props[0].BrowseName.Name != want.prop || props[0].BrowseName.NamespaceIndex != 0 || props[0].NodeClass != ua.NodeClassVariable {
			t.Fatalf("%s properties: %v", name, props)
		}
		pid := props[0].NodeID.NodeID
		if td := browseAll(t, c, pid, id.HasTypeDefinition, ua.BrowseDirectionForward); len(td) != 1 || td[0].NodeID.NodeID.IntID() != id.PropertyType {
			t.Errorf("%s.%s type definition: %v", name, want.prop, td)
		}
		if back := browseAll(t, c, pid, id.HasProperty, ua.BrowseDirectionInverse); len(back) != 1 || !back[0].NodeID.NodeID.Equal(nid) {
			t.Errorf("%s.%s owner: %v", name, want.prop, back)
		}
	}

	if dv := read(t, c, typeNode(s, "Nope"), ua.AttributeIDNodeClass); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("an undeclared type: %s", dv.Status)
	}
}

// The property's Value names every member: EnumValueType[] for Mode,
// LocalizedText[] indexed by value for Color.
func TestEnumValuesAndEnumStrings(t *testing.T) {
	_, s, c := enumServer(t)

	values := ua.NewStringNodeID(s.NamespaceIndex(), "type:Mode.EnumValues")
	if dt := nodeIDOf(read(t, c, values, ua.AttributeIDDataType)); dt.IntID() != id.EnumValueType {
		t.Errorf("EnumValues DataType %s", dt)
	}
	if rank := val(read(t, c, values, ua.AttributeIDValueRank)); rank != int32(1) {
		t.Errorf("EnumValues ValueRank %v", rank)
	}
	dv := read(t, c, values, ua.AttributeIDValue)
	eos, ok := val(dv).([]*ua.ExtensionObject)
	if dv.Status != ua.StatusGood || !ok || len(eos) != 3 {
		t.Fatalf("EnumValues = %#v %s", val(dv), dv.Status)
	}
	want := []struct {
		v    int64
		name string
	}{{0, "Idle"}, {10, "Run"}, {11, "Fault"}}
	for i, eo := range eos {
		ev, ok := eo.Value.(*ua.EnumValueType)
		if !ok || ev.Value != want[i].v || ev.DisplayName.Text != want[i].name {
			t.Errorf("EnumValues[%d] = %#v, want %d %s", i, eo.Value, want[i].v, want[i].name)
		}
	}

	strs := ua.NewStringNodeID(s.NamespaceIndex(), "type:Color.EnumStrings")
	if dt := nodeIDOf(read(t, c, strs, ua.AttributeIDDataType)); dt.IntID() != id.LocalizedText {
		t.Errorf("EnumStrings DataType %s", dt)
	}
	lts, ok := val(read(t, c, strs, ua.AttributeIDValue)).([]*ua.LocalizedText)
	if !ok || len(lts) != 3 || lts[0].Text != "Red" || lts[1].Text != "Green" || lts[2].Text != "Blue" {
		t.Errorf("EnumStrings = %v", lts)
	}
	// EnumStrings is indexed by value, not declaration order: Order is
	// (A := 2, B := 0, C := 1), so [B, C, A].
	ord, ok := val(read(t, c, ua.NewStringNodeID(s.NamespaceIndex(), "type:Order.EnumStrings"), ua.AttributeIDValue)).([]*ua.LocalizedText)
	if !ok || len(ord) != 3 || ord[0].Text != "B" || ord[1].Text != "C" || ord[2].Text != "A" {
		t.Errorf("Order EnumStrings = %v, want [B C A]", ord)
	}

	// Both properties are read-only arrays of one entry per member.
	for _, p := range []*ua.NodeID{values, strs} {
		if dims, ok := val(read(t, c, p, ua.AttributeIDArrayDimensions)).([]uint32); !ok || len(dims) != 1 || dims[0] != 3 {
			t.Errorf("%s ArrayDimensions %v", p, dims)
		}
		for _, a := range []ua.AttributeID{ua.AttributeIDAccessLevel, ua.AttributeIDUserAccessLevel} {
			if lvl := val(read(t, c, p, a)); lvl != uint8(ua.AccessLevelTypeCurrentRead) {
				t.Errorf("%s %s = %v, want CurrentRead", p, a, lvl)
			}
		}
	}
	// The property a type does not carry is no node.
	if dv := read(t, c, ua.NewStringNodeID(s.NamespaceIndex(), "type:Mode.EnumStrings"), ua.AttributeIDValue); dv.Status != ua.StatusBadNodeIDUnknown {
		t.Errorf("Mode.EnumStrings: %s", dv.Status)
	}
}

// A tag, a struct member and an array of an enumeration have the
// enumeration's DataType node, and their Value is still the member's
// Int32. A DINT keeps i=6.
func TestEnumerationTagsCarryTheirDataType(t *testing.T) {
	_, s, c := enumServer(t)
	for _, tc := range []struct {
		addr  string
		dt    *ua.NodeID
		value any
	}{
		{"State", typeNode(s, "Mode"), int32(10)},
		{"Shade", typeNode(s, "Color"), int32(2)},
		{"U1.Mode", typeNode(s, "Mode"), int32(11)},
		{"U1.Shade", typeNode(s, "Color"), int32(1)},
		{"Count", ua.NewNumericNodeID(0, id.Int32), int32(0)},
	} {
		dt := nodeIDOf(read(t, c, s.NodeID(tc.addr), ua.AttributeIDDataType))
		if !dt.Equal(tc.dt) {
			t.Errorf("%s DataType %s, want %s", tc.addr, dt, tc.dt)
		}
		dv := read(t, c, s.NodeID(tc.addr), ua.AttributeIDValue)
		if dv.Status != ua.StatusGood || val(dv) != tc.value {
			t.Errorf("%s = %v (%T) %s, want %v", tc.addr, val(dv), val(dv), dv.Status, tc.value)
		}
	}
	if dt := nodeIDOf(read(t, c, s.NodeID("Modes"), ua.AttributeIDDataType)); !dt.Equal(typeNode(s, "Mode")) {
		t.Errorf("Modes DataType %s", dt)
	}
	if got, ok := val(read(t, c, s.NodeID("Modes"), ua.AttributeIDValue)).([]int32); !ok || len(got) != 3 || got[1] != 10 {
		t.Errorf("Modes = %v", got)
	}
}

// Writes convert by the encoding, Int32, whatever the DataType node: a
// member's value lands, any other integer is refused, and the DataType
// and its property are not writable.
func TestEnumerationWritesKeepWorking(t *testing.T) {
	f := startWritable(t)
	ns := f.s.NamespaceIndex()
	if dt := nodeIDOf(read(t, f.writer, f.s.NodeID("State"), ua.AttributeIDDataType)); !dt.Equal(typeNode(f.s, "Mode")) {
		t.Fatalf("State DataType %s", dt)
	}
	if got := write(t, f.writer, wv("State", int32(11)))[0]; got != ua.StatusGood {
		t.Fatalf("writing Fault (11): %s", statusName(got))
	}
	if v := stored(t, f.rt, "State"); v != "Fault" {
		t.Errorf("State holds %v", v)
	}
	if v := val(read(t, f.writer, f.s.NodeID("State"), ua.AttributeIDValue)); v != int32(11) {
		t.Errorf("State reads %v (%T)", v, v)
	}
	if got := write(t, f.writer, wv("State", int64(10)))[0]; got != ua.StatusGood {
		t.Errorf("writing Run (10) as Int64: %s", statusName(got))
	}
	if got := write(t, f.writer, wv("State", int32(5)))[0]; got != ua.StatusBadOutOfRange {
		t.Errorf("writing 5, no member's value: %s", statusName(got))
	}
	if v := stored(t, f.rt, "State"); v != "Run" {
		t.Errorf("State holds %v after a refused write", v)
	}
	for _, nid := range []*ua.NodeID{typeNode(f.s, "Mode"), ua.NewStringNodeID(ns, "type:Mode.EnumValues")} {
		item := &ua.WriteValue{NodeID: nid, AttributeID: ua.AttributeIDValue,
			Value: &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(int32(1))}}
		if got := write(t, f.writer, item)[0]; got != ua.StatusBadNotWritable {
			t.Errorf("writing %s: %s", nid, statusName(got))
		}
	}
}

func TestDenseEnumerations(t *testing.T) {
	for _, tc := range []struct {
		vals []int64
		want bool
	}{
		{[]int64{0, 1, 2}, true},
		{[]int64{2, 0, 1}, true},
		{[]int64{0}, true},
		{[]int64{0, 10, 11}, false},
		{[]int64{1, 2, 3}, false},
		{[]int64{0, 0}, false},
		{[]int64{-1, 0}, false},
		{nil, false},
	} {
		d := &ir.EnumDef{Name: "E"}
		for i, v := range tc.vals {
			d.Members = append(d.Members, ir.EnumMember{Name: string(rune('A' + i)), Value: v})
		}
		if got := dense(d); got != tc.want {
			t.Errorf("dense(%v) = %v", tc.vals, got)
		}
	}
}

// A Browse of an enumeration DataType by an abstract reference type finds
// its references through the subtype relations: HierarchicalReferences and
// HasChild both cover the inverse HasSubtype to Enumeration and the
// forward HasProperty; Aggregates the property only; References all.
func TestEnumerationTypeBrowsesByAbstractReferenceTypes(t *testing.T) {
	_, s, c := enumServer(t)
	nid := typeNode(s, "Mode")
	type ref struct {
		typ     uint32
		forward bool
	}
	got := func(refType uint32) map[ref]bool {
		out := map[ref]bool{}
		for _, r := range browseAll(t, c, nid, refType, ua.BrowseDirectionBoth) {
			out[ref{r.ReferenceTypeID.IntID(), r.IsForward}] = true
		}
		return out
	}
	sub, prop := ref{id.HasSubtype, false}, ref{id.HasProperty, true}
	for _, tc := range []struct {
		refType uint32
		want    []ref
	}{
		{id.References, []ref{sub, prop}},
		{id.HierarchicalReferences, []ref{sub, prop}},
		{id.HasChild, []ref{sub, prop}},
		{id.Aggregates, []ref{prop}},
		{id.HasSubtype, []ref{sub}},
		{id.HasProperty, []ref{prop}},
		{id.NonHierarchicalReferences, nil},
	} {
		have := got(tc.refType)
		if len(have) != len(tc.want) {
			t.Errorf("browse by i=%d: %v, want %v", tc.refType, have, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !have[w] {
				t.Errorf("browse by i=%d lacks %v: %v", tc.refType, w, have)
			}
		}
	}
	// The property, by HierarchicalReferences, lists its owner backwards.
	pid := ua.NewStringNodeID(s.NamespaceIndex(), "type:Mode.EnumValues")
	if back := browseAll(t, c, pid, id.HierarchicalReferences, ua.BrowseDirectionInverse); len(back) != 1 || !back[0].NodeID.NodeID.Equal(nid) {
		t.Errorf("EnumValues' owner by HierarchicalReferences: %v", back)
	}
}
