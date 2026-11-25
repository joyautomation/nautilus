package writer

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var structGlobals = []Global{
	{Name: "WL101", Type: "Well", Start: map[string]any{"depth": 0.45, "Pump": map[string]any{"Run": true}}},
	{Name: "WL102", Type: "WELL", Retain: true},
}

// TestStructsGolden: the struct fixture's SCL (#1299).
func TestStructsGolden(t *testing.T) {
	out := golden(t, "structs", structGlobals)
	files := map[string]string{}
	for _, f := range out.Files {
		files[f.Path] = string(f.Data)
	}
	db, fb := files["naut_Tags.db"], files["naut_Main.scl"]
	motor, well := files["naut_Motor.udt"], files["naut_Well.udt"]
	for _, c := range []struct{ name, got, want string }{
		// Every STRUCT is its PLC data type, named with the prefix, and
		// carries the TYPE's initial values (#1301 D1b).
		{"Motor", motor, "TYPE \"naut_Motor\"\nVERSION : 0.1\n   STRUCT\n      Run : BOOL;\n      Speed : REAL := 50.0;\n      Starts : INT;\n   END_STRUCT;\n\nEND_TYPE\n"},
		// A struct member names its own type; a reserved member name is
		// quoted.
		{"Well", well, "      Pump : \"naut_Motor\";\n"},
		{"Well", well, "      \"Name\" : DINT := 7;\n"},
		// A tag is of the type; a retained one sits in the RETAIN section.
		{"tags DB", db, "   VAR \n      WL101 : \"naut_Well\";\n   END_VAR\n"},
		{"tags DB", db, "   VAR RETAIN\n      WL102 : \"naut_Well\";\n   END_VAR\n"},
		// The manifest's map overrides in BEGIN, matched as nautilus
		// matches it (case-insensitively), at any depth, each member as
		// the TYPE spells it; what it does not name keeps the type's.
		{"tags DB", db, "\nBEGIN\n   WL101.Pump.Run := TRUE;\n   WL101.Depth := 0.45;\n\nEND_DATA_BLOCK\n"},
	} {
		if !strings.Contains(c.got, c.want) {
			t.Errorf("%s lacks %q:\n%s", c.name, c.want, c.got)
		}
	}
	// The types come first, each after the types it holds: TIA
	// generates a source only once what it names exists.
	if len(out.Files) < 2 || out.Files[0].Path != "naut_Motor.udt" || out.Files[1].Path != "naut_Well.udt" {
		t.Errorf("file order: %v", paths(out))
	}
	for _, want := range []string{
		// Member arithmetic is nautilus's 64-bit sum wrapped at the store,
		// typed from the member's declared INT.
		`"naut_Tags".WL101.Pump.Starts := LINT_TO_INT((((INT_TO_LINT("naut_Tags".WL101.Pump.Starts) + 1`,
		`"naut_Tags".WL102."Name" := LINT_TO_DINT((((DINT_TO_LINT("naut_Tags".WL101."Name") + 1`,
		// Variables are of the type, a whole struct copied whole.
		"      spare : \"naut_Motor\";\n",
		"      t : \"naut_Motor\";\n",
		"\t#last := \"naut_Tags\".WL101.Pump;\n",
		// A struct temp starts every scan at its type's initial values.
		"BEGIN\n\t#t.Run := FALSE;\n\t#t.Speed := 50.0;\n\t#t.Starts := 0;\n",
		// A reserved member name is quoted.
		`"naut_Tags".WL102."Static" := "naut_Tags".WL101."Static";`,
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("FB lacks %q:\n%s", want, fb)
		}
	}
	for path, want := range map[string]string{
		"WL101.Pump.Run": `"naut_Tags".WL101.Pump.Run`,
		"wl101.depth":    `"naut_Tags".WL101.Depth`, // as declared
		"WL101.Static":   `"naut_Tags".WL101."Static"`,
		"WL101":          `"naut_Tags".WL101`,
		"Main.last.Run":  `"naut_Main_DB".last.Run`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%s) = %s %v, want %s", path, got, ok, want)
		}
	}
	for _, path := range []string{"WL101.Pump.Gone", "WL101.Depth.x", "Main.t.Run", "Main.last.Run.x", "WL101.Pump."} {
		if got, ok := out.Symbol(path); ok {
			t.Errorf("Symbol(%s) = %s, want none", path, got)
		}
	}
}

