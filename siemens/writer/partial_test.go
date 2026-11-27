package writer

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #1305 B3: partial access.

// TestPartsAsTIASlices: bytes, words and double words as TIA's slices,
// read and written, LSB-first as nautilus numbers them; a part of a part
// flattened to one slice; .%L0 as the bit pattern.
func TestPartsAsTIASlices(t *testing.T) {
	scl, diags := writeP(t, "d : DINT; w : WORD; l : LINT; lw : LWORD; b : BYTE; ww : WORD; dw : DWORD; q : BOOL;", `
b := d.%B1;
ww := d.%W1;
dw := l.%D1;
lw := l.%L0;
lw := lw.%L0;
d.%B1 := 16#AA;
l.%D1 := d.%D0;
b := w.%W0.%B1;
q := d.%B1.%X3;
ww := l.%D1.%W1;`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"#b := #d.%B1;",
		"#ww := #d.%W1;",
		"#dw := #l.%D1;",
		"#lw := LINT_TO_LWORD(#l);",
		"#lw := #lw;",
		"#d.%B1 := 16#AA;",
		"#l.%D1 := DINT_TO_DWORD(#d);", // d.%D0: TIA has no slice as wide as the variable
		"#b := #w.%B1;",                // w.%W0.%B1: bits 8..15
		"#q := #d.%X11;",               // d.%B1.%X3: bit 8 + 3
		"#ww := #l.%W3;")               // l.%D1.%W1: bits 32 + 16
}

