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
	if strings.Contains(scl, "ENO") && strings.Contains(scl, "ENO =>") {
		t.Errorf("TIA's own ENO is wired:\n%s", scl)
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
		{"A16 rA16 rAbs5 cv", "INT"}, {"A32 rA32 Bumps Scans", "DINT"}, {"A64 rA64", "LINT"},
		{"En Cu eno1 eno2", "BOOL"}, {"X rLim", "REAL"},
		{"Sp", "ARRAY[0..48] OF LREAL"}, {"Gr", "ARRAY[0..143] OF REAL"},
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