func writeStructs(src string, globals []Global, libs ...string) (Output, []Diag) {
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "p.st", Source: src, Libs: libs}},
		Globals:     globals,
		Tasks:       []Task{{Name: "main", Program: "P", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil {
		panic(err)
	}
	return out, diags
}

const motorLib = "TYPE\n  Motor : STRUCT\n    Run : BOOL;\n    Speed : REAL;\n    Tag : WSTRING;\n  END_STRUCT;\n  Pump : STRUCT\n    Run : BOOL;\n    Count : UINT;\n  END_STRUCT;\nEND_TYPE\n"

// TestStructFromLibrary: a library's TYPE types a tag and a program's
// variable, as nautilus composes the library ahead of the program.
func TestStructFromLibrary(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL P1 : Pump; END_VAR\nVAR n : DINT; END_VAR\nP1.Count := P1.Count - 1;\nn := 3;\nEND_PROGRAM\n"
	out, diags := writeStructs(src, []Global{{Name: "P1", Type: "Pump", Start: map[string]any{"Count": 5.0}}}, motorLib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	var fb, db string
	for _, f := range out.Files {
		switch f.Path {
		case "naut_P.scl":
			fb = string(f.Data)
		case "naut_Tags.db":
			db = string(f.Data)
		}
	}
	// UINT wraps modulo 65536, as nautilus stores 0 - 1.
	if !strings.Contains(fb, `"naut_Tags".P1.Count := LINT_TO_UINT(((UINT_TO_LINT("naut_Tags".P1.Count) - 1) MOD LINT#65536 + LINT#65536) MOD LINT#65536);`) {
		t.Errorf("FB:\n%s", fb)
	}
	if !strings.Contains(db, "   P1.Count := 5;\n") {
		t.Errorf("DB:\n%s", db)
	}
}

// TestStructRefusals: what the writer cannot write of a struct, and what
// nautilus's seeding refuses, are refused, each once.
func TestStructRefusals(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL M1, M2, M3 : Motor; END_VAR\nM1.Run := TRUE;\nEND_PROGRAM\n"
	_, diags := writeStructs(src, []Global{{Name: "M1", Type: "Motor"}, {Name: "M2", Type: "Motor"}, {Name: "M3", Type: "Motor"}}, motorLib)
	if len(diags) != 1 || diags[0].Rule != "siemens/type" || !strings.Contains(diags[0].Message, "tag M1: the type Motor: member Tag: the Siemens writer doesn't translate WSTRING yet (#1303)") {
		t.Errorf("STRING member: %v", diags)
	}
	src = "PROGRAM P\nVAR_EXTERNAL P1 : Pump; END_VAR\nP1.Run := TRUE;\nEND_PROGRAM\n"
	for _, c := range []struct {
		start any
		want  string
	}{
		{map[string]any{"Spin": true}, "nautilus refuses the start value: init: unknown member Spin"},
		{map[string]any{"Run": 3.0}, "want BOOL"},
		{map[string]any{"Count": 1.5}, "init.Count: want UINT"},
		{true, "is a struct"},
	} {
		_, diags := writeStructs(src, []Global{{Name: "P1", Type: "Pump", Start: c.start}}, motorLib)
		if len(diags) != 1 || diags[0].Rule != "siemens/init" || !strings.Contains(diags[0].Message, c.want) {
			t.Errorf("start %v: %v, want %q", c.start, diags, c.want)
		}
	}
	// An enumeration is B's second half: still the writer's gap.
	_, diags = writeStructs("PROGRAM P\nVAR m : Mode; END_VAR\nEND_PROGRAM\n", nil, "TYPE Mode : (Idle, Run); END_TYPE\n")
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "the type Mode yet (#1299)") {
		t.Errorf("enum: %v", diags)
	}
}

// TestReservedNamesQuotedEverywhere: a local named as SCL reserves is
// quoted where it is declared, reset, read, written and called, and a
// member prints as its TYPE spells it.
func TestReservedNamesQuotedEverywhere(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL P1 : Pump; END_VAR\nVAR Name : INT; Static : TON; Author : SR; END_VAR\nVAR_TEMP Title : BOOL; END_VAR\nName := Name + 1;\nStatic(IN := TRUE, PT := T#1s);\nTitle := Static.Q;\nAuthor(S1 := Title, R := FALSE);\np1.count := 2;\nEND_PROGRAM\n"
	out, diags := writeStructs(src, []Global{{Name: "P1", Type: "Pump"}}, motorLib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	var fb string
	for _, f := range out.Files {
		if f.Path == "naut_P.scl" {
			fb = string(f.Data)
		}
	}
	for _, want := range []string{
		"      \"Name\" : INT;\n", "      \"Static\" : TON_TIME;\n", "      \"Title\" : BOOL;\n",
		"\t#\"Title\" := FALSE;\n",
		"\t#\"Name\" := LINT_TO_INT(",
		"\t#\"Static\"(IN := TRUE, PT := T#1s, Q => #naut_Static_last.Q, ET => #naut_Static_last.ET);\n",
		"\t#\"Title\" := #naut_Static_last.Q;\n",
		"\t\"naut_Tags\".P1.Count := 2;\n",
		"\t#\"Author\".S1 := #\"Title\";\n",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("FB lacks %q:\n%s", want, fb)
		}
	}
}

// TestDeclTypeNeverDropsAStartValue: a start value that is not a map, or
// names a member the struct lacks, is refused even where nautilus's
// seeding did not run first.
func TestDeclTypeNeverDropsAStartValue(t *testing.T) {
	w := &writer{lib: loadLibrary([]string{motorLib}, nil)}
	if _, why := w.overrides("Pump", "P1", map[string]any{"Run": true, "Spin": true}); !strings.Contains(why, "names a member Pump does not have") {
		t.Errorf("unknown key: %q", why)
	}
	if _, why := w.overrides("Pump", "P1", true); !strings.Contains(why, "not a mapping") {
		t.Errorf("not a map: %q", why)
	}
	w.lib.types = nil
	if why := w.seedCheck("Pump", map[string]any{"Run": true}); why == "" {
		t.Error("an unresolved TYPE table let the start value through unchecked")
	}
}

// TestRefusedOncePerType: many variables of a type the writer cannot
// write yet say so once per program, and a bare enumeration member names
// the enum gap rather than an unknown name.
func TestRefusedOncePerType(t *testing.T) {
	_, diags := writeStructs("PROGRAM P\nVAR a, b : Motor; c : Motor; m1, m2 : Motor; END_VAR\nEND_PROGRAM\n", nil, motorLib)
	if len(diags) != 2 || !strings.Contains(diags[0].Message, "member Tag:") {
		t.Errorf("once per type: %v", diags)
	}
	_, diags = writeStructs("PROGRAM P\nVAR m : INT; END_VAR\nIF m = 0 THEN m := 1; END_IF;\nEND_PROGRAM\n", nil, "TYPE Mode : (Idle, Run); END_TYPE\n")
	if len(diags) != 0 {
		t.Fatalf("no enum used: %v", diags)
	}
	_, diags = writeStructs("PROGRAM P\nVAR_EXTERNAL k : INT; END_VAR\nVAR m : BOOL; END_VAR\nm := Run = Run;\nEND_PROGRAM\n", []Global{{Name: "k", Type: "INT"}}, "TYPE Mode : (Idle, Run); END_TYPE\n")
	if len(diags) == 0 || diags[0].Rule != "siemens/enum" || !strings.Contains(diags[0].Message, "enumerations (Mode) yet (#1299)") {
		t.Errorf("bare member: %v", diags)
	}
}

// TestStartValueEdges: the Sonnet review's cases (#1311). A negative
// start value is one literal, never an empty one; a value out of its
// type's range, a TYPE two programs declare differently, a struct
// VAR_IN_OUT, and a start value naming one member twice are refused; a
// REAL in exponent form keeps its decimal point; a TIME seeds from a bare
// duration, as nautilus seeds it.
func TestStartValueEdges(t *testing.T) {
	lib := "TYPE M : STRUCT i : INT; s : SINT := -3; r : REAL; d : TIME; w : WORD; END_STRUCT; END_TYPE\n"
	src := "PROGRAM P\nVAR_EXTERNAL k : INT; m : M; END_VAR\nk := 1;\nEND_PROGRAM\n"
	db := func(globals []Global) (string, []Diag) {
		out, diags := writeStructs(src, globals, lib)
		for _, f := range out.Files {
			if f.Path == "naut_Tags.db" {
				return string(f.Data), diags
			}
		}
		return "", diags
	}
	got, diags := db([]Global{{Name: "k", Type: "INT", Init: "-5"}, {Name: "m", Type: "M", Start: map[string]any{"i": -5.0, "r": 1e20, "d": "5s"}}})
	for _, want := range []string{"k : INT := -5;", "m.i := -5;", "m.r := 1.0e+20;", "m.d := T#5s;"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %v\n%s", want, diags, got)
		}
	}
	for start, want := range map[string]any{
		"is out of INT's range":     map[string]any{"i": 40000.0},
		"is out of WORD's range":    map[string]any{"w": -1.0},
		"is beyond REAL's range":    map[string]any{"r": 1e40},
		"names I and i, one member": map[string]any{"i": 1.0, "I": 2.0},
	} {
		_, diags := db([]Global{{Name: "k", Type: "INT"}, {Name: "m", Type: "M", Start: want}})
		if len(diags) != 1 || !strings.Contains(diags[0].Message, start) {
			t.Errorf("%v: %v, want %q", want, diags, start)
		}
	}
	// Two programs, one TYPE name, two bodies: refused by name.
	_, diags, _ = Write(Input{Programs: []Program{
		{Path: "a.st", Source: "TYPE N : STRUCT a : INT := 5; END_STRUCT; END_TYPE\nPROGRAM A\nVAR s : N; END_VAR\ns.a := s.a + 1;\nEND_PROGRAM\n"},
		{Path: "b.st", Source: "TYPE N : STRUCT a : REAL := 1.5; END_STRUCT; END_TYPE\nPROGRAM B\nVAR s : N; END_VAR\ns.a := 2.0;\nEND_PROGRAM\n"},
	}})
	if len(diags) != 2 || !strings.Contains(diags[0].Message, "the TYPE N, which two programs declare differently") {
		t.Errorf("conflicting TYPEs: %v", diags)
	}
	// The same declaration in two programs is one type.
	_, diags, _ = Write(Input{Programs: []Program{
		{Path: "a.st", Source: "TYPE N : STRUCT a : INT := 5; END_STRUCT; END_TYPE\nPROGRAM A\nVAR s : N; END_VAR\nEND_PROGRAM\n"},
		{Path: "b.st", Source: "TYPE N : STRUCT\n  a : INT := 5;\nEND_STRUCT; END_TYPE\nPROGRAM B\nVAR s : N; END_VAR\nEND_PROGRAM\n"},
	}, BlockPrefix: "naut_"})
	if len(diags) != 0 {
		t.Errorf("one TYPE declared twice alike: %v", diags)
	}
	_, diags = writeStructs("PROGRAM P\nVAR_IN_OUT io : M; END_VAR\nEND_PROGRAM\n", nil, lib)
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "a struct in a program's VAR_IN_OUT") {
		t.Errorf("VAR_IN_OUT: %v", diags)
	}
	// A TYPE member's initial value the writer cannot print is refused
	// once, with the member, never written as an empty value.
	_, diags = writeStructs(strings.Replace(src, "m : M", "m, m2 : Q", 1), []Global{{Name: "k", Type: "INT"}, {Name: "m", Type: "Q"}, {Name: "m2", Type: "Q"}}, "TYPE Q : STRUCT i : INT := 1 + 2; END_STRUCT; END_TYPE\n")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "member i: its initial value") || diags[0].Path == "" {
		t.Errorf("member initial value: %v", diags)
	}
	// A literal TIA's TIME cannot hold is refused, not written empty.
	_, diags = writeStructs(strings.Replace(src, "m : M", "m : Q", 1), []Global{{Name: "k", Type: "INT"}, {Name: "m", Type: "Q"}}, "TYPE Q : STRUCT d : TIME := T#30d; END_STRUCT; END_TYPE\n")
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "member d: its initial value: the Siemens writer doesn't translate the time literal 30d yet (#1304): it is beyond TIA") {
		t.Errorf("TIME beyond range: %v", diags)
	}
}

