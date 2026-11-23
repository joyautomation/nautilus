package writer

import (
	"os"
	"strings"
	"testing"
	"time"
)

var pousGlobals = []Global{
	{Name: "Run", Type: "BOOL"}, {Name: "Level", Type: "REAL"}, {Name: "Starts", Type: "INT"},
	{Name: "High", Type: "BOOL"}, {Name: "Pct", Type: "REAL"}, {Name: "Count", Type: "DINT"}, {Name: "Next", Type: "INT"},
}

// TestPOUsGolden: the project's FUNCTIONs and FUNCTION_BLOCKs (#1301),
// compared with testdata/pous/golden/ (compiled on TIA V21, README).
func TestPOUsGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/pous/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile("testdata/pous/lib.st")
	if err != nil {
		t.Fatal(err)
	}
	out := goldenInput(t, "pous", Input{
		Programs:    []Program{{Path: "Main.st", Source: string(src), Libs: []string{string(lib)}}},
		Globals:     pousGlobals,
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	// Callees first: TIA generates a block only once the blocks it
	// instantiates exist (measured on V21), so Debounce comes before
	// Station, both before the program; then the rest by name.
	var order []string
	for _, f := range out.Files {
		order = append(order, f.Path)
	}
	idx := func(p string) int {
		for i, o := range order {
			if o == p {
				return i
			}
		}
		t.Fatalf("no %s in %v", p, order)
		return -1
	}
	if !(idx("naut_Debounce.scl") < idx("naut_Station.scl") && idx("naut_Station.scl") < idx("naut_Main.scl") && idx("naut_Bump.scl") < idx("naut_Main.scl") && idx("naut_Scale.scl") < idx("naut_Main.scl")) {
		t.Errorf("files not callees first: %v", order)
	}
	// The Web API reaches into an instance as nautilus names it.
	for path, want := range map[string]string{
		"Main.st1.High":    `"naut_Main_DB".st1.High`,
		"Main.st1.db.Q":    `"naut_Main_DB".st1."db".Q`,
		"Main.st1.db.t.ET": `"naut_Main_DB".st1."db".t.ET`,
		"main.ST2.total":   `"naut_Main_DB".st2.Total`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	// Not readable: an in-out (the caller's variable), a temp, a constant,
	// a name the block does not have.
	for _, path := range []string{"Main.st1.Starts", "Main.st1.scratch", "Main.st1.GAIN", "Main.st1.nope", "Main.st1.db.t.ET.x"} {
		if got, ok := out.Symbol(path); ok {
			t.Errorf("Symbol(%q) = %q, want none", path, got)
		}
	}
	// A library block's file is located at the program that first used
	// it (the writer is not told the library's file).
	if src := out.Sources["naut_Station.scl"]; src.Path != "Main.st" {
		t.Errorf("Station's source = %+v", src)
	}
}

func writePOU(t *testing.T, src string, libs ...string) (Output, []Diag) {
	t.Helper()
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "p.st", Source: src, Libs: libs}},
		Globals:     []Global{{Name: "A", Type: "INT"}, {Name: "B", Type: "BOOL"}, {Name: "R", Type: "REAL"}, {Name: "D", Type: "DINT"}},
		Tasks:       []Task{{Name: "main", Program: "P", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, diags
}

// TestFunctionLocalsStartEveryCall: nautilus starts every local of a
// FUNCTION call at its initial value, the return value at its type's
// zero; an FC's temps start undefined, so the body sets each first, and
// the result is copied to the FC's return value at the end and at each
// RETURN.
func TestFunctionLocalsStartEveryCall(t *testing.T) {
	lib := "FUNCTION F : INT\nVAR_INPUT x : INT; END_VAR\nVAR n : INT := 5; on : BOOL; END_VAR\nVAR_TEMP k : REAL; END_VAR\nIF x > 0 THEN RETURN; END_IF;\nF := n;\nEND_FUNCTION\n"
	out, diags := writePOU(t, "PROGRAM P\nA := F(A);\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	fc := fileOf(t, out, "naut_F.scl")
	wantLines(t, fc, "FUNCTION \"naut_F\" : INT\n", "   VAR_TEMP \n      n : INT;\n      on : BOOL;\n      k : REAL;\n      F : INT;\n   END_VAR\n",
		"BEGIN\n\t#F := 0;\n\t#n := 5;\n\t#on := FALSE;\n\t#k := 0.0;\n",
		"\tIF #x > 0 THEN\n\t\t#naut_F := #F;\n\t\tRETURN;\n\tEND_IF;\n",
		"\t#F := #n;\n\t#naut_F := #F;\nEND_FUNCTION\n")
	if strings.Contains(fc, "VAR \n") {
		t.Errorf("an FC has no statics:\n%s", fc)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), `"naut_Tags".A := "naut_F"(x := "naut_Tags".A);`)
}

// TestArgumentsStoreAsNautilusStores: an argument is stored into its
// input as an assignment stores (lang/st storeInput): integer arithmetic
// at 64 bits, wrapped to the input's width; a result read back from an
// instance or a function is typed by its declaration, so arithmetic on
// it is widened and wrapped too.
func TestArgumentsStoreAsNautilusStores(t *testing.T) {
	lib := "FUNCTION G : INT\nVAR_INPUT x : INT; END_VAR\nG := x;\nEND_FUNCTION\nFUNCTION_BLOCK H\nVAR_INPUT i : INT; END_VAR\nVAR_OUTPUT o : INT; END_VAR\no := i;\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR h : H; END_VAR\nh(i := A + 1);\nA := G(A + 1) + 1;\nA := h.o + 1;\nD := h.O;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wrap := func(s string) string {
		return "LINT_TO_INT((((" + s + ") + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768)"
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"),
		"\t#h(i := "+wrap(`INT_TO_LINT("naut_Tags".A) + 1`)+");\n",
		"\t\"naut_Tags\".A := "+wrap(`INT_TO_LINT("naut_G"(x := `+wrap(`INT_TO_LINT("naut_Tags".A) + 1`)+`)) + 1`)+";\n",
		"\t\"naut_Tags\".A := "+wrap(`INT_TO_LINT(#h.o) + 1`)+";\n",
		"\t\"naut_Tags\".D := #h.o;\n")
}

// TestOutputsAndInOuts: an => output is stored after the call, as
// nautilus copies it; a VAR_IN_OUT is passed by reference, and with
// several tasks the shared tag bound to one counts as assigned, since
// nautilus copies the pin back after the call.
func TestOutputsAndInOuts(t *testing.T) {
	lib := "FUNCTION_BLOCK H\nVAR_IN_OUT io : INT; END_VAR\nVAR_OUTPUT o : BOOL; END_VAR\nio := io + 1;\no := TRUE;\nEND_FUNCTION_BLOCK\n"
	a := "PROGRAM P\nVAR_EXTERNAL A : INT; B : BOOL; END_VAR\nVAR h : H; END_VAR\nh(io := A, o => B);\nEND_PROGRAM\n"
	other := "PROGRAM Q\nVAR_EXTERNAL A : INT; B : BOOL; END_VAR\nIF B THEN A := 0; END_IF;\nEND_PROGRAM\n"
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "p.st", Source: a, Libs: []string{lib}}, {Path: "q.st", Source: other, Libs: []string{lib}}},
		Globals:     []Global{{Name: "A", Type: "INT"}, {Name: "B", Type: "BOOL"}},
		Tasks:       []Task{{Name: "main", Program: "P", Interval: 100 * time.Millisecond}, {Name: "q", Program: "Q", Interval: 50 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"),
		"\t#h(io := \"naut_Scan_main\".tags.A);\n\t\"naut_Scan_main\".assigned.A := TRUE;\n\t\"naut_Scan_main\".tags.B := #h.o;\n\t\"naut_Scan_main\".assigned.B := TRUE;\n")
}

