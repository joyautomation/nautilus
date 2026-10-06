package project

// The project model of the parity batch: manifest tags in scope in every
// program (#177/#210), GVL files (#175), elementary tag types (#200), and
// TIME / ARRAY members seeded by init: (#201).

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

func mapProject(files map[string]string) fstest.MapFS {
	fs := fstest.MapFS{}
	for name, src := range files {
		fs[name] = &fstest.MapFile{Data: []byte(src)}
	}
	return fs
}

func loadRuntime(t *testing.T, files map[string]string) *runtime.Runtime {
	t.Helper()
	proj, err := Load(mapProject(files), "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return rt
}

func newRuntimeErr(t *testing.T, files map[string]string) error {
	t.Helper()
	proj, err := Load(mapProject(files), "")
	if err != nil {
		return err
	}
	_, err = runtime.New(proj.Runtime)
	return err
}

func readReal(t *testing.T, rt *runtime.Runtime, name string) float64 {
	t.Helper()
	v, err := rt.Tags().ReadGlobal(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if v.Kind == ir.TypeInt {
		return float64(v.I)
	}
	return v.F
}

// #175: a gvl.st holding one VAR_GLOBAL block composes into every
// program's prelude, so two programs both compile, see its globals, and
// share them through the tag store.
func TestGVLComposesIntoEveryProgram(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags: []\n",
		"gvl.st": "(* the Codesys GVL *)\nVAR_GLOBAL\n    StartPB : BOOL;\n    Count : DINT;\n    Level : REAL;\nEND_VAR\n",
		"a.st": "PROGRAM A\nIF NOT StartPB THEN Count := Count + 1; END_IF;\nEND_PROGRAM\n",
		// The explicit IEC form names a GVL global again: legal, same type.
		"b.st": "PROGRAM B\nVAR_EXTERNAL\n    Count : DINT;\nEND_VAR\nLevel := INT_TO_REAL(Count) * 2.0;\nEND_PROGRAM\n",
	})
	rt.Scan()
	if err := rt.ScanTask("b"); err != nil {
		t.Fatal(err)
	}
	if got := readReal(t, rt, "Count"); got != 1 {
		t.Errorf("Count = %v, want 1 (a GVL global starts at zero)", got)
	}
	if got := readReal(t, rt, "Level"); got != 2 {
		t.Errorf("Level = %v, want 2 (task b sees task a's write)", got)
	}
}

// A VAR_EXTERNAL that disagrees with the GVL's declaration is an error at
// the declaration, not a second variable.
func TestGVLRedeclarationMustAgree(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\ntags: []\n",
		"gvl.st":        "VAR_GLOBAL\n    Count : DINT;\nEND_VAR\n",
		"a.st":          "PROGRAM A\nVAR_EXTERNAL\n    Count : BOOL;\nEND_VAR\nCount := TRUE;\nEND_PROGRAM\n",
	})
	if err == nil || !strings.Contains(err.Error(), "disagrees with the global's declaration") {
		t.Fatalf("err = %v, want a disagreement with the GVL", err)
	}
}

// #177/#210: a manifest tag needs no VAR_EXTERNAL; its type comes from
// type: or init:.
func TestManifestTagsAreImplicit(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: main.st
tags:
  - { name: Sensor,   role: input, init: 20.0 }
  - { name: Setpoint, role: setpoint, init: 50 }
  - { name: Running,  role: state, init: false }
  - { name: Count,    role: state, type: DINT }
  - { name: Out,      role: output, type: REAL }
`,
		"main.st": `PROGRAM Main
Out := Setpoint - Sensor;
Running := Out > 0.0;
Count := Count + 1;
END_PROGRAM
`,
	})
	rt.Scan()
	if got := readReal(t, rt, "Out"); got != 30 {
		t.Errorf("Out = %v, want 30", got)
	}
	v, _ := rt.Tags().ReadGlobal("Count")
	if v.Kind != ir.TypeInt || v.I != 1 {
		t.Errorf("Count = %+v, want INT 1 (type: DINT)", v)
	}
	// Only the tags a program names are bound by it.
	if _, bound := rt.Globals()["Unused"]; bound {
		t.Error("an unnamed tag should not be bound")
	}
}

// Implicit tags are recorded where they are used, not for every tag.
func TestImplicitTagsBindOnlyWhereUsed(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: main.st
tags:
  - { name: A, role: state, init: 1.0 }
  - { name: B, role: state, init: 2.0 }
`,
		"main.st": "PROGRAM Main\nA := A + 1.0;\nEND_PROGRAM\n",
	})
	g := rt.Globals()
	if _, ok := g["A"]; !ok {
		t.Error("A is used and must be bound")
	}
	if _, ok := g["B"]; ok {
		t.Error("B is never named and must not be bound")
	}
}

