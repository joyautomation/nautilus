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
