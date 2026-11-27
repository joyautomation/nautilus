package writer

import (
	"testing"
	"time"
)

// #1429: TIA keeps 15 significant digits of an LReal literal.

// TestLongLRealLiteralsAreBitPatterns: an LREAL literal 15 digits cannot
// reproduce is written as its bit pattern, wherever it is an LREAL; a
// short one, and any REAL literal, as written.
func TestLongLRealLiteralsAreBitPatterns(t *testing.T) {
	scl, diags := writeP(t, "lr, lx : LREAL; r, rx : REAL; b : BOOL;", `
lr := 3.141592653589793;
lr := 0.1;
r := 3.141592653589793;
lr := lx * 3.141592653589793;
r := rx * 3.141592653589793;
b := lx > 2.718281828459045;
lr := lx + REAL_TO_LREAL(rx * 0.1234567890123456);
lr := MAX(lx, 3.141592653589793);
lr := -3.141592653589793;
lr := 1.0 + 3.141592653589793;
r := LREAL_TO_REAL(MAX(lx, 3.141592653589793));`)
	if diags != nil {
		t.Fatal(diags)
	}
	pi := "LWORD_TO_LREAL(16#400921FB54442D18) (* 3.141592653589793 *)"
	wantLines(t, scl,
		"#lr := "+pi+";",
		"#lr := 0.1;",
		"#r := 3.141592653589793;", // a REAL needs 9 digits: TIA's 15 are enough
		"#lr := #lx * "+pi+";",
		"#r := #rx * 3.141592653589793;",
		"#b := #lx > LWORD_TO_LREAL(16#4005BF0A8B145769) (* 2.718281828459045 *);",
		"REAL_TO_LREAL(#rx * 0.1234567890123456)", // a REAL operand inside an LREAL expression
		"MAX(IN1 := #lx, IN2 := "+pi+")",
		"#lr := -"+pi+";",
		"#lr := 1.0 + "+pi+";",
		// Only the call says LREAL here: the store is a REAL.
		"#r := LREAL_TO_REAL(MAX(IN1 := #lx, IN2 := "+pi+"));")
}

// TestLongStartValuesAreRefused: a start value is one literal in SCL, and
// TIA would keep 15 of its digits.
func TestLongStartValuesAreRefused(t *testing.T) {
	if _, diags := writeP(t, "lr : LREAL := 3.141592653589793;", "lr := lr;"); !hasDiag(diags, "more than the 15 significant digits TIA keeps of a literal") || !hasDiag(diags, "(#1429)") {
		t.Fatalf("a long LREAL start value: %v", diags)
	}
	if _, diags := writeP(t, "lr : LREAL := 3.14159265358979;", "lr := lr;"); diags != nil {
		t.Fatalf("a 15-digit start value: %v", diags)
	}
	// A manifest tag's init.
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR_EXTERNAL g : LREAL; END_VAR\ng := g;\nEND_PROGRAM\n"}},
		Globals: []Global{{Name: "g", Type: "LREAL", Init: "3.141592653589793"}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiag(diags, "(#1429)") {
		t.Fatalf("a long LREAL tag init: %v %v", diags, len(out.Files))
	}
}