// VAR_EXTERNAL stays legal, and must agree with a stated type:.
func TestExplicitDeclarationMustAgreeWithTypedTag(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Speed, role: setpoint, type: REAL }\n",
		"main.st":       "PROGRAM Main\nVAR_EXTERNAL\n    Speed : REAL;\nEND_VAR\nSpeed := Speed + 1.0;\nEND_PROGRAM\n",
	}
	loadRuntime(t, files)
	files["main.st"] = "PROGRAM Main\nVAR_EXTERNAL\n    Speed : BOOL;\nEND_VAR\nSpeed := TRUE;\nEND_PROGRAM\n"
	err := newRuntimeErr(t, files)
	if err == nil || !strings.Contains(err.Error(), "disagrees with the tag Speed, whose type is REAL") {
		t.Fatalf("err = %v, want the declaration to disagree with type: REAL", err)
	}
}

// An inferred type does not override a program's explicit declaration —
// `Count : DINT` with `init: 0` still seeds an integer — but a second
// program reading the same tag implicitly as the REAL its init implies is
// an error that names both and the fix.
func TestProgramsDisagreeingOnAnUntypedTag(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags:\n  - { name: Count, role: state, init: 0 }\n",
		"a.st":          "PROGRAM A\nVAR_EXTERNAL\n    Count : DINT;\nEND_VAR\nCount := Count + 1;\nEND_PROGRAM\n",
		"b.st":          "PROGRAM B\nVAR\n    x : DINT;\nEND_VAR\nx := 1;\nEND_PROGRAM\n",
	}
	rt := loadRuntime(t, files)
	if v, _ := rt.Tags().ReadGlobal("Count"); v.Kind != ir.TypeInt {
		t.Errorf("Count seeded %v, want INT (the program's declaration)", v.Kind)
	}
	files["b.st"] = "PROGRAM B\nVAR\n    x : REAL;\nEND_VAR\nx := Count;\nEND_PROGRAM\n"
	err := newRuntimeErr(t, files)
	if err == nil || !strings.Contains(err.Error(), "give it a type: in the manifest") {
		t.Fatalf("err = %v, want a disagreement naming the fix", err)
	}
	files["nautilus.yaml"] = "tasks:\n  - program: a.st\n  - name: b\n    program: b.st\ntags:\n  - { name: Count, role: state, type: DINT, init: 0 }\n"
	files["b.st"] = "PROGRAM B\nVAR\n    x : DINT;\nEND_VAR\nx := Count;\nEND_PROGRAM\n"
	loadRuntime(t, files)
}

// A tag with neither type: nor init: cannot be typed; naming it without a
// declaration says how to fix it instead of "undeclared".
func TestUntypedTagSaysGiveItAType(t *testing.T) {
	err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Valve, role: output }\n",
		"main.st":       "PROGRAM Main\nValve := TRUE;\nEND_PROGRAM\n",
	})
	if err == nil || !strings.Contains(err.Error(), `"Valve" is a project tag with no type`) {
		t.Fatalf("err = %v", err)
	}
}

// A local of a tag's name shadows it (IEC scoping): the program's own
// variable, not the tag. naut check warns; the runtime runs it.
func TestLocalShadowsTag(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Level, role: state, init: 5.0 }\n",
		"main.st":       "PROGRAM Main\nVAR\n    Level : REAL;\nEND_VAR\nLevel := 99.0;\nEND_PROGRAM\n",
	})
	rt.Scan()
	if got := readReal(t, rt, "Level"); got != 5 {
		t.Errorf("tag Level = %v, want 5 (the local shadows it)", got)
	}
}

// Library function blocks do NOT see tags implicitly: a block reaches a
// tag only through its own VAR_EXTERNAL.
func TestFunctionBlocksDoNotSeeTagsImplicitly(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Level, role: state, init: 5.0 }\n",
		"lib.st":        "FUNCTION_BLOCK Probe\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nQ := Level;\nEND_FUNCTION_BLOCK\n",
		"main.st":       "PROGRAM Main\nVAR\n    p : Probe;\nEND_VAR\np();\nEND_PROGRAM\n",
	}
	err := newRuntimeErr(t, files)
	if err == nil || !strings.Contains(err.Error(), `undeclared identifier "Level"`) {
		t.Fatalf("err = %v, want Level undeclared inside the block", err)
	}
	files["lib.st"] = "FUNCTION_BLOCK Probe\nVAR_OUTPUT\n    Q : REAL;\nEND_VAR\nVAR_EXTERNAL\n    Level : REAL;\nEND_VAR\nQ := Level;\nEND_FUNCTION_BLOCK\n"
	loadRuntime(t, files)
}