// TestTimerCallsPassPT: TIA wants PT at every call of a TON_TIME
// (measured on V21); a call that leaves it out passes the PT the
// instance holds, which is what nautilus keeps.
func TestTimerCallsPassPT(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR t : TON; f : TOF; p : TP; x : BOOL; END_VAR\nt(IN := x, PT := T#1s);\nt(IN := NOT x);\nf(IN := x);\np(IN := x, PT := T#2s);\nEND_PROGRAM\n")
	wantLines(t, scl, "\t#t(IN := #x, PT := T#1s);\n", "\t#t(IN := NOT #x, PT := #t.PT);\n", "\t#f(IN := #x, PT := #f.PT);\n", "\t#p(IN := #x, PT := T#2s);\n")
	// And PV at every call of a counter (measured on V21 too).
	scl, _ = write(t, "PROGRAM P\nVAR c : CTU; d : CTD; ud : CTUD; x : BOOL; END_VAR\nc(CU := x);\nc(CU := x, PV := 3);\nd(CD := x, LD := x);\nud(CU := x);\nEND_PROGRAM\n")
	wantLines(t, scl, "\t#c(CU := #x, PV := #c.PV);\n", "\t#c(CU := #x, PV := 3);\n", "\t#d(CD := #x, LD := #x, PV := #d.PV);\n", "\t#ud(CU := #x, PV := #ud.PV);\n")
}

// TestDoubleUnderscoreQuoted: a name with two underscores in a row is no
// SCL identifier (TIA V21 refused a tag EPIC__Online in the tags DB), so
// it is written quoted, where it is declared and where it is used.
func TestDoubleUnderscoreQuoted(t *testing.T) {
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "p.st", Source: "PROGRAM P\nVAR_EXTERNAL EPIC__Online : BOOL; END_VAR\nVAR a__b : INT; END_VAR\nEPIC__Online := a__b > 0;\nEND_PROGRAM\n"}},
		Globals:     []Global{{Name: "EPIC__Online", Type: "BOOL"}},
		Tasks:       []Task{{Name: "main", Program: "P", Interval: time.Second}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "      \"a__b\" : INT;\n", "\t\"naut_Tags\".\"EPIC__Online\" := #\"a__b\" > 0;\n")
	wantLines(t, fileOf(t, out, "naut_Tags.db"), "      \"EPIC__Online\" : BOOL;\n")
}

