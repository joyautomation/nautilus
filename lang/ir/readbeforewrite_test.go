package ir_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

func lowerOne(t *testing.T, src string) *ir.Program {
	t.Helper()
	prog, err := st.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := st.Lower(prog)
	if err != nil {
		t.Fatal(err)
	}
	return ip
}

// ReadBeforeWrite is a definite-assignment walk: a global is reported when
// some path through one scan reads it before that scan has written it. A
// read that comes first only when a loop of unknown count runs zero times
// is reported as possible ("?" below), not sure.
func TestReadBeforeWrite(t *testing.T) {
	const ext = "VAR_EXTERNAL A : BOOL; B : BOOL; X : BOOL; Y : BOOL; N : INT; END_VAR\n" +
		"VAR c : BOOL; t1 : TON; i : INT; m : Mode; END_VAR\n"
	for _, tc := range []struct {
		name, body string
		want       string // reported globals, sorted, comma-joined; "?" marks a possible one
	}{
		{"seal-in reads its own coil", "X := (A OR X) AND NOT B;", "A,B,X"},
		{"written, then read", "X := A; c := X;", "A"},
		{"both branches write", "IF A THEN X := TRUE; ELSE X := FALSE; END_IF; c := X;", "A"},
		{"one branch writes", "IF A THEN X := TRUE; END_IF; c := X;", "A,X"},
		{"ELSIF chain writes on every arm", "IF A THEN X := TRUE; ELSIF B THEN X := FALSE; ELSE X := A; END_IF; c := X;", "A,B"},
		{"a branch that returns does not fall through", "IF A THEN RETURN; END_IF; X := TRUE; c := X;", "A"},
		{"the returning branch's missing write does not count", "IF A THEN RETURN; ELSE X := TRUE; END_IF; c := X;", "A"},
		{"CASE without ELSE may write nothing", "CASE N OF 1: X := TRUE; 2: X := FALSE; END_CASE; c := X;", "N,X"},
		{"CASE with ELSE writes on every arm", "CASE N OF 1: X := TRUE; ELSE X := FALSE; END_CASE; c := X;", "N"},
		{"CASE naming every enum member covers every path", "CASE m OF Idle: X := TRUE; Run, Fault: X := FALSE; END_CASE; c := X;", ""},
		{"CASE missing an enum member does not", "CASE m OF Idle: X := TRUE; Run: X := FALSE; END_CASE; c := X;", "X"},
		{"a FOR of unknown count may run zero times", "FOR i := 1 TO N DO X := TRUE; END_FOR; c := X;", "N,X?"},
		{"a FOR of constant bounds runs", "FOR i := 1 TO 3 DO X := TRUE; END_FOR; c := X;", ""},
		{"a FOR of constant bounds that never runs", "FOR i := 3 TO 1 DO X := TRUE; END_FOR; c := X;", "X"},
		{"a counting-down FOR runs", "FOR i := 3 TO 1 BY -1 DO X := TRUE; END_FOR; c := X;", ""},
		{"a WHILE may run zero times", "WHILE A DO X := TRUE; A := FALSE; END_WHILE; c := X;", "A,X?"},
		{"REPEAT runs once", "REPEAT X := A; UNTIL TRUE END_REPEAT; c := X;", "A"},
		{"an EXIT before the write leaves the loop unwritten", "REPEAT IF A THEN EXIT; END_IF; X := TRUE; UNTIL TRUE END_REPEAT; c := X;", "A,X"},
		{"UNTIL sees the body's writes", "REPEAT X := A; UNTIL X END_REPEAT;", "A"},
		{"a read inside a loop after its write", "FOR i := 1 TO 3 DO X := A; c := X; END_FOR;", "A"},
		{"a read inside a loop before its write", "FOR i := 1 TO 3 DO c := X; X := A; END_FOR;", "A,X"},
		{"an FB output binding writes", "t1(IN := A, PT := T#1S, Q => Y); c := Y;", "A"},
		{"only written", "X := TRUE; Y := FALSE;", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "TYPE Mode : (Idle, Run, Fault); END_TYPE\n" +
				"PROGRAM P\n" + ext + tc.body + "\nEND_PROGRAM\n"
			if g := rbwString(lowerOne(t, src)); g != tc.want {
				t.Errorf("ReadBeforeWrite = %q, want %q", g, tc.want)
			}
		})
	}
}

func rbwString(p *ir.Program) string {
	var got []string
	for name, r := range p.ReadBeforeWrite() {
		if !r.Sure {
			name += "?"
		}
		got = append(got, name)
	}
	sort.Strings(got)
	return strings.Join(got, ",")
}

