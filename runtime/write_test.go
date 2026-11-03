package runtime

import (
	"errors"
	"math"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// frontDoorTags is a store with one tag of each kind the front door
// converts to, declared the way a manifest or program declares them.
func frontDoorTags(t *testing.T) *Tags {
	t.Helper()
	mode := &ir.EnumDef{Name: "Mode", Members: []ir.EnumMember{{Name: "Idle", Value: 0}, {Name: "Run", Value: 10}}}
	pump := &ir.StructDef{Name: "Pump", Fields: []ir.StructField{{Name: "Speed", Type: ir.RealT}, {Name: "Count", Type: ir.IntNamed("INT")}}, FieldIndex: map[string]int{"Speed": 0, "Count": 1}}
	tags := NewTags()
	tags.types = map[string]*ir.Type{}
	declare := func(name string, typ *ir.Type, v ir.Value) {
		tags.types[ir.NameKey(name)] = typ
		_ = tags.WriteGlobal(name, v)
	}
	for _, n := range []string{"SINT", "INT", "DINT", "USINT", "UINT", "UDINT", "LINT", "ULINT", "BYTE", "WORD", "LWORD"} {
		declare("I_"+n, ir.IntNamed(n), ir.IntVal(0))
	}
	declare("Flag", ir.BoolT, ir.BoolVal(false))
	declare("SP", ir.RealT, ir.RealVal(0))
	declare("Delay", ir.TimeT, ir.TimeVal(0))
	declare("Name", ir.StringT, ir.StringVal(""))
	declare("Mode", &ir.Type{Kind: ir.TypeInt, Enum: mode}, mode.Val(0))
	pt := &ir.Type{Kind: ir.TypeStruct, Struct: pump}
	declare("P1", pt, ir.Value{Kind: ir.TypeStruct, Struct: pump, Fld: []ir.Value{ir.RealVal(0), ir.IntVal(0)}})
	return tags
}

// The conversion table (docs/design/opcua-server-writes.md §3.2), row by
// row, with the boundary values of each integer width: a value lands
// exactly as sent, or is refused with the kind a protocol maps to its code.
func TestFrontDoorConversions(t *testing.T) {
	ok := error(nil)
	refused := errors.New("any refusal") // refused, with no protocol kind
	for _, tc := range []struct {
		path string
		v    any
		want error
	}{
		{"I_SINT", -128.0, ok}, {"I_SINT", 127.0, ok}, {"I_SINT", 128.0, ir.ErrOutOfRange}, {"I_SINT", -129.0, ir.ErrOutOfRange},
		{"I_INT", int64(-32768), ok}, {"I_INT", int64(32767), ok}, {"I_INT", 70000.0, ir.ErrOutOfRange}, {"I_INT", -32769.0, ir.ErrOutOfRange},
		{"I_DINT", float64(math.MaxInt32), ok}, {"I_DINT", float64(math.MaxInt32) + 1, ir.ErrOutOfRange},
		{"I_USINT", 255.0, ok}, {"I_USINT", 256.0, ir.ErrOutOfRange}, {"I_USINT", -1.0, ir.ErrOutOfRange},
		{"I_UINT", 65535.0, ok}, {"I_UINT", 65536.0, ir.ErrOutOfRange},
		{"I_UDINT", float64(math.MaxUint32), ok}, {"I_UDINT", -1.0, ir.ErrOutOfRange},
		{"I_BYTE", 255.0, ok}, {"I_BYTE", 300.0, ir.ErrOutOfRange}, {"I_WORD", 65536.0, ir.ErrOutOfRange},
		{"I_LINT", int64(math.MinInt64), ok}, {"I_LINT", uint64(1 << 63), ir.ErrOutOfRange}, {"I_LINT", 1e19, ir.ErrOutOfRange},
		// ULINT and LWORD hold any 64-bit pattern, as the VM does (NOT of an
		// LWORD 0 is -1 in the store): an operator can write back what reads.
		{"I_ULINT", uint64(math.MaxInt64), ok}, {"I_ULINT", uint64(math.MaxUint64), ok}, {"I_ULINT", -1.0, ok},
		{"I_LWORD", int64(-1), ok}, {"I_LWORD", uint64(1 << 63), ok},
		{"I_INT", 2.5, ir.ErrTypeMismatch}, {"I_INT", true, ir.ErrTypeMismatch}, {"I_INT", "5", ir.ErrTypeMismatch},
		{"Flag", true, ok}, {"Flag", 1.0, ir.ErrTypeMismatch},
		{"SP", 1.5, ok}, {"SP", int64(7), ok}, {"SP", math.NaN(), ir.ErrOutOfRange}, {"SP", math.Inf(1), ir.ErrOutOfRange}, {"SP", "x", ir.ErrTypeMismatch}, {"SP", false, ir.ErrTypeMismatch},
		{"Delay", 1500.0, ok}, {"Delay", 1500.5, ir.ErrOutOfRange}, {"Delay", "T#1s", ok}, {"Delay", "TIME#1m30s", ok}, {"Delay", "T#1x", refused}, {"Delay", "soon", refused},
		{"Name", "", ok}, {"Name", "batch 7", ok}, {"Name", 7.0, ir.ErrTypeMismatch},
		{"Mode", "Run", ok}, {"Mode", 10.0, ok}, {"Mode", 3.0, ir.ErrOutOfRange}, {"Mode", "Walk", refused},
		{"P1.Speed", 60.0, ok}, {"P1.Count", 40000.0, ir.ErrOutOfRange}, {"P1.Count", 4.0, ok},
		{"Nope", 1.0, ErrUndefined},
	} {
		tags := frontDoorTags(t)
		before, _ := tags.ReadPath(tc.path)
		err := tags.Write([]WriteOp{{Path: tc.path, Value: tc.v}})[0]
		switch {
		case tc.want == refused:
			if err == nil {
				t.Errorf("%s = %q landed; want refused", tc.path, tc.v)
			}
		case tc.want == nil && err != nil:
			t.Errorf("%s = %v (%T): %v, want it to land", tc.path, tc.v, tc.v, err)
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s = %v (%T): %v, want %v", tc.path, tc.v, tc.v, err, tc.want)
		}
		after, _ := tags.ReadPath(tc.path)
		if tc.want != nil && after != before {
			t.Errorf("%s = %v refused but the store moved: %v → %v", tc.path, tc.v, before, after)
		}
		// A refused write never retypes the tag (#421: the kind stays the
		// declared one either way).
		if root, _ := ir.SplitAddress(tc.path); tc.want != ErrUndefined {
			cur, _ := tags.ReadGlobal(root)
			if typ := tags.TypeOf(root); cur.Kind != typ.Kind {
				t.Errorf("%s = %v left a %v in a %v tag", tc.path, tc.v, cur.Kind, typ.Kind)
			}
		}
	}
}

// A forced address, a member under a forced tag, and a tag over a forced
// member are all refused with ErrForced, naming the force.
func TestFrontDoorRefusesForced(t *testing.T) {
	tags := frontDoorTags(t)
	if err := tags.Force("P1.Speed", 5.0); err != nil {
		t.Fatal(err)
	}
	if err := tags.Force("SP", 1.0); err != nil {
		t.Fatal(err)
	}
	errs := tags.Write([]WriteOp{{"SP", 2.0}, {"P1", map[string]any{"Count": 1}}, {"P1.Speed", 9.0}, {"P1.Count", 3.0}})
	for i, want := range []string{"SP", "P1.Speed", "P1.Speed", ""} {
		var fe *ForcedError
		switch {
		case want == "" && errs[i] != nil:
			t.Errorf("item %d: %v, want it to land (P1.Count is not forced)", i, errs[i])
		case want != "" && (!errors.As(errs[i], &fe) || fe.Force != want || !errors.Is(errs[i], ErrForced)):
			t.Errorf("item %d: %v, want forced by %s", i, errs[i], want)
		}
	}
	if v, _ := tags.ReadPath("P1.Count"); v != int64(3) {
		t.Errorf("P1.Count = %v, want 3", v)
	}
}

// One request's items land under one lock: a scan's snapshot, taken from
// another goroutine, sees a Hi/Lo pair from the same request together or
// not at all.
func TestFrontDoorBatchIsAtomic(t *testing.T) {
	tags := NewTags()
	tags.types = map[string]*ir.Type{"lo": ir.RealT, "hi": ir.RealT}
	_ = tags.WriteGlobal("Lo", ir.RealVal(0))
	_ = tags.WriteGlobal("Hi", ir.RealVal(0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 1; i <= 2000; i++ {
			for _, err := range tags.Write([]WriteOp{{"Lo", float64(i)}, {"Hi", float64(i)}}) {
				if err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	for {
		select {
		case <-done:
			return
		default:
		}
		s := tags.Snapshot()
		if s["Lo"].F != s["Hi"].F {
			t.Fatalf("snapshot straddled a request: Lo=%v Hi=%v", s["Lo"].F, s["Hi"].F)
		}
	}
}

// An enumeration member reached through a mapping or a list is held to
// its members too, not only one addressed by path (#501 review).
func TestFrontDoorEnumInsideMapAndList(t *testing.T) {
	mode := &ir.EnumDef{Name: "Mode", Members: []ir.EnumMember{{Name: "Idle", Value: 0}, {Name: "Run", Value: 10}}}
	mt := &ir.Type{Kind: ir.TypeInt, Enum: mode}
	line := &ir.StructDef{Name: "Line", Fields: []ir.StructField{{Name: "M", Type: mt}}, FieldIndex: map[string]int{"M": 0}}
	at := &ir.Type{Kind: ir.TypeArray, Elem: mt, ArrLen: 2}
	tags := NewTags()
	tags.types = map[string]*ir.Type{ir.NameKey("L"): {Kind: ir.TypeStruct, Struct: line}, ir.NameKey("Modes"): at}
	_ = tags.WriteGlobal("L", ir.Value{Kind: ir.TypeStruct, Struct: line, Fld: []ir.Value{mode.Val(0)}})
	_ = tags.WriteGlobal("Modes", ir.Value{Kind: ir.TypeArray, Arr: []ir.Value{mode.Val(0), mode.Val(0)}})
	errs := tags.Write([]WriteOp{
		{"L", map[string]any{"M": 3.0}},
		{"Modes", []any{10.0, 3.0}},
		{"L", map[string]any{"M": "Run"}},
		{"Modes", []any{"Run", 0.0}},
	})
	for i, want := range []error{ir.ErrOutOfRange, ir.ErrOutOfRange, nil, nil} {
		if !errors.Is(errs[i], want) && !(want == nil && errs[i] == nil) {
			t.Errorf("item %d: %v, want %v", i, errs[i], want)
		}
	}
	if v, _ := tags.ReadPath("L.M"); v != "Run" {
		t.Errorf("L.M = %v, want Run", v)
	}
	if v, _ := tags.ReadPath("Modes[0]"); v != "Run" {
		t.Errorf("Modes[0] = %v, want Run", v)
	}
	if v, _ := tags.ReadPath("Modes[1]"); v != "Idle" {
		t.Errorf("Modes[1] = %v, want Idle: the refused list must not land", v)
	}
}
