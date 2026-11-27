package writer

import (
	"os"
	"strings"
	"testing"
	"time"
)

// #1305 B1: integer arithmetic where nautilus computes it at 64 bits.

func writeP(t *testing.T, decl, body string) (string, []Diag) {
	t.Helper()
	src := "PROGRAM P\nVAR " + decl + " END_VAR\n" + body + "\nEND_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		return "", diags
	}
	return fileOf(t, out, "naut_P.scl"), nil
}

// TestConversionsOfIntegerArithmetic: nautilus converts integer
// arithmetic from its 64-bit value (lang/ir conversions.go): to an integer
// it wraps (TO_INT), A_TO_B wraps to A first; to LINT nothing is lost.
func TestConversionsOfIntegerArithmetic(t *testing.T) {
	scl, diags := writeP(t, "d1, d2 : DINT; i : INT; l : LINT; r : REAL; tm : TIME;", `
i := TO_INT(d1 + d2);
l := TO_LINT(d1 + d2);
i := DINT_TO_INT(d1 + d2);
r := TO_REAL(d1 + d2);
tm := INT_TO_TIME(i + 1);`)
	if diags != nil {
		t.Fatal(diags)
	}
	sum := "DINT_TO_LINT(#d1) + DINT_TO_LINT(#d2)"
	wantLines(t, scl,
		// TO_INT: the 64-bit sum wrapped to INT.
		"#i := LINT_TO_INT(((("+sum+") + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768);",
		// TO_LINT: the 64-bit sum as it is.
		"#l := "+sum+";",
		// DINT_TO_INT: wrapped to DINT, then DINT to INT (wrapped again).
		"LINT_TO_DINT(((("+sum+") + LINT#2147483648) MOD LINT#4294967296",
		// TO_REAL: from the 64-bit value.
		"#r := LINT_TO_REAL("+sum+");",
		// INT_TO_TIME: wrapped to INT, then converted.
		"INT_TO_TIME(LINT_TO_INT((((INT_TO_LINT(#i) + 1) + LINT#32768)",
	)
}

// TestConversionsToBoolAndTime: TO_BOOL of integer arithmetic is "not
// zero" at 64 bits (nautilus); TO_TIME of it stays refused (#1304: TIA's
// TIME is 32-bit).
func TestConversionsToBoolAndTime(t *testing.T) {
	scl, diags := writeP(t, "n : INT; b : BOOL;", "b := TO_BOOL(n + 1);")
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl, "#b := (INT_TO_LINT(#n) + 1) <> 0;")
	if _, diags := writeP(t, "n : INT; tm : TIME;", "tm := TO_TIME(n + 1);"); !hasDiag(diags, "TO_TIME of integer arithmetic") {
		t.Fatalf("TO_TIME of integer arithmetic: %v", diags)
	}
	if _, diags := writeP(t, "u : ULINT; n : INT; l : LINT;", "l := MAX(u, n + 1);"); !hasDiag(diags, "integer arithmetic") {
		t.Fatalf("MAX of a ULINT and integer arithmetic: %v", diags)
	}
}

// TestSelPassesIntegerArithmeticThrough: an LD inline assignment
// (`ld_N := ld_N + 1` behind a contact) is SEL(…, ADD(…)): nautilus
// returns the 64-bit value and the store wraps it.
func TestSelPassesIntegerArithmeticThrough(t *testing.T) {
	scl, diags := writeP(t, "a : BOOL; n, m : INT;", `
n := SEL(a, n, n + 1);
m := MAX(n + 1, m);
m := LIMIT(0, n * 2, 100);`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"#n := LINT_TO_INT((((SEL(G := #a, IN0 := INT_TO_LINT(#n), IN1 := INT_TO_LINT(#n) + 1)) + LINT#32768)",
		"MAX(IN1 := INT_TO_LINT(#n) + 1, IN2 := INT_TO_LINT(#m))",
		"LIMIT(MN := 0, IN := INT_TO_LINT(#n) * 2, MX := 100)",
	)
	// Outside a 64-bit caller (an FB input) it stays refused.
	if _, diags := writeP(t, "c : CTU; a : BOOL; n : INT;", "c(CU := a, PV := SEL(a, n, n + 1));"); !hasDiag(diags, "integer arithmetic") {
		t.Fatalf("SEL of integer arithmetic into an FB input: %v", diags)
	}
}

