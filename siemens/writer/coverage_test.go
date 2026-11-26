package writer

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
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

// TestSameWidthIdentity is the arithmetic identity the same-width form
// rests on, not a test of the writer: for INT operands the VM's 64-bit
// AND/OR/XOR/NOT (lang/ir vm.go: l.I & r.I on the sign-extended values)
// equals the 16-bit operation on their bits read back as an INT.
func TestSameWidthIdentity(t *testing.T) {
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

// TestLatchesMatchNautilus runs lang/ir's own SR and RS step against the
// SCL the writer emits for them, evaluated, over every input sequence of
// three scans: same Q1 every scan.
func TestLatchesMatchNautilus(t *testing.T) {
	scl, _ := write(t, "PROGRAM P\nVAR a, b, q : BOOL; f : SR; g : RS; END_VAR\nf(S1 := a, R := b);\ng(S := a, R1 := b);\nEND_PROGRAM\n")
	latch := map[string]string{}
	for _, l := range strings.Split(scl, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#f.Q1 := ") || strings.HasPrefix(l, "#g.Q1 := ") {
			latch[l[1:2]] = strings.TrimSuffix(strings.TrimPrefix(l[9:], " "), ";")
		}
	}
	if len(latch) != 2 {
		t.Fatalf("latch lines not found in\n%s", scl)
	}
	for _, c := range []struct{ inst, set, reset string }{{"SR", "S1", "R"}, {"RS", "S", "R1"}} {
		def := ir.LookupFBType(c.inst).FB
		name := map[string]string{"SR": "f", "RS": "g"}[c.inst]
		for seq := 0; seq < 64; seq++ {
			inst := &ir.FBInstance{Def: def, Slots: []ir.Value{ir.BoolVal(false), ir.BoolVal(false), ir.BoolVal(false)}}
			q := false
			for scan := 0; scan < 3; scan++ {
				s, r := seq>>(2*scan)&1 != 0, seq>>(2*scan+1)&1 != 0
				inst.Slots[0], inst.Slots[1] = ir.BoolVal(s), ir.BoolVal(r)
				if err := def.Step(inst, ir.FBStepCtx{}); err != nil {
					t.Fatal(err)
				}
				q = evalBool(t, latch[name], map[string]bool{
					"#" + name + "." + c.set: s, "#" + name + "." + c.reset: r, "#" + name + ".Q1": q,
				})
				if q != inst.Slots[2].B {
					t.Fatalf("%s sequence %06b scan %d: SCL %v, nautilus %v (%s)", c.inst, seq, scan, q, inst.Slots[2].B, latch[name])
				}
			}
		}
	}
}

// evalBool evaluates the boolean SCL the latch is written as: names,
// NOT (binding tightest), AND, OR and parentheses.
func evalBool(t *testing.T, src string, vars map[string]bool) bool {
	t.Helper()
	toks := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(src))
	pos := 0
	var or, atom func() bool
	atom = func() bool {
		tok := toks[pos]
		pos++
		switch tok {
		case "NOT":
			return !atom()
		case "(":
			v := or()
			pos++ // )
			return v
		}
		v, ok := vars[tok]
		if !ok {
			t.Fatalf("unknown name %q in %q", tok, src)
		}
		return v
	}
	and := func() bool {
		v := atom()
		for pos < len(toks) && toks[pos] == "AND" {
			pos++
			v = atom() && v
		}
		return v
	}
	or = func() bool {
		v := and()
		for pos < len(toks) && toks[pos] == "OR" {
			pos++
			v = and() || v
		}
		return v
	}
	return or()
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

