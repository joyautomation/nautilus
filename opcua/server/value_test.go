package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

func fixtureMember(t *testing.T, name string) member {
	t.Helper()
	rt, _ := newRuntime(t)
	tags := rt.Tags()
	v, err := tags.ReadGlobal(name)
	if err != nil {
		t.Fatal(err)
	}
	return member{addr: name, val: v, typ: tags.TypeOf(name)}
}

func TestWalkResolvesEveryMemberShape(t *testing.T) {
	rt, _ := newRuntime(t)
	tags := rt.Tags()
	if err := tags.SetPath("P101.Drive.Amps", 12.5); err != nil {
		t.Fatal(err)
	}
	if err := tags.SetPath("Pumps[2].Hours[3]", 99); err != nil {
		t.Fatal(err)
	}
	get := func(root string) (ir.Value, *ir.Type) {
		v, err := tags.ReadGlobal(root)
		if err != nil {
			t.Fatal(err)
		}
		return v, tags.TypeOf(root)
	}
	cases := []struct {
		root string
		path string
		addr string
		kind ir.TypeKind
		want any
	}{
		{"P101", "", "P101", ir.TypeStruct, nil},
		{"P101", "Drive", "P101.Drive", ir.TypeStruct, nil},
		{"P101", "Drive.Amps", "P101.Drive.Amps", ir.TypeReal, 12.5},
		{"P101", "drive.amps", "P101.Drive.Amps", ir.TypeReal, 12.5}, // members are case-insensitive, spelled as declared
		{"P101", "Hours", "P101.Hours", ir.TypeArray, nil},
		{"P101", "Hours[1]", "P101.Hours[1]", ir.TypeInt, int64(0)},
		{"Pumps", "[2]", "Pumps[2]", ir.TypeStruct, nil},
		{"Pumps", "[2].Hours[3]", "Pumps[2].Hours[3]", ir.TypeInt, int64(99)},
		{"Levels", "[0]", "Levels[0]", ir.TypeReal, 1.0},
	}
	for _, tc := range cases {
		v, typ := get(tc.root)
		m, ok := walk(tc.root, v, typ, ir.PathSegments(tc.path))
		if !ok {
			t.Errorf("%s/%s: not found", tc.root, tc.path)
			continue
		}
		if m.addr != tc.addr || m.val.Kind != tc.kind {
			t.Errorf("%s/%s → %s %v, want %s %v", tc.root, tc.path, m.addr, m.val.Kind, tc.addr, tc.kind)
		}
		switch w := tc.want.(type) {
		case float64:
			if m.val.F != w {
				t.Errorf("%s = %v, want %v", tc.addr, m.val.F, w)
			}
		case int64:
			if m.val.I != w {
				t.Errorf("%s = %v, want %v", tc.addr, m.val.I, w)
			}
		}
	}
	for _, bad := range []struct{ root, path string }{
		{"P101", "Nope"},          // no such member
		{"P101", "Drive.Speed.X"}, // a scalar has no members
		{"P101", "Hours[0]"},      // below the lower bound
		{"P101", "Hours[4]"},      // above the upper bound
		{"P101", "Hours[x]"},      // not an index
		{"Level", "[0]"},          // a scalar is not an array
		{"Pumps", "Drive"},        // an array has no named members
	} {
		v, typ := get(bad.root)
		if m, ok := walk(bad.root, v, typ, ir.PathSegments(bad.path)); ok {
			t.Errorf("%s/%s resolved to %s", bad.root, bad.path, m.addr)
		}
	}
}

func TestChildrenListMembersInDeclarationOrder(t *testing.T) {
	p101 := fixtureMember(t, "P101")
	var names []string
	for _, c := range children(p101) {
		names = append(names, c.addr)
	}
	if want := []string{"P101.Running", "P101.Drive", "P101.Hours"}; !reflect.DeepEqual(names, want) {
		t.Errorf("P101 children %v, want %v", names, want)
	}
	pumps := fixtureMember(t, "Pumps")
	names = nil
	for _, c := range children(pumps) {
		names = append(names, c.addr)
		if c.val.Kind != ir.TypeStruct || c.typ == nil || c.typ.Kind != ir.TypeStruct {
			t.Errorf("%s: kind %v typ %v", c.addr, c.val.Kind, c.typ)
		}
	}
	if want := []string{"Pumps[1]", "Pumps[2]"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Pumps children %v, want %v", names, want)
	}
	// Arrays of scalars and scalars have no children: the array is the value.
	for _, name := range []string{"Levels", "Level", "P101.Hours"} {
		root, segs := ir.SplitAddress(name)
		rm := fixtureMember(t, root)
		m, ok := walk(root, rm.val, rm.typ, segs)
		if !ok {
			t.Fatal(name)
		}
		if kids := children(m); len(kids) != 0 {
			t.Errorf("%s has children %v", name, kids)
		}
		if isObject(m) {
			t.Errorf("%s browses as an object", name)
		}
	}
	for _, name := range []string{"P101", "Pumps", "P101.Drive"} {
		root, segs := ir.SplitAddress(name)
		rm := fixtureMember(t, root)
		m, _ := walk(root, rm.val, rm.typ, segs)
		if !isObject(m) {
			t.Errorf("%s browses as a variable", name)
		}
	}
}

