package writer

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// write writes one ST program and fails on any diagnostic; it returns the
// program FB's SCL and the output.
func write(t *testing.T, src string) (string, Output) {
	t.Helper()
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, BlockPrefix: "naut_"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, diags)
	}
	for _, f := range out.Files {
		if f.Path == "naut_P.scl" {
			return string(f.Data), out
		}
	}
	t.Fatalf("no naut_P.scl in %v", out.Files)
	return "", out
}

func wantLines(t *testing.T, scl string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(scl, w) {
			t.Errorf("missing %q in\n%s", w, scl)
		}
	}
}

// TestCoverageGolden: every PR A construct in one program, compared with
// testdata/coverage/golden/ (the source of the tia1 compile, README).
func TestCoverageGolden(t *testing.T) {
	out := golden(t, "coverage", coverageGlobals)
	if got := out.Files[0].Path; got != "naut_DIV_LINT.scl" {
		t.Errorf("the guarded division FCs must come first (tiad generates in order); first file is %s", got)
	}
	for path, want := range map[string]string{"Main.latch.Q1": `"naut_Main_DB".latch.Q1`, "Main.unlatch": `"naut_Main_DB".unlatch`} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
	if src := out.Sources["naut_DIV_LINT.scl"]; src.Path != "Main.st" {
		t.Errorf("the FC's source = %+v, want the program that first called it", src)
	}
}

// TestBitwise: AND/OR/XOR of one type on the bit string of its width, so
// the operation is on exactly its bits; a literal takes the other
// operand's type when it holds it; anything else, and NOT, at 64 bits as
// nautilus computes them, the store wrapping.
func TestBitwise(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR a, b : INT; u : UINT; d : DINT; w, v : WORD; q : BOOL; END_VAR\n"+
		"a := a AND b;\na := a OR -2;\nw := w XOR v;\nw := w AND 16#FF;\nd := a OR d;\n"+
		"a := NOT a;\nw := NOT w;\nd := NOT u;\nq := NOT u > 5;\na := a AND 16#FFFF;\nEND_PROGRAM\n")
	wantLines(t, scl,
		"#a := WORD_TO_INT(INT_TO_WORD(#a) AND INT_TO_WORD(#b));",
		"#a := WORD_TO_INT(INT_TO_WORD(#a) OR WORD#16#FFFE);",
		"#w := #w XOR #v;",
		"#w := #w AND WORD#16#FF;",
		// INT OR DINT: nautilus ORs the 64-bit values.
		"LWORD_TO_LINT(LINT_TO_LWORD(INT_TO_LINT(#a)) OR LINT_TO_LWORD(DINT_TO_LINT(#d)))",
		// NOT stored into its own type: the type's bits.
		"#a := WORD_TO_INT(NOT INT_TO_WORD(#a));",
		"#w := NOT #w;",
		// NOT of a UINT is a negative 64-bit value in nautilus: stored
		// into a DINT it wraps, compared it is negative.
		"LWORD_TO_LINT(NOT LINT_TO_LWORD(UINT_TO_LINT(#u)))",
		"#q := LWORD_TO_LINT(NOT LINT_TO_LWORD(UINT_TO_LINT(#u))) > 5;",
		// 16#FFFF is no INT: 64 bits, where a AND 16#FFFF of -1 is 65535.
		"LWORD_TO_LINT(LINT_TO_LWORD(INT_TO_LINT(#a)) AND LWORD#16#FFFF)",
	)
}

