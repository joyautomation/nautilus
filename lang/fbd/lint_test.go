package fbd

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/lint"
)

// Each FBD structure rule fires on a minimal bad netlist and is silent on
// the rewrite its message asks for (nautilus#561).
func TestLintFBDRules(t *testing.T) {
	prog := func(body string) string {
		return "PROGRAM P\nVAR a : BOOL; b : BOOL; c : BOOL; d : BOOL; x : BOOL; y : BOOL; z : BOOL; w : BOOL; Level : REAL; HighSP : REAL; t1 : TON; END_VAR\nFBD\n" +
			body + "END_FBD\nEND_PROGRAM\n"
	}
	for _, tc := range []struct {
		rule      string
		bad, good string
		want      string
	}{
		{"FBD001",
			"  x := AND(OR(a, AND(b, c)), d)\n",
			"  bc = AND(b, c)\n  x := AND(OR(a, bc), d)\n",
			"this statement nests blocks 3 deep (the limit is 2)"},
		{"FBD001", // an FB call is a block level of its own
			"  t1(IN := AND(OR(a, b), c), PT := T#1S)\n  x := t1.Q\n",
			"  run = AND(OR(a, b), c)\n  t1(IN := run, PT := T#1S)\n  x := t1.Q\n",
			"nests blocks 3 deep"},
		{"FBD002",
			"  x := AND(a, b)\n  y := OR(c, d)\n  z := GE(Level, HighSP)\n  w := XOR(a, c)\n",
			"  NETWORK 'x: both'\n  x := AND(a, b)\n  NETWORK 'y: either'\n  y := OR(c, d)\n" +
				"  NETWORK 'z: high'\n  z := GE(Level, HighSP)\n  NETWORK 'w: one of'\n  w := XOR(a, c)\n",
			"they hold 4 separate functions"},
		{"FBD002", // an untitled NETWORK line is no better
			"  NETWORK 'x: both'\n  x := AND(a, b)\n  NETWORK\n  y := OR(c, d)\n  hi = GE(Level, HighSP)\n  t1(IN := hi, PT := T#1S)\n  b := t1.Q\n",
			"  NETWORK 'x: both'\n  x := AND(a, b)\n  NETWORK 'y: either'\n  y := OR(c, d)\n  NETWORK 'hi'\n  hi = GE(Level, HighSP)\n  t1(IN := hi, PT := T#1S)\n  b := t1.Q\n",
			"this NETWORK has no title"},
		{"FBD003",
			"  x := GT(Level, 90.0)\n",
			"  x := GT(Level, HighSP)\n",
			"GT(Level, 90.0) compares against the literal 90.0"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			bad := Lint(prog(tc.bad), lint.Defaults())
			if !has(bad, tc.rule, tc.want) {
				t.Errorf("bad sample: want %s saying %q, got %v", tc.rule, tc.want, bad)
			}
			for _, f := range bad {
				if f.Rule != tc.rule {
					t.Errorf("bad sample: an unrelated finding %v", f)
				}
			}
			if good := Lint(prog(tc.good), lint.Defaults()); len(good) != 0 {
				t.Errorf("the rewrite still warns: %v", good)
			}
		})
	}
}

// One function needs no NETWORK title: the POU's name is its title. Wires,
// an FB instance, and a variable one statement assigns and another reads
// join statements into one function; reading the same input does not.
func TestLintFBDOneFunctionIsQuiet(t *testing.T) {
	src := `PROGRAM P
VAR a : BOOL; Level : REAL; HighSP : REAL; Alm : BOOL; Out : REAL; Pump : BOOL; END_VAR
FBD
  hi = GE(Level, HighSP)
  tmr : TON(IN := hi, PT := T#10S)
  Alm := AND(OR(tmr.Q, Alm), NOT a)
  Out := SEL(Alm, 0.0, Level)
END_FBD
END_PROGRAM
`
	// Out reads Alm, which the network assigns: one function.
	if fs := Lint(src, lint.Defaults()); len(fs) != 0 {
		t.Errorf("one function, no title: want nothing, got %v", fs)
	}
	// Pump only shares inputs (Level, HighSP) with the rest: a second function.
	two := strings.Replace(src, "END_FBD", "  Pump := LT(Level, HighSP)\nEND_FBD", 1)
	fs := Lint(two, lint.Defaults())
	if len(fs) != 1 || fs[0].Rule != "FBD002" || !strings.Contains(fs[0].Message, "2 separate functions") {
		t.Fatalf("want one FBD002 for two functions, got %v", fs)
	}
}

// Short bodies and bodies chained through a VAR are not reported (the
// reviewer's adversarial samples for #574), nor is a three-statement body
// of three functions: FBD002 judges untitled networks of four statements
// or more.
func TestLintFBDSmallOrJoinedBodiesAreQuiet(t *testing.T) {
	for name, body := range map[string]string{
		"two lines":                      "  Out1 := MUL(Level, 2.0)\n  Out2 := ADD(Level, SP)\n",
		"joined through a VAR":           "  // scale, then alarm on the scaled value\n  Scaled := MUL(Level, 2.0)\n  Valve := GE(Scaled, SP)\n",
		"three functions in three lines": "  Out1 := MUL(Level, 2.0)\n  Out2 := ADD(Level, SP)\n  Valve := GE(Level, SP)\n",
		"a chain of four through VARs": "  Scaled := MUL(Level, 2.0)\n  Out1 := ADD(Scaled, SP)\n" +
			"  Out2 := LIMIT(0.0, Out1, 100.0)\n  Valve := GE(Out2, SP)\n",
		"an output binding joins": "  t1(IN := Valve, PT := T#2S, Q => Done)\n  Out1 := SEL(Done, 0.0, Level)\n" +
			"  Out2 := MUL(Out1, 2.0)\n  Scaled := ADD(Out2, SP)\n",
	} {
		src := "PROGRAM P\nVAR Level : REAL; SP : REAL; Scaled : REAL; Out1 : REAL; Out2 : REAL; Valve : BOOL; Done : BOOL; t1 : TON; END_VAR\nFBD\n" +
			body + "END_FBD\nEND_PROGRAM\n"
		if fs := Lint(src, lint.Defaults()); len(fs) != 0 {
			t.Errorf("%s: want nothing, got %v", name, fs)
		}
		// It parses (Lint is silent on a parse error): at max-nesting 0
		// every block warns.
		strict := lint.Defaults()
		strict.MaxNesting = 0
		if !has(Lint(src, strict), "FBD001", "nests blocks") {
			t.Errorf("%s: the sample does not parse", name)
		}
	}
}

// FBD003 exempts the count compares LD006 does: integer 0, 1 and -1.
func TestLintFBDSmallCounts(t *testing.T) {
	for call, fire := range map[string]bool{
		"GE(Count, 1)": false, "GT(Count, 0)": false, "LT(Count, 1)": false, "LE(Count, -1)": false,
		"EQ(Count, 3)": false, "NE(Count, 3)": false, "GE(Count, 2)": true, "GT(Level, 1.0)": true,
	} {
		src := "PROGRAM P\nVAR Count : INT; Level : REAL; x : BOOL; END_VAR\nFBD\n  x := " + call + "\nEND_FBD\nEND_PROGRAM\n"
		if got := has(Lint(src, lint.Defaults()), "FBD003", "compares against the literal"); got != fire {
			t.Errorf("%s: FBD003 %v, want %v", call, got, fire)
		}
	}
}

func has(fs []lint.Finding, rule, phrase string) bool {
	for _, f := range fs {
		if f.Rule == rule && strings.Contains(f.Message, phrase) {
			return true
		}
	}
	return false
}