// TestConstants: VAR CONSTANT becomes SCL's VAR CONSTANT, and each block
// that names one of the library's global constants declares its own copy,
// with the same name and value; a local of the same name shadows it, as
// in nautilus, and nothing is declared for it.
func TestConstants(t *testing.T) {
	gvl := "VAR_GLOBAL CONSTANT\n    HI : REAL := 80.0;\n    N : INT := -3;\n    M : INT := N;\n    UNUSED : INT := 1;\nEND_VAR\n"
	lib := gvl + "FUNCTION_BLOCK H\nVAR_OUTPUT o : BOOL; END_VAR\nVAR N : INT; END_VAR\no := HI > 1.0 AND N = 0;\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR CONSTANT K : TIME := T#5s; L : INT := M; END_VAR\nVAR h : H; x : INT := L; END_VAR\nh();\nB := R > HI;\nA := M;\nA := M + 1;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	p := fileOf(t, out, "naut_P.scl")
	wantLines(t, p, "   VAR CONSTANT \n      K : TIME := T#5s;\n      L : INT := -3;\n      HI : REAL := 80.0;\n      M : INT := -3;\n   END_VAR\n", "      x : INT := -3;\n",
		// A global constant is typed by its declaration: arithmetic on it
		// is widened and wrapped like any INT's.
		"\t\"naut_Tags\".A := LINT_TO_INT((((INT_TO_LINT(#M) + 1) + LINT#32768)")
	h := fileOf(t, out, "naut_H.scl")
	wantLines(t, h, "   VAR CONSTANT \n      HI : REAL := 80.0;\n   END_VAR\n", "\t#o := #HI > 1.0 AND #N = 0;\n")
	if strings.Contains(p+h, "UNUSED") || strings.Count(h, " N : INT") != 1 {
		t.Errorf("declared a constant nothing names, or one a local shadows:\n%s\n%s", p, h)
	}
}

