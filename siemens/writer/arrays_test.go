package writer

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/st"
)

func arraysInput(t *testing.T) Input {
	t.Helper()
	src, err := os.ReadFile("testdata/arrays/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile("testdata/arrays/lib.st")
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Programs:    []Program{{Path: "Main.st", Source: string(src), Libs: []string{string(lib)}}},
		Globals:     []Global{{Name: "sel", Type: "INT"}, {Name: "tbl", Type: "ARRAY[1..3] OF REAL", Start: []any{1.0, 2.5}}},
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	}
}

// TestArraysGolden: the arrays fixture's SCL (#1299), compiled on TIA V21
// (testdata/arrays/README.md).
func TestArraysGolden(t *testing.T) {
	out := goldenInput(t, "arrays", arraysInput(t))
	for path, want := range map[string]string{
		"Main.b[-1,3]":      `"naut_Main_DB".b[-1,3]`,
		"Main.b[-1, 3]":     `"naut_Main_DB".b[-1,3]`,
		"main.R[2].V":       `"naut_Main_DB".r[2].v`,
		"Main.r[3].hist[0]": `"naut_Main_DB".r[3].hist[0]`,
		"tbl[3]":            `"naut_Tags".tbl[3]`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	for _, path := range []string{"tbl[4]", "tbl[0]", "Main.b[1]", "Main.b[3,0]", "Main.i[1]", "tbl", "Main.r[1]x"} {
		if got, ok := out.Symbol(path); ok && path != "tbl" {
			t.Errorf("Symbol(%q) = %q, want none", path, got)
		}
	}
}

// TestArrayChecksInOrder: each check comes where nautilus evaluates its
// index: an assignment's value before its target, the FUNCTION that can
// fault after the operand before it and before the store, an FB's inputs
// one by one into the instance, its outputs only once the call did not
// fault.
func TestArrayChecksInOrder(t *testing.T) {
	out, diags, err := Write(arraysInput(t))
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	scl := fileOf(t, out, "naut_Main.scl")
	order := func(lines ...string) {
		t.Helper()
		at := 0
		for _, l := range lines {
			i := strings.Index(scl[at:], l)
			if i < 0 {
				t.Fatalf("%q missing or out of order after offset %d in\n%s", l, at, scl)
			}
			at += i + len(l)
		}
	}
	order("#naut_ix5 := #i;", "#naut_ix5 < 1 OR #naut_ix5 > 3", `#naut_fc7 := "naut_Pick"(k := #j, t := "naut_Tags".tbl, naut_fault => #naut_ff6);`, "IF #naut_ff6.faulted THEN\n\t\t#naut_fault := #naut_ff6;\n\t\tRETURN;", `#x := "naut_Tags".tbl[#naut_ix5] + #naut_fc7 * 2.0;`)
	order(`#inst.k := "naut_Tags".sel;`, "#naut_ix8 := #j;", "#inst.x := \"naut_Tags\".tbl[#naut_ix8];", "#inst();", "IF #inst.naut_fault.faulted THEN\n\t\t#naut_fault := #inst.naut_fault;", "#naut_ix9 := #i;", "#outs[#naut_ix9] := #inst.y;")
	order("#o(k := #j);", "IF #o.naut_fault.faulted THEN", "#y := #o.y;")
	// No check where the index is proved: a literal, a FOR variable.
	order("FOR #i := 1 TO #N DO\n\t\t#a[#i] := #i;", "#a[2] := 7;", "#b[#naut_ix1, #naut_ix2] := #b[-2, 0];")
	outer := fileOf(t, out, "naut_Outer.scl")
	if !strings.Contains(outer, "#g(k := #k, x := 2.0);\n\tIF #g.naut_fault.faulted THEN\n\t\t#naut_fault := #g.naut_fault;\n\t\tRETURN;\n\tEND_IF;\n\t#y := #g.y;") {
		t.Errorf("a nested FB's fault must end its caller before its output is stored:\n%s", outer)
	}
	pick := fileOf(t, out, "naut_Pick.scl")
	if !strings.Contains(pick, "\t\t#naut_Pick := #Pick;\n\t\tRETURN;\n\tEND_IF;") {
		t.Errorf("a FUNCTION leaves with its result at a fault:\n%s", pick)
	}
	ob := fileOf(t, out, "naut_OB_main.scl")
	order2 := []string{`"naut_Main_DB"();`, `IF "naut_Main_DB".naut_fault.faulted THEN`, `"naut_Faults".main.LogicErrors := "naut_Faults".main.LogicErrors + 1;`, `"naut_Faults".main.Faulting := "naut_Main_DB".naut_fault.faulted;`}
	at := 0
	for _, l := range order2 {
		i := strings.Index(ob[at:], l)
		if i < 0 {
			t.Fatalf("OB: %q missing or out of order:\n%s", l, ob)
		}
		at += i + len(l)
	}
}

// TestArraysWithoutChecksWriteNoFaults: a project whose indices are all
// proved in bounds writes no faults DB, and its OB is what it was.
func TestArraysWithoutChecksWriteNoFaults(t *testing.T) {
	src := "PROGRAM P\nVAR a : ARRAY[0..3] OF INT; i : INT; END_VAR\nFOR i := 0 TO 3 DO a[i] := i; END_FOR;\na[0] := a[3];\nEND_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, f := range out.Files {
		if strings.Contains(string(f.Data), "naut_Faults") || strings.Contains(string(f.Data), "naut_ix") {
			t.Errorf("%s checks what is proved in bounds:\n%s", f.Path, f.Data)
		}
	}
}