// A FUNCTION_BLOCK call or a FUNCTION call is walked in order from the
// caller's state: what the body definitely writes through VAR_EXTERNAL
// counts after the call, and what it reads first is the caller's read.
func TestReadBeforeWriteFollowsCalls(t *testing.T) {
	const decls = "FUNCTION_BLOCK Siren\nVAR_INPUT On : BOOL; END_VAR\nVAR_EXTERNAL X : BOOL; END_VAR\nX := On;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK MaybeSiren\nVAR_INPUT On : BOOL; END_VAR\nVAR_EXTERNAL X : BOOL; END_VAR\nIF On THEN X := TRUE; END_IF;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK EarlyOut\nVAR_INPUT On : BOOL; END_VAR\nVAR_EXTERNAL X : BOOL; END_VAR\nIF On THEN RETURN; END_IF;\nX := TRUE;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK Relay\nVAR_EXTERNAL X : BOOL; Y : BOOL; A : BOOL; END_VAR\nX := A;\nY := X;\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION_BLOCK Outer\nVAR s : Siren; END_VAR\nVAR_EXTERNAL A : BOOL; END_VAR\ns(On := A);\nEND_FUNCTION_BLOCK\n" +
		"FUNCTION Peek : BOOL\nVAR_INPUT k : BOOL; END_VAR\nVAR_EXTERNAL X : BOOL; END_VAR\nPeek := X AND k;\nEND_FUNCTION\n" +
		"FUNCTION Poke : BOOL\nVAR_INPUT k : BOOL; END_VAR\nVAR_EXTERNAL X : BOOL; END_VAR\nX := k;\nPoke := k;\nEND_FUNCTION\n"
	const vars = "VAR_EXTERNAL A : BOOL; X : BOOL; END_VAR\nVAR c : BOOL; s : Siren; ms : MaybeSiren; eo : EarlyOut; r : Relay; o : Outer; END_VAR\n"
	for _, tc := range []struct{ name, body, want string }{
		{"an FB that writes X through VAR_EXTERNAL", "s(On := A); c := X;", "A"},
		{"an FB that writes X on one branch", "ms(On := A); c := X;", "A,X"},
		{"an FB whose RETURN skips the write", "eo(On := A); c := X;", "A,X"},
		{"a RETURN in an FB does not end the program", "eo(On := A); X := TRUE; c := X;", "A"},
		{"an FB body that writes, then reads", "r();", "A"},
		{"an FB inside an FB", "o(); c := X;", "A"},
		{"a FUNCTION that reads X before the program writes it", "c := Peek(A); X := A;", "A,X"},
		{"a FUNCTION that writes X", "c := Poke(A); c := X;", "A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := decls + "PROGRAM P\n" + vars + tc.body + "\nEND_PROGRAM\n"
			if g := rbwString(lowerOne(t, src)); g != tc.want {
				t.Errorf("ReadBeforeWrite = %q, want %q", g, tc.want)
			}
		})
	}
}

// The cause names why the first read comes first.
func TestReadBeforeWriteCause(t *testing.T) {
	const ext = "TYPE Pt : STRUCT x : INT; y : INT; END_STRUCT; END_TYPE\n" +
		"PROGRAM P\nVAR_EXTERNAL A : BOOL; X : BOOL; S : Pt; END_VAR\nVAR c : BOOL; END_VAR\n"
	for _, tc := range []struct {
		name, body string
		want       ir.ReadCause
	}{
		{"self-assignment", "X := X OR A;", ir.CauseSelfAssign},
		{"some paths", "IF A THEN X := TRUE; END_IF; c := X;", ir.CauseSomePaths},
		{"not yet", "c := X; X := TRUE;", ir.CauseNotYet},
		{"member write", "S.x := 1;", ir.CauseCompound},
		{"loop", "WHILE A DO X := TRUE; A := FALSE; END_WHILE; c := X;", ir.CauseLoopMayNotRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := lowerOne(t, ext+tc.body+"\nEND_PROGRAM\n")
			rf := p.ReadBeforeWrite()
			name := "X"
			if tc.want == ir.CauseCompound {
				name = "S"
			}
			if got, ok := rf[name]; !ok || got.Cause != tc.want {
				t.Errorf("%s: %+v (reported %v), want cause %v", name, got, ok, tc.want)
			}
		})
	}
}

// GlobalUses follows a user FUNCTION's VAR_EXTERNAL, called from the
// program or from an FB it instantiates: those are the program's reads and
// writes of the tag store.
func TestGlobalUsesFollowsFunctionCalls(t *testing.T) {
	src := "FUNCTION Peek : BOOL\nVAR_INPUT k : BOOL; END_VAR\nVAR_EXTERNAL R : BOOL; W : BOOL; END_VAR\nW := k;\nPeek := R;\nEND_FUNCTION\n" +
		"FUNCTION Peek2 : BOOL\nVAR_INPUT k : BOOL; END_VAR\nVAR_EXTERNAL R2 : BOOL; END_VAR\nPeek2 := R2 AND k;\nEND_FUNCTION\n" +
		"FUNCTION_BLOCK F\nVAR c : BOOL; END_VAR\nc := Peek2(TRUE);\nEND_FUNCTION_BLOCK\n" +
		"PROGRAM P\nVAR c : BOOL; f : F; END_VAR\nc := Peek(TRUE);\nf();\nEND_PROGRAM\n"
	u := lowerOne(t, src).GlobalUses()
	if !u.Read["R"] || !u.Written["W"] || !u.Read["R2"] {
		t.Errorf("GlobalUses = %+v, want R and R2 read, W written", u)
	}
}