// TestBitwiseMatchesNautilus checks the two forms' arithmetic against the
// VM's 64-bit rule for every INT, UINT and WORD case that matters: the
// same-width form must equal the 64-bit result cut to the type.
func TestBitwiseMatchesNautilus(t *testing.T) {
	vals := []int64{0, 1, -1, -2, 255, -32768, 32767, 0x5A5A}
	for _, x := range vals {
		for _, y := range vals {
			for _, op := range []string{"AND", "OR", "XOR"} {
				var v64 int64
				switch op {
				case "AND":
					v64 = x & y
				case "OR":
					v64 = x | y
				default:
					v64 = x ^ y
				}
				// INT: 16-bit two's complement of each, op, back to INT.
				w := uint16(x)
				z := uint16(y)
				var r uint16
				switch op {
				case "AND":
					r = w & z
				case "OR":
					r = w | z
				default:
					r = w ^ z
				}
				if int64(int16(r)) != v64 {
					t.Fatalf("%d %s %d: 16-bit %d, nautilus %d", x, op, y, int16(r), v64)
				}
			}
		}
	}
	// NOT of an INT cut back to INT is NOT of its bits.
	for _, x := range vals {
		if int64(int16(^uint16(x))) != int64(int16(^x)) {
			t.Fatalf("NOT %d", x)
		}
	}
}

// TestGuardedDivision: a divisor that is not a non-zero constant calls an
// FC that answers 0 for 0, as nautilus does; a constant divisor stays
// inline. Only the FCs used are written, first, once each.
func TestGuardedDivision(t *testing.T) {
	scl, out := write(t, "PROGRAM P\nVAR a, b : INT; r, s : REAL; x : LREAL; END_VAR\n"+
		"a := a / b;\na := b / b;\na := a MOD 0;\nr := r / s;\nr := r / a;\nx := x / x;\na := a / 2;\nEND_PROGRAM\n")
	wantLines(t, scl,
		`"naut_DIV_LINT"(A := INT_TO_LINT(#a), B := INT_TO_LINT(#b))`,
		`"naut_MOD_LINT"(A := INT_TO_LINT(#a), B := 0)`,
		`#r := "naut_DIV_REAL"(A := #r, B := #s);`,
		`#r := "naut_DIV_REAL"(A := #r, B := INT_TO_REAL(#a));`,
		`#x := "naut_DIV_LREAL"(A := #x, B := #x);`,
		"((INT_TO_LINT(#a) / 2) + LINT#32768)",
	)
	var fcs []string
	for _, f := range out.Files {
		if strings.HasPrefix(string(f.Data), "FUNCTION ") {
			fcs = append(fcs, f.Path)
		}
	}
	if fmt.Sprint(fcs) != "[naut_DIV_LINT.scl naut_DIV_LREAL.scl naut_DIV_REAL.scl naut_MOD_LINT.scl]" {
		t.Errorf("FCs = %v", fcs)
	}
	if out.Files[len(fcs)-1].Path != "naut_MOD_LINT.scl" {
		t.Errorf("the FCs must come first: %v", out.Files)
	}
	fc := string(out.Files[0].Data)
	wantLines(t, fc, `FUNCTION "naut_DIV_LINT" : LINT`, "IF #B = 0 THEN", "#naut_DIV_LINT := 0;", "#naut_DIV_LINT := #A / #B;")

	// First even when a program's FB sorts before them: tiad generates
	// the sources in order, and an FB calls its FC.
	outA, _, _ := Write(Input{Programs: []Program{{Path: "a.st", Source: "PROGRAM Alpha\nVAR a, b : INT; END_VAR\na := a / b;\nEND_PROGRAM\n"}}, BlockPrefix: "naut_"})
	if len(outA.Files) == 0 || outA.Files[0].Path != "naut_DIV_LINT.scl" {
		t.Errorf("the FC must come before naut_Alpha.scl: %v", outA.Files)
	}

	// None used: none written.
	_, out = write(t, "PROGRAM P\nVAR a : INT; END_VAR\na := a / 2;\nEND_PROGRAM\n")
	if len(out.Files) != 2 {
		t.Errorf("files = %v", out.Files)
	}
	// An FC's name is claimed like any block.
	_, diags, _ := Write(Input{Programs: []Program{
		{Path: "p.st", Source: "PROGRAM DIV_LINT\nVAR a, b : INT; END_VAR\na := a / b;\nEND_PROGRAM\n"},
	}, BlockPrefix: "naut_"})
	if len(diags) != 1 || diags[0].Rule != "siemens/name" {
		t.Errorf("want a block name collision, got %v", diags)
	}
}