// TestForVariableProvedOnlyWhenUnwritten: a FOR variable proves an index
// only when its constant range is inside the bounds and the body never
// writes it.
func TestForVariableProvedOnlyWhenUnwritten(t *testing.T) {
	for name, c := range map[string]struct {
		body    string
		checked bool
	}{
		"inside":           {"FOR i := 0 TO 3 DO a[i] := 1; END_FOR;", false},
		"negative start":   {"FOR i := -2 TO NC - 1 DO m[i] := 1; END_FOR;", false},
		"past the end":     {"FOR i := 0 TO 4 DO a[i] := 1; END_FOR;", true},
		"before the start": {"FOR i := 0 TO 3 DO c[i] := 1; END_FOR;", true},
		"written":          {"FOR i := 0 TO 3 DO i := i + 1; a[i] := 1; END_FOR;", true},
		"bound by =>":      {"FOR i := 0 TO 3 DO f(CLK := TRUE); a[i] := 1; ctr(CU := TRUE, CV => cv); END_FOR;", false},
		"variable end":     {"FOR i := 0 TO n DO a[i] := 1; END_FOR;", true},
		"after the loop":   {"FOR i := 0 TO 3 DO a[i] := 1; END_FOR;\na[i] := 2;", true},
	} {
		src := "PROGRAM P\nVAR CONSTANT NC : INT := 3; END_VAR\nVAR m : ARRAY[-2..2] OF INT; a : ARRAY[0..3] OF INT; c : ARRAY[1..3] OF INT; i, n : INT; f : R_TRIG; ctr : CTU; cv : LINT; END_VAR\n" + c.body + "\nEND_PROGRAM\n"
		out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
		if err != nil || len(diags) > 0 {
			t.Fatal(name, err, diags)
		}
		if name == "negative start" && !strings.Contains(fileOf(t, out, "naut_P.scl"), "FOR #i := -2 TO 2 DO") {
			t.Errorf("a constant FOR bound is one literal:\n%s", fileOf(t, out, "naut_P.scl"))
		}
		if got := strings.Contains(fileOf(t, out, "naut_P.scl"), "naut_ix"); got != c.checked {
			t.Errorf("%s: checked = %v, want %v:\n%s", name, got, c.checked, fileOf(t, out, "naut_P.scl"))
		}
	}
}