// TestLibraryNamesAreWritten: the writer lowers a program with the
// project's library ahead of it, as naut check does, so a function, block
// or constant the library declares compiles, and the writer writes it
// (#1301), nautilus's PID too (D2: its own SCL block, pid.go); a name no
// source declares is nautilus's own refusal.
func TestLibraryNamesAreWritten(t *testing.T) {
	src := "PROGRAM P\nVAR a : REAL; END_VAR\na := Scale(a);\nEND_PROGRAM\n"
	lib := "FUNCTION Scale : REAL\nVAR_INPUT x : REAL; END_VAR\nScale := x * 2.0;\nEND_FUNCTION\n"
	if _, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{lib}}}}); len(diags) != 0 {
		t.Errorf("with the library: %v", diags)
	}
	// A GVL of constants (#176) declares its names too.
	if _, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a : REAL; END_VAR\na := HI;\nEND_PROGRAM\n", Libs: []string{"VAR_GLOBAL CONSTANT\n    HI : REAL := 80.0;\nEND_VAR\n"}}}}); len(diags) != 0 {
		t.Errorf("a GVL constant: %v", diags)
	}
	// A library function block, as an instance's type.
	fb := "FUNCTION_BLOCK Hold\nVAR_INPUT x : BOOL; END_VAR\nEND_FUNCTION_BLOCK\n"
	if _, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR h : Hold; END_VAR\nh(x := TRUE);\nEND_PROGRAM\n", Libs: []string{fb}}}}); len(diags) != 0 {
		t.Errorf("a library block: %v", diags)
	}
	// nautilus's PID, which TIA has no instruction for: its own block (D2).
	out, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR c : PID; END_VAR\nEND_PROGRAM\n"}}})
	if len(diags) != 0 || !strings.Contains(fileOf(t, out, "P.scl"), "      c : \"PID\";\n") {
		t.Errorf("PID: %v", diags)
	}
	_, diags, _ = Write(Input{Programs: []Program{{Path: "p.st", Source: src, Libs: []string{"(* Scale is mentioned here *)\nFUNCTION Scaled : REAL\nVAR_INPUT Scale : REAL; END_VAR\nScaled := Scale;\nEND_FUNCTION\n"}}}})
	if len(diags) != 1 || diags[0].Rule != "siemens/nautilus" || diags[0].Line != 3 || !strings.Contains(diags[0].Message, "line 3: ") {
		t.Errorf("without it: %v", diags)
	}
}

// TestEveryGapNamesItsIssue: no diagnostic speaks of a "first subset";
// every gap says the writer doesn't translate it yet and names one of the
// writer-coverage issues. The real refusals (nautilus's own, a name
// collision, a task TIA cannot run) keep their wording.
func TestEveryGapNamesItsIssue(t *testing.T) {
	// The writer-coverage issues filed for #789, B to G and the rest.
	issues := map[string]bool{"#1299": true, "#1300": true, "#1301": true, "#1303": true, "#1304": true, "#1305": true, "#1375": true}
	ref := regexp.MustCompile(`yet \((#\d+)(?:, (#\d+))?\)`)
	notGaps := map[string]bool{"siemens/nautilus": true, "siemens/name": true, "siemens/program": true}
	var diags []Diag
	for _, src := range []string{
		"PROGRAM P\nVAR s : WSTRING; t : LTIME; x : ARRAY[1..2] OF TON; d : DT; END_VAR\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : ARRAY[1..2] OF INT; i : INT; END_VAR\nREPEAT i := i + 1; UNTIL a[i] > 0 END_REPEAT;\nEND_PROGRAM\n",
		"PROGRAM P\nFUNCTION_BLOCK H\nVAR RETAIN n : INT; END_VAR\nEND_FUNCTION_BLOCK\nVAR h : H; END_VAR\nh();\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a, b : TIME; r, q : REAL; u : ULINT; END_VAR\na := a + b;\nr := r MOD q;\nu := u + 1;\na := T#1500us;\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : INT; r : REAL; t : TON; END_VAR\nr := ATAN2(r, r);\nt(IN := TRUE, PT := INT_TO_TIME(a + 1));\nEND_PROGRAM\n",
		"PROGRAM P\nVAR a : LTIME; END_VAR\nEND_PROGRAM\n",
		"TYPE Mode : (Run, Stop); END_TYPE\nPROGRAM P\nVAR a : Mode; s : STRING; END_VAR\ns := TO_STRING(a);\nEND_PROGRAM\n",
		"FUNCTION F : INT\nF := 1;\nEND_FUNCTION\nPROGRAM P\nVAR a : INT; END_VAR\na := F();\nEND_PROGRAM\n",
		"PROGRAM P\nVAR r : REAL; s : STRING; END_VAR\ns := REAL_TO_STRING(r);\nEND_PROGRAM\n",
	} {
		_, d, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}, Tasks: []Task{{Name: "main", Program: "P", Interval: time.Second}}})
		if err != nil {
			t.Fatal(err)
		}
		diags = append(diags, d...)
	}
	_, d, _ := Write(Input{Globals: []Global{{Name: "Name", Type: "WSTRING"}}})
	diags = append(diags, d...)
	_, d, _ = Write(Input{Globals: []Global{{Name: "A", Type: "ARRAY[1..2] OF REAL", Start: []any{1.0, 2.0}}}})
	diags = append(diags, d...)

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

