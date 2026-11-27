package writer

import (
	"os"
	"strings"
	"testing"
	"time"
)

// #1305 B2.

// TestIntegerAbs: nautilus's ABS of an integer is computed at 64 bits
// (ABS of an INT at -32768 is 32768), and its store wraps it.
func TestIntegerAbs(t *testing.T) {
	scl, diags := writeP(t, "x : INT; d : DINT; l : LINT;", `
x := ABS(x);
d := ABS(d + 1);
l := ABS(l);`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"#x := LINT_TO_INT((((ABS(INT_TO_LINT(#x))) + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768);",
		"#d := LINT_TO_DINT((((ABS(DINT_TO_LINT(#d) + 1)) + LINT#2147483648)",
		"#l := ABS(#l);")
	if _, diags := writeP(t, "c : CTU; a : BOOL; n : INT;", "c(CU := a, PV := ABS(n));"); !hasDiag(diags, "integer arithmetic") {
		t.Fatalf("ABS of an integer into an FB input: %v", diags)
	}
}

// TestENENOGatesTheCall: EN FALSE skips the call (its arguments too) and
// keeps the target and every binding; ENO is the branch the gate took,
// never TIA's own (lang/st lower_eneno.go).
func TestENENOGatesTheCall(t *testing.T) {
	scl, diags := writeP(t, "e, cu, ok, ceno, ok2 : BOOL; x, out : REAL; c : CTU; cv : INT;", `
out := LIMIT(EN := e, MN := 0.0, IN := x, MX := 10.0, ENO => ok);
c(EN := e, CU := cu, R := FALSE, PV := 100, CV => cv, ENO => ceno);
out := LIMIT(MN := 0.0, IN := x, MX := 10.0, ENO => ok2);`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"\tIF #e THEN\n\t\t#out := LIMIT(MN := 0.0, IN := #x, MX := 10.0);\n\t\t#ok := TRUE;\n\tELSE\n\t\t#ok := FALSE;\n\tEND_IF;",
		"\tIF #e THEN\n\t\t#c.CTU(CU := #cu, R := FALSE, PV := 100);\n\t\t#cv := ",
		"\t\t#ceno := TRUE;\n\tELSE\n\t\t#ceno := FALSE;\n\tEND_IF;",
		// ENO alone: the call always runs, and ENO is TRUE.
		"\t#out := LIMIT(MN := 0.0, IN := #x, MX := 10.0);\n\t#ok2 := TRUE;")
	if strings.Contains(scl, "ENO =>") || strings.Contains(scl, "EN :=") {
		t.Errorf("TIA's own EN or ENO is wired:\n%s", scl)
	}
}

// TestENSkipsTheArguments: while EN is FALSE nautilus does not evaluate the
// call's arguments, so a FUNCTION that can fault, and the checks of an
// index in them, run only under the gate.
func TestENSkipsTheArguments(t *testing.T) {
	lib := "FUNCTION F : REAL\nVAR_INPUT k : INT; a : ARRAY[0..3] OF REAL; END_VAR\nF := a[k];\nEND_FUNCTION\n"
	src := "PROGRAM P\nVAR e, ok : BOOL; k : INT; out : REAL; v : ARRAY[0..3] OF REAL; END_VAR\n" +
		"out := LIMIT(EN := e, MN := 0.0, IN := F(k := k, a := v) + v[k], MX := 10.0, ENO => ok);\nEND_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	scl := fileOf(t, out, "naut_P.scl")
	gate := strings.Index(scl, "IF #e THEN")
	call := strings.Index(scl, `"naut_F"(`)
	check := strings.Index(scl, "#naut_ix")
	if gate < 0 || call < gate || check < gate {
		t.Errorf("the FUNCTION call (%d) and the index check (%d) must come after the gate (%d):\n%s", call, check, gate, scl)
	}
}

