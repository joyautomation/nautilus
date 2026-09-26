package acceptance_test

// SFC action-control semantics end to end: the batch-skid repro loaded as a
// manifest project, compiled exactly as `naut run` would, and run on the
// virtual clock. A bare `R X` on a step that never activates must not undo
// an ACTION body's write to X on the next scan (it used to: every S/R target
// was recomputed from its stored flag every scan).

import (
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const sfcResetManifest = `name: sfc-reset-repro
tasks:
  - program: seq.sfc
    scan: 100ms
tags:
  - { name: Go,    role: setpoint, init: false }
  - { name: X,     role: state,    init: false }
`

const sfcResetChart = `PROGRAM Seq
VAR_EXTERNAL Go : BOOL; X : BOOL; END_VAR
SFC
  INITIAL_STEP Idle:
  END_STEP
  STEP Run:
    P1 SetX;
  END_STEP
  STEP Aborted:
    R X;
  END_STEP
  TRANSITION t_go FROM Idle TO Run := Go;
  END_TRANSITION
  TRANSITION t_dead FROM Run TO Aborted := FALSE;
  END_TRANSITION
  TRANSITION t_back FROM Aborted TO Idle := TRUE;
  END_TRANSITION
  ACTION SetX:
    X := TRUE;
  END_ACTION
END_SFC
END_PROGRAM
`

func TestSFCResetOnInactiveStepKeepsActionWrite(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(sfcResetManifest)},
		"seq.sfc":       &fstest.MapFile{Data: []byte(sfcResetChart)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rt, sch, err := acceptance.NewRuntime(proj.Runtime)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rt.Tags().SetBool("Go", true)
	if err := sch.Scans(1); err != nil {
		t.Fatal(err)
	}
	if !rt.Tags().Bool("X") {
		t.Fatal("P1 SetX should have set X on Run's activation")
	}
	sch.Advance(10 * time.Second)
	if !rt.Tags().Bool("X") {
		t.Fatalf("X went FALSE by t=%v — the R on never-active step Aborted wrote it", sch.Elapsed())
	}
	if n, last := sch.LogicErrors(); n > 0 {
		t.Fatalf("%d logic error(s), last: %s", n, last)
	}
}
