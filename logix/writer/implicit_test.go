package writer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// A rung that names a manifest tag without a VAR_EXTERNAL (#177/#210) still
// gets its controller tag, typed from Options.Tags; a declared one is not
// written twice, and a tag no rung names is not written at all.
func TestImplicitTagsBecomeControllerTags(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL\n    Start : BOOL;\nEND_VAR\nLD\n  RUNG r1\n    Start CNT_OK ( Motor )\nEND_LD\nEND_PROGRAM\n"
	out, diags, err := Write(src, Options{Tags: map[string]string{"Start": "BOOL", "Motor": "BOOL", "cnt_ok": "BOOL", "Unused": "REAL"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("err %v diags %v", err, diags)
	}
	s := string(out)
	for _, want := range []string{`<Tag Name="Start"`, `<Tag Name="Motor"`, `<Tag Name="cnt_ok"`} {
		if strings.Count(s, want) != 1 {
			t.Errorf("want exactly one %s in:\n%s", want, s)
		}
	}
	if strings.Contains(s, `Name="Unused"`) {
		t.Error("a tag no rung names was written")
	}
}

// stTags writes an ST program and returns its controller tags (name →
// DataType) and program tags, and the routine text.
func stTags(t *testing.T, src string, opts Options) (ctrl, prog map[string]string, text string) {
	t.Helper()
	doc, diags, err := WriteST(src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatal(joinDiags(diags))
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatalf("does not parse: %v\n%s", err, doc)
	}
	ctrl, prog = map[string]string{}, map[string]string{}
	for _, tg := range f.Controller.Tags {
		if _, dup := ctrl[strings.ToLower(tg.Name)]; dup {
			t.Errorf("controller tag %s written twice", tg.Name)
		}
		ctrl[tg.Name] = tg.DataType
	}
	for _, p := range f.Controller.Programs {
		if p.Name != SideProgram {
			for _, tg := range p.Tags {
				prog[tg.Name] = tg.DataType
			}
			text = p.Routines[0].Text
		}
	}
	return ctrl, prog, text
}

// An ST program gets a controller tag for each manifest tag its body names
// without a declaration (#248), wherever the compiler resolves a variable,
// spelled as the manifest spells it however the body cases it.
func TestImplicitTagsInST(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL
    HIALM : BOOL;
END_VAR
VAR
    i : DINT;
    t : TON;
    Mode : INT;
    Buf : ARRAY [0..3] OF REAL;
END_VAR
IF level > HiSP THEN
    HiAlm := TRUE;
END_IF;
CASE Cmd OF
    0: Out := ABS(Level - Bias);
END_CASE;
FOR Idx := 0 TO Last DO
    Buf[Idx] := Gain;
END_FOR;
Mode := 1;
t(IN := Run, PT := T#2S, Q => Delayed);
Timers[Sel](IN := Run);
END_PROGRAM
`
	tags := map[string]string{
		"Level": "REAL", "HiSP": "REAL", "HiAlm": "BOOL", "Cmd": "DINT", "Out": "REAL", "Bias": "REAL",
		"Idx": "DINT", "Last": "DINT", "Gain": "REAL", "Run": "BOOL", "Delayed": "BOOL",
		"Mode":  "REAL", // shadowed by the program's own VAR
		"Sel":   "DINT", // an index in a call's head
		"Spare": "REAL",
	}
	// Timers is undeclared, so its call is a diagnostic; the uses are
	// what this test is about, and they are settled before the body is
	// written.
	lw, err := lowerST(src, Options{Tags: tags}.withDefaults("P"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range lw.ctrlTags {
		got = append(got, tg.Name+":"+tg.DataType)
	}
	want := []string{"HIALM:BOOL", "Level:REAL", "HiSP:REAL", "Cmd:DINT", "Out:REAL", "Bias:REAL", "Idx:DINT", "Last:DINT", "Gain:REAL", "Run:BOOL", "Delayed:BOOL", "Sel:DINT"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("controller tags\n got: %v\nwant: %v", got, want)
	}
	for _, tg := range lw.progTags {
		if tg.Name == "Mode" && tg.DataType != "INT" {
			t.Errorf("the program's own Mode should shadow the tag: %+v", tg)
		}
	}
}

// Names that are not variables never become tags: a member name, a pin
// name, an enumeration literal's member, a function's name, and anything in
// a FUNCTION_BLOCK body declared beside the program.
func TestImplicitTagsSkipNonVariables(t *testing.T) {
	lib := "TYPE\n  Motor : STRUCT\n    Speed : REAL;\n    Run : BOOL;\n  END_STRUCT;\n  Mode : (Idle, Run := 5);\nEND_TYPE\n"
	src := `PROGRAM P
VAR
    t : TON;
    x : REAL;
END_VAR
M1.Speed := 1.0;
t(IN := TRUE, PT := T#1S);
x := ABS(x);
State := Mode#Idle;
END_PROGRAM
`
	ctrl, _, text := stTags(t, src, Options{Libs: []string{lib}, Tags: map[string]string{
		"M1": "Motor", "State": "Mode", "Speed": "REAL", "PT": "DINT", "IN": "BOOL", "ABS": "REAL", "Idle": "DINT", "Spare": "REAL",
	}})
	if len(ctrl) != 2 || ctrl["M1"] != "Motor" || ctrl["State"] != "DINT" {
		t.Errorf("controller tags = %v, want M1:Motor and State:DINT only", ctrl)
	}
	if !strings.Contains(text, "State := 0;") {
		t.Errorf("Mode#Idle should be its value:\n%s", text)
	}
}

// A ladder program's tag uses come from the ST the compiler compiles, not
// from the rung text: a member name and a TIME literal's letters are not
// tags.
func TestImplicitTagsLadderMembersAndLiterals(t *testing.T) {
	lib := "TYPE\n  Motor : STRUCT\n    Speed : REAL;\n    Run : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	src := "PROGRAM P\nVAR\n  t : TON;\nEND_VAR\nLD\n  RUNG r1\n    M1.Run t:TON(PT := T#2S) ( Done )\n  RUNG r2\n    { M1.Speed := Gain * 2.0 }\nEND_LD\nEND_PROGRAM\n"
	f := mustWrite(t, src, Options{Libs: []string{lib}, Tags: map[string]string{
		"M1": "Motor", "Done": "BOOL", "Gain": "REAL", "Speed": "REAL", "Run": "BOOL", "S": "BOOL", "T": "BOOL", "PT": "DINT",
	}})
	got := map[string]string{}
	for _, tg := range f.Controller.Tags {
		got[tg.Name] = tg.DataType
	}
	want := map[string]string{"M1": "Motor", "Done": "BOOL", "Gain": "REAL"}
	if len(got) != len(want) {
		t.Errorf("controller tags = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("tag %s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// Enumerations have no Logix type: a tag of one is a DINT, its initial
// value and every member the program names are the members' values.
func TestEnumerationsAreDINT(t *testing.T) {
	lib := "TYPE\n  MachineState : (Idle := 1, Running := 10, Fault);\nEND_TYPE\n"
	src := `PROGRAM P
VAR
    Local : MachineState := Fault;
END_VAR
CASE Cmd OF
    0: State := Idle;
    1: State := MachineState#Running;
END_CASE;
IF State = Fault THEN
    No := TO_DINT(State);
END_IF;
END_PROGRAM
`
	doc, diags, err := WriteST(src, Options{Libs: []string{lib},
		Tags:  map[string]string{"State": "MachineState", "Cmd": "DINT", "No": "DINT"},
		Inits: map[string]any{"State": "Running"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %s", err, joinDiags(diags))
	}
	s := string(doc)
	for _, want := range []string{
		`<Tag Name="State" TagType="Base" DataType="DINT"`,
		`<Tag Name="Local" TagType="Base" DataType="DINT"`,
		"State := 1;", "State := 10;", "IF (State = 11) THEN", "No := State;",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	f, _ := l5x.Parse(doc)
	vals := map[string]string{}
	for _, tg := range append(f.Controller.Tags, f.Controller.Programs[0].Tags...) {
		vals[tg.Name] = fmt.Sprint(tg.Value)
	}
	if vals["State"] != "10" || vals["Local"] != "11" {
		t.Errorf("initial values State=%q (want 10, the manifest's Running) Local=%q (want 11, Fault)", vals["State"], vals["Local"])
	}

	// A ladder program: compares, assignments and Type#Member operands.
	ldSrc := "PROGRAM Q\nLD\n  RUNG r1\n    EQ(State, Fault) ( Bad )\n  RUNG r2\n    EQ(Cmd, 1) { State := MachineState#Running }\nEND_LD\nEND_PROGRAM\n"
	lf := mustWrite(t, ldSrc, Options{Libs: []string{lib}, Tags: map[string]string{"State": "MachineState", "Cmd": "DINT", "Bad": "BOOL"}})
	var rungs []string
	for _, r := range lf.Controller.Programs[0].Routines[0].Rungs {
		rungs = append(rungs, r.Text)
	}
	joined := strings.Join(rungs, "\n")
	for _, want := range []string{"EQ(State,11)OTE(Bad);", "MOVE(10,State)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}

	// A member name two enumerations share with different values must be
	// qualified.
	two := lib + "TYPE\n  Other : (Idle := 7);\nEND_TYPE\n"
	_, diags, _ = WriteST("PROGRAM P\nState := Idle;\nEND_PROGRAM\n", Options{Libs: []string{two}, Tags: map[string]string{"State": "MachineState"}})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "MachineState#Idle") && !strings.Contains(diags[0].Message, "Type#Idle") {
		t.Errorf("want one diagnostic asking for Type#Idle, got %s", joinDiags(diags))
	}
}

// The online forms carry the implicit tags too: an ST routine's partial
// lists them as context, so an online edit sees the tag set a download
// would create.
func TestImplicitTagsInRoutinePartial(t *testing.T) {
	src := "PROGRAM P\nOut := Level * 2.0;\nEND_PROGRAM\n"
	doc, diags, err := WriteRoutine("p.st", src, Options{Tags: map[string]string{"Out": "REAL", "Level": "REAL"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	for _, want := range []string{`<Tag Name="Out"`, `<Tag Name="Level"`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("missing %s in the routine partial:\n%s", want, doc)
		}
	}
}

// A declaration spelled in another case than the manifest's tag is the
// same tag: it takes the manifest's initial value and description.
func TestDeclarationCaseFindsTheManifestEntry(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL\n    hisp : REAL;\nEND_VAR\nIF HISP > 1.0 THEN\n    HiSP := 0.0;\nEND_IF;\nEND_PROGRAM\n"
	doc, diags, err := WriteST(src, Options{Tags: map[string]string{"HiSP": "REAL"}, Inits: map[string]any{"HiSP": 80.0}, Descs: map[string]string{"HiSP": "high setpoint"}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %s", err, joinDiags(diags))
	}
	s := string(doc)
	if strings.Count(s, "<Tag Name=") != 1 || !strings.Contains(s, `Value="80.0"`) || !strings.Contains(s, "high setpoint") {
		t.Errorf("want one hisp tag carrying 80.0 and its description:\n%s", s)
	}
	if !strings.Contains(s, "IF (hisp > 1.0) THEN") || !strings.Contains(s, "hisp := 0.0;") {
		t.Errorf("references should take the declared spelling:\n%s", s)
	}
}