// TestArrayRefusals: what the writer cannot write exactly is refused,
// worded as its gap (#1305).
func TestArrayRefusals(t *testing.T) {
	lib := "FUNCTION W : INT\nVAR_INPUT k : INT; END_VAR\nVAR_EXTERNAL Tg : INT; END_VAR\nTg := k;\nW := k;\nEND_FUNCTION\n" +
		"FUNCTION Fa : INT\nVAR_INPUT k : INT; t : ARRAY[1..3] OF INT; END_VAR\nFa := t[k];\nEND_FUNCTION\n" +
		"FUNCTION Fw : INT\nVAR_INPUT t : ARRAY[1..3] OF INT; END_VAR\nt[1] := 0;\nFw := 0;\nEND_FUNCTION\n" +
		"FUNCTION Wf : INT\nVAR_INPUT k : INT; t : ARRAY[1..3] OF INT; END_VAR\nVAR_EXTERNAL Tg : INT; END_VAR\nTg := k;\nWf := t[k];\nEND_FUNCTION\n" +
		"FUNCTION_BLOCK Io\nVAR_IN_OUT v : INT; END_VAR\nVAR a : ARRAY[1..3] OF INT; k : INT; END_VAR\nv := a[k];\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK Plain\nVAR_IN_OUT v : INT; END_VAR\nv := v + 1;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION Fx : INT\nVAR_INPUT t : ARRAY[1..3] OF INT; END_VAR\nVAR_EXTERNAL Arr : ARRAY[1..3] OF INT; END_VAR\nArr[2] := 1;\nFx := t[2];\nEND_FUNCTION\n"
	decl := "VAR_EXTERNAL Tg : INT; END_VAR\nVAR a : ARRAY[1..3] OF INT; b : ARRAY[1..2, 1..2] OF INT; i, k : INT; io : Io; pl : Plain; tm : TON; rt : R_TRIG; tf : BOOL; row : ARRAY[1..2] OF INT; END_VAR\n"
	for name, c := range map[string]struct{ body, want string }{
		"instances":       {"", "an array of function block instances yet (#1305)"},
		"temp":            {"", "temp yet (#1305)"},
		"row":             {"row := b[1];", "a row of a multi-dimensional array"},
		"FC in index":     {"k := a[W(k := i)];", "a FUNCTION called inside an array index yet (#1305)"},
		"impure first":    {"k := W(k := i) + a[i];", "an array index checked after a FUNCTION that writes a tag"},
		"impure faulting": {"k := i + Wf(k := i, t := a);", "which can fault the scan, called after"},
		"repeat":          {"REPEAT k := k + 1; UNTIL a[k] > 0 END_REPEAT;", "in a REPEAT's UNTIL yet (#1305)"},
		"in-out faults":   {"io(v := k);", "an in-out bound to FUNCTION_BLOCK Io, which can fault the scan yet (#1305)"},
		"in-out element":  {"pl(v := a[i]);", "an array element bound to the in-out v of FUNCTION_BLOCK Plain yet (#1305)"},
		"timer input":     {"tm(IN := tf, PT := INT_TO_TIME(a[i]));", "an array index or a FUNCTION that can fault in an input of tm after its first yet (#1305)"},
		"edge output":     {"rt(CLK := tf, Q => tf);\nrt(CLK := tf);", ""},
		"FC writes input": {"k := Fw(t := a);", "a STRUCT or ARRAY input of FUNCTION Fw yet (#1305)"},
		"FC aliases tag":  {"k := Fx(t := Arr);", "FUNCTION Fx, which takes the STRUCT or ARRAY input t and writes the STRUCT or ARRAY tag Arr yet (#1305)"},
		"based bound":     {"", "an array bound written as an expression or a based literal yet (#1305)"},
	} {
		d := decl
		switch name {
		case "instances":
			d = "VAR ts : ARRAY[1..2] OF TON; END_VAR\n"
		case "temp":
			d = "VAR_TEMP tmp : ARRAY[1..2] OF INT; END_VAR\n"
		case "based bound":
			d = "VAR h : ARRAY[0..16#F] OF INT; END_VAR\n"
		case "FC aliases tag":
			d = "VAR_EXTERNAL Arr : ARRAY[1..3] OF INT; END_VAR\nVAR k : INT; END_VAR\n"
		}
		src := "PROGRAM P\n" + d + c.body + "\nEND_PROGRAM\n"
		_, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}, Globals: []Global{{Name: "Tg", Type: "INT"}, {Name: "Arr", Type: "ARRAY[1..3] OF INT"}},
			Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
		if err != nil {
			t.Fatal(name, err)
		}
		if c.want == "" {
			if len(diags) > 0 {
				t.Errorf("%s: %v", name, diags)
			}
			continue
		}
		if !hasDiag(diags, c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, diags)
		}
	}
	// => to an edge's output with a checked index: TIA writes it at the
	// call, nautilus checks the index after the block ran.
	src := "PROGRAM P\nVAR a : ARRAY[1..3] OF BOOL; i : INT; rt : R_TRIG; END_VAR\nrt(CLK := TRUE, Q => a[i]);\nEND_PROGRAM\n"
	_, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if !hasDiag(diags, "an array element bound with => to rt yet (#1305)") {
		t.Errorf("edge => element: %v", diags)
	}
}

