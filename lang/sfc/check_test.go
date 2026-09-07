package sfc

import (
	"strings"
	"testing"
)

// mustParse parses src or fails the test immediately.
func mustParse(t *testing.T, src string) *Program {
	t.Helper()
	prog, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, src)
	}
	return prog
}

// wantDiag asserts diags contains a diagnostic of the given severity whose
// message contains substr.
func wantDiag(t *testing.T, diags []Diagnostic, sev Severity, substr string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == sev && strings.Contains(d.Message, substr) {
			return
		}
	}
	t.Errorf("expected a %s diagnostic containing %q, got: %v", sev, substr, diags)
}

// wantNoDiag asserts no diagnostic contains substr.
func wantNoDiag(t *testing.T, diags []Diagnostic, substr string) {
	t.Helper()
	for _, d := range diags {
		if strings.Contains(d.Message, substr) {
			t.Errorf("unexpected diagnostic containing %q: %v", substr, d)
		}
	}
}

// baseline is a minimal, fully clean two-step loop: no diagnostics at all.
// Individual tests mutate a copy of it to isolate exactly one rule.
const baseline = `PROGRAM P
VAR
  X : BOOL;
  Lamp : BOOL;
END_VAR
SFC
INITIAL_STEP A:
  N Lamp;
END_STEP
STEP B:
  N Lamp;
END_STEP
TRANSITION FROM A TO B := X;
END_TRANSITION
TRANSITION FROM B TO A := X;
END_TRANSITION
END_SFC
END_PROGRAM
`

func TestCheckBaselineIsClean(t *testing.T) {
	prog := mustParse(t, baseline)
	if diags := Check(prog); len(diags) != 0 {
		t.Errorf("Check(baseline) = %v, want none", diags)
	}
}

func TestCheckDuplicateStepName(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP A:
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "duplicate step name")
}

func TestCheckMissingInitialStep(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
STEP A:
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "no INITIAL_STEP")
}

func TestCheckMultipleInitialSteps(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
INITIAL_STEP B:
END_STEP
TRANSITION FROM A TO B := TRUE;
END_TRANSITION
TRANSITION FROM B TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "multiple INITIAL_STEP")
}

func TestCheckDuplicateActionName(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
  N Foo;
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
ACTION Foo:
END_ACTION
ACTION Foo:
END_ACTION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "duplicate ACTION name")
}

func TestCheckDuplicateTransitionName(t *testing.T) {
	src := `PROGRAM P
VAR
  X : BOOL;
END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP B:
END_STEP
TRANSITION t1 FROM A TO B := X;
END_TRANSITION
TRANSITION t1 FROM B TO A := X;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "duplicate TRANSITION name")
}

func TestCheckUnknownFromTo(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
TRANSITION FROM A TO Nope := TRUE;
END_TRANSITION
TRANSITION FROM Nope2 TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	diags := Check(prog)
	wantDiag(t, diags, SeverityError, `TO references unknown step "Nope"`)
	wantDiag(t, diags, SeverityError, `FROM references unknown step "Nope2"`)
}

func TestCheckEmptyCondition(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP B:
END_STEP
TRANSITION FROM A TO B := ;
END_TRANSITION
TRANSITION FROM B TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "empty condition")
}

func TestCheckUnreachableStep(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP Orphan:
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, `step "Orphan" is unreachable`)
}

func TestCheckDeadEndStepWarns(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP Terminal:
END_STEP
TRANSITION FROM A TO Terminal := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	diags := Check(prog)
	wantDiag(t, diags, SeverityWarning, `step "Terminal" is a dead end`)
	// A dead end is a warning, not an error — the chart otherwise compiles.
	wantNoDiag(t, diags, `error: step "Terminal" is a dead end`)
}

func TestCheckUnresolvedActionAssociation(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
  N NeitherActionNorVar;
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "references neither an ACTION block nor a declared variable")
}

func TestCheckActionAssociationResolvesToVariable(t *testing.T) {
	src := `PROGRAM P
VAR
  Lamp : BOOL;
END_VAR
SFC
INITIAL_STEP A:
  N Lamp;
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantNoDiag(t, Check(prog), "references neither")
}

func TestCheckUnsupportedQualifier(t *testing.T) {
	src := `PROGRAM P
VAR
  Lamp : BOOL;
END_VAR
SFC
INITIAL_STEP A:
  Q Lamp;
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, `unknown action qualifier "Q"`)
}

func TestCheckStagedQualifier(t *testing.T) {
	src := `PROGRAM P
VAR
  Lamp : BOOL;
END_VAR
SFC
INITIAL_STEP A:
  L Lamp(T#5s);
END_STEP
TRANSITION FROM A TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, `timed qualifier "L" is not implemented`)
}

