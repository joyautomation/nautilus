package sfc

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/lint"
)

// Each SFC structure rule fires on a minimal bad chart and is silent on
// the rewrite its message asks for (nautilus#561).
func TestLintSFCRules(t *testing.T) {
	// The CASE machine written as a chart: steps numbered, the real
	// conditions in an action's IFs, the transitions testing State.
	caseMachine := `PROGRAM Seq
VAR State : INT; Start : BOOL; Full : BOOL; FillReq : BOOL; END_VAR
SFC
  INITIAL_STEP S0:
    N Logic;
  END_STEP
  STEP S1:
    N Logic;
  END_STEP
  TRANSITION FROM S0 TO S1 := State = 1;
  END_TRANSITION
  TRANSITION t_back FROM S1 TO S0 := (State = 0);
  END_TRANSITION
  ACTION Logic:
    IF Start THEN State := 1; END_IF;
    IF Full THEN State := 0; END_IF;
    FillReq := State = 1;
  END_ACTION
END_SFC
END_PROGRAM
`
	good := `PROGRAM Seq
VAR Start : BOOL; Full : BOOL; FillReq : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Fill:
    N FillReq;
  END_STEP
  TRANSITION t_start FROM Idle TO Fill := Start;
  END_TRANSITION
  TRANSITION t_full FROM Fill TO Idle := Full;
  END_TRANSITION
END_SFC
END_PROGRAM
`
	fs := Lint(caseMachine, lint.Defaults())
	var sfc001, sfc002 int
	for _, f := range fs {
		switch f.Rule {
		case "SFC001":
			sfc001++
			if !strings.Contains(f.Message, "only tests State, which this chart's own actions set") {
				t.Errorf("SFC001 message: %s", f.Message)
			}
		case "SFC002":
			sfc002++
			if !strings.Contains(f.Message, "is named for its number") {
				t.Errorf("SFC002 message: %s", f.Message)
			}
		default:
			t.Errorf("unexpected %v", f)
		}
	}
	if sfc001 != 2 || sfc002 != 2 {
		t.Errorf("want 2 SFC001 (both transitions) and 2 SFC002 (S0, S1), got %v", fs)
	}
	if fs := Lint(good, lint.Defaults()); len(fs) != 0 {
		t.Errorf("the rewrite still warns: %v", fs)
	}
}

// A mode compare is not a state machine unless the chart writes the mode
// itself: an operator's Mode setpoint, or a status string the actions set
// only for display, stay quiet.
func TestLintSFCStateTestNeedsAChartWrite(t *testing.T) {
	src := `PROGRAM Seq
VAR Mode : INT; SeqStep : STRING; END_VAR
SFC
  INITIAL_STEP Idle:
    N Show;
  END_STEP
  STEP Run:
  END_STEP
  TRANSITION t_auto FROM Idle TO Run := Mode = 2;
  END_TRANSITION
  TRANSITION t_off FROM Run TO Idle := Mode <> 2;
  END_TRANSITION
  ACTION Show:
    SeqStep := 'Idle';
  END_ACTION
END_SFC
END_PROGRAM
`
	if fs := Lint(src, lint.Defaults()); len(fs) != 0 {
		t.Errorf("want nothing, got %v", fs)
	}
}