// TestForLoops: a plain FOR only where it provably ends on the CPU; every
// other loop runs on a LINT counter that surely ends, with nautilus's
// semantics (exit values: TestForExitValue).
func TestForLoops(t *testing.T) {
	plain := func(decl, body string) {
		t.Helper()
		scl, diags := writeP(t, decl, body)
		if diags != nil {
			t.Fatal(diags)
		}
		if !strings.Contains(scl, "FOR #") || strings.Contains(scl, "WHILE") {
			t.Errorf("%s: want a plain FOR:\n%s", body, scl)
		}
	}
	counter := func(decl, body string, want ...string) string {
		t.Helper()
		scl, diags := writeP(t, decl, body)
		if diags != nil {
			t.Fatal(diags)
		}
		if strings.Contains(scl, "FOR #") || !strings.Contains(scl, "WHILE #naut_for_r") {
			t.Errorf("%s: want the counter loop:\n%s", body, scl)
		}
		wantLines(t, scl, want...)
		return scl
	}
	plain("i : INT; x : INT;", "FOR i := 1 TO 10 DO x := x + 1; END_FOR;")
	// TIA's FOR runs on a temp; the variable takes each value first thing
	// in the body, so a body that writes it does not change the loop.
	scl0, _ := writeP(t, "i : INT; x : INT;", "FOR i := 0 TO 10 DO i := 0; x := x + 1; END_FOR;")
	wantLines(t, scl0, "FOR #naut_for_v1 := 0 TO 10 DO", "#i := #naut_for_v1;\n\t\t#i := 0;")
	plain("i : INT; x : INT;", "FOR i := 10 TO 1 BY -2 DO x := i; END_FOR;")

	// The end is the type's last value: TIA's FOR could wrap round.
	counter("i : INT; x : INT;", "FOR i := 0 TO 32767 DO x := i; END_FOR;",
		"#naut_for_c1 := 0;", "#naut_for_e2 := 32767;", "#naut_for_r3 := #naut_for_c1 <= #naut_for_e2;",
		"#i := LINT_TO_INT(#naut_for_c1);",
		"#naut_for_r3 := #naut_for_c1 <= LINT#9223372036854775806 AND #naut_for_c1 + 1 <= #naut_for_e2;",
		"#naut_for_c1 := #naut_for_c1 + 1;")
	// A variable end, even of a narrower type: TIA's FOR is not proved to
	// read it once.
	counter("i : DINT; n : INT; x : DINT;", "FOR i := 1 TO n DO x := i; END_FOR;", "#naut_for_e2 := INT_TO_LINT(#n);")
	// A variable end of the variable's own type can be its last value.
	scl := counter("i, n : INT; x : INT;", "FOR i := 1 TO n DO x := i; END_FOR;", "#naut_for_e2 := INT_TO_LINT(#n);")
	if strings.Contains(scl, "#naut_fault") {
		t.Errorf("bounds of the variable's own type need no range guard:\n%s", scl)
	}
	// plclab: an end that is integer arithmetic, at 64 bits, guarded.
	counter("i, n, x : DINT;", "FOR i := 0 TO n - 1 DO x := i; END_FOR;",
		"#naut_for_e2 := DINT_TO_LINT(#n) - 1;",
		"IF #naut_for_c1 < LINT#-2147483648 OR #naut_for_c1 > LINT#2147483647 THEN",
		"#naut_fault.faulted := TRUE;",
		"#i := LINT_TO_DINT(#naut_for_c1);")
	// Descending, and a variable step (zero faults, as in nautilus).
	counter("i : INT; x : INT;", "FOR i := 0 TO -32768 BY -1 DO x := i; END_FOR;",
		"#naut_for_r3 := #naut_for_c1 >= #naut_for_e2;",
		"#naut_for_r3 := #naut_for_c1 >= LINT#-9223372036854775807 AND #naut_for_c1 - 1 >= #naut_for_e2;",
		"#naut_for_c1 := #naut_for_c1 - 1;")
	counter("i, s : INT; x : INT;", "FOR i := 1 TO 10 BY s DO x := i; END_FOR;",
		"#naut_for_s4 := INT_TO_LINT(#s);",
		"IF #naut_for_s4 = 0 THEN",
		"#naut_for_r3 := (#naut_for_s4 > 0 AND #naut_for_c1 <= #naut_for_e2) OR (#naut_for_s4 < 0 AND #naut_for_c1 >= #naut_for_e2);",
		"(#naut_for_s4 > 0 AND #naut_for_c1 <= LINT#9223372036854775807 - #naut_for_s4 AND #naut_for_c1 + #naut_for_s4 <= #naut_for_e2) OR (#naut_for_s4 < 0 AND #naut_for_c1 >= LINT#-9223372036854775807 - 1 - #naut_for_s4 AND #naut_for_c1 + #naut_for_s4 >= #naut_for_e2)")
	// A LINT variable is the counter's own type: no conversion, no guard.
	counter("i, n, x : LINT;", "FOR i := 1 TO n DO x := i; END_FOR;", "#i := #naut_for_c1;")

	// A step that is integer arithmetic stays refused.
	if _, diags := writeP(t, "i, s : INT; x : INT;", "FOR i := 1 TO 10 BY s + 1 DO x := i; END_FOR;"); !hasDiag(diags, "a FOR step that is integer arithmetic") {
		t.Fatalf("an integer arithmetic step: %v", diags)
	}
}