// TestSectionsInTIAOrder: whatever order the source declares them in, the
// sections come out as TIA's own sources have them, one of each kind.
func TestSectionsInTIAOrder(t *testing.T) {
	lib := "FUNCTION_BLOCK H\nVAR a : INT; END_VAR\nVAR_TEMP t : INT; END_VAR\nVAR_OUTPUT o : INT; END_VAR\nVAR b : INT; END_VAR\nVAR_INPUT i : INT; END_VAR\no := i + a + b + t;\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR h : H; END_VAR\nh(i := 1);\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_H.scl"), "VERSION : 0.1\n   VAR_INPUT \n      i : INT;\n   END_VAR\n   VAR_OUTPUT \n      o : INT;\n   END_VAR\n   VAR \n      a : INT;\n      b : INT;\n   END_VAR\n   VAR_TEMP \n      t : INT;\n   END_VAR\n\nBEGIN\n")
}

// TestENOnABlockCall: EN/ENO on a call of the project's block is refused
// (SCL calls a block without them).
func TestENOnABlockCall(t *testing.T) {
	_, diags := writePOU(t, "PROGRAM P\nFUNCTION_BLOCK H\nVAR_INPUT x : BOOL; END_VAR\nEND_FUNCTION_BLOCK\nVAR h : H; END_VAR\nh(EN := B, x := TRUE);\nEND_PROGRAM\n")
	if len(diags) == 0 || diags[0].Line != 6 || !strings.Contains(diags[0].Message, "EN/ENO on a call of FUNCTION_BLOCK H yet (#1305)") {
		t.Errorf("EN: %v", diags)
	}
}

// TestOnlyReachedBlocksAreWritten: a library block nothing calls is not
// written, and what it uses that the writer does not translate refuses
// nothing.
func TestOnlyReachedBlocksAreWritten(t *testing.T) {
	lib := "FUNCTION_BLOCK Used\nVAR_OUTPUT o : BOOL; END_VAR\no := TRUE;\nEND_FUNCTION_BLOCK\nFUNCTION_BLOCK Unused\nVAR s : STRING; END_VAR\ns := 'x';\nEND_FUNCTION_BLOCK\n"
	out, diags := writePOU(t, "PROGRAM P\nVAR u : Used; END_VAR\nu();\nB := u.o;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	for _, f := range out.Files {
		if strings.Contains(f.Path, "Unused") {
			t.Errorf("wrote %s", f.Path)
		}
	}
	fileOf(t, out, "naut_Used.scl")
}

// TestProgramFileBlocks: a block declared in the program's own file is
// written like a library one, its diagnostics at its own lines.
func TestProgramFileBlocks(t *testing.T) {
	src := "PROGRAM P\nFUNCTION_BLOCK Own\nVAR s : STRING; END_VAR\ns := 'x';\nEND_FUNCTION_BLOCK\nVAR h : Own; END_VAR\nh();\nEND_PROGRAM\n"
	_, diags := writePOU(t, src)
	if len(diags) == 0 || diags[0].Path != "p.st" || diags[0].Line != 3 || !strings.Contains(diags[0].Message, "STRING yet (#1303)") {
		t.Errorf("own block: %v", diags)
	}
	out, diags := writePOU(t, "PROGRAM P\nFUNCTION_BLOCK Own\nVAR_OUTPUT o : BOOL; END_VAR\no := TRUE;\nEND_FUNCTION_BLOCK\nVAR h : Own; END_VAR\nh();\nEND_PROGRAM\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	if src := out.Sources["naut_Own.scl"]; src.Path != "p.st" || len(src.Body) != 1 || src.Body[0] != 4 {
		t.Errorf("own block's source = %+v", src)
	}
}