// paths are the generated files' paths, in order.
func paths(out Output) []string {
	var p []string
	for _, f := range out.Files {
		p = append(p, f.Path)
	}
	return p
}

// d1bLib declares two STRUCTs (one holding the other), a block with a
// STRUCT pin of each direction and a function returning a STRUCT (#1301
// D1b).
const d1bLib = `TYPE
  M : STRUCT run : BOOL; speed : REAL := 50.0; END_STRUCT;
  W : STRUCT pump : M; depth : REAL; END_STRUCT;
  Unused : STRUCT label : WSTRING; END_STRUCT;
END_TYPE
FUNCTION_BLOCK H
VAR_INPUT i : M; END_VAR
VAR_OUTPUT o : M; END_VAR
VAR_IN_OUT io : W; END_VAR
o := i;
io.pump.run := i.run;
END_FUNCTION_BLOCK
FUNCTION F : M
VAR_INPUT x : REAL; m : M; END_VAR
F := m;
F.speed := x;
END_FUNCTION
`

func fileMap(out Output) map[string]string {
	f := map[string]string{}
	for _, x := range out.Files {
		f[x.Path] = string(x.Data)
	}
	return f
}

// TestStructPinsAndResults: a STRUCT pin of the project's block, in, out
// and in-out, is its PLC data type, as is a function's STRUCT result;
// the call binds them, and a STRUCT result is assigned whole (#1301 D1b).
func TestStructPinsAndResults(t *testing.T) {
	src := "PROGRAM P\nVAR h : H; a, b : M; w : W; END_VAR\nh(i := a, io := w, o => b);\na := F(x := 2.0, m := b);\nEND_PROGRAM\n"
	out, diags := writeStructs(src, nil, d1bLib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	f := fileMap(out)
	for _, c := range []struct{ file, want string }{
		{"naut_H.scl", "   VAR_INPUT \n      i : \"naut_M\";\n   END_VAR\n   VAR_OUTPUT \n      o : \"naut_M\";\n   END_VAR\n   VAR_IN_OUT \n      io : \"naut_W\";\n   END_VAR\n"},
		{"naut_H.scl", "\t#o := #i;\n"},
		{"naut_F.scl", "FUNCTION \"naut_F\" : \"naut_M\"\n"},
		// The result's temp starts at the type's values, member by member,
		// and is handed back whole.
		{"naut_F.scl", "   VAR_TEMP \n      F : \"naut_M\";\n   END_VAR\n"},
		{"naut_F.scl", "BEGIN\n\t#F.run := FALSE;\n\t#F.speed := 50.0;\n\t#F := #m;\n"},
		{"naut_F.scl", "\t#naut_F := #F;\nEND_FUNCTION\n"},
		{"naut_P.scl", "\t#h(i := #a, io := #w);\n\t#b := #h.o;\n"},
		{"naut_P.scl", "\t#a := \"naut_F\"(x := 2.0, m := #b);\n"},
		{"naut_W.udt", "      pump : \"naut_M\";\n"},
	} {
		if !strings.Contains(f[c.file], c.want) {
			t.Errorf("%s lacks %q:\n%s", c.file, c.want, f[c.file])
		}
	}
	got := paths(out)
	if len(got) < 2 || got[0] != "naut_M.udt" || got[1] != "naut_W.udt" {
		t.Errorf("types first, each after what it holds: %v", got)
	}
	// Held before holder even where the names sort the other way.
	out2, diags := writeStructs("PROGRAM P\nVAR a : A; END_VAR\na.z.n := 1;\nEND_PROGRAM\n", nil, "TYPE A : STRUCT z : Zed; END_STRUCT; Zed : STRUCT n : INT; END_STRUCT; END_TYPE\n")
	if got := paths(out2); len(diags) > 0 || len(got) < 2 || got[0] != "naut_Zed.udt" || got[1] != "naut_A.udt" {
		t.Errorf("Zed before A: %v %v", got, diags)
	}
	// A STRUCT output's member reads through the instance.
	if sym, ok := out.Symbol("P.h.o.run"); !ok || sym != `"naut_P_DB".h.o.run` {
		t.Errorf("Symbol(P.h.o.run) = %s %v", sym, ok)
	}
}

// TestOnlyReachedTypesWritten: a TYPE nothing reaches is not written, so
// a library STRUCT the writer could not write (a STRING member) refuses
// nothing; a type reached only as another's member is written.
func TestOnlyReachedTypesWritten(t *testing.T) {
	out, diags := writeStructs("PROGRAM P\nVAR w : W; END_VAR\nw.depth := 1.0;\nEND_PROGRAM\n", nil, d1bLib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	f := fileMap(out)
	if _, ok := f["naut_Unused.udt"]; ok {
		t.Error("an unreached TYPE was written")
	}
	if _, ok := f["naut_M.udt"]; !ok {
		t.Errorf("W's member type M was not written: %v", paths(out))
	}
	if _, ok := f["naut_H.scl"]; ok {
		t.Error("an unreached block was written")
	}
}

// TestTypeNameClaims: a PLC data type shares TIA's one name space with
// the blocks, so a TYPE named like any generated block is refused by
// name, whichever kind of block it meets.
func TestTypeNameClaims(t *testing.T) {
	for _, c := range []struct{ typ, prog, want string }{
		{"P", "", "collides with program P"},
		{"P_DB", "", "collides with the instance DB of P"},
		{"Tags", "", "collides with the tags DB"},
		{"OB_main", "", "collides with the OB of task main"},
		{"DIV_LINT", "n := n / k;\n", "collides with the guarded division function DIV_LINT"},
		{"G", "n := G(1);\n", "collides with the project's function G"},
	} {
		lib := "TYPE " + c.typ + " : STRUCT a : INT; END_STRUCT; END_TYPE\nFUNCTION G : INT\nVAR_INPUT x : INT; END_VAR\nG := x;\nEND_FUNCTION\n"
		src := "PROGRAM P\nVAR_EXTERNAL k : DINT; END_VAR\nVAR s : " + c.typ + "; n : DINT; END_VAR\ns.a := 1;\n" + c.prog + "END_PROGRAM\n"
		_, diags := writeStructs(src, []Global{{Name: "k", Type: "DINT"}}, lib)
		want := `block "naut_` + c.typ + `" (the PLC data type of TYPE ` + c.typ + `) ` + c.want
		if !strings.Contains(fmt.Sprint(diags), want) {
			t.Errorf("TYPE %s: %v, want %q", c.typ, diags, want)
		}
	}
	// A task's scan DB and the clock DB too.
	_, diags, _ := Write(Input{
		Programs: []Program{
			{Path: "a.st", Source: "TYPE Scan_main : STRUCT a : INT; END_STRUCT; END_TYPE\nPROGRAM A\nVAR_EXTERNAL L : REAL; END_VAR\nVAR s : Scan_main; END_VAR\nL := 1.0;\nEND_PROGRAM\n"},
			{Path: "b.st", Source: "TYPE Clock : STRUCT a : INT; END_STRUCT; END_TYPE\nPROGRAM B\nVAR_EXTERNAL L : REAL; END_VAR\nVAR s : Clock; END_VAR\nL := 2.0;\nEND_PROGRAM\n"},
		},
		Globals:     []Global{{Name: "L", Type: "REAL"}, {Name: "D", Type: "REAL"}},
		Tasks:       []Task{{Name: "main", Program: "A", Interval: time.Second, DtTag: "D"}, {Name: "b", Program: "B", Interval: time.Second}},
		BlockPrefix: "naut_",
	})
	for _, want := range []string{
		`block "naut_Scan_main" (the PLC data type of TYPE Scan_main) collides with the scan DB of task main`,
		`block "naut_Clock" (the PLC data type of TYPE Clock) collides with the clock DB`,
	} {
		if !strings.Contains(fmt.Sprint(diags), want) {
			t.Errorf("%v, want %q", diags, want)
		}
	}
}

// TestTypeNeedsPrefix: with no block prefix a STRUCT is refused, never
// written as a bare-named data type beside a customer's (Fable on #1340).
func TestTypeNeedsPrefix(t *testing.T) {
	_, diags, _ := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR s : M; END_VAR\ns.run := TRUE;\nEND_PROGRAM\n", Libs: []string{d1bLib}}},
		Tasks:    []Task{{Name: "main", Program: "P", Interval: time.Second}},
	})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "the TYPE M needs a block prefix") {
		t.Errorf("%v", diags)
	}
}