func TestCheckUnknownStepXTRef(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP B:
END_STEP
TRANSITION FROM A TO B := Typo.X;
END_TRANSITION
TRANSITION FROM B TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, `references unknown step "Typo"`)
}

func TestCheckKnownStepXTRefIsClean(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP A:
END_STEP
STEP B:
END_STEP
TRANSITION FROM A TO B := A.X OR B.T > T#0s;
END_TRANSITION
TRANSITION FROM B TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantNoDiag(t, Check(prog), "references unknown step")
}

// TestCheckConvergenceUnreachableWarns: a simultaneous convergence whose
// sources were never split from a common simultaneous divergence.
func TestCheckConvergenceUnreachableWarns(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP S0:
END_STEP
STEP A:
END_STEP
STEP B:
END_STEP
STEP C:
END_STEP
TRANSITION FROM S0 TO A := TRUE;
END_TRANSITION
TRANSITION FROM S0 TO B := TRUE;
END_TRANSITION
TRANSITION FROM (A, B) TO C := TRUE;
END_TRANSITION
TRANSITION FROM C TO S0 := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityWarning, "not structurally reachable from a common simultaneous divergence")
}

// TestCheckConvergenceReachableIsClean: the mirror-image chart where A and B
// really do come from ONE simultaneous divergence — no warning.
func TestCheckConvergenceReachableIsClean(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP S0:
END_STEP
STEP A:
END_STEP
STEP B:
END_STEP
STEP C:
END_STEP
TRANSITION FROM S0 TO (A, B) := TRUE;
END_TRANSITION
TRANSITION FROM (A, B) TO C := TRUE;
END_TRANSITION
TRANSITION FROM C TO S0 := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantNoDiag(t, Check(prog), "not structurally reachable")
}

// TestCheckAmbiguousAltGroupWarns: three transitions t1/t2/t3 where t1-t2
// share a source and t2-t3 share a (different) source, but t1-t3 share
// none — a non-transitive three-way overlap (§2.3/§5.1).
func TestCheckAmbiguousAltGroupWarns(t *testing.T) {
	src := `PROGRAM P
VAR END_VAR
SFC
INITIAL_STEP S0:
END_STEP
STEP A:
END_STEP
STEP B:
END_STEP
STEP C:
END_STEP
STEP D:
END_STEP
TRANSITION t1 FROM (S0, A) TO B := TRUE;
END_TRANSITION
TRANSITION t2 FROM (A, C) TO B := TRUE;
END_TRANSITION
TRANSITION t3 FROM (C, D) TO B := TRUE;
END_TRANSITION
TRANSITION FROM B TO S0 := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityWarning, "ambiguous alternative-priority group")
}

// TestCheckCliqueAltGroupIsClean: three transitions that all pairwise share
// a source (a real clique) — priority is well-defined pairwise, no warning.
func TestCheckCliqueAltGroupIsClean(t *testing.T) {
	src := `PROGRAM P
VAR
  X : BOOL;
END_VAR
SFC
INITIAL_STEP S0:
END_STEP
STEP A:
END_STEP
STEP B:
END_STEP
STEP C:
END_STEP
TRANSITION t1 FROM S0 TO A := X;
END_TRANSITION
TRANSITION t2 FROM S0 TO B := X;
END_TRANSITION
TRANSITION t3 FROM S0 TO C := X;
END_TRANSITION
TRANSITION FROM A TO S0 := TRUE;
END_TRANSITION
TRANSITION FROM B TO S0 := TRUE;
END_TRANSITION
TRANSITION FROM C TO S0 := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantNoDiag(t, Check(prog), "ambiguous alternative-priority group")
}

func TestCheckSingleStepChartIsNotADeadEnd(t *testing.T) {
	// One INITIAL_STEP and nothing else is a valid degenerate chart (the
	// scaffold's starter): a continuously active step with no transitions.
	src := `PROGRAM P
VAR Out : BOOL; END_VAR
SFC
INITIAL_STEP Track:
  N Out;
END_STEP
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	if diags := Check(prog); len(diags) != 0 {
		t.Errorf("Check(single-step chart) = %v, want none", diags)
	}
}

func TestCheckTimeArgumentOnUntimedQualifierIsAnError(t *testing.T) {
	// The parser keeps `(T#5s)` so the editor round-trips it, but N/S/R/P
	// never read it; a chart must not run while silently ignoring a duration.
	src := `PROGRAM P
VAR Out : BOOL; END_VAR
SFC
INITIAL_STEP A:
  N Out (T#5s);
END_STEP
STEP B:
END_STEP
TRANSITION FROM A TO B := TRUE;
END_TRANSITION
TRANSITION FROM B TO A := TRUE;
END_TRANSITION
END_SFC
END_PROGRAM
`
	prog := mustParse(t, src)
	wantDiag(t, Check(prog), SeverityError, "does not take a time argument")
}