// TestB2Golden: the B2 fixture's SCL (testdata/b2/README.md), compiled on
// TIA V21 and run on the bench CPU against naut run.
func TestB2Golden(t *testing.T) {
	src, err := os.ReadFile("testdata/b2/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	lib, err := os.ReadFile("testdata/b2/lib.st")
	if err != nil {
		t.Fatal(err)
	}
	var globals []Global
	for _, g := range []struct{ names, typ string }{
		{"A16 rA16 rAbs5 rAbs128 cv", "INT"}, {"A32 rA32 rAbs32k Bumps Scans", "DINT"}, {"A64 rA64 rAbs2g", "LINT"},
		{"En Cu eno1 eno2", "BOOL"}, {"X rLim", "REAL"},
		{"Sp", "ARRAY[0..48] OF LREAL"}, {"Gr", "ARRAY[0..143] OF REAL"}, {"Sq", "ARRAY[0..1] OF LREAL"}, {"SpB", "ARRAY[0..48] OF LWORD"}, {"GrB", "ARRAY[0..143] OF DWORD"}, {"SqB SvB", "ARRAY[0..6] OF LWORD"}, {"LitB", "LWORD"},
	} {
		for _, n := range strings.Fields(g.names) {
			globals = append(globals, Global{Name: n, Type: g.typ})
		}
	}
	goldenInput(t, "b2", Input{
		Programs:    []Program{{Path: "Main.st", Source: string(src), Libs: []string{string(lib)}}},
		Globals:     globals,
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
}

// TestTheGateCoversEveryCallShape: a timer, a latch, a user FB and
// ENO alone on a block; an indexed ENO target; an EN with its own index
// check (lifted before the gate, as any IF condition's).
func TestTheGateCoversEveryCallShape(t *testing.T) {
	lib := "FUNCTION_BLOCK H\nVAR_INPUT x : INT; END_VAR\nVAR_OUTPUT y : INT; END_VAR\ny := x;\nEND_FUNCTION_BLOCK\n"
	src := "PROGRAM P\nVAR e : BOOL; t : TON; s : SR; h : H; k, n : INT; ok : ARRAY[0..3] OF BOOL; ens : ARRAY[0..3] OF BOOL; q : BOOL; END_VAR\n" +
		"t(EN := e, IN := TRUE, PT := T#1s, Q => q, ENO => ok[k]);\n" +
		"s(EN := e, S1 := TRUE, R := FALSE);\n" +
		"h(EN := ens[k], x := k, y => n);\n" +
		"h(x := k, y => n, ENO => q);\n" +
		"END_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	scl := fileOf(t, out, "naut_P.scl")
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
	// The timer under its gate, its latch copied, ENO into ok[k] checked.
	order("IF #e THEN", "#t(IN := TRUE, PT := T#1s", "#q := ", "ok[", ":= TRUE;", "ELSE", ":= FALSE;", "END_IF;")
	// The latch is written inline under the gate.
	order("IF #e THEN", "#s.Q1 :=", "END_IF;")
	// EN's index is checked before the gate; the user FB under it.
	order("#naut_ix", "IF #ens[#naut_ix", "#h(x := #k);", "#n := #h.y;", "END_IF;")
	// ENO alone: the call, its binding, then ENO TRUE.
	order("#h(x := #k);", "#n := #h.y;", "#q := TRUE;")
	if strings.Contains(scl, "EN :=") || strings.Contains(scl, "ENO =>") {
		t.Errorf("TIA's own EN or ENO is wired:\n%s", scl)
	}
}

// TestOwnENENOPinsAreRefused: a block that declares its own EN or ENO
// pin keeps it as an ordinary pin in nautilus; in SCL those names are
// TIA's execution control, so the writer refuses the block.
func TestOwnENENOPinsAreRefused(t *testing.T) {
	lib := "FUNCTION_BLOCK OwnB\nVAR_INPUT EN : BOOL; x : INT; END_VAR\nVAR_OUTPUT ENO : BOOL; y : INT; END_VAR\ny := x;\nENO := EN;\nEND_FUNCTION_BLOCK\n"
	src := "PROGRAM P\nVAR u : OwnB; e, ok : BOOL; k, n : INT; END_VAR\nu(EN := e, x := k, y => n, ENO => ok);\nEND_PROGRAM\n"
	_, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiag(diags, "OwnB's own EN pin") || !hasDiag(diags, "OwnB's own ENO pin") {
		t.Fatalf("own EN/ENO pins: %v", diags)
	}
}

// TestAbsOfALiteralIsFolded: TIA types a lone literal by its value (-128
// as SINT), so ABS of a constant is folded, with Go's -min = min.
func TestAbsOfALiteralIsFolded(t *testing.T) {
	scl, diags := writeP(t, "a : INT; l : LINT;", "a := ABS(-128);\nl := ABS(-2147483648);")
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl, "((LINT#128) + LINT#32768)", "#l := LINT#2147483648;")
}

// TestAtan2Arguments: an LREAL or a literal goes in as it is (the formal
// is LReal: the literal keeps every digit), a REAL is widened, an integer
// converted as nautilus converts it.
func TestAtan2Arguments(t *testing.T) {
	scl, diags := writeP(t, "lx, lr : LREAL; r : REAL; n : INT;", "lr := ATAN2(0.1, lx);\nr := ATAN2(r, n);")
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl, `#lr := "naut_ATAN2"(Y := 0.1, X := #lx);`, `#r := LREAL_TO_REAL("naut_ATAN2"(Y := REAL_TO_LREAL(#r), X := INT_TO_LREAL(#n)));`)
}
