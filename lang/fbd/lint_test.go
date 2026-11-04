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
		return "PROGRAM P\nVAR a : BOOL; b : BOOL; c : BOOL; d : BOOL; x : BOOL; y : BOOL; Level : REAL; HighSP : REAL; t1 : TON; END_VAR\nFBD\n" +
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
			"  x := AND(a, b)\n  y := OR(c, d)\n",
			"  NETWORK 'x: both'\n  x := AND(a, b)\n  NETWORK 'y: either'\n  y := OR(c, d)\n",
			"they hold 2 separate functions"},
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

// One function needs no NETWORK title: the POU's name is its title. Wires
// and an FB instance join statements into one function; reading the same
// variable does not.
func TestLintFBDOneFunctionIsQuiet(t *testing.T) {
	src := `PROGRAM P
VAR a : BOOL; Level : REAL; HighSP : REAL; Alm : BOOL; Out : REAL; END_VAR
FBD
  hi = GE(Level, HighSP)
  tmr : TON(IN := hi, PT := T#10S)
  Alm := AND(OR(tmr.Q, Alm), NOT a)
  Out := SEL(Alm, 0.0, Level)
END_FBD
END_PROGRAM
`
	fs := Lint(src, lint.Defaults())
	// Out reads Alm (a variable), so it is a second function.
	if len(fs) != 1 || fs[0].Rule != "FBD002" || !strings.Contains(fs[0].Message, "2 separate functions") {
		t.Fatalf("want one FBD002 for two functions, got %v", fs)
	}
	one := strings.Replace(src, "  Out := SEL(Alm, 0.0, Level)\n", "", 1)
	if fs := Lint(one, lint.Defaults()); len(fs) != 0 {
		t.Errorf("one function, no title: want nothing, got %v", fs)
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
