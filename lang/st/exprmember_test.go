package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// An expr: is one expression: anything after it, a second statement
// smuggled in with a `;` included, is refused (it would assign).
func TestParseExprIsOneExpression(t *testing.T) {
	for _, src := range []string{"Running AND NOT Fault", "ABS(Speed - 2.0) > 1.0", "Levels[1] > 3.0"} {
		if _, err := ParseExpr(src); err != nil {
			t.Errorf("%q: %v", src, err)
		}
	}
	for src, want := range map[string]string{
		"TRUE; P101.Start := TRUE": "an expr: is one expression, with no ; and no statements",
		"Running Fault":            "after the expression",
		"":                         "empty",
		"'open":                    "col 1",
	} {
		if _, err := ParseExpr(src); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", src, err, want)
		}
	}
}

// A bare sibling name is rewritten onto the instance; a tag, a member
// name after a dot, a function name and a literal are left as written.
func TestRewriteExprPutsSiblingsOnTheInstance(t *testing.T) {
	sib := func(n string) bool { return n == "Running" || n == "Fault" || n == "Cmd" || n == "Levels" }
	got, refs, err := RewriteExpr("P101", sib, "Running AND NOT Fault AND Cmd.Start AND ABS(Level) > Levels[i].X AND P102.Running")
	if err != nil {
		t.Fatal(err)
	}
	want := "P101.Running AND NOT P101.Fault AND P101.Cmd.Start AND ABS(Level) > P101.Levels[i].X AND P102.Running"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	var full []string
	for _, r := range refs {
		full = append(full, r.Full("P101"))
	}
	if strings.Join(full, ",") != "P101.Running,P101.Fault,P101.Cmd.Start,Level,P101.Levels,i,P102.Running" {
		t.Errorf("refs %v", full)
	}
}

