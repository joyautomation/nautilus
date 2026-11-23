package writer

import (
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
	for _, want := range []string{
		// The manifest's map wins, matched as nautilus matches it
		// (case-insensitively), at any depth.
		"            Run : BOOL := TRUE;\n",
		"         Depth : REAL := 0.45;\n",
		// A member it does not name keeps the TYPE's initial value.
		"            Speed : REAL := 50.0;\n",
		"         \"Name\" : DINT := 7;\n",
		// A retained struct tag sits in the RETAIN section.
		"   VAR RETAIN\n      WL102 : Struct\n",
	} {
		if !strings.Contains(db, want) {
			t.Errorf("tags DB lacks %q:\n%s", want, db)
		}
	}
	for _, want := range []string{
		// Member arithmetic is nautilus's 64-bit sum wrapped at the store,
		// typed from the member's declared INT.
		`"naut_Tags".WL101.Pump.Starts := LINT_TO_INT((((INT_TO_LINT("naut_Tags".WL101.Pump.Starts) + 1`,
		`"naut_Tags".WL102."Name" := LINT_TO_DINT((((DINT_TO_LINT("naut_Tags".WL101."Name") + 1`,
		// A whole struct is copied member by member.
		"\t#last.Run := \"naut_Tags\".WL101.Pump.Run;\n\t#last.Speed := \"naut_Tags\".WL101.Pump.Speed;\n\t#last.Starts := \"naut_Tags\".WL101.Pump.Starts;\n",
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

const motorLib = "TYPE\n  Motor : STRUCT\n    Run : BOOL;\n    Speed : REAL;\n    Tag : STRING;\n  END_STRUCT;\n  Pump : STRUCT\n    Run : BOOL;\n    Count : UINT;\n  END_STRUCT;\nEND_TYPE\n"

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
	if !strings.Contains(db, "Count : UINT := 5;") {
		t.Errorf("DB:\n%s", db)
	}
}

// TestStructRefusals: what the writer cannot write of a struct, and what
// nautilus's seeding refuses, are refused, each once.
func TestStructRefusals(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL M1, M2, M3 : Motor; END_VAR\nM1.Run := TRUE;\nEND_PROGRAM\n"
	_, diags := writeStructs(src, []Global{{Name: "M1", Type: "Motor"}, {Name: "M2", Type: "Motor"}, {Name: "M3", Type: "Motor"}}, motorLib)
	if len(diags) != 1 || diags[0].Rule != "siemens/type" || !strings.Contains(diags[0].Message, "tag M1: the type Motor: member Tag: the Siemens writer doesn't translate STRING yet (#1303)") {
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
		"\t#\"Static\"(IN := TRUE, PT := T#1s);\n",
		"\t#\"Title\" := #\"Static\".Q;\n",
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
	if _, why := w.declType("Pump", "", map[string]any{"Run": true, "Spin": true}, true); !strings.Contains(why, "names a member Pump does not have") {
		t.Errorf("unknown key: %q", why)
	}
	if _, why := w.declType("Pump", "", true, true); !strings.Contains(why, "not a mapping") {
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
	for _, want := range []string{"k : INT := -5;", "i : INT := -5;", "s : SINT := -3;", "r : REAL := 1.0e+20;", "d : TIME := T#5s;"} {
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
	}})
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
