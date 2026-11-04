package ir_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
)

// ReadBeforeWrite is a definite-assignment walk: a global is reported when
// some path through one scan reads it before that scan has written it.
func TestReadBeforeWrite(t *testing.T) {
	const ext = "VAR_EXTERNAL A : BOOL; B : BOOL; X : BOOL; Y : BOOL; N : INT; END_VAR\n" +
		"VAR c : BOOL; t1 : TON; i : INT; END_VAR\n"
	for _, tc := range []struct {
		name, body string
		want       string // reported globals, sorted, comma-joined
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
		{"a loop may run zero times", "FOR i := 1 TO N DO X := TRUE; END_FOR; c := X;", "N,X"},
		{"a read inside a loop after its write", "FOR i := 1 TO 3 DO X := A; c := X; END_FOR;", "A"},
		{"an FB output binding writes", "t1(IN := A, PT := T#1S, Q => Y); c := Y;", "A"},
		{"only written", "X := TRUE; Y := FALSE;", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "PROGRAM P\n" + ext + tc.body + "\nEND_PROGRAM\n"
			prog, err := st.Parse(src)
			if err != nil {
				t.Fatal(err)
			}
			ip, err := st.Lower(prog)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for name := range ip.ReadBeforeWrite() {
				got = append(got, name)
			}
			sort.Strings(got)
			if g := strings.Join(got, ","); g != tc.want {
				t.Errorf("ReadBeforeWrite = %q, want %q", g, tc.want)
			}
		})
	}
}