// TestDivergencesAreWarnedWhereTheyBite: where nautilus faults the scan
// and the CPU sets ENO FALSE and carries on, the use site gets a warning
// (not an error): a MUX selector that may be out of range, a signed
// shift count, TRUNC and a REAL → integer conversion. A constant K in
// range, an unsigned count and an integer conversion get none.
func TestDivergencesAreWarnedWhereTheyBite(t *testing.T) {
	src := "PROGRAM P\nVAR a, k : INT; u : UINT; w : WORD; r : REAL; l : LINT; d : DINT; s : USINT; END_VAR\n" +
		"a := MUX(k, a, 1);\n" + // 3: warned
		"a := MUX(1, a, 2);\n" + // 4: constant in range
		"a := MUX(2, a, 2);\n" + // 5: constant out of range: warned
		"w := SHL(w, k);\n" + // 6: signed count: warned
		"w := SHL(w, u);\n" + // 7: unsigned count
		"l := TRUNC(r);\n" + // 8: warned
		"d := REAL_TO_DINT(r);\n" + // 9: warned
		"d := INT_TO_DINT(a);\n" + // 10
		"d := REAL_TO_DINT(2.5);\n" + // 11: a constant in range
		"l := TRUNC(-3.7);\n" + // 12: a constant in range
		"a := REAL_TO_INT(40000.0);\n" + // 13: a constant out of range: warned
		"s := REAL_TO_USINT(-1.0);\n" + // 14: a negative constant, no USINT: warned
		"END_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	var lines []int
	for _, w := range out.Warnings {
		// MUX and shifts stand at their line; TRUNC and REAL → integer
		// are the CLI's per-program summary.
		rule := map[int]string{3: "siemens/divergence", 5: "siemens/divergence", 6: "siemens/divergence"}[w.Line]
		if rule == "" {
			rule = ConversionRangeRule
		}
		if w.Rule != rule || w.Path != "p.st" || !strings.Contains(w.Message, " can differ if ") || !strings.Contains(w.Message, ": nautilus faults the scan; on Siemens ") {
			t.Errorf("warning %v", w)
		}
		lines = append(lines, w.Line)
	}
	if fmt.Sprint(lines) != "[3 5 6 8 9 13 14]" {
		t.Errorf("warned lines %v, want [3 5 6 8 9 13 14]: %v", lines, out.Warnings)
	}
	if out.Warnings[0].Message != "MUX can differ if the selector K is outside 0..1: nautilus faults the scan; on Siemens MUX sets ENO FALSE and the scan carries on" {
		t.Errorf("MUX warning %q", out.Warnings[0].Message)
	}

	// A refused program still reports its warnings.
	out, diags, _ = Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a, k : INT; r : REAL; END_VAR\na := MUX(k, a, 1);\nr := ATAN2(r, r);\nEND_PROGRAM\n"}}})
	if len(diags) == 0 || len(out.Warnings) != 1 || len(out.Files) != 0 {
		t.Errorf("refused: diags %v, warnings %v", diags, out.Warnings)
	}
}

// TestWarningsPointAtTheDiagramLine: a warning in an FBD (or ladder)
// program names the diagram's own line, as a refusal does.
func TestWarningsPointAtTheDiagramLine(t *testing.T) {
	src := []byte("PROGRAM F\n(* a note,\n   two lines *)\nVAR\n    K, X, Y, Z, M : INT;\nEND_VAR\nFBD\n  M := MAX(X, Y)\n\n  M := MUX(K := K, IN0 := X, IN1 := Y, IN2 := Z)\nEND_FBD\nEND_PROGRAM\n")
	st, _, err := toST(Program{Path: "f.fbd", Source: string(src)})
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(st, "\n") {
		if strings.Contains(l, "MUX(") && i+1 == 10 {
			t.Fatal("the ST has MUX on the FBD's line too, so this test cannot tell a mapped line from an unmapped one")
		}
	}
	out, _, err := Write(Input{Programs: []Program{{Path: "f.fbd", Source: string(src)}}})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")
	found := false
	for _, w := range out.Warnings {
		if !strings.HasPrefix(w.Message, "MUX ") {
			continue
		}
		found = true
		if w.Line < 1 || w.Line > len(lines) || !strings.Contains(lines[w.Line-1], "MUX(") {
			t.Errorf("MUX warning on f.fbd:%d, which is not the MUX line", w.Line)
		}
	}
	if !found {
		t.Fatalf("no MUX warning: %v", out.Warnings)
	}
}

