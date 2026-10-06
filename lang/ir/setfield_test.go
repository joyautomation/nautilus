package ir

import (
	"strings"
	"testing"
)

// writeMotorType is a two-level UDT: enough to exercise a leaf, a nested leaf,
// and a merge that only partly addresses the nested struct.
func writeMotorType() *Type {
	limits := &StructDef{
		Name:       "Limits",
		Fields:     []StructField{{Name: "HSP", Type: RealT}, {Name: "LSP", Type: RealT}},
		FieldIndex: map[string]int{"HSP": 0, "LSP": 1},
	}
	motor := &StructDef{
		Name: "Motor",
		Fields: []StructField{
			{Name: "START", Type: BoolT},
			{Name: "Starts", Type: IntT},
			{Name: "Speed", Type: RealT},
			{Name: "Name", Type: StringT},
			{Name: "LVL", Type: &Type{Kind: TypeStruct, Struct: limits}},
		},
		FieldIndex: map[string]int{"START": 0, "Starts": 1, "Speed": 2, "Name": 3, "LVL": 4},
	}
	return &Type{Kind: TypeStruct, Struct: motor}
}

func seeded(t *testing.T) Value {
	t.Helper()
	v, err := SeedFromInit(writeMotorType(), map[string]any{
		"Speed": 42.0,
		"LVL":   map[string]any{"HSP": 80.0, "LSP": 20.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSetFieldLeafAndNested(t *testing.T) {
	base := seeded(t)
	got, err := SetField(base, []string{"START"}, true, "tag P101")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fld[0].B {
		t.Error("START not set")
	}
	got, err = SetField(got, []string{"LVL", "HSP"}, 95.5, "tag P101")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fld[4].Fld[0].F != 95.5 {
		t.Errorf("LVL.HSP = %v", got.Fld[4].Fld[0].F)
	}
	// The source value is untouched: the tag store hands out values whose
	// Fld slice may still back the last scan's snapshot.
	if base.Fld[0].B || base.Fld[4].Fld[0].F != 80.0 {
		t.Error("SetField mutated its input instead of copying")
	}
}

// The one deliberate difference from SeedFromInit: a map WRITE merges, a map
// INIT zero-fills.
func TestSetFieldMapMergesWhereSeedZeroFills(t *testing.T) {
	base := seeded(t)
	merged, err := SetField(base, nil, map[string]any{"START": true}, "tag P101")
	if err != nil {
		t.Fatal(err)
	}
	if merged.Fld[2].F != 42.0 || merged.Fld[4].Fld[0].F != 80.0 {
		t.Errorf("merge zeroed the members it did not name: %v", merged.Fld)
	}
	seed, err := SeedFromInit(writeMotorType(), map[string]any{"START": true})
	if err != nil {
		t.Fatal(err)
	}
	if seed.Fld[2].F != 0 {
		t.Error("SeedFromInit no longer zero-fills — the two are supposed to differ")
	}
}

func TestSetFieldCoercesToTheMembersOwnType(t *testing.T) {
	base := seeded(t)
	// A whole-numbered float lands on an INT member as an INT (JSON and YAML
	// both have one number kind), and the member does not get retyped.
	got, err := SetField(base, []string{"Starts"}, 7.0, "tag P101")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fld[1].Kind != TypeInt || got.Fld[1].I != 7 {
		t.Errorf("Starts = %v %s, want INT 7", got.Fld[1].I, got.Fld[1].Kind)
	}
	if _, err := SetField(base, []string{"Starts"}, 7.5, "tag P101"); err == nil {
		t.Error("a fractional number was accepted for an INT member")
	}
	// A typed value from a driver assigns directly when its kind agrees.
	got, err = SetField(base, []string{"Speed"}, RealVal(3.5), "tag P101")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fld[2].F != 3.5 {
		t.Errorf("Speed = %v", got.Fld[2].F)
	}
	if _, err := SetField(base, []string{"Speed"}, BoolVal(true), "tag P101"); err == nil {
		t.Error("a BOOL value was accepted for a REAL member")
	}
}

func TestSetFieldErrors(t *testing.T) {
	base := seeded(t)
	cases := []struct {
		name string
		path []string
		v    any
		want string
	}{
		{"unknown member", []string{"STRAT"}, true,
			"tag P101: unknown member STRAT (did you mean START?)"},
		{"nested unknown member", []string{"LVL", "HSPX"}, 1.0,
			"tag P101.LVL: unknown member HSPX (did you mean HSP?)"},
		{"leaf is not a struct", []string{"Speed", "Hi"}, 1.0,
			"tag P101.Speed is a REAL, not a struct — it has no member Hi"},
		{"type mismatch", []string{"START"}, 1.0,
			"tag P101.START: want BOOL, got a number"},
		{"string member", []string{"Name"}, 1.0,
			"tag P101.Name: want STRING, got a number"},
		{"scalar into a struct", []string{"LVL"}, 1.0,
			"tag P101.LVL: Limits is a struct — a write must be a mapping of member: value, not a number"},
		{"empty segment", []string{"LVL", ""}, 1.0,
			"tag P101.LVL: empty member name in the path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := SetField(base, c.path, c.v, "tag P101")
			if err == nil {
				t.Fatal("accepted")
			}
			if err.Error() != c.want {
				t.Errorf("error = %q, want %q", err.Error(), c.want)
			}
		})
	}
	// A bad key deep in a merge names the path it reached, not just the tag.
	_, err := SetField(base, nil, map[string]any{"LVL": map[string]any{"HSPX": 1.0}}, "tag P101")
	if err == nil || !strings.HasPrefix(err.Error(), "tag P101.LVL: unknown member HSPX") {
		t.Errorf("merge error = %v", err)
	}
}

// #247: a recipe UDT with an enumerated member, a nested struct holding
// one, and an array of structs (from 1) holding one — every place a write by
// path can reach an enumeration.
func recipeType() *Type {
	mode := &Type{Kind: TypeInt, Enum: &EnumDef{Name: "Mode", Members: []EnumMember{{"Idle", 0}, {"Run", 10}, {"Fault", 11}}}}
	step := &StructDef{
		Name:       "Step",
		Fields:     []StructField{{Name: "Mode", Type: mode}, {Name: "Secs", Type: RealT}},
		FieldIndex: map[string]int{"Mode": 0, "Secs": 1},
	}
	stepT := &Type{Kind: TypeStruct, Struct: step}
	recipe := &StructDef{
		Name: "Recipe",
		Fields: []StructField{
			{Name: "Mode", Type: mode},
			{Name: "First", Type: stepT},
			{Name: "Steps", Type: &Type{Kind: TypeArray, Elem: stepT, ArrLen: 3, ArrLoBound: 1}},
		},
		FieldIndex: map[string]int{"Mode": 0, "First": 1, "Steps": 2},
	}
	return &Type{Kind: TypeStruct, Struct: recipe}
}

func TestSetFieldEnumMemberByName(t *testing.T) {
	rt := recipeType()
	base := Zero(rt)
	for _, c := range []struct {
		path string
		v    any
		want Value
	}{
		{"Mode", "Run", Value{Kind: TypeInt, I: 10, S: "Run"}},
		{"Mode", "run", Value{Kind: TypeInt, I: 10, S: "Run"}},          // case-insensitive
		{"Mode", "Mode#Fault", Value{Kind: TypeInt, I: 11, S: "Fault"}}, // qualified
		{"Mode", 10, Value{Kind: TypeInt, I: 10, S: "Run"}},             // the member's integer, named
		{"Mode", 10.0, Value{Kind: TypeInt, I: 10, S: "Run"}},           // as JSON spells it
		{"Mode", 7, Value{Kind: TypeInt, I: 7}},                         // no member: kept, unnamed (as TO_Mode(7))
		{"First.Mode", "Fault", Value{Kind: TypeInt, I: 11, S: "Fault"}},
		{"Steps[2].Mode", "Run", Value{Kind: TypeInt, I: 10, S: "Run"}},
		{"Steps[3].Mode", "Idle", Value{Kind: TypeInt, I: 0, S: "Idle"}},
	} {
		got, err := SetFieldTyped(base, rt, PathSegments(c.path), c.v, "tag R")
		if err != nil {
			t.Errorf("%s := %v: %v", c.path, c.v, err)
			continue
		}
		leaf, _, err := FieldAt(got, rt, PathSegments(c.path), "R")
		if err != nil {
			t.Fatal(err)
		}
		if leaf.Kind != c.want.Kind || leaf.I != c.want.I || leaf.S != c.want.S {
			t.Errorf("%s := %v stored %+v, want %+v", c.path, c.v, leaf, c.want)
		}
	}
	// Even untyped, a member below a struct knows its type from the StructDef.
	got, err := SetField(base, []string{"First", "Mode"}, "Run", "tag R")
	if err != nil || got.Fld[1].Fld[0].S != "Run" {
		t.Errorf("untyped First.Mode := Run: %+v, %v", got.Fld[1].Fld[0], err)
	}
}

func TestSetFieldEnumNonMemberListsMembers(t *testing.T) {
	rt := recipeType()
	for _, c := range []struct {
		path string
		v    any
		want string
	}{
		{"Mode", "Stop", `tag R.Mode: "Stop" is not a member of Mode (Idle, Run, Fault)`},
		{"Steps[1].Mode", "Stop", `tag R.Steps[1].Mode: "Stop" is not a member of Mode (Idle, Run, Fault)`},
		{"Mode", "Other#Run", `"Other#Run" is not a member of Mode`},
		{"Mode", true, "want a member of Mode (Idle, Run, Fault), got a boolean"},
		{"Steps[4].Mode", "Run", "tag R.Steps[4]: index out of bounds 1..3"},
		{"Steps[0].Mode", "Run", "index out of bounds 1..3"},
		{"Mode[1]", "Run", "not an array"},
		{"Steps[x]", "Run", `"[x]" is not an index`},
	} {
		_, err := SetFieldTyped(Zero(rt), rt, PathSegments(c.path), c.v, "tag R")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s := %v: err = %v, want %q", c.path, c.v, err, c.want)
		}
	}
}

// A mapping (and a list inside one) merges member by member, enumerations
// coerced at every depth — what a struct write over the API sends.
func TestSetFieldMergeCoercesEnumsInArraysOfStructs(t *testing.T) {
	rt := recipeType()
	got, err := SetFieldTyped(Zero(rt), rt, nil, map[string]any{
		"Mode":  "Run",
		"Steps": []any{map[string]any{"Mode": "Fault"}, nil, map[string]any{"Mode": 10, "Secs": 2.5}},
	}, "tag R")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fld[0].S != "Run" || got.Fld[2].Arr[0].Fld[0].S != "Fault" || got.Fld[2].Arr[1].Fld[0].S != "Idle" ||
		got.Fld[2].Arr[2].Fld[0].S != "Run" || got.Fld[2].Arr[2].Fld[1].F != 2.5 {
		t.Errorf("merged: %+v", got)
	}
	if _, err := SetFieldTyped(Zero(rt), rt, nil, map[string]any{"Steps": []any{nil, nil, nil, nil}}, "tag R"); err == nil {
		t.Error("a list longer than the array was accepted")
	}
	if _, err := SetFieldTyped(Zero(rt), rt, nil, map[string]any{"Steps": []any{map[string]any{"Mode": "Nope"}}}, "tag R"); err == nil ||
		!strings.Contains(err.Error(), `tag R.Steps[1].Mode: "Nope" is not a member of Mode (Idle, Run, Fault)`) {
		t.Errorf("err = %v", err)
	}
}

// struct init: seeds enum members by name inside nested structs and arrays
// of structs, and a non-member names the members.
func TestSeedEnumMembersInArraysOfStructs(t *testing.T) {
	rt := recipeType()
	v, err := SeedFromInit(rt, map[string]any{
		"Mode":  "Run",
		"First": map[string]any{"Mode": "Fault"},
		"Steps": []any{map[string]any{"Mode": "Run"}, map[string]any{"Mode": 11}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.Fld[0].S != "Run" || v.Fld[1].Fld[0].S != "Fault" || v.Fld[2].Arr[0].Fld[0].S != "Run" || v.Fld[2].Arr[1].Fld[0].S != "Fault" {
		t.Errorf("seeded: %+v", v)
	}
	_, err = SeedFromInit(rt, map[string]any{"Steps": []any{map[string]any{"Mode": "Walk"}}})
	if err == nil || !strings.Contains(err.Error(), `init.Steps[1].Mode: "Walk" is not a member of Mode (Idle, Run, Fault)`) {
		t.Errorf("err = %v", err)
	}
}

func TestPathSegments(t *testing.T) {
	for in, want := range map[string]string{
		"":              "",
		"Speed":         "Speed",
		"Drive.Speed":   "Drive|Speed",
		"Steps[2].Mode": "Steps|[2]|Mode",
		"[1].Mode":      "[1]|Mode",
		"Grid[1][2]":    "Grid|[1]|[2]",
		"Grid[1, 2].X":  "Grid|[1]|[2]|X",
	} {
		if got := strings.Join(PathSegments(in), "|"); got != want {
			t.Errorf("PathSegments(%q) = %q, want %q", in, got, want)
		}
	}
	root, segs := SplitAddress("Recipes[2].Mode")
	if root != "Recipes" || strings.Join(segs, "|") != "[2]|Mode" {
		t.Errorf("SplitAddress = %q %q", root, segs)
	}
	if got := JoinPath("Recipes", segs); got != "Recipes[2].Mode" {
		t.Errorf("JoinPath = %q", got)
	}
}