// TestFunctionTable: TRUNC (nautilus's 64-bit int), TRUNC_<int>, TO_* from
// the argument's type, MIN/MAX of more than two, MUX, shifts and rotates
// on the bit string of the operand's width, named arguments in order.
func TestFunctionTable(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR a, b, n : INT; u : UINT; d : DINT; w : WORD; l : LINT; r : REAL; ok : BOOL; END_VAR\n"+
		"l := TRUNC(r);\nd := TRUNC_DINT(r);\nd := TO_DINT(a);\nr := TO_REAL(a);\na := TO_INT(a);\nu := TO_UINT(a);\n"+
		"a := MAX(a, b, n);\na := MIN(IN2 := a, IN1 := b);\na := MUX(n, a, b, 7);\na := LIMIT(MX := 10, IN := a, MN := 0);\n"+
		"a := SEL(IN1 := a, G := ok, IN0 := b);\n"+
		"w := SHL(w, n);\na := SHR(a, n);\nw := ROL(w, 3);\nu := ROR(u, u);\nEND_PROGRAM\n")
	wantLines(t, scl,
		"#l := TRUNC_LINT(#r);",
		"#d := TRUNC_DINT(#r);",
		"#d := INT_TO_DINT(#a);",
		"#r := INT_TO_REAL(#a);",
		"#a := #a;",
		// INT into UINT wraps, as INT_TO_UINT does in nautilus.
		"#u := LINT_TO_UINT(((INT_TO_LINT(#a)) MOD LINT#65536 + LINT#65536) MOD LINT#65536);",
		"#a := MAX(IN1 := #a, IN2 := #b, IN3 := #n);",
		"#a := MIN(IN1 := #b, IN2 := #a);",
		"#a := MUX(K := #n, IN0 := #a, IN1 := #b, IN2 := 7);",
		"#a := LIMIT(MN := 0, IN := #a, MX := 10);",
		"#a := SEL(G := #ok, IN0 := #b, IN1 := #a);",
		"#w := SHL(IN := #w, N := INT_TO_UINT(#n));",
		// SHR of an INT is logical over its 16 bits in nautilus.
		"#a := WORD_TO_INT(SHR(IN := INT_TO_WORD(#a), N := INT_TO_UINT(#n)));",
		"#w := ROL(IN := #w, N := 3);",
		"#u := WORD_TO_UINT(ROR(IN := UINT_TO_WORD(#u), N := #u));",
	)
}

// TestLatches: SR and RS as a static STRUCT, the latch inline, inputs
// kept across calls (a call may leave one out), the output read anywhere.
func TestLatches(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR s, r, q : BOOL; f : SR; g : RS; END_VAR\n"+
		"f(S1 := s, R := r, Q1 => q);\ng(S := s);\ng(R1 := r);\nq := g.Q1 OR f.Q1;\nEND_PROGRAM\n")
	wantLines(t, scl,
		"      f : Struct\n         S1 : Bool;\n         R : Bool;\n         Q1 : Bool;\n      END_STRUCT;",
		"      g : Struct\n         S : Bool;\n         R1 : Bool;\n         Q1 : Bool;\n      END_STRUCT;",
		"\t#f.S1 := #s;\n\t#f.R := #r;\n\t#f.Q1 := #f.S1 OR (#f.Q1 AND NOT #f.R);\n\t#q := #f.Q1;\n",
		"\t#g.S := #s;\n\t#g.Q1 := NOT #g.R1 AND (#g.S OR #g.Q1);\n",
		"\t#g.R1 := #r;\n\t#g.Q1 := NOT #g.R1 AND (#g.S OR #g.Q1);\n",
		"#q := #g.Q1 OR #f.Q1;",
	)
}

