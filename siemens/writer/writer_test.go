package writer

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var benchGlobals = []Global{
	{Name: "Heartbeat", Type: "UINT"},
	{Name: "Echo", Type: "UINT"},
	{Name: "Command", Type: "UINT"},
	{Name: "Start", Type: "BOOL"},
	{Name: "Running", Type: "BOOL"},
	{Name: "Fault", Type: "BOOL"},
	{Name: "Reset", Type: "BOOL"},
	{Name: "Done", Type: "BOOL"},
	{Name: "Level", Type: "REAL", Init: "12.5", Retain: true},
}

// TestGolden writes the bench fixture and compares every generated file
// with testdata/bench/golden/. The goldens encode what TIA's exported
// sources look like as far as public knowledge goes; they are unverified
// against a TIA compile until S2b, which corrects them from real exports.
func TestGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/bench/Main.st")
	if err != nil {
		t.Fatal(err)
	}
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "Main.st", Source: string(src)}},
		Globals:  benchGlobals,
		Tasks:    []Task{{Name: "main", Program: "Main", Interval: 100 * time.Millisecond}},
		// The default the CLI passes: the FB "Main" would otherwise
		// collide with TIA's own "Main" [OB1] (#789).
		BlockPrefix: "naut_",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Fatalf("diagnostics: %v", diags)
	}
	dir := "testdata/bench/golden"
	if *update {
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
		for _, f := range out.Files {
			os.WriteFile(filepath.Join(dir, f.Path), f.Data, 0o644)
		}
	}
	got := map[string]string{}
	for _, f := range out.Files {
		got[f.Path] = string(f.Data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no goldens (run go test ./siemens/writer/ -update): %v", err)
	}
	if len(entries) != len(out.Files) {
		t.Errorf("golden files = %d, generated = %d", len(entries), len(out.Files))
	}
	for _, e := range entries {
		want, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		if got[e.Name()] != string(want) {
			t.Errorf("%s differs from its golden (go test ./siemens/writer/ -update, then review the diff):\n%s", e.Name(), got[e.Name()])
		}
	}

	if len(out.OBs) != 1 || out.OBs[0].Number != 30 || out.OBs[0].Cycle != 100*time.Millisecond {
		t.Errorf("OBs: %+v", out.OBs)
	}
	for path, want := range map[string]string{
		"Level":        `"naut_Tags".Level`,
		"Main.count":   `"naut_Main_DB".count`,
		"main.dwell.Q": `"naut_Main_DB".dwell.Q`,
	} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestTaskRules(t *testing.T) {
	prog := []Program{{Path: "p.st", Source: "PROGRAM P\nVAR x : REAL; END_VAR\nx := x + 1.0;\nEND_PROGRAM\n"}}
	for _, c := range []struct {
		name  string
		tasks []Task
		want  string
	}{
		{"no interval", []Task{{Name: "main", Program: "P"}}, "no interval"},
		{"over 60 s", []Task{{Name: "main", Program: "P", Interval: 61 * time.Second}}, "60 s"},
		{"sub-millisecond", []Task{{Name: "main", Program: "P", Interval: 1500 * time.Microsecond}}, "whole number"},
		{"two tasks", []Task{{Name: "main", Program: "P", Interval: time.Second}, {Name: "slow", Program: "P", Interval: 2 * time.Second}}, "2 tasks"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, diags, err := Write(Input{Programs: prog, Tasks: c.tasks})
			if err != nil || len(diags) == 0 || !strings.Contains(fmt.Sprint(diags), c.want) {
				t.Fatalf("want %q, got %v %v", c.want, diags, err)
			}
		})
	}
}

func TestBlockNamesDoNotCollide(t *testing.T) {
	_, diags, err := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM Tags\nVAR x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n"}},
		Globals:  []Global{{Name: "Level", Type: "REAL"}},
	})
	if err != nil || len(diags) != 1 || diags[0].Rule != "siemens/name" {
		t.Fatalf("%v %v", diags, err)
	}
}