// TestLibraryBlockDiagnostics: what a library block uses that the writer
// refuses is reported where the program first uses the block, naming the
// block and its line in the library.
func TestLibraryBlockDiagnostics(t *testing.T) {
	lib := "(* a library *)\nFUNCTION_BLOCK Lib\nVAR s : STRING; END_VAR\ns := 'x';\nEND_FUNCTION_BLOCK\n"
	_, diags := writePOU(t, "PROGRAM P\nVAR\n  h : Lib;\nEND_VAR\nh();\nEND_PROGRAM\n", lib)
	if len(diags) == 0 || diags[0].Path != "p.st" || diags[0].Line != 3 || !strings.Contains(diags[0].Message, "in FUNCTION_BLOCK Lib (the project's library, line 3): s: the Siemens writer doesn't translate STRING yet (#1303)") {
		t.Errorf("library block: %v", diags)
	}
}

// TestBlockRefusals: what the writer refuses of the project's blocks, each
// with its reason, a STRUCT pin once per block.
func TestBlockRefusals(t *testing.T) {
	for _, c := range []struct{ name, lib, prog, want string }{
		{"retain", "FUNCTION_BLOCK H\nVAR RETAIN n : INT; END_VAR\nn := n + 1;\nEND_FUNCTION_BLOCK\n",
			"PROGRAM P\nVAR h : H; END_VAR\nh();\nEND_PROGRAM\n", "VAR RETAIN in FUNCTION_BLOCK H yet (#1305)"},
		{"function as statement", "FUNCTION F : INT\nVAR_INPUT x : INT; END_VAR\nF := x;\nEND_FUNCTION\n",
			"PROGRAM P\nF(1);\nEND_PROGRAM\n", "F(…) as a statement yet (#1305)"},
		{"instance in a function", "FUNCTION_BLOCK H\nEND_FUNCTION_BLOCK\nFUNCTION F : INT\nVAR h : H; t : TON; END_VAR\nh();\nF := 1;\nEND_FUNCTION\n",
			"PROGRAM P\nVAR a : INT; END_VAR\na := F();\nEND_PROGRAM\n", "an instance of FUNCTION_BLOCK H in a FUNCTION yet (#1305)"},
	} {
		_, diags := writePOU(t, c.prog, c.lib)
		if len(diags) == 0 || !strings.Contains(diags[0].Message, c.want) {
			t.Errorf("%s: %v", c.name, diags)
		}
	}
}

// TestTagInABlockWithSeveralTasks: a block names no manifest tag in a
// project with several tasks (each task has its own copy); with one task
// it reads the tags DB.
func TestTagInABlockWithSeveralTasks(t *testing.T) {
	lib := "FUNCTION_BLOCK H\nVAR_EXTERNAL A : INT; END_VAR\nA := 1;\nEND_FUNCTION_BLOCK\n"
	prog := "PROGRAM P\nVAR h : H; END_VAR\nh();\nEND_PROGRAM\n"
	out, diags := writePOU(t, prog, lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_H.scl"), "\t\"naut_Tags\".A := 1;\n")
	_, diags, _ = Write(Input{
		Programs:    []Program{{Path: "p.st", Source: prog, Libs: []string{lib}}, {Path: "q.st", Source: "PROGRAM Q\nVAR_EXTERNAL B : BOOL; END_VAR\nB := TRUE;\nEND_PROGRAM\n", Libs: []string{lib}}},
		Globals:     []Global{{Name: "A", Type: "INT"}, {Name: "B", Type: "BOOL"}},
		Tasks:       []Task{{Name: "main", Program: "P", Interval: time.Second}, {Name: "q", Program: "Q", Interval: time.Second}},
		BlockPrefix: "naut_",
	})
	if len(diags) == 0 || !strings.Contains(diags[0].Message, "the manifest tag A named inside FUNCTION_BLOCK H in a project with several tasks yet (#1305)") {
		t.Errorf("several tasks: %v", diags)
	}
}

