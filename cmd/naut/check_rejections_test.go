package main

import (
	"strings"
	"testing"
)

// Every construct the language pages list under "Not supported" is refused by
// `naut check` with a diagnostic, not accepted and not a crash. One subtest
// per construct, so the claims inventory can name it
// (TestCheckRejects/REF_TO, ...). The diagnostics asserted are the ones the
// tool prints today; the docs promise "rejected", and only the SCL # prefix
// has a quoted message (tested elsewhere).

const (
	stVars = "PROGRAM p\nVAR\n  x : INT;\n  r : REAL;\n"
	stTail = "END_VAR\nx := 1;\nEND_PROGRAM\n"
	stBody = "END_PROGRAM\n"

	ldHead  = "PROGRAM p\nVAR_EXTERNAL\n  A : BOOL;\n  B : BOOL;\n  T : REAL;\nEND_VAR\nLD\n"
	ldTail  = "END_LD\nEND_PROGRAM\n"
	fbdHead = "PROGRAM p\nVAR_EXTERNAL\n  A : BOOL;\n  B : BOOL;\n  Out : BOOL;\n  Lv : ARRAY[0..3] OF REAL;\n  i : INT;\n  R : REAL;\nEND_VAR\nFBD\n"
	fbdTail = "END_FBD\nEND_PROGRAM\n"
	sfcHead = "PROGRAM p\nVAR_EXTERNAL\n  A : BOOL;\n  Y : BOOL;\nEND_VAR\nSFC\n"
	sfcTail = "END_SFC\nEND_PROGRAM\n"
)