// TestRefusals: what the first subset does not write is a diagnostic on
// the line that wrote it, and no files at all.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, decls, body, rule string
		line                    int
	}{
		{"division by a variable", "a, b : INT;", "a := a / b;", "siemens/division", 3},
		{"division by zero constant", "a : INT;", "a := a / 0;", "siemens/division", 3},
		{"mod by a variable", "a, b : INT;", "a := a MOD b;", "siemens/division", 3},
		{"string", "s : STRING;", "", "siemens/type", 2},
		{"array", "a : ARRAY[1..3] OF INT;", "", "siemens/type", 2},
		{"sr", "f : SR;", "", "siemens/type", 2},
		{"overloaded conversion", "a : INT; r : REAL;", "r := TO_REAL(a);", "siemens/conversion", 3},
		{"unknown function (nautilus refuses first)", "a : INT;", "a := FOO(a);", "siemens/nautilus", 3},
		{"unknown name (nautilus refuses first)", "a : INT;", "a := Nope;", "siemens/nautilus", 3},
		{"truncated real", "r : REAL; l : LINT;", "l := TRUNC(r);", "siemens/function", 3},
		{"ltime", "t : LTIME;", "", "siemens/type", 2},
		{"time arithmetic", "a, b : TIME;", "a := a + b;", "siemens/time", 3},
		{"bitwise and", "a, b : WORD;", "a := a AND b;", "siemens/operator", 3},
		{"int arithmetic into an FB input", "t : TON; n : INT; b : BOOL;", "t(IN := b, PT := INT_TO_TIME(n + 1));", "siemens/int-width", 3},
		{"int arithmetic as a CASE selector", "n : INT;", "CASE n + 1 OF 1: n := 0; END_CASE;", "siemens/int-width", 3},
		{"int arithmetic stored into a REAL", "n : INT; r : REAL;", "r := n + 1;", "siemens/int-width", 3},
		{"counter CV bound to an INT", "c : CTU; n : INT; b : BOOL;", "c(CU := b, PV := 5, CV => n);", "siemens/int-width", 3},
		{"sqrt of an int", "n : INT; r : REAL;", "r := SQRT(n);", "siemens/function", 3},
		{"zero divisor written 0.0e0", "r : REAL;", "r := r / 0.0e0;", "siemens/division", 3},
		{"zero divisor written 16#0", "a : INT;", "a := a / 16#0;", "siemens/division", 3},
		{"instance as an input", "", "", "siemens/var", 0},
		{"positional instance args (nautilus refuses first)", "t : TON; b : BOOL;", "t(b, T#1s);", "siemens/nautilus", 3},
		{"instance in temp", "", "", "siemens/nautilus", 0},
		{"enum literal (nautilus refuses first)", "a : INT;", "a := Mode#Run;", "siemens/nautilus", 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "PROGRAM P\nVAR " + c.decls + " END_VAR\n" + c.body + "\nEND_PROGRAM\n"
			if c.name == "instance in temp" {
				src = "PROGRAM P\nVAR_TEMP t : TON; END_VAR\nEND_PROGRAM\n"
				c.line = 2
			}
			if c.name == "instance as an input" {
				src = "PROGRAM P\nVAR_INPUT t : TON; END_VAR\nEND_PROGRAM\n"
				c.line = 2
			}
			out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Files) != 0 {
				t.Fatal("a refused program must write no files")
			}
			for _, d := range diags {
				if d.Rule == c.rule && d.Line == c.line {
					return
				}
			}
			t.Fatalf("want %s on line %d, got %v", c.rule, c.line, diags)
		})
	}
}