// TestReviewFixes: a long bitwise chain types in linear time; a REAL
// division's INT operand inside a wide comparison converts once; a bit
// string compared with an integer compares 64-bit values, as nautilus
// does; MIN/MAX/LIMIT of bit strings are a gap.
func TestReviewFixes(t *testing.T) {
	chain := "a" + strings.Repeat(" AND b", 40)
	start := time.Now()
	scl, _ := write(t, "PROGRAM P\nVAR a, b : INT; END_VAR\na := "+chain+";\nEND_PROGRAM\n")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("a chain of 41 ANDs took %s", d)
	}
	wantLines(t, scl, "WORD_TO_INT(INT_TO_WORD(WORD_TO_INT(")

	scl, _ = write(t, "PROGRAM P\nVAR a, b : INT; r : REAL; w : WORD; q : BOOL; END_VAR\nq := (a + b) > r / a;\nq := a = w;\nEND_PROGRAM\n")
	wantLines(t, scl, `B := INT_TO_REAL(#a))`, "#q := INT_TO_LINT(#a) = UINT_TO_LINT(WORD_TO_UINT(#w));")
	if strings.Contains(scl, "INT_TO_REAL(INT_TO_LINT") {
		t.Errorf("a REAL division operand converted twice:\n%s", scl)
	}

	_, diags, _ := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR w, v : WORD; END_VAR\nw := MAX(w, v);\nEND_PROGRAM\n"}}})
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "MAX of bit strings yet (#1305)") {
		t.Errorf("MAX of WORDs: %v", diags)
	}
}

// TestRealTimeConversionsGoThroughDINT: TIA V21 has no REAL or LREAL
// conversion to or from TIME (lift-station's PumpSequence hit it), so the
// writer goes through DINT, TIA's TIME being a DINT of milliseconds. A
// REAL into TIME is warned (the CPU's TIME is 32-bit) unless it is a
// constant that fits.
func TestRealTimeConversionsGoThroughDINT(t *testing.T) {
	scl, out := write(t, "PROGRAM P\nVAR r : REAL; l : LREAL; t : TIME; END_VAR\nt := REAL_TO_TIME(r * 1000.0);\nt := LREAL_TO_TIME(l);\nr := TIME_TO_REAL(t);\nl := TIME_TO_LREAL(t);\nt := TO_TIME(r);\nt := REAL_TO_TIME(1500.0);\nEND_PROGRAM\n")
	wantLines(t, scl,
		"#t := DINT_TO_TIME(REAL_TO_DINT(#r * 1000.0));",
		"#t := DINT_TO_TIME(LREAL_TO_DINT(#l));",
		"#r := DINT_TO_REAL(TIME_TO_DINT(#t));",
		"#l := DINT_TO_LREAL(TIME_TO_DINT(#t));",
		"#t := DINT_TO_TIME(REAL_TO_DINT(#r));",
		"#t := DINT_TO_TIME(REAL_TO_DINT(1500.0));")
	var lines []int
	for _, w := range out.Warnings {
		if w.Rule == ConversionRangeRule && strings.Contains(w.Message, "32-bit milliseconds") {
			lines = append(lines, w.Line)
		}
	}
	if fmt.Sprint(lines) != "[3 4 7]" {
		t.Errorf("TIME range warnings on lines %v, want [3 4 7]: %v", lines, out.Warnings)
	}
	// A constant beyond the CPU's 32-bit TIME is refused, as T#35d is:
	// nautilus has the right TIME and the CPU cannot hold it.
	for _, c := range []string{"3.0E9", "-2147483649.0"} {
		_, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR t : TIME; END_VAR\nt := REAL_TO_TIME(" + c + ");\nEND_PROGRAM\n"}}, BlockPrefix: "naut_"})
		if err != nil || len(diags) == 0 || diags[0].Rule != "siemens/time" || !strings.Contains(diags[0].Message, "(#1304)") {
			t.Errorf("REAL_TO_TIME(%s): %v %v", c, diags, err)
		}
	}
	scl, _ = write(t, "PROGRAM P\nVAR t : TIME; END_VAR\nt := REAL_TO_TIME(-2147483648.0);\nEND_PROGRAM\n")
	wantLines(t, scl, "#t := DINT_TO_TIME(REAL_TO_DINT(-2147483648.0));")
}