// TestForExitValue (#1427): after the loop the variable holds nautilus's
// exit value, the first value that failed the test wrapped to its type,
// start when no pass ran; a loop its body EXITs keeps the pass value (a
// flag skips the store). A LINT counter steps past LINT's range by
// wrapping, in an order that never leaves LINT.
func TestForExitValue(t *testing.T) {
	w := func(decl, body string, want ...string) string {
		t.Helper()
		scl, diags := writeP(t, decl, body)
		if diags != nil {
			t.Fatal(diags)
		}
		wantLines(t, scl, want...)
		return scl
	}
	// Plain FOR: the exit value is a constant.
	w("i, x : INT;", "FOR i := 1 TO 10 DO x := i; END_FOR;", "\tEND_FOR;\n\t#i := 11;\n")
	w("i, x : INT;", "FOR i := 10 TO 1 BY -3 DO x := i; END_FOR;", "\tEND_FOR;\n\t#i := -2;\n")
	w("i, x : INT;", "FOR i := 1 TO 10 BY 4 DO x := i; END_FOR;", "\tEND_FOR;\n\t#i := 13;\n")
	// A constant FOR that never runs: TIA refuses to compile it ("The high
	// limit is not higher than the low limit", bench 11-27), so the counter
	// loop writes it, and the variable takes start.
	for _, body := range []string{"FOR i := 5 TO 1 DO x := i; END_FOR;", "FOR i := 1 TO 5 BY -1 DO x := i; END_FOR;"} {
		scl := w("i, x : INT;", body, "\tEND_WHILE;\n\t#i := LINT_TO_INT((((#naut_for_c1) + LINT#32768)")
		if strings.Contains(scl, "FOR #") {
			t.Errorf("%s: a constant FOR that never runs is no TIA FOR:\n%s", body, scl)
		}
	}
	// Counter loop: the counter steps every pass and the variable takes it,
	// wrapped (to INT's last value: the exit value is -32768).
	w("i, x : INT;", "FOR i := 0 TO 32767 DO x := i; END_FOR;",
		"\t#naut_for_c1 := #naut_for_c1 + 1;\n\t\t#x := #i;\n\tEND_WHILE;\n\t#i := LINT_TO_INT((((#naut_for_c1) + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768);\n")
	// EXIT sets the loop's flag; an EXIT of a loop nested in it does not.
	scl := w("i, x : INT;", "FOR i := 1 TO 10 DO IF x > 3 THEN EXIT; END_IF; WHILE x > 0 DO EXIT; END_WHILE; END_FOR;",
		"\t#naut_for_x1 := FALSE;\n\tFOR #naut_for_v2 := 1 TO 10 DO",
		"\t\t\t#naut_for_x1 := TRUE;\n\t\t\tEXIT;\n",
		"\t\tWHILE #x > 0 DO\n\t\t\tEXIT;\n",
		"\tEND_FOR;\n\tIF NOT #naut_for_x1 THEN\n\t\t#i := 11;\n\tEND_IF;\n")
	if strings.Count(scl, ":= TRUE;") != 1 {
		t.Errorf("only the FOR's own EXIT sets its flag:\n%s", scl)
	}
	w("i, n, x : DINT;", "FOR i := 0 TO n - 1 DO x := i; IF x > 3 THEN EXIT; END_IF; END_FOR;",
		"#naut_for_x4 := FALSE;",
		"\tEND_WHILE;\n\tIF NOT #naut_for_x4 THEN\n\t\t#i := LINT_TO_DINT(")
	// No EXIT: no flag.
	if scl := w("i, x : INT;", "FOR i := 1 TO 10 DO x := i; END_FOR;"); strings.Contains(scl, "naut_for_x") {
		t.Errorf("a loop with no EXIT needs no flag:\n%s", scl)
	}
	// LINT: past LINT's range the step wraps, partial sums inside LINT.
	w("i, n, x : LINT;", "FOR i := 1 TO n BY 3 DO x := i; END_FOR;",
		"\tIF #naut_for_c1 <= LINT#9223372036854775804 THEN\n\t\t\t#naut_for_c1 := #naut_for_c1 + 3;\n\t\tELSE\n\t\t\t#naut_for_c1 := #naut_for_c1 - LINT#9223372036854775807 - 1 + 3 - LINT#9223372036854775807 - 1;",
		"\tEND_WHILE;\n\t#i := #naut_for_c1;\n")
	w("i, n, x : LINT;", "FOR i := n TO 1 BY -3 DO x := i; END_FOR;",
		"\tIF #naut_for_c1 >= LINT#-9223372036854775805 THEN\n\t\t\t#naut_for_c1 := #naut_for_c1 - 3;\n\t\tELSE\n\t\t\t#naut_for_c1 := #naut_for_c1 + LINT#9223372036854775807 + 1 + (-3) + LINT#9223372036854775807 + 1;")
	w("i, n, s, x : LINT;", "FOR i := n TO 1 BY s DO x := i; END_FOR;",
		"ELSE\n\t\t\t\t#naut_for_c1 := #naut_for_c1 - LINT#9223372036854775807 - 1 + #naut_for_s4 - LINT#9223372036854775807 - 1;",
		"ELSIF #naut_for_c1 >= (LINT#-9223372036854775807 - 1) - #naut_for_s4 THEN\n\t\t\t#naut_for_c1 := #naut_for_c1 + #naut_for_s4;",
		"ELSE\n\t\t\t#naut_for_c1 := #naut_for_c1 + LINT#9223372036854775807 + 1 + #naut_for_s4 + LINT#9223372036854775807 + 1;")
}