// TestFunctionStructInputWrites: TIA hands an FC its STRUCT input by
// reference, so every way an FC can write one (a member, the whole of
// it, a bit of a member) is refused; reading it is not. (An FC holds no
// instance, so no in-out or output binding of one reaches it.)
func TestFunctionStructInputWrites(t *testing.T) {
	types := "TYPE M : STRUCT run : BOOL; n : INT; END_STRUCT; END_TYPE\n"
	for name, body := range map[string]string{
		"member": "m.run := TRUE;\n",
		"whole":  "m := o;\n",
		"bit":    "m.n.3 := TRUE;\n",
	} {
		lib := types + "FUNCTION G : INT\nVAR_INPUT m : M; END_VAR\nVAR o : M; END_VAR\n" + body + "G := 1;\nEND_FUNCTION\n"
		_, diags := writeStructs("PROGRAM P\nVAR a : M; n : INT; END_VAR\nn := G(m := a);\nEND_PROGRAM\n", nil, lib)
		if len(diags) != 1 || !strings.Contains(diags[0].Message, "a STRUCT input of FUNCTION G yet (#1299): TIA hands a function its STRUCT input by reference") {
			t.Errorf("%s: %v", name, diags)
		}
	}
	// An elementary input is TIA's copy too: writing it is no refusal.
	lib := types + "FUNCTION K : INT\nVAR_INPUT x : INT; END_VAR\nx := x + 1;\nK := x;\nEND_FUNCTION\n"
	if _, diags := writeStructs("PROGRAM P\nVAR n : INT; END_VAR\nn := K(x := n);\nEND_PROGRAM\n", nil, lib); len(diags) > 0 {
		t.Errorf("writing an elementary input: %v", diags)
	}
	lib = types + "FUNCTION G : BOOL\nVAR_INPUT m : M; END_VAR\nG := m.run AND m.n > 0;\nEND_FUNCTION\n"
	if _, diags := writeStructs("PROGRAM P\nVAR a : M; b : BOOL; END_VAR\nb := G(m := a);\nEND_PROGRAM\n", nil, lib); len(diags) > 0 {
		t.Errorf("reading a STRUCT input: %v", diags)
	}
}