// TestLatchesMatchNautilus drives the inline forms through every input
// sequence of two scans and compares them with lang/ir's SR and RS.
func TestLatchesMatchNautilus(t *testing.T) {
	sr := func(s1, r, q bool) bool { return s1 || (q && !r) }       // lang/ir builtins_fb.go
	rs := func(s, r1, q bool) bool { return !r1 && (s || q) }       // lang/ir builtins_fb.go
	srInline := func(s1, r, q bool) bool { return s1 || (q && !r) } // #f.S1 OR (#f.Q1 AND NOT #f.R)
	rsInline := func(s, r1, q bool) bool { return !r1 && (s || q) } // NOT #g.R1 AND (#g.S OR #g.Q1)
	for i := 0; i < 8; i++ {
		a, b, q := i&1 != 0, i&2 != 0, i&4 != 0
		if sr(a, b, q) != srInline(a, b, q) || rs(a, b, q) != rsInline(a, b, q) {
			t.Fatalf("latch differs at %v %v %v", a, b, q)
		}
	}
}

// TestBitAccess: .n and .%Xn, read and written, as TIA's .%Xn.
func TestBitAccess(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR w : WORD; a : INT; q : BOOL; END_VAR\nq := w.3 AND a.%X15;\nw.4 := q;\nEND_PROGRAM\n")
	wantLines(t, scl, "#q := #w.%X3 AND #a.%X15;", "#w.%X4 := #q;")
}

// TestBitStringArithmetic: a bit string is an unsigned integer of its
// width in nautilus's arithmetic: widened by its unsigned value, the
// store wrapping it back.
func TestBitStringArithmetic(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR w, v : WORD; END_VAR\nw := w + 1;\nw := (w AND v) + 1;\nEND_PROGRAM\n")
	wantLines(t, scl, "#w := UINT_TO_WORD(LINT_TO_UINT(((UINT_TO_LINT(WORD_TO_UINT(#w)) + 1) MOD LINT#65536 + LINT#65536) MOD LINT#65536));",
		// A WORD AND WORD inside arithmetic is widened like a WORD.
		"#w := UINT_TO_WORD(LINT_TO_UINT(((UINT_TO_LINT(WORD_TO_UINT(#w AND #v)) + 1) MOD LINT#65536 + LINT#65536) MOD LINT#65536));")
}

// TestSFCIsAGap: an SFC program is the writer's gap (#1302), not a parse
// error, and its PROGRAM name still reads for the task.
func TestSFCIsAGap(t *testing.T) {
	src := "PROGRAM Seq\nVAR_EXTERNAL Go : BOOL; END_VAR\nSFC\n  INITIAL_STEP Idle:\n  END_STEP\nEND_SFC\nEND_PROGRAM\n"
	p := Program{Path: "seq.sfc", Source: src}
	if name, err := ProgramName(p); err != nil || name != "Seq" {
		t.Fatalf("ProgramName = %q, %v", name, err)
	}
	_, diags, err := Write(Input{Programs: []Program{p}, Tasks: []Task{{Name: "main", Program: "Seq", Interval: time.Second}}})
	if err != nil || len(diags) == 0 || diags[0].Rule != "siemens/sfc" || !strings.Contains(diags[0].Message, "(#1302)") {
		t.Fatalf("%v %v", diags, err)
	}
	if got := ExternalTypes([]Program{p}); got["go"] != "BOOL" {
		t.Errorf("ExternalTypes = %v", got)
	}
}

// TestExternalTypes: the type each program binds a tag as, from VAR_EXTERNAL
// and VAR_GLOBAL, ladder included.
func TestExternalTypes(t *testing.T) {
	got := ExternalTypes([]Program{
		{Path: "a.st", Source: "PROGRAM A\nVAR_EXTERNAL X, Y : INT; END_VAR\nVAR z : REAL; END_VAR\nEND_PROGRAM\n"},
		{Path: "b.st", Source: "not a program"},
	})
	if fmt.Sprint(got) != "map[x:INT y:INT]" {
		t.Errorf("ExternalTypes = %v", got)
	}
}