// The wrapping steps' arithmetic: c + k - 2^64 and c + k + 2^64, left to
// right, never leave LINT and land on nautilus's wrapped exit value.
func TestForLintStepWraps(t *testing.T) {
	const max, min = int64(1<<63 - 1), int64(-1 << 63)
	for _, k := range []int64{1, 3, 1 << 40, max} {
		for _, c := range []int64{max, max - k + 1} {
			// c - max - 1 + k - max - 1, each partial sum checked.
			sums := []int64{c - max, c - max - 1, c - max - 1 + k, c - max - 1 + k - max, c - max - 1 + k - max - 1}
			if sums[2] < 0 || sums[1] > 0 { // overflow-free bounds of the middle sums
				t.Fatalf("c=%d k=%d: partial sums %v", c, k, sums)
			}
			if got, want := sums[4], c+k; got != want { // Go wraps c+k
				t.Errorf("c=%d k=%d: %d, want %d", c, k, got, want)
			}
		}
		for _, c := range []int64{min, min + k - 1} {
			nk := -k
			sums := []int64{c + max, c + max + 1, c + max + 1 + nk, c + max + 1 + nk + max, c + max + 1 + nk + max + 1}
			if sums[1] < 0 || sums[2] > 0 {
				t.Fatalf("c=%d k=%d: partial sums %v", c, nk, sums)
			}
			if got, want := sums[4], c+nk; got != want {
				t.Errorf("c=%d k=%d: %d, want %d", c, nk, got, want)
			}
		}
	}
}

// TestIntWidthGolden: the B1 fixture's SCL (testdata/intwidth/README.md),
// compiled on TIA V21 and run on the bench CPU against naut run.
func TestIntWidthGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/intwidth/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	var globals []Global
	for _, g := range []struct{ names, typ string }{
		{"D1 D2", "DINT"}, {"N M S Z", "INT"}, {"DoFault DoZero", "BOOL"},
		{"rToInt rDintToInt rSel rMax rLimit", "INT"}, {"rToLint", "LINT"}, {"rToReal", "REAL"},
		{"itTop itBelow itDown itVarEnd itVarStep itPlclab", "DINT"}, {"exTop exBelow exDown exVarEnd", "INT"}, {"exPlclab Scans itPlain", "DINT"}, {"exPlain", "INT"},
	} {
		for _, n := range strings.Fields(g.names) {
			globals = append(globals, Global{Name: n, Type: g.typ})
		}
	}
	goldenInput(t, "intwidth", Input{
		Programs:    []Program{{Path: "Main.st", Source: string(src)}},
		Globals:     globals,
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
}
