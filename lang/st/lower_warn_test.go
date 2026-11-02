package st

import (
	"fmt"
	"strings"
	"testing"
)

// lowerWarnings lowers src and returns the warnings it raised, as
// "line:col: message".
func lowerWarnings(t *testing.T, src string) []string {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	warn := func(w LowerWarning) {
		out = append(out, fmt.Sprintf("%d:%d: %s", w.Pos.Line, w.Pos.Col, w.Msg))
	}
	if _, err := LowerWithOpts(prog, LowerOpts{Warn: warn}); err != nil {
		t.Fatal(err)
	}
	return out
}

// F-16: an integer where a TIME is expected compiles, read as
// milliseconds — IEC 61131-3 has no such conversion, and an author who
// meant seconds gets a timer a thousand times short. Lowering warns where
// it happens and names the literal that says the same thing; a T# literal
// and an explicit TO_TIME are silent, and so is a REAL (only an integer
// reads as milliseconds; #498). A FUNCTION_BLOCK's declarations are
// lowered twice (signature, body) and still warn once.
func TestIntegerWhereTimeIsExpectedWarns(t *testing.T) {
	got := lowerWarnings(t, `
FUNCTION_BLOCK Blinker
VAR_INPUT Period : TIME := 750; END_VAR
END_FUNCTION_BLOCK
PROGRAM Main
VAR
    t1 : TON;
    t2 : TON;
    t3 : TON;
    t4 : TON;
    t5 : TON;
    b  : Blinker;
    n  : DINT := 5;
    d  : TIME := 90000;
END_VAR
t1(IN := TRUE, PT := 30000);
t2(IN := TRUE, PT := n);
t3(IN := TRUE, PT := T#30S);
t4(IN := TRUE, PT := TO_TIME(n));
t5(IN := TRUE, PT := 1.5);
b(Period := 250);
END_PROGRAM`)
	want := []string{
		"3:28: initial value: integer 750 read as milliseconds where a TIME is expected; write T#750MS",
		"14:18: initial value: integer 90000 read as milliseconds where a TIME is expected; write T#1M30S",
		"16:22: TON input PT: integer 30000 read as milliseconds where a TIME is expected; write T#30S",
		"17:22: TON input PT: n (a DINT) read as milliseconds where a TIME is expected; write a T# literal, or TO_TIME(n) if milliseconds are meant",
		"21:13: Blinker input Period: integer 250 read as milliseconds where a TIME is expected; write T#250MS",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
