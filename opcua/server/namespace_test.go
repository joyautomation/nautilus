package server

import (
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// newNamespace is a namespace over rt as Start would build it, at index 1,
// with no gopcua server behind it: Browse, Attribute and SetAttribute are
// plain methods, so the address-space rules can be checked without a
// socket.
func newNamespace(rt *runtime.Runtime) *namespace {
	return &namespace{rt: rt, uri: "urn:test", title: "proj", sweep: 100 * time.Millisecond, id: 1, stamps: map[string]stamp{}, mstamps: map[string]mstamp{}}
}

func TestNamespaceResolvesOnlyItsOwnStringIDs(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	if m, ok := ns.resolve(ua.NewStringNodeID(1, "level")); !ok || m.addr != "Level" {
		t.Errorf("case-folded lookup: %v %v", m, ok)
	}
	for _, nid := range []*ua.NodeID{
		ua.NewStringNodeID(2, "Level"), // another namespace
		ua.NewStringNodeID(3, "Level"),
		ua.NewNumericNodeID(1, 7),  // not a string id
		ua.NewNumericNodeID(1, 1),  // the folder is not a member
		ua.NewStringNodeID(1, ""),  // no tag
		ua.NewStringNodeID(1, "."), // no tag either
		ua.NewStringNodeID(1, "Level.X"),
		nil,
	} {
		if m, ok := ns.resolve(nid); ok {
			t.Errorf("%v resolved to %s", nid, m.addr)
		}
	}
	if !ns.isFolder(ua.NewNumericNodeID(1, folderID)) || ns.isFolder(ua.NewNumericNodeID(0, folderID)) || ns.isFolder(ua.NewStringNodeID(1, "1")) {
		t.Error("isFolder")
	}
}

func TestNamespaceAttributesOfFolderAndMembers(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	folder := ua.NewNumericNodeID(1, folderID)
	if got := ns.Attribute(folder, ua.AttributeIDNodeClass).Value.Value(); got != int32(ua.NodeClassObject) {
		t.Errorf("folder class %v", got)
	}
	if got := ns.Attribute(folder, ua.AttributeIDBrowseName).Value.Value().(*ua.QualifiedName); got.Name != "proj" || got.NamespaceIndex != 1 {
		t.Errorf("folder browse name %+v", got)
	}
	if st := ns.Attribute(folder, ua.AttributeIDValue).Status; st != ua.StatusBadAttributeIDInvalid {
		t.Errorf("folder value: %s", st)
	}
	if st := ns.Attribute(ua.NewStringNodeID(1, "Nope"), ua.AttributeIDBrowseName).Status; st != ua.StatusBadNodeIDUnknown {
		t.Errorf("unknown node: %s", st)
	}
	if st := ns.Attribute(ua.NewStringNodeID(5, "Level"), ua.AttributeIDValue).Status; st != ua.StatusBadNodeIDUnknown {
		t.Errorf("foreign namespace: %s", st)
	}

	speed := ua.NewStringNodeID(1, "P101.Drive.Speed")
	attrs := map[ua.AttributeID]any{
		ua.AttributeIDNodeClass:               int32(ua.NodeClassVariable),
		ua.AttributeIDValueRank:               int32(-1),
		ua.AttributeIDAccessLevel:             uint8(ua.AccessLevelTypeCurrentRead),
		ua.AttributeIDUserAccessLevel:         uint8(ua.AccessLevelTypeCurrentRead),
		ua.AttributeIDAccessLevelEx:           uint32(ua.AccessLevelExTypeCurrentRead),
		ua.AttributeIDHistorizing:             false,
		ua.AttributeIDWriteMask:               uint32(0),
		ua.AttributeIDUserWriteMask:           uint32(0),
		ua.AttributeIDMinimumSamplingInterval: 100.0,
	}
	for a, want := range attrs {
		dv := ns.Attribute(speed, a)
		if dv.Status != ua.StatusGood {
			t.Errorf("Speed %s: %s", a, dv.Status)
			continue
		}
		if got := dv.Value.Value(); got != want {
			t.Errorf("Speed %s = %v (%T), want %v (%T)", a, got, got, want, want)
		}
	}
	if bn := ns.Attribute(speed, ua.AttributeIDBrowseName).Value.Value().(*ua.QualifiedName); bn.Name != "Speed" {
		t.Errorf("browse name %q", bn.Name)
	}
	if dn := ns.Attribute(speed, ua.AttributeIDDisplayName).Value.Value().(*ua.LocalizedText); dn.Text != "Speed" {
		t.Errorf("display name %q", dn.Text)
	}
	if dt := ns.Attribute(speed, ua.AttributeIDDataType).Value.Value().(*ua.NodeID); dt.IntID() != id.Double {
		t.Errorf("data type %v", dt)
	}
	if dims := ns.Attribute(speed, ua.AttributeIDArrayDimensions).Value.Value().([]uint32); len(dims) != 0 {
		t.Errorf("scalar dimensions %v", dims)
	}
	for _, a := range []ua.AttributeID{ua.AttributeIDEventNotifier, ua.AttributeIDExecutable, ua.AttributeIDIsAbstract, ua.AttributeIDRolePermissions} {
		if st := ns.Attribute(speed, a).Status; st != ua.StatusBadAttributeIDInvalid {
			t.Errorf("variable %s: %s", a, st)
		}
	}

	// An object: class, event notifier, no value/data type.
	p101 := ua.NewStringNodeID(1, "P101")
	if got := ns.Attribute(p101, ua.AttributeIDNodeClass).Value.Value(); got != int32(ua.NodeClassObject) {
		t.Errorf("P101 class %v", got)
	}
	if got := ns.Attribute(p101, ua.AttributeIDEventNotifier).Value.Value(); got != uint8(0) {
		t.Errorf("P101 event notifier %v", got)
	}
	for _, a := range []ua.AttributeID{ua.AttributeIDValue, ua.AttributeIDDataType, ua.AttributeIDValueRank, ua.AttributeIDAccessLevel} {
		if st := ns.Attribute(p101, a).Status; st != ua.StatusBadAttributeIDInvalid {
			t.Errorf("object %s: %s", a, st)
		}
	}

	// An array variable: rank 1, its length as the one dimension.
	hours := ua.NewStringNodeID(1, "P101.Hours")
	if got := ns.Attribute(hours, ua.AttributeIDValueRank).Value.Value(); got != int32(1) {
		t.Errorf("Hours rank %v", got)
	}
	if dims := ns.Attribute(hours, ua.AttributeIDArrayDimensions).Value.Value().([]uint32); len(dims) != 1 || dims[0] != 3 {
		t.Errorf("Hours dimensions %v", dims)
	}
}

func TestNamespaceDescriptionFromTagMeta(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL\n A : REAL; B : REAL; C : REAL; D : REAL;\nEND_VAR\nEND_PROGRAM\n",
		Scan:    time.Second,
		Tags: []runtime.TagDef{
			runtime.State("A", 1.0, runtime.Desc("Flow"), runtime.Unit("gpm")),
			runtime.State("B", 1.0, runtime.Desc("Flow")),
			runtime.State("C", 1.0, runtime.Unit("gpm")),
			runtime.State("D", 1.0),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ns := newNamespace(rt)
	for name, want := range map[string]string{"A": "Flow [gpm]", "B": "Flow", "C": "[gpm]", "D": ""} {
		got := ns.Attribute(ua.NewStringNodeID(1, name), ua.AttributeIDDescription).Value.Value().(*ua.LocalizedText).Text
		if got != want {
			t.Errorf("%s description %q, want %q", name, got, want)
		}
	}
}

func TestNamespaceBrowseReferences(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	all := func(nid *ua.NodeID, dir ua.BrowseDirection) map[string]*ua.ReferenceDescription {
		res := ns.Browse(&ua.BrowseDescription{NodeID: nid, BrowseDirection: dir, ResultMask: uint32(ua.BrowseResultMaskAll)})
		if res.StatusCode != ua.StatusGood {
			t.Fatalf("browse %s: %s", nid, res.StatusCode)
		}
		out := map[string]*ua.ReferenceDescription{}
		for _, r := range res.References {
			out[r.BrowseName.Name] = r
		}
		return out
	}
	// The folder: type definition, its parent Objects (inverse), the tags.
	folder := ua.NewNumericNodeID(1, folderID)
	fwd := all(folder, ua.BrowseDirectionForward)
	if fwd["FolderType"] == nil || fwd["FolderType"].ReferenceTypeID.IntID() != id.HasTypeDefinition {
		t.Errorf("folder forward refs %v", fwd)
	}
	if fwd["Level"] == nil || fwd["Level"].ReferenceTypeID.IntID() != id.Organizes || fwd["Objects"] != nil {
		t.Errorf("folder forward refs %v", fwd)
	}
	inv := all(folder, ua.BrowseDirectionInverse)
	if len(inv) != 1 || inv["Objects"] == nil || inv["Objects"].NodeID.NodeID.IntID() != id.ObjectsFolder {
		t.Errorf("folder inverse refs %v", inv)
	}
	// A tag: inverse is the folder; a member: inverse is its parent.
	if inv := all(ua.NewStringNodeID(1, "Level"), ua.BrowseDirectionInverse); len(inv) != 1 || inv["proj"] == nil || inv["proj"].NodeID.NodeID.IntID() != folderID {
		t.Errorf("Level inverse refs %v", inv)
	}
	inv = all(ua.NewStringNodeID(1, "P101.Drive.Speed"), ua.BrowseDirectionInverse)
	if len(inv) != 1 || inv["Drive"] == nil || inv["Drive"].NodeID.NodeID.StringID() != "P101.Drive" || inv["Drive"].ReferenceTypeID.IntID() != id.HasComponent {
		t.Errorf("Speed inverse refs %v", inv)
	}
	if inv := all(ua.NewStringNodeID(1, "Pumps[2].Hours"), ua.BrowseDirectionInverse); inv["Pumps[2]"] == nil || inv["Pumps[2]"].NodeID.NodeID.StringID() != "Pumps[2]" {
		t.Errorf("Pumps[2].Hours inverse refs %v", inv)
	}
	// A variable's forward refs: its type definition only.
	fwd = all(ua.NewStringNodeID(1, "Level"), ua.BrowseDirectionForward)
	if len(fwd) != 1 || fwd["BaseDataVariableType"] == nil || fwd["BaseDataVariableType"].NodeClass != ua.NodeClassVariableType {
		t.Errorf("Level forward refs %v", fwd)
	}
	// An object's: type definition and components.
	fwd = all(ua.NewStringNodeID(1, "P101"), ua.BrowseDirectionForward)
	if fwd["BaseObjectType"] == nil || fwd["Drive"] == nil || fwd["Drive"].ReferenceTypeID.IntID() != id.HasComponent || fwd["Drive"].NodeClass != ua.NodeClassObject || fwd["Running"].NodeClass != ua.NodeClassVariable {
		t.Errorf("P101 forward refs %v", fwd)
	}
	// Node class mask narrows the result.
	res := ns.Browse(&ua.BrowseDescription{NodeID: folder, BrowseDirection: ua.BrowseDirectionForward, NodeClassMask: uint32(ua.NodeClassObject)})
	for _, r := range res.References {
		if r.NodeClass != ua.NodeClassObject {
			t.Errorf("mask let through %s (%v)", r.BrowseName.Name, r.NodeClass)
		}
	}
	// Bad requests.
	if st := ns.Browse(&ua.BrowseDescription{NodeID: ua.NewStringNodeID(1, "Nope")}).StatusCode; st != ua.StatusBadNodeIDUnknown {
		t.Errorf("unknown: %s", st)
	}
	if st := ns.Browse(&ua.BrowseDescription{}).StatusCode; st != ua.StatusBadNodeIDInvalid {
		t.Errorf("nil node: %s", st)
	}
	if st := ns.Browse(nil).StatusCode; st != ua.StatusBadNodeIDInvalid {
		t.Errorf("nil description: %s", st)
	}
}

func TestNamespaceBrowseOrderIsStable(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	bd := &ua.BrowseDescription{NodeID: ua.NewNumericNodeID(1, folderID), BrowseDirection: ua.BrowseDirectionForward, NodeClassMask: uint32(ua.NodeClassVariable | ua.NodeClassObject)}
	first := ns.Browse(bd).References
	for i := 0; i < 5; i++ {
		again := ns.Browse(bd).References
		if len(again) != len(first) {
			t.Fatalf("browse size changed %d → %d", len(first), len(again))
		}
		for j := range first {
			if first[j].BrowseName.Name != again[j].BrowseName.Name {
				t.Fatalf("browse order changed at %d: %s vs %s (BrowseNext would skip or repeat)", j, first[j].BrowseName.Name, again[j].BrowseName.Name)
			}
		}
	}
	for j := 1; j < len(first); j++ {
		if first[j-1].BrowseName.Name > first[j].BrowseName.Name {
			t.Errorf("not sorted: %s before %s", first[j-1].BrowseName.Name, first[j].BrowseName.Name)
		}
	}
}

func TestNamespaceSetAttributeRefusesEverything(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	dv := &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(1.0)}
	cases := map[*ua.NodeID]map[ua.AttributeID]ua.StatusCode{
		ua.NewStringNodeID(1, "Level"):            {ua.AttributeIDValue: ua.StatusBadUserAccessDenied, ua.AttributeIDDescription: ua.StatusBadNotWritable},
		ua.NewStringNodeID(1, "P101.Drive.Speed"): {ua.AttributeIDValue: ua.StatusBadUserAccessDenied},
		ua.NewStringNodeID(1, "P101"):             {ua.AttributeIDValue: ua.StatusBadNotWritable},
		ua.NewNumericNodeID(1, folderID):          {ua.AttributeIDValue: ua.StatusBadNotWritable, ua.AttributeIDDisplayName: ua.StatusBadNotWritable},
		ua.NewStringNodeID(1, "Nope"):             {ua.AttributeIDValue: ua.StatusBadNodeIDUnknown},
	}
	for nid, attrs := range cases {
		for a, want := range attrs {
			if got := ns.SetAttribute(nid, a, dv); got != want {
				t.Errorf("write %s %s: %s, want %s", nid, a, got, want)
			}
		}
	}
	if rt.Tags().Real("Level") != 3.5 {
		t.Error("a refused write changed the store")
	}
}

func TestStampAtMovesOnlyWithTheGeneration(t *testing.T) {
	rt, _ := newRuntime(t)
	ns := newNamespace(rt)
	t0 := time.Unix(1000, 0)
	t1 := t0.Add(time.Second)
	if got := ns.stampAt("Level", 5, t0); !got.Equal(t0) {
		t.Errorf("first sighting %v", got)
	}
	if got := ns.stampAt("Level", 5, t1); !got.Equal(t0) {
		t.Errorf("same generation restamped %v", got)
	}
	if got := ns.stampAt("Level", 6, t1); !got.Equal(t1) {
		t.Errorf("new generation %v", got)
	}
	if got := ns.stampAt("Count", 6, t0); !got.Equal(t0) {
		t.Errorf("another tag %v", got)
	}
	// Through readValue: a member reads its root tag's stamp.
	rt.Tags().Set("Level", ir.RealVal(1))
	a := ns.readValue(member{addr: "P101.Drive.Speed", val: ir.RealVal(0)})
	b := ns.readValue(member{addr: "P101.Running", val: ir.BoolVal(false)})
	if !a.SourceTimestamp.Equal(b.SourceTimestamp) {
		t.Errorf("members of one tag stamped apart: %v vs %v", a.SourceTimestamp, b.SourceTimestamp)
	}
	if a.EncodingMask&ua.DataValueSourceTimestamp == 0 || a.EncodingMask&ua.DataValueStatusCode == 0 {
		t.Errorf("encoding mask %08b", a.EncodingMask)
	}
}