// TestSharedArrayElementWriteRefused: nautilus lands each element a scan
// assigned; the writer copies a shared tag back member by member, so a
// write into an element of a tag two tasks share is refused, while a
// read of one and a whole-array write are written.
func TestSharedArrayElementWriteRefused(t *testing.T) {
	in := func(a string) Input {
		return Input{
			Programs: []Program{
				{Path: "a.st", Source: "PROGRAM A\nVAR_EXTERNAL v : ARRAY[1..3] OF INT; END_VAR\nVAR w : ARRAY[1..3] OF INT; k : INT; END_VAR\n" + a + "\nEND_PROGRAM\n"},
				{Path: "b.st", Source: "PROGRAM B\nVAR_EXTERNAL v : ARRAY[1..3] OF INT; END_VAR\nVAR k : INT; END_VAR\nk := v[2];\nEND_PROGRAM\n"},
			},
			Globals:     []Global{{Name: "v", Type: "ARRAY[1..3] OF INT"}},
			Tasks:       []Task{{Name: "fast", Program: "A", Interval: 10 * time.Millisecond}, {Name: "slow", Program: "B", Interval: 100 * time.Millisecond}},
			BlockPrefix: "naut_",
		}
	}
	_, diags, _ := Write(in("v[2] := 1;"))
	if !hasDiag(diags, "writing an element of v, a tag several tasks share yet (#1305)") {
		t.Errorf("element write: %v", diags)
	}
	out, diags, err := Write(in("v := w;\nk := v[3];"))
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	ob := fileOf(t, out, "naut_OB_fast.scl")
	if !strings.Contains(ob, "IF \"naut_Scan_fast\".assigned.v THEN\n\t\t\"naut_Tags\".v := \"naut_Scan_fast\".tags.v;") {
		t.Errorf("a whole-array write lands whole:\n%s", ob)
	}
}

// TestArrayStartValues: a manifest array tag's start value, element by
// element from its lower bound, nested lists for more dimensions, a map
// for a STRUCT element; a short list leaves the rest at their start.
func TestArrayStartValues(t *testing.T) {
	lib := "TYPE\n  R : STRUCT\n    v : REAL := 1.5;\n    on : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR_EXTERNAL a : ARRAY[-1..1] OF INT; g : ARRAY[0..1, 1..2] OF REAL; r : ARRAY[1..2] OF R; END_VAR\nEND_PROGRAM\n", Libs: []string{lib}}},
		Globals: []Global{
			{Name: "a", Type: "ARRAY[-1..1] OF INT", Start: []any{4, 5}},
			{Name: "g", Type: "ARRAY[0..1, 1..2] OF REAL", Start: []any{[]any{1.0}, []any{2.0, 3.0}}},
			{Name: "r", Type: "ARRAY[1..2] OF R", Start: []any{map[string]any{}, map[string]any{"on": true}}},
		},
		Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	wantLines(t, fileOf(t, out, "naut_Tags.db"),
		"a : Array[-1..1] of INT;", "g : Array[0..1, 1..2] of REAL;", `r : Array[1..2] of "naut_R";`,
		"   a[-1] := 4;\n   a[0] := 5;\n", "   g[0, 1] := 1.0;\n   g[1, 1] := 2.0;\n   g[1, 2] := 3.0;\n", "   r[2].on := TRUE;\n")
	if strings.Contains(fileOf(t, out, "naut_Tags.db"), "a[1] :=") {
		t.Error("a short list leaves the rest at their start value")
	}
	// nautilus refuses a list longer than the array, before the writer.
	_, diags, _ = Write(Input{Globals: []Global{{Name: "a", Type: "ARRAY[1..2] OF INT", Start: []any{1, 2, 3}}}, BlockPrefix: "naut_"})
	if !hasDiag(diags, "nautilus refuses the start value") {
		t.Errorf("too long: %v", diags)
	}
}

