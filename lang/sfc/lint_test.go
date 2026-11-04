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

// SFC001 is the hidden state machine only: a variable the chart's actions
// set to two or more different constants. A batch-start snapshot latched
// by a P1 action (Recipe := RecipeReq), a pass counter (Pass := Pass + 1)
// and a value reset to one constant are data the sequence branches on, so
// the reviewer's batch-start chart (#574) stays quiet.
func TestLintSFCStateVariableNeedsConstantSteps(t *testing.T) {
	chart := func(actions string) string {
		return `PROGRAM Seq
VAR Recipe : INT; RecipeReq : INT; Pass : INT; Start : BOOL; Stop : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
    P1 Latch;
  END_STEP
  STEP ChargeA:
    P1 Count;
  END_STEP
  STEP ChargeB:
    P0 Clear;
  END_STEP
  TRANSITION FROM Idle TO ChargeA := Start AND Recipe = 1;
  END_TRANSITION
  TRANSITION ToB FROM Idle TO ChargeB := Recipe = 2;
  END_TRANSITION
  TRANSITION Again FROM ChargeA TO Idle := Pass <> 3;
  END_TRANSITION
  TRANSITION FROM ChargeB TO Idle := Stop;
  END_TRANSITION
` + actions + `END_SFC
END_PROGRAM
`
	}
	quiet := chart(`  ACTION Latch:
    Recipe := RecipeReq;
  END_ACTION
  ACTION Count:
    Pass := Pass + 1;
  END_ACTION
  ACTION Clear:
    Recipe := 0;
  END_ACTION
`)
	if _, err := Parse(quiet); err != nil {
		t.Fatal(err)
	}
	if fs := Lint(quiet, lint.Defaults()); len(fs) != 0 {
		t.Errorf("snapshot, counter, one reset: want nothing, got %v", fs)
	}
	// The same chart with Recipe stepped through constants by the actions
	// is the hidden machine: ToB fires.
	machine := chart(`  ACTION Latch:
    IF Start THEN Recipe := 1; ELSE Recipe := 2; END_IF;
  END_ACTION
  ACTION Count:
    Pass := Pass + 1;
  END_ACTION
  ACTION Clear:
    Recipe := 0;
  END_ACTION
`)
	fs := Lint(machine, lint.Defaults())
	if len(fs) != 1 || fs[0].Rule != "SFC001" || !strings.Contains(fs[0].Message, "transition ToB only tests Recipe, which this chart's own actions set to 3 different constants") {
		t.Errorf("want SFC001 on ToB, got %v", fs)
	}
}

// SFC002 points at the step's name, not the STEP keyword.
func TestLintSFCStepColumn(t *testing.T) {
	src := "PROGRAM Seq\nSFC\n  INITIAL_STEP S0:\n  END_STEP\n  STEP Step1:\n  END_STEP\n" +
		"  TRANSITION FROM S0 TO Step1 := TRUE;\n  END_TRANSITION\n  TRANSITION FROM Step1 TO S0 := TRUE;\n  END_TRANSITION\nEND_SFC\nEND_PROGRAM\n"
	fs := Lint(src, lint.Defaults())
	if len(fs) != 2 || fs[0].Line != 3 || fs[0].Col != 16 || fs[1].Line != 5 || fs[1].Col != 8 {
		t.Errorf("want SFC002 at 3:16 and 5:8, got %v", fs)
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
