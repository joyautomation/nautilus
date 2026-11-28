package st

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// WRITTEN(<path>), the expr: stage's built-in (#1458 slice 8c).

// Its argument is rewritten onto the instance like any sibling name, but it
// is marked: the stage reads the push state there, never the value.
func TestWrittenArgIsRewrittenAndMarked(t *testing.T) {
	sib := func(n string) bool { return n == "Cmd" || n == "Speed" }
	out, refs, err := RewriteExpr("P101", sib, "written(Cmd.SpeedRef) AND Speed > 1.0")
	if err != nil {
		t.Fatal(err)
	}
	if out != "written(P101.Cmd.SpeedRef) AND P101.Speed > 1.0" {
		t.Errorf("rewritten %q", out)
	}
	if len(refs) != 2 || !refs[0].Written || refs[0].Full("P101") != "P101.Cmd.SpeedRef" || refs[1].Written {
		t.Errorf("refs %+v", refs)
	}
	// Another call's arguments are reads.
	_, refs, _ = RewriteExpr("P101", sib, "ABS(Speed) > 1.0")
	if len(refs) != 1 || refs[0].Written {
		t.Errorf("refs %+v", refs)
	}
}

// The path WRITTEN's argument names: constant indices only.
func TestWrittenPath(t *testing.T) {
	for src, want := range map[string]string{
		"P101.Cmd.Start":  "P101.Cmd.Start",
		"P101.Lim[01]":    "P101.Lim[1]",
		"P101.A[2].B":     "P101.A[2].B",
		"Go":              "Go",
		"P101.Lim[16#10]": "P101.Lim[16]",
		"P101.Lim[16#1E]": "P101.Lim[30]",
		"P101.Lim[2#11]":  "P101.Lim[3]",
		"P101.Neg[-1]":    "P101.Neg[-1]",
	} {
		e, _ := ParseExpr(src)
		if got, ok := WrittenPath(e); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", src, got, ok, want)
		}
	}
	for _, src := range []string{"P101.Lim[i]", "P101.Lim[i].B", "a + b", "F(x)", "1", "P101.Lim[1.0]", "P101.Grid[0,1]", "P101.Lim[-i]"} {
		e, _ := ParseExpr(src)
		if got, ok := WrittenPath(e); ok {
			t.Errorf("%s: %q, want refused", src, got)
		}
	}
}

// Only the program body lowered with LowerOpts.Written has WRITTEN: a
// FUNCTION of the same source does not (the resolver is never asked), and
// without the option a program has no WRITTEN at all.
func TestWrittenOnlyInTheBodyGivenIt(t *testing.T) {
	asked := []string{}
	opts := LowerOpts{
		ImplicitGlobals: map[string]*ir.Type{"P101": nil},
		Written: func(p string) (int, error) {
			asked = append(asked, p)
			return 7, nil
		},
	}
	prog, err := Parse("PROGRAM E\nVAR x : BOOL; END_VAR\nx := WRITTEN(P101.Cmd);\nEND_PROGRAM\n")
	if err != nil {
		t.Fatal(err)
	}
	low, err := LowerWithOpts(prog, opts)
	if err != nil {
		t.Fatal(err)
	}
	a := low.Body[0].(*ir.Assign)
	c, ok := a.Value.(*ir.Call)
	if !ok || c.HostFn == nil || len(c.Args) != 1 || c.Args[0].(*ir.Lit).V.I != 7 || c.T != ir.BoolT {
		t.Fatalf("lowered %#v", a.Value)
	}
	if len(low.Globals) != 0 {
		t.Errorf("the path was read as a value: globals %v", low.Globals)
	}
	if len(asked) != 1 || asked[0] != "P101.Cmd" {
		t.Errorf("asked %v", asked)
	}

	asked = nil
	fn, _ := Parse("FUNCTION F : BOOL\nVAR_INPUT y : INT; END_VAR\nF := WRITTEN(y);\nEND_FUNCTION\nPROGRAM E\nEND_PROGRAM\n")
	if _, err := LowerWithOpts(fn, opts); err == nil || !strings.Contains(err.Error(), `unknown function "WRITTEN": WRITTEN(<path>) is an expr: member's built-in`) || len(asked) != 0 {
		t.Errorf("a FUNCTION's WRITTEN: %v (asked %v)", err, asked)
	}
	opts.Written = nil
	if _, err := LowerWithOpts(prog, opts); err == nil || !strings.Contains(err.Error(), `unknown function "WRITTEN"`) {
		t.Errorf("a program without the option: %v", err)
	}
}

// Purity: WRITTEN in the expression itself is allowed (it reads only the push
// state); inside a FUNCTION it is refused; a project FUNCTION of the name
// called from a FUNCTION is checked as any FUNCTION.
func TestWrittenPurity(t *testing.T) {
	prog, err := Parse("FUNCTION Wr : BOOL\nVAR_INPUT x : INT; END_VAR\nWr := WRITTEN(x);\nEND_FUNCTION\nPROGRAM E\nEND_PROGRAM\n")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := ParseExpr("WRITTEN(P101.Cmd) AND Wr(1)")
	ps := ExprPurity(prog, e)
	if len(ps) != 1 || ps[0].Func != "Wr" || !strings.Contains(ps[0].Rule, "calls WRITTEN(), which only an expr: itself may call") {
		t.Errorf("problems %+v", ps)
	}
	own, _ := Parse("FUNCTION Written : BOOL\nVAR_INPUT x : INT; END_VAR\nWritten := x > 0;\nEND_FUNCTION\nFUNCTION Wr : BOOL\nVAR_INPUT x : INT; END_VAR\nWr := Written(x);\nEND_FUNCTION\nPROGRAM E\nEND_PROGRAM\n")
	e, _ = ParseExpr("Wr(1)")
	if ps := ExprPurity(own, e); len(ps) != 0 {
		t.Errorf("a project FUNCTION Written called from a FUNCTION: %+v", ps)
	}
}