// TestGlobalConstantFolded: a project constant written as arithmetic
// (STAGES : INT := 2 + 1) is folded into one literal, as nautilus folds
// it; one out of its type's range stays refused.
func TestGlobalConstantFolded(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR k : INT; END_VAR\nk := STAGES;\nEND_PROGRAM\n", Libs: []string{"VAR_GLOBAL CONSTANT\n  STAGES : INT := 2 + 1;\nEND_VAR\n"}}},
		Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "STAGES : INT := 3;")
	lib := "VAR_GLOBAL CONSTANT\n  BIG : SINT := 100 + 100;\nEND_VAR\n"
	_, diags, _ = Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR k : INT; END_VAR\nk := BIG;\nEND_PROGRAM\n", Libs: []string{lib}}},
		Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	// nautilus refuses it first (the writer's own range check backs that).
	if !hasDiag(diags, "nautilus refuses the project's library") {
		t.Errorf("a constant out of its type's range: %v", diags)
	}
}

// TestUncheckedIndexRefused: the printer refuses an index no statement
// checked, never writes it: the CPU stops on an index out of bounds.
func TestUncheckedIndexRefused(t *testing.T) {
	w := &writer{globals: map[string]Global{}, lib: loadLibrary(nil, nil), prefix: "naut_"}
	pw := &progWriter{w: w, path: "p.st", locals: map[string]local{"a": {name: "a", typ: "ARRAY[1..3] OF INT"}, "i": {name: "i", typ: "INT"}}, ixTemps: map[string]bool{}, forRange: map[string]forBounds{}}
	e := &st.IndexExpr{Array: &st.IdentExpr{Name: "a"}, Indices: []st.Expression{&st.IdentExpr{Name: "i"}}}
	if s := pw.expr(e, 0); s != "" || len(w.diags) != 1 || !strings.Contains(w.diags[0].Message, "did not check") {
		t.Errorf("%q %v", s, w.diags)
	}
	w.diags = nil
	pw.ixTemps["i"] = true
	if s := pw.expr(e, 0); s != "#a[#i]" || len(w.diags) > 0 {
		t.Errorf("a checked index: %q %v", s, w.diags)
	}
}