// TestInOutFromOutside: an instance's in-out named from outside it is
// refused: TIA keeps a reference there, nautilus the last value (#1301
// D1b fold a).
func TestInOutFromOutside(t *testing.T) {
	src := "PROGRAM P\nVAR h : H; a, b : M; w : W; END_VAR\nh(i := a, io := w);\nb.run := h.io.pump.run;\nEND_PROGRAM\n"
	_, diags := writeStructs(src, nil, d1bLib)
	if len(diags) == 0 || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "h.io, an in-out of FUNCTION_BLOCK H, from outside it yet (#1305)") {
		t.Errorf("%v", diags)
	}
	lib := "FUNCTION_BLOCK K\nVAR_IN_OUT n : INT; END_VAR\nn := n + 1;\nEND_FUNCTION_BLOCK\n"
	_, diags = writeStructs("PROGRAM P\nVAR k : K; x : INT; END_VAR\nk(n := x);\nk.n := 3;\nEND_PROGRAM\n", nil, lib)
	if len(diags) == 0 || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "k.n, an in-out of FUNCTION_BLOCK K") {
		t.Errorf("write: %v", diags)
	}
}

// TestProgramTypeGapKeepsChecking: an enumeration the program declares is
// refused, and the rest of the program is still checked in the same run
// (#1301 D1b fold b).
func TestProgramTypeGapKeepsChecking(t *testing.T) {
	src := "PROGRAM P\nTYPE Mode : (Idle, Run); END_TYPE\nVAR s : WSTRING; END_VAR\nEND_PROGRAM\n"
	_, diags := writeStructs(src, nil)
	got := fmt.Sprint(diags)
	if !strings.Contains(got, "the type Mode yet (#1299)") || !strings.Contains(got, "WSTRING yet (#1303)") {
		t.Errorf("%v", diags)
	}
}