func TestCheckRejects(t *testing.T) {
	cases := []struct {
		name, file, src, want string
		bug                   string // set when the construct is accepted today
	}{
		// Structured Text (ST-050 .. ST-056)
		{name: "REF_TO", file: "p.st", src: stVars + "  q : REF_TO INT;\n" + stTail,
			want: `p.st:5:1: var decl: line 5: expected an identifier, got "INT"`},
		{name: "VAR_ACCESS", file: "p.st", src: "VAR_ACCESS\n  a : x : INT READ_ONLY;\nEND_VAR\n" + stVars + stTail,
			want: `"VAR_ACCESS" alone is not a statement`},
		{name: "CONFIGURATION", file: "p.st", src: "CONFIGURATION c\nEND_CONFIGURATION\n",
			want: `"CONFIGURATION" alone is not a statement`},
		{name: "RESOURCE", file: "p.st", src: "RESOURCE r ON cpu\nEND_RESOURCE\n",
			want: `"RESOURCE" alone is not a statement`},
		{name: "TASK", file: "p.st", src: "TASK t(INTERVAL := T#10ms, PRIORITY := 1);\n",
			want: `"TASK" alone is not a statement`},
		{name: "subrange", file: "p.st", src: "TYPE Pct : INT (0..100);\nEND_TYPE\n" + stVars + stTail,
			want: `type decl: line 1: expected an identifier, got "("`},
		{name: "DATE", file: "p.st", src: stVars + "  d : DATE;\n" + stTail, want: `unknown scalar type "DATE"`},
		{name: "TOD", file: "p.st", src: stVars + "  d : TOD;\n" + stTail, want: `unknown scalar type "TOD"`},
		{name: "TIME_OF_DAY", file: "p.st", src: stVars + "  d : TIME_OF_DAY;\n" + stTail, want: `unknown scalar type "TIME_OF_DAY"`},
		{name: "DT", file: "p.st", src: stVars + "  d : DT;\n" + stTail, want: `unknown scalar type "DT"`},
		{name: "DATE_AND_TIME", file: "p.st", src: stVars + "  d : DATE_AND_TIME;\n" + stTail, want: `unknown scalar type "DATE_AND_TIME"`},
		{name: "DATE literal", file: "p.st", src: stVars + "END_VAR\nr := DATE#2024-01-01;\n" + stBody,
			want: "DATE# literals are not supported"},
		{name: "D literal", file: "p.st", src: stVars + "END_VAR\nx := D#20;\n" + stBody,
			want: "D# literals are not supported"},
		{name: "D literal date", file: "p.st", src: stVars + "END_VAR\nr := D#2024-01-01;\n" + stBody,
			want: "D# literals are not supported"},
		{name: "DT literal", file: "p.st", src: stVars + "END_VAR\nr := DT#2024-01-01-12:00:00;\n" + stBody,
			want: "DT# literals are not supported"},
		{name: "DATE_AND_TIME literal", file: "p.st", src: stVars + "END_VAR\nr := DATE_AND_TIME#2024-01-01-12:00:00;\n" + stBody,
			want: "DATE_AND_TIME# literals are not supported"},
		{name: "TOD literal", file: "p.st", src: stVars + "END_VAR\nx := TOD#12:00:00;\n" + stBody,
			want: "TOD# literals are not supported"},
		{name: "TIME_OF_DAY literal", file: "p.st", src: stVars + "END_VAR\nx := TIME_OF_DAY#12:00:00;\n" + stBody,
			want: "TIME_OF_DAY# literals are not supported"},
		{name: "**", file: "p.st", src: stVars + "END_VAR\nr := 2.0 ** 3.0;\n" + stBody,
			want: `unexpected token "*"`},
		{name: "STRING[80]", file: "p.st", src: stVars + "  s : STRING[80];\n" + stTail,
			want: `var decl: line 5: expected an identifier, got "["`},
		{name: "array initializer", file: "p.st", src: stVars + "  a : ARRAY[0..2] OF INT := [1, 2, 3];\n" + stTail,
			want: `var decl initial: line 5: unexpected token "["`},

		// Ladder (LD-015, LD-027 .. LD-031, NS-001, NS-002, NS-008)
		{name: "JMP", file: "p.ld", src: ldHead + "  RUNG r1\n    A JMP(skip) ( B )\n" + ldTail, want: `unknown function "JMP"`},
		{name: "JMPC", file: "p.ld", src: ldHead + "  RUNG r1\n    A JMPC(skip) ( B )\n" + ldTail, want: `unknown function "JMPC"`},
		{name: "LBL", file: "p.ld", src: ldHead + "  RUNG r1\n    A LBL(skip) ( B )\n" + ldTail, want: `unknown function "LBL"`},
		{name: "RET", file: "p.ld", src: ldHead + "  RUNG r1\n    A RET() ( B )\n" + ldTail, want: `unknown function "RET"`},
		{name: "MCR", file: "p.ld", src: ldHead + "  RUNG r1\n    A MCR() ( B )\n" + ldTail, want: `unknown function "MCR"`},
		{name: "negated coil", file: "p.ld", src: ldHead + "  RUNG r1\n    A ( /B )\n" + ldTail,
			want: `ld: line 8: expected a name, got "/"`},
		{name: "vendor instruction", file: "p.ld", src: ldHead + "  RUNG r1\n    A XIC(B) ( B )\n" + ldTail, want: `unknown function "XIC"`},
		{name: "numeric function contact", file: "p.ld", src: ldHead + "  RUNG r1\n    ADD(T, 0.0) ( A )\n" + ldTail,
			want: "cannot assign REAL to BOOL"},

		// Function Block Diagram (FBD-026 .. FBD-029)
		{name: "FBD FUNCTION", file: "p.fbd", src: "FUNCTION f : REAL\nVAR_INPUT\n  x : REAL;\nEND_VAR\nFBD\n  f := x\nEND_FBD\nEND_FUNCTION\n",
			want: "a FUNCTION can't have an FBD body"},
		{name: "FBD IF", file: "p.fbd", src: fbdHead + "  IF A THEN\n    Out := B\n  END_IF\n" + fbdTail,
			want: `expected a wire, coil, or block, got "IF"`},
		{name: "FBD CASE", file: "p.fbd", src: fbdHead + "  CASE i OF\n  1: Out := B\n  END_CASE\n" + fbdTail,
			want: `expected a wire, coil, or block, got "CASE"`},
		{name: "FBD FOR", file: "p.fbd", src: fbdHead + "  FOR i := 0 TO 3 DO\n    Out := B\n  END_FOR\n" + fbdTail,
			want: `expected a wire, coil, or block, got "FOR"`},
		{name: "FBD WHILE", file: "p.fbd", src: fbdHead + "  WHILE A DO\n    Out := B\n  END_WHILE\n" + fbdTail,
			want: `expected a wire, coil, or block, got "WHILE"`},
		{name: "FBD index arithmetic", file: "p.fbd", src: fbdHead + "  R := Lv[i + 1]\n" + fbdTail,
			want: "an FBD index is a number or a tag, not arithmetic (+): compute it on a named wire first"},
		{name: "FBD duplicate wire", file: "p.fbd", src: fbdHead + "  w = AND(A, B)\n  w = OR(A, B)\n  Out := w\n" + fbdTail,
			want: `wire "w" defined twice`},
		{name: "FBD self-feeding wire", file: "p.fbd", src: fbdHead + "  w = AND(w, B)\n  Out := w\n" + fbdTail,
			want: `combinational loop through wire "w"`},

		// Sequential Function Chart (SFC-041, SFC-043, NS-006)
		{name: "SFC macro step", file: "p.sfc",
			src:  sfcHead + "  INITIAL_STEP S0:\n  END_STEP\n  MACRO_STEP M1:\n  END_MACRO_STEP\n" + sfcTail,
			want: `expected STEP, INITIAL_STEP, TRANSITION, or ACTION, got "MACRO_STEP"`},
		{name: "SFC IL action", file: "p.sfc",
			src:  sfcHead + "  INITIAL_STEP S0:\n    N Act;\n  END_STEP\n  ACTION Act:\n    LD A\n    ST Y\n  END_ACTION\n" + sfcTail,
			want: `"LD" alone is not a statement`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, code := checkIn(t, map[string]string{tc.file: tc.src})
			if tc.bug != "" {
				if code != 0 {
					t.Fatalf("now rejected (exit %d); drop the bug marker:\n%s", code, out)
				}
				t.Skip("bug: " + tc.bug)
			}
			if code != 1 {
				t.Fatalf("exit = %d, want 1 (rejected)\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("check output missing %q:\n%s", tc.want, out)
			}
		})
	}
}
