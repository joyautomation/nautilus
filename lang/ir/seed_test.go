package ir

import "testing"

// levelT/motorT mirror the shape a real UDT tag test wants: a struct nested
// two levels deep, exactly what Pomona WRD's Motor1Speed/LevelControl pair
// looks like — a member seeded per-tag, the rest zero.
func levelStructDef() *StructDef {
	sd := &StructDef{
		Name: "Level",
		Fields: []StructField{
			{Name: "CTL1HSP", Type: RealT},
			{Name: "CTL1LSP", Type: RealT},
		},
		FieldIndex: map[string]int{"CTL1HSP": 0, "CTL1LSP": 1},
	}
	return sd
}

func motorStructDef() *StructDef {
	lvl := levelStructDef()
	sd := &StructDef{
		Name: "Motor",
		Fields: []StructField{
			{Name: "STRTTMRSP", Type: IntT},
			{Name: "REMOTE", Type: BoolT},
			{Name: "LVL", Type: &Type{Kind: TypeStruct, Struct: lvl}},
		},
		FieldIndex: map[string]int{"STRTTMRSP": 0, "REMOTE": 1, "LVL": 2},
	}
	return sd
}

func motorType() *Type {
	return &Type{Kind: TypeStruct, Struct: motorStructDef()}
}

func TestSeedFromInitNilIsZero(t *testing.T) {
	mt := motorType()
	v, err := SeedFromInit(mt, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Zero(mt)
	if v.Fld[0].I != want.Fld[0].I || v.Fld[1].B != want.Fld[1].B {
		t.Errorf("nil init did not produce zero-of-type: %+v", v)
	}
}

// A member the map omits stays at zero of its own field type; a member it
// names, at any depth, takes the given value.
func TestSeedFromInitSetsGivenMembersOnly(t *testing.T) {
	mt := motorType()
	init := map[string]any{
		"STRTTMRSP": 30,
		"LVL": map[string]any{
			"CTL1HSP": 60.0,
			"CTL1LSP": 40.0,
		},
	}
	v, err := SeedFromInit(mt, init)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != TypeStruct || v.Struct != mt.Struct {
		t.Fatalf("seeded value lost its StructDef: %+v", v)
	}
	if got := v.Fld[0].I; got != 30 {
		t.Errorf("STRTTMRSP = %d, want 30", got)
	}
	if got := v.Fld[1].B; got != false {
		t.Errorf("REMOTE = %v, want zero (false) — it was not in the map", got)
	}
	lvl := v.Fld[2]
	if lvl.Kind != TypeStruct {
		t.Fatalf("LVL did not seed as a struct: %+v", lvl)
	}
	if got := lvl.Fld[0].F; got != 60.0 {
		t.Errorf("LVL.CTL1HSP = %v, want 60", got)
	}
	if got := lvl.Fld[1].F; got != 40.0 {
		t.Errorf("LVL.CTL1LSP = %v, want 40", got)
	}
}

// A wholly nil/omitted nested struct still comes back correctly shaped —
// zero-of-type for LVL — when the map never mentions it at all.
func TestSeedFromInitLeavesUnmentionedNestedStructZero(t *testing.T) {
	mt := motorType()
	v, err := SeedFromInit(mt, map[string]any{"STRTTMRSP": 5})
	if err != nil {
		t.Fatal(err)
	}
	lvl := v.Fld[2]
	if lvl.Kind != TypeStruct || lvl.Struct == nil {
		t.Fatalf("LVL lost its shape when omitted from init: %+v", lvl)
	}
	if lvl.Fld[0].F != 0 || lvl.Fld[1].F != 0 {
		t.Errorf("LVL fields should be zero, got %+v", lvl.Fld)
	}
}

func TestSeedFromInitUnknownMemberIsAnError(t *testing.T) {
	mt := motorType()
	_, err := SeedFromInit(mt, map[string]any{"STRTTMRS": 30})
	if err == nil {
		t.Fatal("an unknown member was accepted")
	}
	const want = "init: unknown member STRTTMRS (did you mean STRTTMRSP?)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestSeedFromInitUnknownNestedMemberNamesThePath(t *testing.T) {
	mt := motorType()
	_, err := SeedFromInit(mt, map[string]any{"LVL": map[string]any{"CTL1HP": 1.0}})
	if err == nil {
		t.Fatal("an unknown nested member was accepted")
	}
	const want = "init.LVL: unknown member CTL1HP (did you mean CTL1HSP?)"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// A struct-typed tag given a bare scalar init is an error, not a silently
// ignored value — before SeedFromInit existed, this shape hit no error path
// at all (Tags.Set's switch has no struct-map case), which is the bug this
// closes.
func TestSeedFromInitScalarOnStructIsAnError(t *testing.T) {
	mt := motorType()
	_, err := SeedFromInit(mt, 42.0)
	if err == nil {
		t.Fatal("a scalar init against a struct type was accepted")
	}
	if got := err.Error(); got == "" {
		t.Fatal("expected a message naming the mismatch")
	}
}

func TestSeedFromInitTypeMismatchOnALeaf(t *testing.T) {
	mt := motorType()
	_, err := SeedFromInit(mt, map[string]any{"STRTTMRSP": "thirty"})
	if err == nil {
		t.Fatal("a string against an INT member was accepted")
	}
	const want = "init.STRTTMRSP: want INT, got a string"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// A YAML integer literal (65) and a float with no fraction (65.0) must both
// seed a REAL member the same way the top-level (untyped) init: already
// treats them — only genuinely fractional values are REAL-only.
func TestSeedFromInitAcceptsIntForReal(t *testing.T) {
	sd := &StructDef{
		Name:       "AI",
		Fields:     []StructField{{Name: "HHSP", Type: RealT}},
		FieldIndex: map[string]int{"HHSP": 0},
	}
	at := &Type{Kind: TypeStruct, Struct: sd}
	v, err := SeedFromInit(at, map[string]any{"HHSP": 2800})
	if err != nil {
		t.Fatal(err)
	}
	if v.Fld[0].F != 2800.0 {
		t.Errorf("HHSP = %v, want 2800", v.Fld[0].F)
	}
}

func TestSeedFromInitScalarType(t *testing.T) {
	v, err := SeedFromInit(RealT, 5)
	if err != nil {
		t.Fatal(err)
	}
	if v.Kind != TypeReal || v.F != 5.0 {
		t.Errorf("scalar init on a scalar type = %+v, want REAL 5.0", v)
	}
}