// TestInstanceInVarRetain: an instance of the project's block in a
// program's VAR RETAIN is refused (unused in every example) (#1301 D1b
// fold c). Keep it so, or revisit pid.go first: TIA keeps a multi-instance
// in a RETAIN section (it compiles, V21), so a PID inside such a block
// would be kept across a restart, which nautilus never does
// (TestPIDNeverRetained).
func TestInstanceInVarRetain(t *testing.T) {
	src := "PROGRAM P\nVAR RETAIN h : H; END_VAR\nVAR w : W; END_VAR\nh(io := w);\nEND_PROGRAM\n"
	_, diags := writeStructs(src, nil, d1bLib)
	if len(diags) == 0 || diags[0].Line != 2 || !strings.Contains(diags[0].Message, "an instance of FUNCTION_BLOCK H in VAR RETAIN (h) yet (#1305)") {
		t.Errorf("%v", diags)
	}
}

// TestStartValueOverridesQuoted: a start value of a member SCL reserves
// is written quoted in BEGIN, at any depth, and a retained tag's too.
func TestStartValueOverridesQuoted(t *testing.T) {
	lib := "TYPE In : STRUCT Name : INT; END_STRUCT; Q : STRUCT Static : In; x : REAL; END_STRUCT; END_TYPE\n"
	out, diags := writeStructs("PROGRAM P\nVAR_EXTERNAL Title : Q; END_VAR\nTitle.x := 1.0;\nEND_PROGRAM\n",
		[]Global{{Name: "Title", Type: "Q", Retain: true, Start: map[string]any{"static": map[string]any{"name": 3.0}}}}, lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	db := fileMap(out)["naut_Tags.db"]
	if !strings.Contains(db, "   VAR RETAIN\n      \"Title\" : \"naut_Q\";\n") || !strings.Contains(db, "BEGIN\n   \"Title\".\"Static\".\"Name\" := 3;\n") {
		t.Errorf("%s", db)
	}
}

// TestFunctionStructInputAliasesATag: an FC with a STRUCT input that
// writes a STRUCT manifest tag, itself or through a function it calls,
// is refused: handed that tag, TIA's by-reference input would read the
// write, nautilus's copy would not (Sonnet on #1301 D1b). An FC with a
// STRUCT input writing an elementary tag, or one with no STRUCT input
// writing a STRUCT tag, is written.
func TestFunctionStructInputAliasesATag(t *testing.T) {
	types := "TYPE M : STRUCT run : BOOL; n : INT; END_STRUCT; END_TYPE\n"
	globals := []Global{{Name: "T", Type: "M"}, {Name: "K", Type: "INT"}}
	prog := "PROGRAM P\nVAR_EXTERNAL T : M; K : INT; END_VAR\nVAR b : BOOL; END_VAR\nb := F(s := T);\nEND_PROGRAM\n"
	for name, c := range map[string]struct{ lib, want string }{
		"direct": {"FUNCTION F : BOOL\nVAR_INPUT s : M; END_VAR\nVAR_EXTERNAL T : M; END_VAR\nT.run := TRUE;\nF := s.run;\nEND_FUNCTION\n",
			"FUNCTION F, which takes the STRUCT input s and writes the STRUCT tag T yet (#1299)"},
		"whole": {"FUNCTION F : BOOL\nVAR_INPUT s : M; END_VAR\nVAR o : M; END_VAR\nVAR_EXTERNAL T : M; END_VAR\nT := o;\nF := s.run;\nEND_FUNCTION\n",
			"writes the STRUCT tag T yet"},
		"through": {"FUNCTION G : BOOL\nVAR_INPUT x : BOOL; END_VAR\nVAR_EXTERNAL T : M; END_VAR\nT.n := 1;\nG := x;\nEND_FUNCTION\nFUNCTION F : BOOL\nVAR_INPUT s : M; END_VAR\nF := G(x := s.run);\nEND_FUNCTION\n",
			"writes the STRUCT tag T (through FUNCTION G) yet"},
	} {
		_, diags := writeStructs(prog, globals, types+c.lib)
		if !strings.Contains(fmt.Sprint(diags), c.want) {
			t.Errorf("%s: %v, want %q", name, diags, c.want)
		}
	}
	for name, lib := range map[string]string{
		"elementary tag":  "FUNCTION F : BOOL\nVAR_INPUT s : M; END_VAR\nVAR_EXTERNAL K : INT; END_VAR\nK := 1;\nF := s.run;\nEND_FUNCTION\n",
		"no STRUCT input": "FUNCTION F : BOOL\nVAR_INPUT s : BOOL; END_VAR\nVAR_EXTERNAL T : M; END_VAR\nT.run := s;\nF := s;\nEND_FUNCTION\n",
	} {
		p := prog
		if name == "no STRUCT input" {
			p = strings.Replace(prog, "F(s := T)", "F(s := T.run)", 1)
		}
		if _, diags := writeStructs(p, globals, types+lib); len(diags) > 0 {
			t.Errorf("%s: %v", name, diags)
		}
	}
}
