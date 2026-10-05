package fbd

import (
	"fmt"
	"strings"
	"testing"
)

func TestStatementsLenient(t *testing.T) {
	src := `PROGRAM P
VAR
    Run, Start : BOOL;
END_VAR
FBD
  latch = OR(Start, Run)
  Run := AND(latch,
  t1 : TON(IN := latch, PT := T#5s)
  t1(IN := Start)
  Run := t1.Q;
END_FBD
END_PROGRAM
`
	var got []string
	for _, s := range Statements(src) {
		got = append(got, fmt.Sprintf("%s %s %d:%d-%d:%d name %d:%d-%d %q", s.Kind, s.Name, s.Line, s.Col, s.EndLine, s.EndCol, s.NameLine, s.NameCol, s.NameEndCol, s.Rest))
	}
	// The broken `Run := AND(latch,` swallows the next line; the parse
	// resumes after it.
	want := strings.Join([]string{
		`wire latch 6:3-6:25 name 6:3-8 "= OR(Start, Run)"`,
		`call t1 9:3-9:18 name 9:3-5 "(IN := Start)"`,
		`coil Run 10:3-10:15 name 10:3-6 ":= t1.Q;"`,
	}, "\n")
	if g := strings.Join(got, "\n"); g != want {
		t.Errorf("got:\n%s\nwant:\n%s", g, want)
	}
}
