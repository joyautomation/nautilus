package acceptance_test

// Claims about what a test run does without being told to (TEST-011,
// TEST-018): an undeclared tag in `given:` is an error, and a scan that
// faults fails the test that ran it, with the error and the virtual time.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

func runProject(t *testing.T, program, tags, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml":    &fstest.MapFile{Data: []byte("tasks:\n  - program: program.st\ntags:\n" + tags)},
		"program.st":       &fstest.MapFile{Data: []byte(program)},
		"claims_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

// TEST-011: given: names a tag the project does not declare; that is an
// error, not a new tag.
func TestGivenOfAnUndeclaredTagIsAnError(t *testing.T) {
	_, err := runProject(t, pumpProgram,
		"  - { name: LevelPct, role: input, init: 0.0 }\n  - { name: PumpRun, role: output, init: false }\n",
		"tests:\n  - name: x\n    steps:\n      - given: { Typo: 1.0 }\n        scans: 1\n")
	if err == nil || !strings.Contains(err.Error(), `no tag "Typo"`) {
		t.Fatalf("err = %v, want one naming the undeclared tag Typo", err)
	}
}

// TEST-018: the test has no expectations, yet the scan that faults fails
// it, with the error and the virtual time of the fault.
func TestAFaultingScanFailsTheTestWithoutAnAssertion(t *testing.T) {
	const prog = `PROGRAM Main
VAR_EXTERNAL
    Idx : DINT;
    Out : DINT;
END_VAR
VAR
    A : ARRAY[0..2] OF DINT;
END_VAR
Out := A[Idx];
END_PROGRAM`
	const tags = "  - { name: Idx, role: input, type: DINT, init: 0 }\n  - { name: Out, role: output, type: DINT, init: 0 }\n"
	results, err := runProject(t, prog, tags, `tests:
  - name: in bounds
    steps:
      - given: { Idx: 1 }
        scans: 1
  - name: out of bounds
    steps:
      - given: { Idx: 7 }
        scans: 2
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].Passed {
		t.Fatalf("results = %+v, want the in-bounds test to pass", results)
	}
	r := results[1]
	if r.Passed || r.Failure == nil {
		t.Fatalf("the faulting test passed: %+v", r)
	}
	if !strings.Contains(r.Failure.Reason, "logic error") || !strings.Contains(strings.ToLower(r.Failure.Detail), "bounds") {
		t.Errorf("failure = %q / %q, want the logic error and its message", r.Failure.Reason, r.Failure.Detail)
	}
	if r.Failure.At <= 0 || r.Failure.At != r.Elapsed {
		t.Errorf("failure time = %v, want the virtual time the run reached (%v)", r.Failure.At, r.Elapsed)
	}
}