// #200: type: takes every IEC elementary type, and init: must agree.
func TestElementaryTagTypes(t *testing.T) {
	var tags strings.Builder
	elementary := []string{"BOOL", "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT",
		"BYTE", "WORD", "DWORD", "LWORD", "REAL", "LREAL", "TIME", "STRING"}
	for _, ty := range elementary {
		tags.WriteString("  - { name: T_" + ty + ", role: setpoint, type: " + ty + " }\n")
	}
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n" + tags.String() +
			"  - { name: FT101_Raw, role: input, type: INT, init: 0 }\n" +
			"  - { name: Profile, role: setpoint, type: 'ARRAY[1..3] OF REAL', init: [1.5, 2.5] }\n",
		"main.st": "PROGRAM Main\nT_INT := FT101_Raw + 1;\nEND_PROGRAM\n",
	})
	want := map[string]ir.TypeKind{"BOOL": ir.TypeBool, "INT": ir.TypeInt, "LWORD": ir.TypeInt,
		"REAL": ir.TypeReal, "LREAL": ir.TypeReal, "TIME": ir.TypeTime, "STRING": ir.TypeString}
	for ty, kind := range want {
		v, err := rt.Tags().ReadGlobal("T_" + ty)
		if err != nil || v.Kind != kind {
			t.Errorf("T_%s = %+v (%v), want a zero %v", ty, v, err, kind)
		}
	}
	if v, _ := rt.Tags().ReadGlobal("FT101_Raw"); v.Kind != ir.TypeInt {
		t.Errorf("FT101_Raw seeded %v, want INT", v.Kind)
	}
	v, _ := rt.Tags().ReadGlobal("Profile")
	if v.Kind != ir.TypeArray || len(v.Arr) != 3 || v.Arr[1].F != 2.5 || v.Arr[2].F != 0 {
		t.Errorf("Profile = %+v, want [1.5 2.5 0]", v)
	}
}

func TestTagTypeAndInitMustAgree(t *testing.T) {
	cases := map[string]string{
		"  - { name: N, role: setpoint, type: INT, init: 2.5 }\n":   "tag N (type INT): init: want INT, got a number",
		"  - { name: N, role: setpoint, type: BOOL, init: 0 }\n":    "tag N (type BOOL): init: want BOOL, got a number",
		"  - { name: N, role: setpoint, type: TIME, init: soon }\n": "tag N (type TIME): init: want TIME",
		"  - { name: N, role: setpoint, type: Widget }\n":           "tag N: type Widget is neither an IEC elementary type",
		"  - { name: N, role: setpoint, type: TON }\n":              "TON is a function block",
	}
	for tag, want := range cases {
		err := newRuntimeErr(t, map[string]string{
			"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n" + tag,
			"main.st":       "PROGRAM Main\nEND_PROGRAM\n",
		})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s→ err = %v, want %q", tag, err, want)
		}
	}
}