func TestDivisionByAConstantIsWritten(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a : INT; r : REAL; END_VAR\na := a / 2;\nr := r / 2.5;\na := a MOD 3;\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{"((INT_TO_LINT(#a) / 2) + LINT#32768)", "#r := #r / 2.5;", "((INT_TO_LINT(#a) MOD 3) + LINT#32768)"} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

func TestPrecedenceKeepsMeaning(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a, b, c : REAL; x, y, z : BOOL; END_VAR\n" +
		"a := (a - b) - c;\na := a - (b - c);\na := (a + b) * c;\nx := (x OR y) AND z;\nx := NOT (x AND y);\na := EXPT(-a, 2.0);\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		"#a := #a - #b - #c;",
		"#a := #a - (#b - #c);",
		"#a := (#a + #b) * #c;",
		"#x := (#x OR #y) AND #z;",
		"#x := NOT (#x AND #y);",
		// SCL binds ** tighter than unary minus: (-a)², not -(a²).
		"#a := ((-#a) ** 2.0);",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

// TestIntegerArithmeticIsWidenedAndWrapped: nautilus computes integer
// arithmetic at 64 bits and wraps at the store; the SCL does the same.
func TestIntegerArithmeticIsWidenedAndWrapped(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR a, b : INT; u : UINT; l : LINT; c : CTU; ok : BOOL; END_VAR\n" +
		"a := a + b;\nu := u - 1;\nl := l * 2;\nok := a + b > 30000;\na := c.CV;\na := 16#FF;\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		"#a := LINT_TO_INT((((INT_TO_LINT(#a) + INT_TO_LINT(#b)) + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768);",
		"#u := LINT_TO_UINT(((UINT_TO_LINT(#u) - 1) MOD LINT#65536 + LINT#65536) MOD LINT#65536);",
		"#l := #l * 2;",
		"#ok := INT_TO_LINT(#a) + INT_TO_LINT(#b) > 30000;",
		"#a := LINT_TO_INT((((#c.CV) + LINT#32768) MOD LINT#65536 + LINT#65536) MOD LINT#65536 - LINT#32768);",
		"#a := 16#FF;",
		"c : CTU_LINT;",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

func TestTempsAreSetEveryScan(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR x : INT; END_VAR\nVAR_TEMP k : INT := 5; f : BOOL; END_VAR\nx := k;\nEND_PROGRAM\n"}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{"      k : INT;\n", "\t#k := 5;\n\t#f := FALSE;\n\t#x := #k;"} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "k : INT := 5") {
		t.Error("a temp has no start value in TIA")
	}
	if _, ok := out.Symbol("P.k"); ok {
		t.Error("a temp is not readable over the Web API")
	}
	if _, ok := out.Symbol("P.x"); !ok {
		t.Error("a static is")
	}
}

func TestStringLiteralsAreEscaped(t *testing.T) {
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR c : CHAR; END_VAR\nc := '$'';\nEND_PROGRAM\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) > 0 {
		t.Skipf("CHAR is not runnable by nautilus here: %v", diags)
	}
	if fb := string(out.Files[0].Data); !strings.Contains(fb, "'$''") {
		t.Errorf("quote not escaped:\n%s", fb)
	}
}

func TestSymbolAnswersOnlyForWhatExists(t *testing.T) {
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR x : REAL; t : TON; END_VAR\nx := Level;\nEND_PROGRAM\n"}},
		Globals:  []Global{{Name: "Level", Type: "REAL"}},
		Tasks:    []Task{{Name: "main", Program: "P", Interval: time.Second}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, p := range []string{"Level", "P.x", "p.t.Q"} {
		if _, ok := out.Symbol(p); !ok {
			t.Errorf("Symbol(%q) should resolve", p)
		}
	}
	for _, p := range []string{"Nope", "P.nope", "Q.x"} {
		if s, ok := out.Symbol(p); ok {
			t.Errorf("Symbol(%q) = %q, want no answer", p, s)
		}
	}
}

// TestLadderDiagnosticPointsAtTheLadderLine: a refusal inside a rung
// names the ladder's own line, which differs from the line of the ST it
// was lowered to.
func TestLadderDiagnosticPointsAtTheLadderLine(t *testing.T) {
	src := "PROGRAM L\nVAR\n    a : REAL;\n    b : REAL;\n    Run : BOOL;\n    Out : BOOL;\nEND_VAR\nLD\n  RUNG first\n    Run ( Out )\n\n  // a note above the rung\n  RUNG ratio\n    GT(DIV(a, b), 1.0) ( Run )\nEND_LD\nEND_PROGRAM\n"
	_, diags, err := Write(Input{Programs: []Program{{Path: "l.ld", Source: src}}})
	if err != nil {
		t.Fatal(err)
	}
	stSrc, _, err := toST(Program{Path: "l.ld", Source: src})
	if err != nil {
		t.Fatal(err)
	}
	stLine := 0
	for i, l := range strings.Split(stSrc, "\n") {
		if strings.Contains(l, "/ b") || strings.Contains(l, "DIV(") {
			stLine = i + 1
		}
	}
	for _, d := range diags {
		if d.Rule != "siemens/division" {
			continue
		}
		if d.Path != "l.ld" || (d.Line != 13 && d.Line != 14) {
			t.Fatalf("want the refusal on rung ratio (l.ld:13-14), got %v", d)
		}
		if stLine == d.Line {
			t.Fatalf("the ST division is also on line %d, so this test cannot tell a mapped line from an unmapped one", stLine)
		}
		return
	}
	t.Fatalf("want siemens/division, got %v", diags)
}

func TestGlobalTypeRefusedInTheTagsDB(t *testing.T) {
	_, diags, err := Write(Input{Globals: []Global{{Name: "Name", Type: "STRING"}}})
	if err != nil || len(diags) != 1 || diags[0].Rule != "siemens/type" {
		t.Fatalf("%v %v", err, diags)
	}
}

// TestLadderIsWrittenThroughST: a ladder file goes through nautilus's own
// LD → FBD → ST lowering, and a refusal points at the ladder's line.
func TestLadderIsWrittenThroughST(t *testing.T) {
	src, err := os.ReadFile("../../logix/writer/testdata/demoline.ld")
	if err != nil {
		t.Fatal(err)
	}
	out, diags, err := Write(Input{Programs: []Program{{Path: "demoline.ld", Source: string(src)}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		`FUNCTION_BLOCK "MainProgram"`,
		"#RunCmd := (#StartPB OR #RunCmd) AND NOT #StopPB;",
		"#HiLevelAlm := #LevelPct >= #HiLevelSP;",
		"HiLevelSP : REAL := 85.0;",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
}

// TestStoresThatChangeWidthOrSignednessWrap: nautilus wraps an integer
// store into a type that cannot hold it (#800: -1 into a UINT is 65535);
// an exact widening is written plain.
func TestStoresThatChangeWidthOrSignednessWrap(t *testing.T) {
	src := "PROGRAM P\nVAR a : INT; u : UINT; d : DINT; s : SINT; END_VAR\n" +
		"u := INT_TO_UINT(a);\nd := INT_TO_DINT(a);\ns := DINT_TO_SINT(d);\nd := a;\nEND_PROGRAM\n"
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	fb := string(out.Files[0].Data)
	for _, want := range []string{
		"#u := LINT_TO_UINT(((INT_TO_LINT(#a)) MOD LINT#65536 + LINT#65536) MOD LINT#65536);",
		"#d := INT_TO_DINT(#a);",
		"#s := LINT_TO_SINT((((DINT_TO_LINT(#d)) + LINT#128) MOD LINT#256 + LINT#256) MOD LINT#256 - LINT#128);",
		"#d := #a;",
	} {
		if !strings.Contains(fb, want) {
			t.Errorf("missing %q in\n%s", want, fb)
		}
	}
	if strings.Contains(fb, "INT_TO_UINT(#a)") {
		t.Error("an out-of-range INT_TO_UINT is the CPU's guess; the writer must wrap")
	}
}

// TestImplicitNarrowingIsRefusedByNautilus pins #800 as the writer meets
// it: nautilus's own lowering refuses an implicit store that changes width
// or signedness, and an out-of-range literal, before the writer writes
// anything. (A value nautilus types as its 64-bit int, a counter's CV, is
// accepted and wrapped by the writer: TestIntegerArithmeticIsWidenedAndWrapped.)
func TestImplicitNarrowingIsRefusedByNautilus(t *testing.T) {
	for _, body := range []string{"u := a;", "s := d;", "u := 70000;"} {
		src := "PROGRAM P\nVAR a : INT; u : UINT; d : DINT; s : SINT; END_VAR\n" + body + "\nEND_PROGRAM\n"
		out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: src}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Files) != 0 || len(diags) != 1 || diags[0].Rule != "siemens/nautilus" || diags[0].Line != 3 {
			t.Errorf("%s: want one siemens/nautilus refusal on line 3, got %v", body, diags)
		}
	}
}

func TestTimeLiterals(t *testing.T) {
	for raw, want := range map[string]string{"5s": "T#5s", "1h30m": "T#1h30m", "250ms": "T#250ms", "-2s": "T#-2s", "24d": "T#24d"} {
		if got, why := timeLit(raw); why != "" || got != want {
			t.Errorf("timeLit(%q) = %q %q, want %q", raw, got, why, want)
		}
	}
	for _, raw := range []string{"1500us", "3ns", "1.5s", "25d", "x"} {
		if _, why := timeLit(raw); why == "" {
			t.Errorf("timeLit(%q): want a refusal", raw)
		}
	}
	// TIME#5s is a typed literal around a time literal: written once.
	out, diags, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR d : TIME; END_VAR\nd := TIME#5s;\nd := LT#1500us;\nEND_PROGRAM\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].Rule != "siemens/time" || diags[0].Line != 4 {
		t.Fatalf("want one siemens/time on line 4 (LT#1500us), got %v %v", diags, out.Files)
	}
	ok, _, err := Write(Input{Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR d : TIME; END_VAR\nd := TIME#5s;\nEND_PROGRAM\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if fb := string(ok.Files[0].Data); !strings.Contains(fb, "#d := T#5s;") || strings.Contains(fb, "TIME#T#") {
		t.Errorf("TIME#5s:\n%s", fb)
	}
}

func TestHoldsMatchesGoConversions(t *testing.T) {
	samples := []int64{-1 << 40, -2147483649, -2147483648, -32769, -32768, -129, -128, -1, 0, 1, 127, 128, 255, 256,
		32767, 32768, 65535, 65536, 2147483647, 2147483648, 4294967295, 4294967296, 1 << 40}
	for from := range intWidth {
		for to := range intWidth {
			if from == "ULINT" || to == "ULINT" {
				continue
			}
			lossless := true
			for _, v := range samples {
				if inRange(v, from) && !inRange(v, to) {
					lossless = false
				}
			}
			if holds(to, from) != lossless {
				t.Errorf("holds(%s, %s) = %v, samples say %v", to, from, holds(to, from), lossless)
			}
		}
	}
}

func TestSymbolRefusesMembersOfScalars(t *testing.T) {
	out, diags, err := Write(Input{
		Programs: []Program{{Path: "p.st", Source: "PROGRAM P\nVAR x : REAL; t : TON; END_VAR\nx := Level;\nEND_PROGRAM\n"}},
		Globals:  []Global{{Name: "Level", Type: "REAL"}},
		Tasks:    []Task{{Name: "main", Program: "P", Interval: time.Second}},
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	for _, p := range []string{"P.x.y", "Level.x", "P.t.Q.z"} {
		if s, ok := out.Symbol(p); ok {
			t.Errorf("Symbol(%q) = %q, want no answer", p, s)
		}
	}
	if s, ok := out.Symbol("P.t.ET"); !ok || s != `"P_DB".t.ET` {
		t.Errorf("Symbol(P.t.ET) = %q %v", s, ok)
	}
}

// TestBlockPrefixIsEverywhere: the prefix is on every generated block, DB
// and tag table name, in file names, in every reference inside the SCL,
// and in the roots Symbol answers from (#789). No block keeps the bare
// program name, which would collide with TIA's "Main" [OB1].
func TestBlockPrefixIsEverywhere(t *testing.T) {
	src := "PROGRAM Main\nVAR count : INT; END_VAR\ncount := count + 1;\nlevel := 1.0;\nEND_PROGRAM\n"
	out, diags, err := Write(Input{
		Programs:    []Program{{Path: "Main.st", Source: src}},
		Globals:     []Global{{Name: "level", Type: "REAL"}},
		Tasks:       []Task{{Name: "main", Program: "Main", Interval: time.Second}},
		BlockPrefix: "plant_",
	})
	if err != nil || len(diags) > 0 {
		t.Fatal(err, diags)
	}
	files := map[string]string{}
	for _, f := range out.Files {
		files[f.Path] = string(f.Data)
	}
	for path, want := range map[string]string{
		"plant_Main.scl":    `FUNCTION_BLOCK "plant_Main"`,
		"plant_Main_DB.db":  `DATA_BLOCK "plant_Main_DB"`,
		"plant_Tags.db":     `DATA_BLOCK "plant_Tags"`,
		"plant_OB_main.scl": `ORGANIZATION_BLOCK "plant_OB_main"`,
	} {
		if !strings.Contains(files[path], want) {
			t.Errorf("%s lacks %s:\n%s", path, want, files[path])
		}
	}
	for path, want := range map[string]string{
		"plant_Main.scl":    `"plant_Tags".level`,
		"plant_Main_DB.db":  "\"plant_Main\"\n",
		"plant_OB_main.scl": `"plant_Main_DB"();`,
	} {
		if !strings.Contains(files[path], want) {
			t.Errorf("%s: reference %q missing:\n%s", path, want, files[path])
		}
	}
	if len(files) != 4 {
		t.Errorf("files: %v", files)
	}
	if out.OBs[0].Name != "plant_OB_main" {
		t.Errorf("OB name %q", out.OBs[0].Name)
	}
	for path, want := range map[string]string{"level": `"plant_Tags".level`, "Main.count": `"plant_Main_DB".count`} {
		if got, ok := out.Symbol(path); !ok || got != want {
			t.Errorf("Symbol(%q) = %q, want %q", path, got, want)
		}
	}
	if out.Roots[""] != `"plant_Tags"` || out.Roots["Main"] != `"plant_Main_DB"` {
		t.Errorf("roots %v", out.Roots)
	}
}

// TestPrefixedNamesStillCollide: the collision check works on the
// prefixed names, so two programs differing only by a prefix-visible
// clash (a program named Tags next to the tags DB) still refuse.
func TestPrefixedNamesStillCollide(t *testing.T) {
	_, diags, err := Write(Input{
		Programs:    []Program{{Path: "p.st", Source: "PROGRAM Tags\nVAR x : REAL; END_VAR\nx := 1.0;\nEND_PROGRAM\n"}},
		Globals:     []Global{{Name: "Level", Type: "REAL"}},
		BlockPrefix: "naut_",
	})
	if err != nil || len(diags) != 1 || diags[0].Rule != "siemens/name" {
		t.Fatalf("%v %v", diags, err)
	}
}
