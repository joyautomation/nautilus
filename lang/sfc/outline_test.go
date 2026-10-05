package sfc

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseOutlineLenient(t *testing.T) {
	src := `PROGRAM P
VAR
    a : BOOL;
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Broken
  TRANSITION t1 FROM Idle TO Run := a;
  END_TRANSITION
  ACTION Act:
    a := TRUE;
  END_ACTION
`
	if _, err := Parse(src); err == nil {
		t.Fatal("the fixture parses; it should not")
	}
	p := ParseOutline(src)
	var got []string
	for _, s := range p.Steps {
		got = append(got, fmt.Sprintf("step %s %v %d:%d-%d", s.Name, s.Initial, s.Pos.Line, s.Pos.Col, s.EndPos.Line))
	}
	for _, tr := range p.Transitions {
		got = append(got, fmt.Sprintf("transition %s %v→%v %d-%d", tr.Name, tr.From, tr.To, tr.Pos.Line, tr.EndPos.Line))
	}
	for _, a := range p.Actions {
		got = append(got, fmt.Sprintf("action %s %d-%d", a.Name, a.Pos.Line, a.EndPos.Line))
	}
	want := `step Idle true 5:3-6
transition t1 [Idle]→[Run] 8-9
action Act 10-12`
	if g := strings.Join(got, "\n"); g != want {
		t.Errorf("got:\n%s\nwant:\n%s", g, want)
	}
}