// TestLibraryNamesAreAGap: a name the lowering does not know is the
// writer's gap when a library source declares it (the writer lowers the
// program alone), and nautilus's refusal when none does.
func TestLibraryNamesAreAGap(t *testing.T) {
	src := "PROGRAM P\nVAR a : REAL; END_VAR\na := Scale(a);\nEND_PROGRAM\n"
	lib := "FUNCTION Scale : REAL\nVAR_INPUT x : REAL; END_VAR\nScale := x * 2.0;\nEND_FUNCTION\n"
	_, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}})
	if len(diags) != 1 || diags[0].Rule != "siemens/library" || !strings.Contains(diags[0].Message, "(#1299, #1301)") {
		t.Errorf("with the library: %v", diags)
	}
	_, diags, _ = Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{"FUNCTION Scaled : REAL\nEND_FUNCTION\n"}}}})
	if len(diags) != 1 || diags[0].Rule != "siemens/nautilus" {
		t.Errorf("without it: %v", diags)
	}
}

// TestEveryGapNamesItsIssue: no diagnostic speaks of a "first subset";
// every gap says the writer doesn't translate it yet and names one of the
// writer-coverage issues. The real refusals (nautilus's own, a name
// collision, a task TIA cannot run) keep their wording.
func TestEveryGapNamesItsIssue(t *testing.T) {
	// The writer-coverage issues filed for #789, B to G and the rest.
	issues := map[string]bool{"#1299": true, "#1300": true, "#1301": true, "#1302": true, "#1303": true, "#1304": true, "#1305": true}
	ref := regexp.MustCompile(`yet \((#\d+)(?:, (#\d+))?\)`)
	notGaps := map[string]bool{"siemens/nautilus": true, "siemens/name": true, "siemens/program": true}
	var diags []Diag
	for _, src := range []string{
		"PROGRAM P\nVAR s : STRING; t : LTIME; x : ARRAY[1..2] OF INT; d : DT; END_VAR\nEND_PROGRAM\n",
		"PROGRAM P\nVAR CONSTANT k : INT := 1; END_VAR\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a, b : TIME; r, q : REAL; u : ULINT; END_VAR\na := a + b;\nr := r MOD q;\nu := u + 1;\na := T#1500us;\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : INT; r : REAL; t : TON; END_VAR\nr := ATAN2(r, r);\nt(IN := TRUE, PT := INT_TO_TIME(a + 1));\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : LTIME; END_VAR\nEND_PROGRAM\n",
		"TYPE Mode : (Run, Stop); END_TYPE\nPROGRAM P\nVAR a : INT; END_VAR\nEND_PROGRAM\n",
		"FUNCTION F : INT\nF := 1;\nEND_FUNCTION\nPROGRAM P\nVAR a : INT; END_VAR\na := F();\nEND_PROGRAM\n",
	} {
		_, d, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{
			{Name: "main", Program: "P", Interval: time.Second}, {Name: "slow", Program: "P", Interval: time.Second},
		}})
		if err != nil {
			t.Fatal(err)
		}
		diags = append(diags, d...)
	}
	_, d, _ := Write(Input{Globals: []Global{{Name: "Name", Type: "STRING"}}})
	diags = append(diags, d...)
	diags = append(diags, Diag{Rule: "siemens/type", Message: StructuredInitGap(map[string]any{"a": 1})})

	gaps := 0
	for _, d := range diags {
		if strings.Contains(d.Message, "first subset") {
			t.Errorf("still says first subset: %v", d)
		}
		if notGaps[d.Rule] || strings.Contains(d.Message, "which the writer could not write") {
			continue
		}
		m := ref.FindStringSubmatch(d.Message)
		if m == nil || !strings.HasPrefix(d.Message, "the Siemens writer doesn't translate ") && !strings.Contains(d.Message, ": the Siemens writer doesn't translate ") {
			t.Errorf("not worded as a gap: %v", d)
			continue
		}
		for _, n := range m[1:] {
			if n != "" && !issues[n] {
				t.Errorf("%v names %s, which is no writer-coverage issue", d, n)
			}
		}
		gaps++
	}
	if gaps < 12 {
		t.Errorf("only %d gaps exercised: %v", gaps, diags)
	}
}