// #201: a recipe's step times seed from init: — T#3s, 3s, or ms — and so
// do nested struct and array members.
func TestTimeMembersSeedFromInit(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: main.st
tags:
  - name: RecipeA
    role: setpoint
    type: DoseRecipe
    init: { TargetL: 10.0, SettleTime: T#2s, MixTime: 1m30s, DrainTime: 500, Steps: [T#1s, 2s], Limits: { Hold: TIME#250ms } }
  - { name: Delay, role: setpoint, init: T#5s }
`,
		"types.st": `TYPE
  Limits : STRUCT
    Hold : TIME;
  END_STRUCT;
  DoseRecipe : STRUCT
    TargetL    : REAL;
    SettleTime : TIME;
    MixTime    : TIME;
    DrainTime  : TIME;
    Steps      : ARRAY[0..2] OF TIME;
    Limits     : Limits;
  END_STRUCT;
END_TYPE
`,
		"main.st": "PROGRAM Main\nVAR\n    t : TON;\nEND_VAR\nt(IN := TRUE, PT := RecipeA.SettleTime + Delay);\nEND_PROGRAM\n",
	})
	v, err := rt.Tags().ReadGlobal("RecipeA")
	if err != nil {
		t.Fatal(err)
	}
	field := func(name string) ir.Value {
		i, _ := v.Struct.FieldOf(name)
		return v.Fld[i]
	}
	for name, ms := range map[string]int64{"SettleTime": 2000, "MixTime": 90000, "DrainTime": 500} {
		if f := field(name); f.Kind != ir.TypeTime || f.I != ms {
			t.Errorf("%s = %+v, want TIME %d ms", name, f, ms)
		}
	}
	if s := field("Steps"); s.Arr[0].I != 1000 || s.Arr[1].I != 2000 || s.Arr[2].I != 0 {
		t.Errorf("Steps = %+v", s.Arr)
	}
	lim := field("Limits")
	if lim.Fld[0].I != 250 {
		t.Errorf("Limits.Hold = %+v, want 250 ms", lim.Fld[0])
	}
	if d, _ := rt.Tags().ReadGlobal("Delay"); d.Kind != ir.TypeTime || d.I != 5000 {
		t.Errorf("Delay = %+v, want TIME 5000 (init: T#5s implies TIME)", d)
	}
}

// An implicit struct-typed tag is the program's own TYPE, not a lookalike:
// it binds to a block's VAR_IN_OUT (passed by reference, so the types must
// be identical) without a VAR_EXTERNAL.
func TestImplicitStructTagBindsInOut(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: RecipeA, role: setpoint, type: Recipe, init: { Count: 1 } }\n",
		"lib.st": `TYPE
  Recipe : STRUCT
    Count : INT;
  END_STRUCT;
END_TYPE
FUNCTION_BLOCK Bump
VAR_IN_OUT
    R : Recipe;
END_VAR
R.Count := R.Count + 1;
END_FUNCTION_BLOCK
`,
		"main.st": "PROGRAM Main\nVAR\n    b : Bump;\nEND_VAR\nb(R := RecipeA);\nEND_PROGRAM\n",
	})
	rt.Scan()
	v, _ := rt.Tags().ReadGlobal("RecipeA")
	if v.Fld[0].I != 2 {
		t.Errorf("RecipeA.Count = %+v, want 2", v.Fld[0])
	}
}

// #200 with #238: a tag typed by a project enumeration seeds from a member
// name, is in scope by that type, and an operator's write of a member name
// or its integer lands as the named value.
func TestEnumTypedTag(t *testing.T) {
	rt := loadRuntime(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Mode, role: setpoint, type: E_Mode, init: Run }\n  - { name: Running, role: output, type: BOOL }\n",
		"types.st":      "TYPE E_Mode : (Idle, Run := 10, Fault); END_TYPE\n",
		"main.st":       "PROGRAM Main\nRunning := Mode = E_Mode#Run;\nEND_PROGRAM\n",
	})
	if v, _ := rt.Tags().ReadGlobal("Mode"); v.I != 10 || v.S != "Run" {
		t.Errorf("Mode seeded %+v, want Run (10)", v)
	}
	rt.Scan()
	if v, _ := rt.Tags().ReadGlobal("Running"); !v.B {
		t.Error("Running should be TRUE")
	}
	if err := rt.Tags().SetPath("Mode", "Fault"); err != nil {
		t.Fatal(err)
	}
	if v, _ := rt.Tags().ReadGlobal("Mode"); v.S != "Fault" {
		t.Errorf("after writing \"Fault\": %+v", v)
	}
	if err := rt.Tags().SetPath("Mode", 10.0); err != nil {
		t.Fatal(err)
	}
	if v, _ := rt.Tags().ReadGlobal("Mode"); v.S != "Run" || v.I != 10 {
		t.Errorf("after writing 10: %+v, want Run", v)
	}
	if err := rt.Tags().SetPath("Mode", "Walk"); err == nil || !strings.Contains(err.Error(), "not a member of E_Mode") {
		t.Errorf("a non-member write: %v", err)
	}
	if err := newRuntimeErr(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - { name: Mode, role: setpoint, type: E_Mode, init: Walk }\n",
		"types.st":      "TYPE E_Mode : (Idle, Run := 10, Fault); END_TYPE\n",
		"main.st":       "PROGRAM Main\nEND_PROGRAM\n",
	}); err == nil || !strings.Contains(err.Error(), `"Walk" is not a member of E_Mode`) {
		t.Errorf("init: Walk → %v", err)
	}
}