func purityOf(t *testing.T, lib, expr string) []ExprProblem {
	t.Helper()
	prog, err := Parse(lib + "PROGRAM P\nEND_PROGRAM\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	e, err := ParseExpr(expr)
	if err != nil {
		t.Fatal(err)
	}
	return ExprPurity(prog, e)
}

// Each purity rule of §6, at the depth it can hide: in the expression, in
// a FUNCTION it calls, and in one that function calls.
func TestExprPurityRules(t *testing.T) {
	ok := "FUNCTION Sq : REAL\nVAR_INPUT x : REAL; END_VAR\nVAR t : REAL; END_VAR\nt := x * x;\nIF t > 1.0 THEN Sq := t; ELSE Sq := 0.0; END_IF;\nEND_FUNCTION\n"
	if ps := purityOf(t, ok, "Sq(Flow) + ABS(Level) > MAX(1.0, 2.0)"); len(ps) != 0 {
		t.Errorf("a pure function and standard ones refused: %+v", ps)
	}
	inner := func(body string) string {
		return "FUNCTION Inner : REAL\nVAR_INPUT x : REAL; END_VAR\n" + body + "Inner := x;\nEND_FUNCTION\n" +
			"FUNCTION Outer : REAL\nVAR_INPUT x : REAL; END_VAR\nOuter := Inner(x);\nEND_FUNCTION\n"
	}
	for _, c := range []struct {
		name, lib, expr, fn, rule string
		via                       string
	}{
		{"VAR_EXTERNAL", inner("VAR_EXTERNAL G : REAL; END_VAR\n"), "Outer(1.0)", "Inner", "declares VAR_EXTERNAL", "Outer>Inner"},
		{"VAR_GLOBAL", inner("VAR_GLOBAL G : REAL; END_VAR\n"), "Outer(1.0)", "Inner", "declares VAR_GLOBAL", "Outer>Inner"},
		{"VAR_IN_OUT", "FUNCTION F : REAL\nVAR_IN_OUT x : REAL; END_VAR\nF := x;\nEND_FUNCTION\n", "F(Level)", "F", "VAR_IN_OUT", "F"},
		{"FB instance", inner("VAR tm : TON; END_VAR\n"), "Outer(1.0)", "Inner", "a function block instance", "Outer>Inner"},
		{"FB array", inner("VAR tm : ARRAY[0..1] OF TON; END_VAR\n"), "Outer(1.0)", "Inner", "a function block instance", "Outer>Inner"},
		{"system fn in expr", "", "FIRST_SCAN()", "", "the system function FIRST_SCAN()", ""},
		{"system fn deep", inner("VAR b : BOOL; END_VAR\nb := TAKEOVER_SCAN();\n"), "Outer(1.0)", "Inner", "TAKEOVER_SCAN()", "Outer>Inner"},
		{"=> in expr", "FUNCTION F : REAL\nVAR_INPUT x : REAL; END_VAR\nVAR_OUTPUT q : REAL; END_VAR\nF := x;\nEND_FUNCTION\n", "F(x := 1.0, q => Spare)", "", "uses => in a call to F", ""},
		{"WHILE", inner("WHILE x > 0.0 DO x := x - 1.0; END_WHILE;\n"), "Outer(1.0)", "Inner", "WHILE loop", "Outer>Inner"},
		{"REPEAT", inner("REPEAT x := x - 1.0; UNTIL x < 0.0 END_REPEAT;\n"), "Outer(1.0)", "Inner", "REPEAT loop", "Outer>Inner"},
		{"FOR", inner("VAR i : INT; END_VAR\nFOR i := 1 TO 3 DO x := x + 1.0; END_FOR;\n"), "Outer(1.0)", "Inner", "FOR loop", "Outer>Inner"},
		{"loop in IF", inner("IF x > 0.0 THEN WHILE TRUE DO END_WHILE; END_IF;\n"), "Outer(1.0)", "Inner", "WHILE loop", "Outer>Inner"},
		{"recursion", "FUNCTION R : REAL\nVAR_INPUT x : REAL; END_VAR\nR := R(x);\nEND_FUNCTION\n", "R(1.0)", "R", "calls R recursively", "R"},
		{"mutual recursion", "FUNCTION A : REAL\nVAR_INPUT x : REAL; END_VAR\nA := B(x);\nEND_FUNCTION\nFUNCTION B : REAL\nVAR_INPUT x : REAL; END_VAR\nB := A(x);\nEND_FUNCTION\n", "A(1.0)", "B", "calls A recursively", "A>B"},
		{"user FB", "FUNCTION_BLOCK Blk\nEND_FUNCTION_BLOCK\n", "Blk()", "", "the function block Blk", ""},
		{"call statement", "FUNCTION_BLOCK Blk\nEND_FUNCTION_BLOCK\n" + inner("Blk();\n"), "Outer(1.0)", "Inner", "the function block Blk", "Outer>Inner"},
	} {
		ps := purityOf(t, c.lib, c.expr)
		if len(ps) == 0 {
			t.Errorf("%s: not refused", c.name)
			continue
		}
		p := ps[0]
		if p.Func != c.fn || !strings.Contains(p.Rule, c.rule) || strings.Join(p.Via, ">") != c.via {
			t.Errorf("%s: %+v, want func %q via %q rule %q", c.name, p, c.fn, c.via, c.rule)
		}
		if c.fn != "" && p.Pos.Line == 0 {
			t.Errorf("%s: no line", c.name)
		}
	}
}

// A function reached twice is checked once, and each chain that reaches it
// is named from its own start.
func TestExprPurityReRootsAMemoisedFunction(t *testing.T) {
	lib := "FUNCTION Bad : REAL\nVAR_EXTERNAL G : REAL; END_VAR\nBad := G;\nEND_FUNCTION\n" +
		"FUNCTION A : REAL\nA := Bad();\nEND_FUNCTION\nFUNCTION B : REAL\nB := Bad();\nEND_FUNCTION\n"
	ps := purityOf(t, lib, "A() + B()")
	if len(ps) != 2 || strings.Join(ps[0].Via, ">") != "A>Bad" || strings.Join(ps[1].Via, ">") != "B>Bad" {
		t.Errorf("%+v", ps)
	}
}

// The units compile as one program after the prelude: a pure expression
// lowers, a narrowing one is refused on its own unit's line, with both
// types; a sibling rewritten onto an instance tag resolves as the tag.
func TestExprProgramCompilesAndAttributesByLine(t *testing.T) {
	lib := "TYPE Pump : STRUCT Running : BOOL; Speed : REAL; END_STRUCT; END_TYPE\n" +
		"FUNCTION Sq : REAL\nVAR_INPUT x : REAL; END_VAR\nSq := x * x;\nEND_FUNCTION\n"
	units := []ExprUnit{{Text: "P101.Running AND Level > 2.0", Result: "BOOL"}, {Text: "Sq(P101.Speed)", Result: "INT"}}
	src := lib + ExprProgram(units)
	prog, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	types, err := Types(prog)
	if err != nil {
		t.Fatal(err)
	}
	_, err = LowerWithOpts(prog, LowerOpts{ImplicitGlobals: map[string]*ir.Type{"P101": types["Pump"], "Level": ir.RealT}})
	le, ok := AsLowerError(err)
	if !ok {
		t.Fatalf("want a lower error, got %v", err)
	}
	libLines := strings.Count(lib, "\n")
	if u := ExprUnitOfLine(len(units), le.Pos.Line-libLines); u != 1 || !strings.Contains(le.Err.Error(), "cannot assign REAL to INT") {
		t.Errorf("unit %d, err %v", u, le.Err)
	}
	units[1].Result = "REAL"
	prog, _ = Parse(lib + ExprProgram(units))
	if _, err := LowerWithOpts(prog, LowerOpts{ImplicitGlobals: map[string]*ir.Type{"P101": types["Pump"], "Level": ir.RealT}}); err != nil {
		t.Errorf("clean units: %v", err)
	}
}