// TestTwoTasksWriteDifferentBytesOfOneTag: nautilus commits a shared tag
// bit by bit, so a byte one task writes must flag all of its bits, and
// copy them back only: the other task's byte of the same DWORD survives.
func TestTwoTasksWriteDifferentBytesOfOneTag(t *testing.T) {
	out, diags, err := Write(Input{
		Programs: []Program{
			{Path: "fast.st", Source: "PROGRAM Fast\nVAR_EXTERNAL S : DWORD; END_VAR\nS.%B0 := 16#11;\nEND_PROGRAM\n"},
			{Path: "slow.st", Source: "PROGRAM Slow\nVAR_EXTERNAL S : DWORD; END_VAR\nS.%B1 := 16#22;\nEND_PROGRAM\n"},
		},
		Globals:     []Global{{Name: "S", Type: "DWORD"}},
		Tasks:       []Task{{Name: "main", Program: "Fast", Interval: 50 * time.Millisecond}, {Name: "slow", Program: "Slow", Interval: 200 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	f := map[string]string{}
	for _, x := range out.Files {
		f[x.Path] = string(x.Data)
	}
	wantLines(t, f["naut_Fast.scl"], `"naut_Scan_main".assigned.S.%B0 := BYTE#16#FF;`)
	wantLines(t, f["naut_Slow.scl"], `"naut_Scan_slow".assigned.S.%B1 := BYTE#16#FF;`)
	copies := func(ob string, bit int) bool {
		return strings.Contains(f[ob], ".assigned.S.%X"+strconv.Itoa(bit)+" THEN")
	}
	for bit := 0; bit < 16; bit++ {
		mine, theirs := bit < 8, bit >= 8
		if copies("naut_OB_main.scl", bit) != mine {
			t.Errorf("main's copy-out of bit %d: %v, want %v", bit, !mine, mine)
		}
		if copies("naut_OB_slow.scl", bit) != theirs {
			t.Errorf("slow's copy-out of bit %d: %v, want %v", bit, !theirs, theirs)
		}
	}
	if t.Failed() {
		t.Logf("main OB:\n%s\nslow OB:\n%s", f["naut_OB_main.scl"], f["naut_OB_slow.scl"])
	}
}

func partialInput(t *testing.T) Input {
	t.Helper()
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var globals []Global
	for _, g := range []struct{ names, typ string }{
		{"Dx Neg Top rDw rNg FastN SlowN", "DINT"}, {"Lx rLm", "LINT"}, {"Wx rW0 rW1 rWm rIw", "WORD"}, {"Ix", "INT"}, {"Dx2 rIo", "DINT"}, {"rGt", "BOOL"}, {"Sh rD0", "DWORD"},
		{"rB0 rB1 rB2 rB3 rFl Bx rBf", "BYTE"}, {"rS", "SINT"}, {"rX11", "BOOL"},
		{"rL0 kStore kBin kCall kAtan kTemp kNeg", "LWORD"},
	} {
		for _, n := range strings.Fields(g.names) {
			globals = append(globals, Global{Name: n, Type: g.typ})
		}
	}
	return Input{
		Programs:    []Program{{Path: "Main.st", Source: read("testdata/partial/Main.st"), Libs: []string{read("testdata/partial/lib.st")}}, {Path: "slow.st", Source: read("testdata/partial/slow.st")}},
		Globals:     globals,
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: 50 * time.Millisecond}, {Name: "slow", Program: "Slow", Interval: 200 * time.Millisecond}},
		BlockPrefix: "naut_",
	}
}

// TestPartialGolden: the B3 fixture's SCL (testdata/partial/README.md),
// compiled on TIA V21 and run on the bench CPU against naut run.
func TestPartialGolden(t *testing.T) { goldenInput(t, "partial", partialInput(t)) }

// TestAPartInIntegerArithmeticIsWidened (Fable): in a 64-bit context a
// part is widened as its BYTE/WORD/DWORD twin is (nautilus adds the
// bytes' values at 64 bits: 200 + 100 is 300, not a byte's 44).
func TestAPartInIntegerArithmeticIsWidened(t *testing.T) {
	scl, diags := writeP(t, "d : DINT; q : BOOL; b, b2 : BYTE; q2 : BOOL;", `
q := d.%B0 + d.%B1 > 255;
q2 := b + b2 > 255;`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"#q := USINT_TO_LINT(BYTE_TO_USINT(#d.%B0)) + USINT_TO_LINT(BYTE_TO_USINT(#d.%B1)) > 255;",
		"#q2 := USINT_TO_LINT(BYTE_TO_USINT(#b)) + USINT_TO_LINT(BYTE_TO_USINT(#b2)) > 255;")
}

// TestLongPartAsAnInOutIsRefused (Fable): .%L0 of an integer has no TIA
// slice, so it cannot be an in-out actual.
func TestLongPartAsAnInOutIsRefused(t *testing.T) {
	lib := "FUNCTION_BLOCK F\nVAR_IN_OUT io : LWORD; END_VAR\nio := io;\nEND_FUNCTION_BLOCK\n"
	src := "PROGRAM P\nVAR f : F; l : LINT; END_VAR\nf(io := l.%L0);\nEND_PROGRAM\n"
	_, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}, BlockPrefix: "naut_"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiag(diags, "writing .%L0 of LINT") {
		t.Fatalf(".%%L0 as an in-out: %v", diags)
	}
}

// TestAPartAsWideAsItsVariable: TIA refuses a slice as wide as its
// variable ("Invalid access %D0 for Dx", bench 2026-11-27). It reads as
// the variable or its bit pattern; a write to one of another type is
// refused, one of the variable's own type writes the variable.
func TestAPartAsWideAsItsVariable(t *testing.T) {
	scl, diags := writeP(t, "d : DINT; s : SINT; i : INT; b, b2 : BYTE; w : WORD; dw : DWORD; q : BOOL;", `
dw := d.%D0;
b := s.%B0;
w := i.%W0;
b2 := b.%B0;
b.%B0 := 16#5A;
q := b.%B0 > 16#10;`)
	if diags != nil {
		t.Fatal(diags)
	}
	wantLines(t, scl,
		"#dw := DINT_TO_DWORD(#d);",
		"#b := SINT_TO_BYTE(#s);",
		"#w := INT_TO_WORD(#i);",
		"#b2 := #b;",
		"#b := 16#5A;",
		"#q := #b > 16#10;")
	for _, c := range []struct{ decl, body, want string }{
		{"d : DINT; v : DWORD;", "d.%D0 := v;", "writing .%D0 of DINT"},
		{"i : INT; v : WORD;", "i.%W0 := v;", "writing .%W0 of INT"},
	} {
		if _, diags := writeP(t, c.decl, c.body); !hasDiag(diags, c.want) {
			t.Errorf("%s: %v", c.body, diags)
		}
	}
}

// TestAWholeWidthPartOfASharedTagFlagsTheMember: B.%B0 of a shared BYTE
// is B itself; its flag is the member's, never a slice TIA refuses.
func TestAWholeWidthPartOfASharedTagFlagsTheMember(t *testing.T) {
	out, diags, err := Write(Input{
		Programs: []Program{
			{Path: "a.st", Source: "PROGRAM A\nVAR_EXTERNAL B : BYTE; END_VAR\nB.%B0 := 16#11;\nEND_PROGRAM\n"},
			{Path: "c.st", Source: "PROGRAM C\nVAR_EXTERNAL B : BYTE; END_VAR\nB := 16#22;\nEND_PROGRAM\n"},
		},
		Globals:     []Global{{Name: "B", Type: "BYTE"}},
		Tasks:       []Task{{Name: "main", Program: "A", Interval: 50 * time.Millisecond}, {Name: "slow", Program: "C", Interval: 200 * time.Millisecond}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	a := fileOf(t, out, "naut_A.scl")
	wantLines(t, a, `"naut_Scan_main".tags.B := 16#11;`, `"naut_Scan_main".assigned.B := TRUE;`)
	if strings.Contains(a, ".%B0") {
		t.Errorf("a slice as wide as B:\n%s", a)
	}
}
