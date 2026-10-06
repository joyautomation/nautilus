package runtime_test

import (
	"strings"
	"testing"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #247: a write that reaches an enumerated member by path — the API's
// SetPath, a force, a struct-shaped write — coerces through the same rule
// as a whole-tag write: a member name (any case, Type#-qualified) or the
// integer lands as the NAMED value; any other name is an error that lists
// the members. Arrays of structs are addressed [n] from their declared
// lower bound.
const recipeLib = `
TYPE
  Mode : (Idle, Run := 10, Fault) := Idle;
  Step : STRUCT
    Mode : Mode;
    Secs : REAL;
  END_STRUCT;
  Recipe : STRUCT
    Mode  : Mode;
    First : Step;
    Steps : ARRAY[1..3] OF Step;
  END_STRUCT;
END_TYPE
`

const recipeProg = `
PROGRAM Main
VAR_EXTERNAL
    R     : Recipe;
    Line  : ARRAY[0..2] OF Step;
    Cmd   : Mode;
    Echo  : Mode;
END_VAR
Echo := R.Steps[2].Mode;
END_PROGRAM
`

func recipeRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:   recipeProg,
		Libraries: []string{recipeLib},
		Driver:    nio.NewMemory(),
		Tags: []runtime.TagDef{
			runtime.Typed("R", runtime.RoleState, "Recipe",
				runtime.Init(map[string]any{"Mode": "Run", "Steps": []any{map[string]any{"Mode": "Fault"}}})),
			runtime.Typed("Line", runtime.RoleState, "ARRAY[0..2] OF Step"),
			runtime.Typed("Cmd", runtime.RoleState, "Mode"),
			runtime.Typed("Echo", runtime.RoleState, "Mode"),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

func TestSetPathEnumMemberByName(t *testing.T) {
	rt := recipeRuntime(t)
	tags := rt.Tags()
	if v, _ := tags.ReadPath("R.Mode"); v != "Run" {
		t.Fatalf("seeded R.Mode = %v, want Run (struct init: by name)", v)
	}
	if v, _ := tags.ReadPath("R.Steps[1].Mode"); v != "Fault" {
		t.Fatalf("seeded R.Steps[1].Mode = %v, want Fault", v)
	}
	for _, c := range []struct {
		path string
		v    any
		want any
	}{
		{"R.Mode", "Fault", "Fault"},
		{"r.mode", "idle", "Idle"},
		{"R.Mode", "Mode#Run", "Run"},
		{"R.Mode", 11.0, "Fault"}, // JSON's number for the member's integer
		{"R.First.Mode", "Run", "Run"},
		{"R.Steps[2].Mode", "Run", "Run"},
		{"Line[0].Mode", "Fault", "Fault"},
		{"Line[2].Mode", 10.0, "Run"},
		{"Cmd", "Run", "Run"}, // whole tag, through the same rule
	} {
		if err := tags.SetPath(c.path, c.v); err != nil {
			t.Errorf("SetPath(%s, %v): %v", c.path, c.v, err)
			continue
		}
		if got, _ := tags.ReadPath(c.path); got != c.want {
			t.Errorf("SetPath(%s, %v) → %v, want %v", c.path, c.v, got, c.want)
		}
	}
	// The program reads the member the operator wrote, as the enum value.
	rt.Scan()
	if v, _ := tags.ReadPath("Echo"); v != "Run" {
		t.Errorf("Echo = %v after a scan, want Run", v)
	}
	// The integer underneath is the member's.
	if v, _ := tags.ReadGlobal("R"); v.Fld[2].Arr[1].Fld[0].I != 10 {
		t.Errorf("R.Steps[2].Mode integer = %d, want 10", v.Fld[2].Arr[1].Fld[0].I)
	}
}

func TestSetPathEnumNonMemberIsAnError(t *testing.T) {
	rt := recipeRuntime(t)
	tags := rt.Tags()
	for _, c := range []struct {
		path string
		v    any
		want string
	}{
		{"R.Mode", "Stop", `tag R.Mode: "Stop" is not a member of Mode (Idle, Run, Fault)`},
		{"R.Steps[3].Mode", "Stop", `tag R.Steps[3].Mode: "Stop" is not a member of Mode (Idle, Run, Fault)`},
		{"Line[1].Mode", "Stop", `tag Line[1].Mode: "Stop" is not a member of Mode`},
		{"Cmd", "Stop", `tag Cmd: "Stop" is not a member of Mode (Idle, Run, Fault)`},
		{"R.Steps[0].Mode", "Run", "index out of bounds 1..3"},
		{"Line[3].Mode", "Run", "index out of bounds 0..2"},
	} {
		err := tags.SetPath(c.path, c.v)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SetPath(%s, %v): err = %v, want %q", c.path, c.v, err, c.want)
		}
	}
	if v, _ := tags.ReadPath("R.Mode"); v != "Run" {
		t.Errorf("R.Mode = %v after refused writes, want the seeded Run", v)
	}
	if _, ok := tags.All()["Line[3]"]; ok {
		t.Error("an indexed write created a junk tag")
	}
	tags.Set("Line[9]", 1.0)
	if _, ok := tags.All()["Line[9]"]; ok {
		t.Error("Set of an indexed name created a junk tag")
	}
}

func TestSetPathStructWriteCoercesEnumsDeep(t *testing.T) {
	rt := recipeRuntime(t)
	tags := rt.Tags()
	err := tags.SetPath("R", map[string]any{
		"First": map[string]any{"Mode": "Fault"},
		"Steps": []any{nil, map[string]any{"Mode": "Idle", "Secs": 4.0}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]any{"R.Mode": "Run", "R.First.Mode": "Fault", "R.Steps[1].Mode": "Fault", "R.Steps[2].Mode": "Idle", "R.Steps[2].Secs": 4.0} {
		if got, _ := tags.ReadPath(path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

func TestForceEnumMemberByName(t *testing.T) {
	rt := recipeRuntime(t)
	tags := rt.Tags()
	if err := tags.Force("R.Steps[2].Mode", "Fault"); err != nil {
		t.Fatal(err)
	}
	if got, _ := tags.ReadPath("R.Steps[2].Mode"); got != "Fault" {
		t.Fatalf("forced R.Steps[2].Mode = %v", got)
	}
	if f := tags.ForcedOverlap("R.Steps[2]"); f != "R.Steps[2].Mode" {
		t.Errorf("ForcedOverlap(R.Steps[2]) = %q", f)
	}
	// An operator write elsewhere in the struct leaves the force holding.
	if err := tags.SetPath("R.Steps[2].Secs", 9.0); err != nil {
		t.Fatal(err)
	}
	if got, _ := tags.ReadPath("R.Steps[2].Mode"); got != "Fault" {
		t.Errorf("after a sibling write R.Steps[2].Mode = %v, want the forced Fault", got)
	}
	if err := tags.Force("R.Mode", "Halt"); err == nil || !strings.Contains(err.Error(), "(Idle, Run, Fault)") {
		t.Errorf("force of a non-member: %v", err)
	}
	if err := tags.Force("Cmd", "Fault"); err != nil {
		t.Fatalf("whole-tag enum force by name: %v", err)
	}
	if got, _ := tags.ReadPath("Cmd"); got != "Fault" {
		t.Errorf("forced Cmd = %v", got)
	}
	if !tags.Unforce("R.Steps[2].Mode") {
		t.Error("unforce by the indexed address failed")
	}
}