func TestChildrenOfAnFBAreItsVisiblePins(t *testing.T) {
	rt, _ := newRuntime(t)
	v := tonTag(t, rt, "T1")
	m := member{addr: "T1", val: v, typ: rt.Tags().TypeOf("T1")}
	if v.Kind != ir.TypeFB || !isObject(m) {
		t.Fatalf("T1 is %v", v.Kind)
	}
	seen := map[string]bool{}
	for _, c := range children(m) {
		seen[c.addr] = true
		if c.addr[len("T1."):][0] == '_' {
			t.Errorf("internal pin exposed: %s", c.addr)
		}
	}
	for _, pin := range []string{"T1.IN", "T1.PT", "T1.Q", "T1.ET"} {
		if !seen[pin] {
			t.Errorf("pin %s missing from %v", pin, seen)
		}
	}
	if q, ok := walk("T1", v, m.typ, ir.PathSegments("Q")); !ok || q.val.Kind != ir.TypeBool {
		t.Errorf("T1.Q: %v %v", ok, q.val.Kind)
	}
	if et, ok := walk("T1", v, m.typ, ir.PathSegments("et")); !ok || et.addr != "T1.ET" || et.val.Kind != ir.TypeTime {
		t.Errorf("T1.ET by another casing: %v %+v", ok, et)
	}
	for _, s := range v.FB.Def.AllSlots() {
		if strings.HasPrefix(s.Name, "_") {
			if _, ok := walk("T1", v, m.typ, ir.PathSegments(s.Name)); ok {
				t.Errorf("internal %s resolved by path", s.Name)
			}
		}
	}
}

// tonTag stores a TON instance in rt's store under name — the shape the
// editor's watch puts there for a program's FB — and returns it.
func tonTag(t *testing.T, rt *runtime.Runtime, name string) ir.Value {
	t.Helper()
	typ := ir.LookupFBType("TON")
	if typ == nil {
		t.Fatal("no TON")
	}
	v := ir.Value{Kind: ir.TypeFB, FB: ir.NewFBInstance(typ.FB)}
	rt.Tags().Set(name, v)
	return v
}

func intMember(name string, n int64) member {
	return member{addr: "X", val: ir.IntVal(n), typ: &ir.Type{Kind: ir.TypeInt, Name: name}}
}

func TestDataTypeFollowsTheDeclaredWidth(t *testing.T) {
	widths := map[string]uint32{
		"SINT": id.SByte, "USINT": id.Byte, "BYTE": id.Byte,
		"INT": id.Int16, "UINT": id.UInt16, "WORD": id.UInt16,
		"DINT": id.Int32, "UDINT": id.UInt32, "DWORD": id.UInt32,
		"LINT": id.Int64, "ULINT": id.UInt64, "LWORD": id.UInt64,
		"": id.Int64, "dint": id.Int32,
	}
	for name, want := range widths {
		if got := dataType(intMember(name, 1)); got != want {
			t.Errorf("%q → i=%d, want i=%d", name, got, want)
		}
	}
	enum := member{val: ir.IntVal(10), typ: &ir.Type{Kind: ir.TypeInt, Enum: &ir.EnumDef{Name: "Mode"}}}
	if got := dataType(enum); got != id.Int32 {
		t.Errorf("enum → i=%d", got)
	}
	for _, tc := range []struct {
		v    ir.Value
		want uint32
	}{
		{ir.BoolVal(true), id.Boolean},
		{ir.RealVal(1), id.Double},
		{ir.TimeVal(1), id.Duration},
		{ir.StringVal("x"), id.String},
	} {
		if got := dataType(member{val: tc.v}); got != tc.want {
			t.Errorf("%v → i=%d, want i=%d", tc.v.Kind, got, tc.want)
		}
	}
	// An array reports its element type, from a value or, when empty, from
	// the declared type.
	arr := member{val: ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{ir.RealVal(1)}}}
	if got := dataType(arr); got != id.Double {
		t.Errorf("REAL array → i=%d", got)
	}
	empty := member{val: ir.Value{Kind: ir.TypeArray}, typ: &ir.Type{Kind: ir.TypeArray, Elem: &ir.Type{Kind: ir.TypeInt, Name: "INT"}}}
	if got := dataType(empty); got != id.Int16 {
		t.Errorf("empty INT array → i=%d", got)
	}
	if got := dataType(member{val: ir.Value{Kind: ir.TypeArray}}); got != id.BaseDataType {
		t.Errorf("untyped empty array → i=%d", got)
	}
}

func TestScalarConvertsInRangeAndWidensOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		n    int64
		want any
	}{
		{"SINT", -5, int8(-5)}, {"SINT", 200, int64(200)},
		{"USINT", 200, uint8(200)}, {"USINT", -1, int64(-1)},
		{"INT", -30000, int16(-30000)}, {"INT", 40000, int64(40000)},
		{"UINT", 60000, uint16(60000)}, {"UINT", 70000, int64(70000)},
		{"DINT", 1 << 30, int32(1 << 30)}, {"DINT", 1 << 40, int64(1 << 40)},
		{"UDINT", 1<<32 - 1, uint32(1<<32 - 1)}, {"UDINT", -3, int64(-3)},
		{"ULINT", 7, uint64(7)}, {"ULINT", -7, int64(-7)},
		{"LINT", 7, int64(7)}, {"", 7, int64(7)},
	}
	for _, tc := range cases {
		m := intMember(tc.name, tc.n)
		got := scalar(m.val, dataType(m))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %d → %v (%T), want %v (%T)", tc.name, tc.n, got, got, tc.want, tc.want)
		}
	}
	if got := scalar(ir.TimeVal(1500), id.Duration); got != 1500.0 {
		t.Errorf("TIME → %v (%T)", got, got)
	}
	if got := scalar(ir.Value{Kind: ir.TypeStruct}, id.BaseDataType); got != nil {
		t.Errorf("struct scalar → %v", got)
	}
}

func TestVariantEncodesArraysOfEachKind(t *testing.T) {
	arr := func(elem ...ir.Value) ir.Value { return ir.Value{Kind: ir.TypeArray, Arr: elem} }
	typed := func(name string) *ir.Type {
		return &ir.Type{Kind: ir.TypeArray, Elem: &ir.Type{Kind: ir.TypeInt, Name: name}}
	}
	cases := []struct {
		m    member
		want any
	}{
		{member{val: arr(ir.BoolVal(true), ir.BoolVal(false))}, []bool{true, false}},
		{member{val: arr(ir.RealVal(1.5), ir.RealVal(2))}, []float64{1.5, 2}},
		{member{val: arr(ir.TimeVal(10), ir.TimeVal(20))}, []float64{10, 20}},
		{member{val: arr(ir.StringVal("a"), ir.StringVal("b"))}, []string{"a", "b"}},
		{member{val: arr(ir.IntVal(1), ir.IntVal(2))}, []int64{1, 2}},
		{member{val: arr(ir.IntVal(1), ir.IntVal(2)), typ: typed("INT")}, []int16{1, 2}},
		{member{val: arr(ir.IntVal(1), ir.IntVal(2)), typ: typed("USINT")}, ua.ByteArray{1, 2}}, // not a ByteString
		{member{val: arr(ir.IntVal(1), ir.IntVal(2)), typ: typed("UDINT")}, []uint32{1, 2}},
		{member{val: arr(ir.IntVal(1), ir.IntVal(2)), typ: typed("ULINT")}, []uint64{1, 2}},
		// one element that does not fit widens the whole array, so it stays
		// one well-typed array
		{member{val: arr(ir.IntVal(1), ir.IntVal(70000)), typ: typed("INT")}, []int64{1, 70000}},
		{member{val: arr(ir.IntVal(1), ir.IntVal(-1)), typ: typed("DWORD")}, []int64{1, -1}},
	}
	for _, tc := range cases {
		v, err := variant(tc.m)
		if err != nil {
			t.Errorf("%v: %v", tc.want, err)
			continue
		}
		if !v.Has(ua.VariantArrayValues) {
			t.Errorf("%v (%T): not encoded as an array, mask %08b", tc.want, v.Value(), v.EncodingMask())
		}
		if !reflect.DeepEqual(v.Value(), tc.want) {
			t.Errorf("got %v (%T), want %v (%T)", v.Value(), v.Value(), tc.want, tc.want)
		}
	}
	// A scalar is a plain variant with the declared width.
	v, err := variant(intMember("DINT", 7))
	if err != nil || v.Value() != int32(7) {
		t.Errorf("DINT scalar → %v %v", v, err)
	}
	// Composite values have no variant.
	for _, name := range []string{"P101", "Pumps"} {
		if _, err := variant(fixtureMember(t, name)); err == nil {
			t.Errorf("%s encoded as a variant", name)
		}
	}
}