// TestArrayScopes (Fable, #1299): a constant is folded only where it is
// in scope. A variable that shadows a project constant is no constant (a
// FOR to it proves nothing, an index of it is checked); an array's bounds
// are those of the scope that declares it, not the using block's.
func TestArrayScopes(t *testing.T) {
	gconst := "VAR_GLOBAL CONSTANT\n  N : INT := 3;\nEND_VAR\n"
	write := func(src string, libs ...string) (Output, []Diag) {
		t.Helper()
		out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: libs}}, Globals: []Global{{Name: "tg", Type: "INT"}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
		if err != nil {
			t.Fatal(err)
		}
		return out, diags
	}
	// An input n shadows the project's N: the FOR proves nothing.
	out, diags := write("PROGRAM P\nVAR g : G; END_VAR\ng(n := 100);\nEND_PROGRAM\n",
		gconst+"FUNCTION_BLOCK G\nVAR_INPUT n : INT; END_VAR\nVAR a : ARRAY[0..3] OF INT; i : INT; END_VAR\nFOR i := 0 TO n DO a[i] := 1; END_FOR;\nEND_FUNCTION_BLOCK\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if g := fileOf(t, out, "naut_G.scl"); !regexp.MustCompile(`#naut_ix\d+ := #i;`).MatchString(g) {
		t.Errorf("a FOR to a variable named like a constant must be checked:\n%s", g)
	}
	// A STRUCT member's bounds are the project's N, whatever P calls N.
	out, diags = write("PROGRAM P\nVAR CONSTANT N : INT := 10; END_VAR\nVAR r : R; k, x : INT; END_VAR\nx := r.h[k];\nEND_PROGRAM\n",
		gconst+"TYPE\n  R : STRUCT\n    h : ARRAY[0..N] OF INT;\n  END_STRUCT;\nEND_TYPE\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "#naut_ix1 < 0 OR #naut_ix1 > 3")
	// A pin's bounds are its block's N.
	out, diags = write("PROGRAM P\nVAR g : G; k, x : INT; END_VAR\ng();\nx := g.o[k];\nEND_PROGRAM\n",
		"VAR_GLOBAL CONSTANT\n  N : INT := 10;\nEND_VAR\nFUNCTION_BLOCK G\nVAR CONSTANT N : INT := 3; END_VAR\nVAR_OUTPUT o : ARRAY[0..N] OF INT; END_VAR\no[0] := 1;\nEND_FUNCTION_BLOCK\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "#naut_ix1 < 0 OR #naut_ix1 > 3")
	// A FOR up to its variable type's last value could wrap on the CPU.
	out, diags = write("PROGRAM P\nVAR a : ARRAY[0..127] OF INT; i : SINT; END_VAR\nFOR i := 0 TO 127 DO a[i] := 1; END_FOR;\nEND_PROGRAM\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if p := fileOf(t, out, "naut_P.scl"); !regexp.MustCompile(`#naut_ix\d+ := #i;`).MatchString(p) {
		t.Errorf("a FOR up to its variable type's last value must be checked:\n%s", p)
	}
	// A FUNCTION in the arguments of one that can fault writes a tag: it
	// must not pass the read before it.
	_, diags = write("PROGRAM P\nVAR_EXTERNAL tg : INT; END_VAR\nVAR a : ARRAY[0..3] OF INT; x : INT; END_VAR\nx := tg + F(k := Wr(), a := a);\nEND_PROGRAM\n",
		"FUNCTION Wr : INT\nVAR_EXTERNAL tg : INT; END_VAR\ntg := 99;\nWr := 1;\nEND_FUNCTION\nFUNCTION F : INT\nVAR_INPUT k : INT; a : ARRAY[0..3] OF INT; END_VAR\nF := a[k];\nEND_FUNCTION\n")
	if !hasDiag(diags, "which can fault the scan, called after") {
		t.Errorf("impure argument: %v", diags)
	}
}

// TestFaultTravelsWithTheCallChain (naut-lead, #1394): a fault is kept by
// the block that faulted (an FB's static, an FC's output) and handed up
// the call chain; no block reads or writes a global for it, so a task that
// interrupts another (a cyclic OB of a higher priority) cannot clear or
// overwrite the other's fault between a call and its check. Only each
// task's OB writes its own record, from its own program's instance. One
// FB type that faults, run by two tasks: each instance keeps its own.
func TestFaultTravelsWithTheCallChain(t *testing.T) {
	lib := "FUNCTION_BLOCK G\nVAR_INPUT k : INT; END_VAR\nVAR_OUTPUT y : INT; END_VAR\nVAR a : ARRAY[0..3] OF INT; END_VAR\ny := a[k] + Pk(k := k, t := a);\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION Pk : INT\nVAR_INPUT k : INT; t : ARRAY[0..3] OF INT; END_VAR\nPk := t[k];\nEND_FUNCTION\n"
	prog := func(name string) Program {
		return Program{Path: strings.ToLower(name) + ".st", Libs: []string{lib}, Source: "PROGRAM " + name + "\nVAR_EXTERNAL k" + name + " : INT; END_VAR\nVAR g : G; y : INT; END_VAR\ng(k := k" + name + ", y => y);\nEND_PROGRAM\n"}
	}
	out, diags, err := Write(Input{
		Programs:    []Program{prog("Fast"), prog("Slow")},
		Globals:     []Global{{Name: "kFast", Type: "INT"}, {Name: "kSlow", Type: "INT"}},
		Tasks:       []Task{{Name: "fast", Program: "Fast", Interval: 10 * time.Millisecond}, {Name: "slow", Program: "Slow", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, f := range out.Files {
		body := string(f.Data)
		if strings.HasPrefix(f.Path, "naut_OB_") {
			task := strings.TrimSuffix(strings.TrimPrefix(f.Path, "naut_OB_"), ".scl")
			prog := map[string]string{"fast": "Fast", "slow": "Slow"}[task]
			for _, l := range strings.Split(body, "\n") {
				if strings.Contains(l, `"naut_Faults".`) && !strings.Contains(l, `"naut_Faults".`+task+".") {
					t.Errorf("%s touches another task's record: %s", f.Path, l)
				}
				if strings.Contains(l, "naut_fault") && !strings.Contains(l, `"naut_`+prog+`_DB".naut_fault`) {
					t.Errorf("%s reads another program's fault: %s", f.Path, l)
				}
			}
			continue
		}
		if f.Path != "naut_Faults.db" && strings.Contains(body, "naut_Faults") {
			t.Errorf("%s names the faults DB: a block's fault must travel with its call chain\n%s", f.Path, body)
		}
	}
	wantLines(t, fileOf(t, out, "naut_G.scl"), "naut_fault : \"naut_Fault\";", "\t#naut_fault.faulted := FALSE;\n",
		"naut_fault => #naut_ff", "#naut_fault.faulted := TRUE;")
	wantLines(t, fileOf(t, out, "naut_Pk.scl"), "   VAR_OUTPUT \n      naut_fault : \"naut_Fault\";")
	for _, p := range []string{"Fast", "Slow"} {
		wantLines(t, fileOf(t, out, "naut_"+p+".scl"), "IF #g.naut_fault.faulted THEN\n\t\t#naut_fault := #g.naut_fault;\n\t\tRETURN;\n\tEND_IF;\n\t#y := #g.y;")
	}
	wantLines(t, fileOf(t, out, "naut_Faults.db"), "      fast : Struct", "      slow : Struct")
}

// TestCalleeDeclaresItsFaultWhenCallersCheckIt: a caller checks the fault
// of every block that can fault by its body (any index), so such a block
// declares naut_fault even when all its indices are proved and it checks
// nothing itself: TIA would refuse the caller's #inst.naut_fault otherwise.
func TestCalleeDeclaresItsFaultWhenCallersCheckIt(t *testing.T) {
	lib := "FUNCTION_BLOCK L\nVAR a : ARRAY[1..3] OF INT; END_VAR\na[1] := 1;\nEND_FUNCTION_BLOCK\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Libs: []string{lib}, Source: "PROGRAM P\nVAR l : L; END_VAR\nl();\nEND_PROGRAM\n"}},
		Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	wantLines(t, fileOf(t, out, "naut_L.scl"), "naut_fault : \"naut_Fault\";", "#naut_fault.faulted := FALSE;")
	wantLines(t, fileOf(t, out, "naut_P.scl"), "IF #l.naut_fault.faulted THEN")
}

// TestWriterNamesAvoidProjectConstants (Fable re-check): a project
// constant is copied into each block that names it, under its name, so a
// temp the writer adds never takes a project constant's name, and a
// project constant named naut_fault is refused, not declared twice.
func TestWriterNamesAvoidProjectConstants(t *testing.T) {
	write := func(lib, body string) (Output, []Diag) {
		out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Libs: []string{lib}, Source: "PROGRAM P\nVAR a : ARRAY[0..3] OF INT; i, x : INT; END_VAR\n" + body + "\nEND_PROGRAM\n"}},
			Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
		if err != nil {
			t.Fatal(err)
		}
		return out, diags
	}
	_, diags := write("VAR_GLOBAL CONSTANT\n  naut_fault : INT := 4;\nEND_VAR\n", "x := a[i] + naut_fault;")
	if !hasDiag(diags, "naut_fault: the writer keeps the block's fault in a variable of that name") {
		t.Errorf("naut_fault constant: %v", diags)
	}
	out, diags := write("VAR_GLOBAL CONSTANT\n  naut_ix1 : INT := 3;\nEND_VAR\n", "x := a[i] + naut_ix1;")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "naut_ix1 : INT := 3;", "#naut_ix2 := #i;", "#a[#naut_ix2]) + INT_TO_LINT(#naut_ix1)")
}
