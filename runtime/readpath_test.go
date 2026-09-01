package runtime_test

import (
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// Tags.ReadPath is the read counterpart of SetPath: "Tag" or
// "Tag.Member.Sub", resolved the same way (exact tag name wins over member
// resolution, then a step per dotted segment). setPathRuntime and its Motor
// (with a nested Limits) type, declared in setpath_test.go, cover exactly
// the shapes this needs.

func TestReadPathScalarLeaf(t *testing.T) {
	rt := setPathRuntime(t)
	v, ok := rt.Tags().ReadPath("P101.Speed")
	if !ok {
		t.Fatal("P101.Speed did not resolve")
	}
	if f, isFloat := v.(float64); !isFloat || f != 42.0 {
		t.Errorf("P101.Speed = %#v (%T), want float64 42", v, v)
	}
}

func TestReadPathNestedLeaf(t *testing.T) {
	rt := setPathRuntime(t)
	v, ok := rt.Tags().ReadPath("P101.LVL.HSP")
	if !ok {
		t.Fatal("P101.LVL.HSP did not resolve")
	}
	if f, isFloat := v.(float64); !isFloat || f != 80.0 {
		t.Errorf("P101.LVL.HSP = %#v (%T), want float64 80", v, v)
	}
}

// Every leaf kind collapses to its plain Go scalar, not an ir.Value — a
// caller compares against a literal without importing ir.
func TestReadPathLeafKinds(t *testing.T) {
	rt := setPathRuntime(t)
	if err := rt.Tags().SetPath("P101.START", true); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if err := rt.Tags().SetPath("P101.Starts", 7.0); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	if err := rt.Tags().SetPath("P101.Name", "P-101A"); err != nil {
		t.Fatalf("SetPath: %v", err)
	}
	cases := []struct {
		path string
		want any
	}{
		{"P101.START", true},
		{"P101.Starts", int64(7)},
		{"P101.Name", "P-101A"},
		{"P101.Speed", 42.0},
	}
	for _, c := range cases {
		got, ok := rt.Tags().ReadPath(c.path)
		if !ok {
			t.Errorf("%s did not resolve", c.path)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %#v (%T), want %#v (%T)", c.path, got, got, c.want, c.want)
		}
	}
}

// A struct sub-tree — one level short of a leaf — comes back as ir.Value,
// since there is no lossless plain form for it.
func TestReadPathStructSubTree(t *testing.T) {
	rt := setPathRuntime(t)
	got, ok := rt.Tags().ReadPath("P101.LVL")
	if !ok {
		t.Fatal("P101.LVL did not resolve")
	}
	v, isIR := got.(ir.Value)
	if !isIR || v.Kind != ir.TypeStruct || v.Struct == nil || v.Struct.Name != "Limits" {
		t.Fatalf("P101.LVL = %#v, want a Limits ir.Value", got)
	}
	if v.Fld[v.Struct.FieldIndex["HSP"]].F != 80.0 {
		t.Errorf("P101.LVL.HSP inside the sub-tree = %v, want 80", v.Fld[v.Struct.FieldIndex["HSP"]].F)
	}
}

// A whole struct tag with no dotted path at all is the same case as above —
// the "exact tag name" branch, not the member walk.
func TestReadPathWholeStructTag(t *testing.T) {
	rt := setPathRuntime(t)
	got, ok := rt.Tags().ReadPath("P101")
	if !ok {
		t.Fatal("P101 did not resolve")
	}
	v, isIR := got.(ir.Value)
	if !isIR || v.Kind != ir.TypeStruct || v.Struct == nil || v.Struct.Name != "Motor" {
		t.Fatalf("P101 = %#v, want a Motor ir.Value", got)
	}
}

func TestReadPathUnknownTag(t *testing.T) {
	rt := setPathRuntime(t)
	if _, ok := rt.Tags().ReadPath("Nope"); ok {
		t.Error("Nope resolved, want false")
	}
	if _, ok := rt.Tags().ReadPath("Nope.Field"); ok {
		t.Error("Nope.Field resolved, want false")
	}
}

func TestReadPathUnknownMember(t *testing.T) {
	rt := setPathRuntime(t)
	if _, ok := rt.Tags().ReadPath("P101.Sped"); ok {
		t.Error("P101.Sped (misspelled) resolved, want false")
	}
	if _, ok := rt.Tags().ReadPath("P101.LVL.HSPX"); ok {
		t.Error("P101.LVL.HSPX (misspelled nested) resolved, want false")
	}
}

// A dotted path whose root is a scalar tag, not a struct, must fail rather
// than panic — TempSP is a REAL with no members at all.
func TestReadPathNonStructRoot(t *testing.T) {
	rt := setPathRuntime(t)
	if _, ok := rt.Tags().ReadPath("TempSP.Hi"); ok {
		t.Error("TempSP.Hi resolved on a scalar root, want false")
	}
	// The whole tag still reads fine.
	got, ok := rt.Tags().ReadPath("TempSP")
	if !ok || got != 65.0 {
		t.Errorf("TempSP = %#v, %v, want 65.0, true", got, ok)
	}
}

// A tag whose own name contains a dot wins over member resolution — the
// same rule SetPath documents and enforces. Nothing in nautilus creates one
// through the operator-facing Set/SetPath (a name with "." always means a
// member address there), but a driver's seed can, so exercise it that way:
// a fresh runtime seeded with a literal "A.B" tag.
func TestReadPathExactNameWinsOverMemberWalk(t *testing.T) {
	rt, err := runtime.New(runtime.Options{
		Program: "PROGRAM Main\nVAR_EXTERNAL X : REAL; END_VAR\nEND_PROGRAM",
		Seed:    nio.Values{"A.B": 3.5},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, ok := rt.Tags().ReadPath("A.B")
	if !ok || got != 3.5 {
		t.Errorf("A.B = %#v, %v, want 3.5, true (the literal tag, not a member walk into A)", got, ok)
	}
}

// Concurrent reads and member writes to the same tag must not race — run
// with -race.
func TestReadPathConcurrentWithWrites(t *testing.T) {
	rt := setPathRuntime(t)
	done := make(chan struct{}, 3)
	go func() { defer func() { done <- struct{}{} }(); _ = rt.Tags().SetPath("P101.Speed", 12.5) }()
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 100; i++ {
			rt.Tags().ReadPath("P101.Speed")
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 100; i++ {
			rt.Tags().ReadPath("P101.LVL.HSP")
		}
	}()
	for i := 0; i < 3; i++ {
		<-done
	}
}