func TestStatusOfQuality(t *testing.T) {
	want := map[nio.Quality]ua.StatusCode{
		nio.Good:         ua.StatusGood,
		nio.Stale:        ua.StatusUncertainLastUsableValue,
		nio.Bad:          ua.StatusBadDeviceFailure,
		nio.NotConnected: ua.StatusBadNotConnected,
	}
	for q, st := range want {
		if got := statusOf(q); got != st {
			t.Errorf("%s → %s, want %s", q, got, st)
		}
	}
}

func TestWantRefFilters(t *testing.T) {
	ref := func(typ uint32, forward bool, class ua.NodeClass) *ua.ReferenceDescription {
		return &ua.ReferenceDescription{ReferenceTypeID: ua.NewNumericNodeID(0, typ), IsForward: forward, NodeClass: class}
	}
	child := ref(id.HasComponent, true, ua.NodeClassVariable)
	parent := ref(id.HasComponent, false, ua.NodeClassObject)
	organizes := ref(id.Organizes, true, ua.NodeClassObject)
	typeDef := ref(id.HasTypeDefinition, true, ua.NodeClassVariableType)
	bd := func(dir ua.BrowseDirection, refType uint32, subtypes bool, mask uint32) *ua.BrowseDescription {
		d := &ua.BrowseDescription{BrowseDirection: dir, IncludeSubtypes: subtypes, NodeClassMask: mask}
		if refType != 0 {
			d.ReferenceTypeID = ua.NewNumericNodeID(0, refType)
		}
		return d
	}
	cases := []struct {
		name string
		bd   *ua.BrowseDescription
		ref  *ua.ReferenceDescription
		want bool
	}{
		{"forward takes forward", bd(ua.BrowseDirectionForward, 0, false, 0), child, true},
		{"forward drops inverse", bd(ua.BrowseDirectionForward, 0, false, 0), parent, false},
		{"inverse takes inverse", bd(ua.BrowseDirectionInverse, 0, false, 0), parent, true},
		{"inverse drops forward", bd(ua.BrowseDirectionInverse, 0, false, 0), child, false},
		{"both takes both", bd(ua.BrowseDirectionBoth, 0, false, 0), parent, true},
		{"no ref type is everything", bd(ua.BrowseDirectionForward, 0, false, 0), typeDef, true},
		{"exact type", bd(ua.BrowseDirectionForward, id.HasComponent, false, 0), child, true},
		{"exact type excludes others", bd(ua.BrowseDirectionForward, id.HasComponent, false, 0), organizes, false},
		{"hierarchical with subtypes takes component", bd(ua.BrowseDirectionForward, id.HierarchicalReferences, true, 0), child, true},
		{"hierarchical with subtypes takes organizes", bd(ua.BrowseDirectionForward, id.HierarchicalReferences, true, 0), organizes, true},
		{"hierarchical drops type definition", bd(ua.BrowseDirectionForward, id.HierarchicalReferences, true, 0), typeDef, false},
		{"abstract type without the subtypes flag still means its subtypes", bd(ua.BrowseDirectionForward, id.HierarchicalReferences, false, 0), child, true},
		{"gopcua's default: References without the flag", bd(ua.BrowseDirectionForward, id.References, false, 0), typeDef, true},
		{"aggregates takes component", bd(ua.BrowseDirectionForward, id.Aggregates, true, 0), child, true},
		{"aggregates drops organizes", bd(ua.BrowseDirectionForward, id.Aggregates, true, 0), organizes, false},
		{"has child takes component", bd(ua.BrowseDirectionForward, id.HasChild, true, 0), child, true},
		{"non-hierarchical takes type definition", bd(ua.BrowseDirectionForward, id.NonHierarchicalReferences, true, 0), typeDef, true},
		{"non-hierarchical drops component", bd(ua.BrowseDirectionForward, id.NonHierarchicalReferences, true, 0), child, false},
		{"references takes everything", bd(ua.BrowseDirectionForward, id.References, true, 0), organizes, true},
		{"foreign namespace ref type takes nothing", &ua.BrowseDescription{ReferenceTypeID: ua.NewNumericNodeID(3, id.HasComponent)}, child, false},
		{"class mask keeps objects", bd(ua.BrowseDirectionForward, 0, false, uint32(ua.NodeClassObject)), organizes, true},
		{"class mask drops variables", bd(ua.BrowseDirectionForward, 0, false, uint32(ua.NodeClassObject)), child, false},
		{"unrelated ref type", bd(ua.BrowseDirectionForward, id.HasProperty, true, 0), child, false},
	}
	for _, tc := range cases {
		if got := wantRef(tc.bd, tc.ref); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

func TestLeafName(t *testing.T) {
	for addr, want := range map[string]string{
		"Level":            "Level",
		"P101.Drive.Speed": "Speed",
		"Pumps[2]":         "Pumps[2]",
		"Pumps[2].Hours":   "Hours",
	} {
		if got := leafName(addr); got != want {
			t.Errorf("%s → %s, want %s", addr, got, want)
		}
	}
}