// TestBlockDeclaredTwiceDifferently: the CPU holds one block of a name, so
// two program files declaring one name differently are refused; the same
// declaration twice is one block.
func TestBlockDeclaredTwiceDifferently(t *testing.T) {
	two := func(b2 string) []Diag {
		_, diags, err := Write(Input{
			Programs: []Program{
				{Path: "p.st", Source: "PROGRAM P\nFUNCTION_BLOCK H\nVAR_OUTPUT o : BOOL; END_VAR\no := TRUE;\nEND_FUNCTION_BLOCK\nVAR h : H; END_VAR\nh();\nEND_PROGRAM\n"},
				{Path: "q.st", Source: "PROGRAM Q\n" + b2 + "VAR h : H; END_VAR\nh();\nEND_PROGRAM\n"},
			},
			Tasks:       []Task{{Name: "main", Program: "P", Interval: time.Second}, {Name: "q", Program: "Q", Interval: time.Second}},
			BlockPrefix: "naut_",
		})
		if err != nil {
			t.Fatal(err)
		}
		return diags
	}
	if d := two("FUNCTION_BLOCK H\nVAR_OUTPUT o : BOOL; END_VAR\no := TRUE;\nEND_FUNCTION_BLOCK\n"); len(d) > 0 {
		t.Errorf("the same block twice: %v", d)
	}
	if d := two("FUNCTION_BLOCK H\nVAR_OUTPUT o : BOOL; END_VAR\no := FALSE;\nEND_FUNCTION_BLOCK\n"); len(d) == 0 || !strings.Contains(d[0].Message, "declares FUNCTION_BLOCK H twice, differently (p.st and q.st)") {
		t.Errorf("two different blocks: %v", d)
	}
}

// TestBlockNameCollides: a block's name is claimed like every other.
func TestBlockNameCollides(t *testing.T) {
	_, diags := writePOU(t, "PROGRAM P\nVAR h : P_DB; END_VAR\nh();\nEND_PROGRAM\n", "FUNCTION_BLOCK P_DB\nEND_FUNCTION_BLOCK\n")
	if len(diags) == 0 || !strings.Contains(diags[0].Message, `block "naut_P_DB"`) {
		t.Errorf("collision: %v", diags)
	}
}

// TestReviewFixesD1: an FC whose name needs quoting returns through the
// quoted name; a temp or a constant of an instance is not read from
// outside it; a program file's own global constants are written like a
// library's.
func TestReviewFixesD1(t *testing.T) {
	out, diags := writePOU(t, "PROGRAM P\nA := Calc__X(A);\nEND_PROGRAM\n", "FUNCTION Calc__X : INT\nVAR_INPUT x : INT; END_VAR\nCalc__X := x;\nEND_FUNCTION\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_Calc__X.scl"), "FUNCTION \"naut_Calc__X\" : INT\n", "\t#\"naut_Calc__X\" := #\"Calc__X\";\n")

	lib := "FUNCTION_BLOCK H\nVAR_OUTPUT o : INT; END_VAR\nVAR_TEMP tmp : INT; END_VAR\nVAR CONSTANT K : INT := 2; END_VAR\nVAR cnt : INT; END_VAR\ntmp := 1;\no := tmp;\ncnt := K;\nEND_FUNCTION_BLOCK\n"
	for _, m := range []string{"tmp", "K"} {
		_, diags = writePOU(t, "PROGRAM P\nVAR h : H; END_VAR\nh();\nA := h."+m+";\nEND_PROGRAM\n", lib)
		if len(diags) == 0 || diags[0].Line != 4 || !strings.Contains(diags[0].Message, "reading h."+m+", a temp or constant of FUNCTION_BLOCK H, from outside it yet (#1305)") {
			t.Errorf("h.%s: %v", m, diags)
		}
	}
	out, diags = writePOU(t, "PROGRAM P\nVAR h : H; END_VAR\nh();\nA := h.cnt;\nEND_PROGRAM\n", lib)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "\t\"naut_Tags\".A := #h.cnt;\n")

	out, diags = writePOU(t, "VAR_GLOBAL CONSTANT\n    TOP : INT := 7;\nEND_VAR\nPROGRAM P\nA := TOP;\nEND_PROGRAM\n")
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	wantLines(t, fileOf(t, out, "naut_P.scl"), "      TOP : INT := 7;\n", "\t\"naut_Tags\".A := #TOP;\n")
}
