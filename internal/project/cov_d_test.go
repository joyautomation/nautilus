package project

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// docs/functions.md#project-tags-are-in-scope: a tag with no type: takes the
// type its init: implies — TRUE BOOL, a number REAL, T#5s TIME, other text
// STRING. Each is read here by a program in a way only that type allows,
// and the seeded values are read back with their kinds.
func TestTagTypeFromInit(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: main.st
tags:
  - { name: Flag,   role: state, init: TRUE }
  - { name: Num,    role: state, init: 5 }
  - { name: Delay,  role: state, init: T#5s }
  - { name: Label,  role: state, init: pump }
  - { name: NotFlag, role: state, type: BOOL }
  - { name: Half,   role: state, type: REAL }
  - { name: Later,  role: state, type: TIME }
  - { name: Shout,  role: state, type: STRING }
`,
		// NOT on a BOOL, / 2.0 that keeps a fraction, TIME arithmetic and
		// CONCAT: each line compiles only against the inferred type.
		"main.st": `PROGRAM Main
NotFlag := NOT Flag;
Half := Num / 2.0;
Later := Delay + T#1s;
Shout := CONCAT(Label, '!');
END_PROGRAM
`,
	})
	kinds := map[string]ir.TypeKind{"Flag": ir.TypeBool, "Num": ir.TypeReal, "Delay": ir.TypeTime, "Label": ir.TypeString}
	for name, kind := range kinds {
		v, err := rt.Tags().ReadGlobal(name)
		if err != nil || v.Kind != kind {
			t.Errorf("%s = %+v (%v), want a %v from its init:", name, v, err, kind)
		}
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("NotFlag"); v.B {
		t.Errorf("NotFlag = TRUE, want NOT TRUE")
	}
	if got := readReal(t, rt, "Half"); got != 2.5 {
		t.Errorf("Half = %v, want 2.5 (Num is REAL 5, not an integer)", got)
	}
	if v, _ := rt.Tags().ReadGlobal("Later"); v.Kind != ir.TypeTime || v.I != 6000 {
		t.Errorf("Later = %+v, want TIME 6000 ms (T#5s + T#1s)", v)
	}
	if v, _ := rt.Tags().ReadGlobal("Shout"); v.S != "pump!" {
		t.Errorf("Shout = %+v, want 'pump!'", v)
	}
}

// A tag's type: may be an ARRAY of a project TYPE: the program writes one
// element's field and the whole array round-trips through the tag store.
func TestTagTypeArrayOfProjectType(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Batches, role: state, type: 'ARRAY[1..3] OF Recipe' }\n",
		"types.st":      "TYPE\n  Recipe : STRUCT\n    Count : INT;\n    Temp : REAL;\n  END_STRUCT;\nEND_TYPE\n",
		"main.st":       "PROGRAM Main\nBatches[2].Count := Batches[2].Count + 7;\nBatches[3].Temp := 80.5;\nEND_PROGRAM\n",
	})
	rt.Scan()
	v, err := rt.Tags().ReadGlobal("Batches")
	if err != nil || v.Kind != ir.TypeArray || len(v.Arr) != 3 {
		t.Fatalf("Batches = %+v (%v), want an ARRAY of 3 Recipe", v, err)
	}
	if v.Arr[1].Kind != ir.TypeStruct || v.Arr[1].Fld[0].I != 7 || v.Arr[2].Fld[1].F != 80.5 || v.Arr[0].Fld[0].I != 0 {
		t.Errorf("Batches = %+v, want [ {0 0} {7 0} {0 80.5} ]", v.Arr)
	}
}

// Naming a tag with neither type: nor init: is an error that says to give
// it a type:.
func TestUntypedTagErrorWording(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Valve, role: output }\n",
		"main.st":       "PROGRAM Main\nValve := TRUE;\nEND_PROGRAM\n",
	})
	if err == nil || !strings.Contains(err.Error(), "give Valve a type:") {
		t.Fatalf("err = %v, want it to say to give Valve a type:", err)
	}
}

// docs/functions.md#project-tags-are-in-scope: a VAR_EXTERNAL that
// disagrees with type: is an error at the declaration, and two programs
// binding one untyped tag with different types is an error naming both.
func TestTagTypeDisagreementsNameTheirSources(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Speed, role: setpoint, type: REAL }\n",
		// The disagreeing declaration is on line 6.
		"main.st": "PROGRAM Main\nVAR\n    x : BOOL;\nEND_VAR\nVAR_EXTERNAL\n    Speed : BOOL;\nEND_VAR\nSpeed := TRUE;\nEND_PROGRAM\n",
	})
	if err == nil || !strings.HasPrefix(err.Error(), "line 6:") || !strings.Contains(err.Error(), "Speed : BOOL disagrees with the tag Speed") {
		t.Fatalf("err = %v, want it at line 6, the VAR_EXTERNAL declaration", err)
	}

	err = newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags:\n  - { name: Count, role: state, init: 0 }\n",
		"a.st":          "PROGRAM A\nVAR_EXTERNAL\n    Count : DINT;\nEND_VAR\nCount := Count + 1;\nEND_PROGRAM\n",
		"b.st":          "PROGRAM B\nVAR\n    x : REAL;\nEND_VAR\nx := Count;\nEND_PROGRAM\n",
	})
	if err == nil {
		t.Fatal("two programs binding Count as DINT and REAL loaded cleanly")
	}
	for _, want := range []string{"task main", "task b", "DINT", "REAL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not name %q", err, want)
		}
	}
}

// docs/functions.md#project-tags-are-in-scope: FUNCTION bodies do not see
// tags implicitly; a FUNCTION reaches a tag through its own VAR_EXTERNAL.
func TestFunctionsDoNotSeeTagsImplicitly(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Level, role: state, init: 5.0 }\n  - { name: Out, role: state, init: 0.0 }\n",
		"lib.st":        "FUNCTION Probe : REAL\nVAR_INPUT\n    k : REAL;\nEND_VAR\nProbe := Level * k;\nEND_FUNCTION\n",
		"main.st":       "PROGRAM Main\nOut := Probe(2.0);\nEND_PROGRAM\n",
	}
	err := newRuntimeErr(t, files)
	if err == nil || !strings.Contains(err.Error(), "FUNCTION Probe") || !strings.Contains(err.Error(), `undeclared identifier "Level"`) {
		t.Fatalf("err = %v, want Level undeclared inside the FUNCTION", err)
	}
	files["lib.st"] = "FUNCTION Probe : REAL\nVAR_INPUT\n    k : REAL;\nEND_VAR\nVAR_EXTERNAL\n    Level : REAL;\nEND_VAR\nProbe := Level * k;\nEND_FUNCTION\n"
	rt := loadRuntime(t, files)
	rt.Scan()
	if got := readReal(t, rt, "Out"); got != 10 {
		t.Errorf("Out = %v, want 10 (Level 5 through the FUNCTION's VAR_EXTERNAL)", got)
	}
}

// docs/functions.md#project-tags-are-in-scope: a GVL global the manifest
// does not declare is a state tag starting at zero — before any scan, at
// the zero of its declared type.
func TestGVLGlobalIsAStateTagAtZero(t *testing.T) {
	proj, err := Load(mapProject(map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\ntags:\n  - { name: Level, role: setpoint, type: REAL, init: 7.5 }\n",
		"gvl.st":        "VAR_GLOBAL\n    StartPB : BOOL;\n    Count : DINT;\n    Level : REAL;\n    Wait : TIME;\nEND_VAR\n",
		"a.st":          "PROGRAM A\nIF StartPB THEN Count := Count + 1; Wait := T#1s; END_IF;\nLevel := Level;\nEND_PROGRAM\n",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]runtime.TagRole{}
	for _, d := range proj.Runtime.Tags {
		roles[d.Name] = d.Role
	}
	for _, name := range []string{"StartPB", "Count", "Wait"} {
		if r, ok := roles[name]; !ok || r != runtime.RoleState {
			t.Errorf("GVL global %s: role %v (declared %v), want a state tag", name, r, ok)
		}
	}
	if roles["Level"] != runtime.RoleSetpoint {
		t.Errorf("Level is declared by the manifest too: role %v, want its manifest role setpoint", roles["Level"])
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]ir.Value{
		"StartPB": ir.BoolVal(false), "Count": {Kind: ir.TypeInt}, "Wait": ir.TimeVal(0),
	} {
		v, err := rt.Tags().ReadGlobal(name)
		if err != nil || v.Kind != want.Kind || v.B || v.I != 0 {
			t.Errorf("%s = %+v (%v) before any scan, want the zero %v", name, v, err, want.Kind)
		}
	}
	if got := readReal(t, rt, "Level"); got != 7.5 {
		t.Errorf("Level = %v, want the manifest's init 7.5", got)
	}
}

// docs/functions.md#types: an element of an array tag assigns directly
// (Levels[2] := 41.0), and the whole array round-trips through the tag
// store, so a second task sees the element and the untouched rest.
func TestArrayTagElementAssignsDirectly(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags:\n" +
			"  - { name: Levels, role: state, type: 'ARRAY[1..3] OF REAL', init: [1.0, 2.0, 3.0] }\n" +
			"  - { name: Sum, role: state, init: 0.0 }\n",
		"a.st": "PROGRAM A\nLevels[2] := 41.0;\nEND_PROGRAM\n",
		"b.st": "PROGRAM B\nSum := Levels[1] + Levels[2] + Levels[3];\nEND_PROGRAM\n",
	})
	rt.Scan()
	if err := rt.ScanTask("b"); err != nil {
		t.Fatal(err)
	}
	v, _ := rt.Tags().ReadGlobal("Levels")
	if len(v.Arr) != 3 || v.Arr[0].F != 1 || v.Arr[1].F != 41 || v.Arr[2].F != 3 {
		t.Errorf("Levels = %+v, want [1 41 3]", v.Arr)
	}
	if got := readReal(t, rt, "Sum"); got != 45 {
		t.Errorf("Sum = %v, want 45 (task b reads the stored array)", got)
	}
}

// docs/functions.md#enumerations: an enumeration-typed tag seeds by member
// name or by number.
func TestEnumTagSeededByNumber(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n" +
			"  - { name: Mode, role: setpoint, type: E_Mode, init: 10 }\n" +
			"  - { name: Other, role: setpoint, type: E_Mode, init: 11 }\n" +
			"  - { name: Running, role: output, type: BOOL }\n",
		"types.st": "TYPE E_Mode : (Idle, Run := 10, Fault); END_TYPE\n",
		"main.st":  "PROGRAM Main\nRunning := Mode = E_Mode#Run AND Other = E_Mode#Fault;\nEND_PROGRAM\n",
	})
	if v, _ := rt.Tags().ReadGlobal("Mode"); v.I != 10 || v.S != "Run" {
		t.Errorf("Mode seeded %+v, want Run (10) from init: 10", v)
	}
	if v, _ := rt.Tags().ReadGlobal("Other"); v.I != 11 || v.S != "Fault" {
		t.Errorf("Other seeded %+v, want Fault (11) from init: 11", v)
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("Running"); !v.B {
		t.Error("Running should be TRUE: both tags hold the members their numbers name")
	}
}

// docs/functions.md#a-ld-library: order never decides whether a call
// resolves — blocks may reference each other in either direction, across
// files. a.st (composed first) instantiates a block from b.st and one from
// a ladder library (composed after every .st); b.st calls a FUNCTION from
// a.st.
func TestLibraryReferencesResolveInEitherDirection(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: In, role: state, type: INT, init: 3 }\n  - { name: Out, role: state, type: INT }\n  - { name: Latched, role: state, type: BOOL }\n",
		"a.st": `FUNCTION Twice : INT
VAR_INPUT
    x : INT;
END_VAR
Twice := x * 2;
END_FUNCTION

FUNCTION_BLOCK Outer
VAR_INPUT
    x : INT;
END_VAR
VAR_OUTPUT
    y : INT;
    q : BOOL;
END_VAR
VAR
    inner : Inner;
    seal : LdSeal;
END_VAR
inner(x := x);
y := inner.y;
seal(Set := x > 0);
q := seal.Q;
END_FUNCTION_BLOCK
`,
		"b.st": `FUNCTION_BLOCK Inner
VAR_INPUT
    x : INT;
END_VAR
VAR_OUTPUT
    y : INT;
END_VAR
y := Twice(x) + 1;
END_FUNCTION_BLOCK
`,
		"c.ld": `FUNCTION_BLOCK LdSeal
VAR_INPUT
    Set : BOOL;
END_VAR
VAR_OUTPUT
    Q : BOOL;
END_VAR
LD
  RUNG seal
    [ Set | Q ] ( Q )
END_LD
END_FUNCTION_BLOCK
`,
		"main.st": "PROGRAM Main\nVAR\n    o : Outer;\nEND_VAR\no(x := In);\nOut := o.y;\nLatched := o.q;\nEND_PROGRAM\n",
	})
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("Out"); v.I != 7 {
		t.Errorf("Out = %+v, want 7 (Twice(3) + 1 across a.st -> b.st -> a.st)", v)
	}
	if v, _ := rt.Tags().ReadGlobal("Latched"); !v.B {
		t.Error("Latched should be TRUE: a.st's block ran the ladder library's LdSeal")
	}
}

// docs/functions.md#dialects: nautilus is the default and adds nothing — a
// project with no dialect: (or dialect: nautilus) has no TONR, logix
// composes it, and the reserved siemens and codesys are accepted.
func TestDialectNamesAndDefault(t *testing.T) {
	prog := "PROGRAM Main\nVAR\n    t : TONR;\nEND_VAR\nt(IN := Go, PT := T#1S, Reset := FALSE);\nDone := t.Q;\nEND_PROGRAM\n"
	tags := "tags:\n  - { name: Go, role: state, init: TRUE }\n  - { name: Done, role: state, init: FALSE }\n"
	for _, d := range []string{"", "dialect: nautilus\n"} {
		err := newRuntimeErr(t, map[string]string{
			"nautilus.yaml": d + "tasks:\n  - program: main.st\n" + tags,
			"main.st":       prog,
		})
		if err == nil || !strings.Contains(err.Error(), "TONR") {
			t.Errorf("%q: err = %v, want TONR unknown (the nautilus dialect adds nothing)", d, err)
		}
	}
	loadRuntime(t, map[string]string{
		"nautilus.yaml": "dialect: logix\ntasks:\n  - program: main.st\n" + tags,
		"main.st":       prog,
	})
	for _, d := range []string{"siemens", "codesys"} {
		if _, err := ReadManifest(mapProject(map[string]string{
			"nautilus.yaml": "dialect: " + d + "\ntasks:\n  - program: main.st\n" + tags,
		}), ""); err != nil {
			t.Errorf("dialect: %s is reserved and must be accepted: %v", d, err)
		}
	}
}
