package st

import (
	"fmt"
	"strings"
	"testing"
)

// TestLowerErrorPinsOffendingName covers issue #141: a lowering error about
// a name lands on that name (line, column and end column), not on the
// statement's first token, and the message text is unchanged.
func TestLowerErrorPinsOffendingName(t *testing.T) {
	const header = "PROGRAM P\nVAR\n  x : INT;\n  b : BOOL;\n  settle : TON;\nEND_VAR\n"
	cases := []struct {
		name    string
		body    string // statement(s) on line 7 onward
		line    int
		col     int
		endCol  int // 0: no end expected
		wantMsg string
	}{
		{
			name: "IF condition",
			body: "IF Heatr THEN\n  x := 1;\nEND_IF;\n",
			line: 7, col: 4, endCol: 9,
			wantMsg: `undeclared identifier "Heatr" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "compound IF condition",
			body: "IF b AND x > 0 OR Heatr THEN\n  x := 1;\nEND_IF;\n",
			line: 7, col: 19, endCol: 24,
			wantMsg: `undeclared identifier "Heatr" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "inside IF body",
			body: "IF b THEN\n  x := x + bogus;\nEND_IF;\n",
			line: 8, col: 12, endCol: 17,
			wantMsg: `undeclared identifier "bogus" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "function call argument",
			body: "x := ABS(bogus);\n",
			line: 7, col: 10, endCol: 15,
			wantMsg: `function ABS arg: undeclared identifier "bogus" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "FB call argument",
			body: "settle(IN := bogus, PT := T#1s);\n",
			line: 7, col: 14, endCol: 19,
			wantMsg: `FB TON arg "IN": undeclared identifier "bogus" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "right of assignment",
			body: "x := x + 1 + bogus;\n",
			line: 7, col: 14, endCol: 19,
			wantMsg: `undeclared identifier "bogus" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "left of assignment",
			body: "  bogus := 1;\n",
			line: 7, col: 3, endCol: 8,
			wantMsg: `undeclared identifier "bogus" (declare in VAR_* or VAR_GLOBAL block)`,
		},
		{
			name: "unknown FB member",
			body: "b := b OR settle.Qx;\n",
			line: 7, col: 18, endCol: 20,
			wantMsg: `FB TON has no field "Qx"`,
		},
		{
			name: "unknown FB input",
			body: "settle(INN := b, PT := T#1s);\n",
			line: 7, col: 8, endCol: 11,
			wantMsg: `FB TON has no input "INN"`,
		},
		{
			name: "unknown function",
			body: "x := 1 + FROB(x);\n",
			line: 7, col: 10, endCol: 14,
			wantMsg: `unknown function "FROB"`,
		},
		{
			name: "call to undeclared FB instance",
			body: "  nosuch(IN := b);\n",
			line: 7, col: 3, endCol: 9,
			wantMsg: `call to undeclared name "nosuch"`,
		},
		{
			name: "operand type mismatch in a condition",
			body: "IF b AND x THEN\n  x := 1;\nEND_IF;\n",
			line: 7, col: 4,
			wantMsg: "operator AND on BOOL and INT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prog, err := Parse(header + tc.body + "END_PROGRAM\n")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			_, err = Lower(prog)
			if err == nil {
				t.Fatal("expected a lowering error")
			}
			le, ok := AsLowerError(err)
			if !ok {
				t.Fatalf("not a LowerError: %v", err)
			}
			if le.Pos.Line != tc.line || le.Pos.Col != tc.col {
				t.Errorf("pos = %d:%d, want %d:%d (%v)", le.Pos.Line, le.Pos.Col, tc.line, tc.col, err)
			}
			if tc.endCol != 0 && (le.End.Line != tc.line || le.End.Col != tc.endCol) {
				t.Errorf("end = %d:%d, want %d:%d", le.End.Line, le.End.Col, tc.line, tc.endCol)
			}
			if !strings.HasPrefix(le.Err.Error(), tc.wantMsg) {
				t.Errorf("message = %q, want prefix %q", le.Err.Error(), tc.wantMsg)
			}
			if want := fmt.Sprintf("line %d: %s", tc.line, le.Err.Error()); err.Error() != want {
				t.Errorf("Error() = %q, want %q", err.Error(), want)
			}
		})
	}
}

// A statement-level error with no narrower span still lands on the
// statement, with no end.
func TestLowerErrorStatementFallback(t *testing.T) {
	prog, err := Parse("PROGRAM P\nVAR\n  x : INT;\n  s : STRING;\nEND_VAR\n  x := s;\nEND_PROGRAM\n")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Lower(prog)
	le, ok := AsLowerError(err)
	if !ok {
		t.Fatalf("not a LowerError: %v", err)
	}
	if le.Pos.Line != 6 || le.Pos.Col != 3 || le.End != (Pos{}) {
		t.Errorf("pos = %+v end = %+v, want 6:3 and no end", le.Pos, le.End)
	}
}
