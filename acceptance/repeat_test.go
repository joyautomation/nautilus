package acceptance_test

// `repeat:` and group `steps:` (#1075): a counting test needs N identical
// stimuli, and without a repeat it was hundreds of generated lines. These
// tests hold what it must do: run the stimulus N times, name the iteration
// that failed (and stop there), keep every other step's number, and refuse
// the shapes that would never end.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// A rising-edge counter whose Tenth is TRUE for the scan that counts every
// 10th pulse: the finding's own example.
const counterProgram = `PROGRAM Main
VAR_EXTERNAL
    In    : BOOL;
    Count : DINT;
    Tenth : BOOL;
    N     : DINT;
END_VAR
VAR
    prev : BOOL;
END_VAR
N := N + 1;
Tenth := FALSE;
IF In AND NOT prev THEN
    Count := Count + 1;
    IF Count MOD 10 = 0 THEN
        Tenth := TRUE;
    END_IF;
END_IF;
prev := In;
END_PROGRAM`

func counterFS(suite string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: In, role: setpoint, type: BOOL, init: false }
  - { name: Count, role: state, type: DINT, init: 0 }
  - { name: Tenth, role: state, type: BOOL, init: false }
  - { name: N, role: state, type: DINT, init: 0 }
`)},
		"program.st":        &fstest.MapFile{Data: []byte(counterProgram)},
		"counter_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
}

func runCounter(t *testing.T, suite string) []acceptance.Result {
	t.Helper()
	fsys := counterFS(suite)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

func TestRepeatedGroupIsAPulseTrain(t *testing.T) {
	res := runCounter(t, `
tests:
  - name: the 10th and 25th pulse
    steps:
      - repeat: 9
        steps:
          - { given: { In: true }, scans: 1 }
          - { given: { In: false }, scans: 1 }
      - { expect: { Count: 9, Tenth: false } }
      - { given: { In: true }, scans: 1, expect: { Count: 10, Tenth: true } }
      - { given: { In: false }, scans: 1 }
      - repeat: 14
        steps:
          - { given: { In: true }, scans: 1 }
          - { given: { In: false }, scans: 1 }
      - { given: { In: true }, scans: 1, expect: { Count: 25 } }
`)
	if len(res) != 1 || !res[0].Passed {
		t.Fatalf("a pulse-train test failed: %+v\n%s", res, acceptance.FormatFailure(res[0]))
	}
}

func TestRepeatOnAnOrdinaryStep(t *testing.T) {
	res := runCounter(t, `
tests:
  - name: a leaf step repeats
    steps:
      - repeat: 5
        scans: 2
      - expect: { N: 10 }
`)
	if len(res) != 1 || !res[0].Passed {
		t.Fatalf("repeat on a plain step did not run it 5 times: %s", acceptance.FormatFailure(res[0]))
	}
}

func TestNestedRepeatsMultiply(t *testing.T) {
	res := runCounter(t, `
tests:
  - name: three rounds of four pulses
    steps:
      - repeat: 3
        steps:
          - repeat: 4
            steps:
              - { given: { In: true }, scans: 1 }
              - { given: { In: false }, scans: 1 }
      - { expect: { Count: 12 } }
`)
	if !res[0].Passed {
		t.Fatalf("%s", acceptance.FormatFailure(res[0]))
	}
}

// One failure, at the first iteration that breaks, named as the source
// step: not 24 identical ones, and not an index into an expanded list.
func TestRepeatFailureNamesTheIterationAndStops(t *testing.T) {
	const suite = `tests:
  - name: the count stays under 4
    steps:
      - scans: 1
      - repeat: 24
        steps:
          - { given: { In: true }, scans: 1 }
          - given: { In: false }
            scans: 1
            expect:
              - Count < 4
      - { expect: { Count: 24 } }
`
	res := runCounter(t, suite)
	f := res[0].Failure
	if res[0].Passed || f == nil {
		t.Fatal("the test should have failed")
	}
	if f.Step != 2 || f.StepPath != "2.2" || f.Repeat != "4 of 24" {
		t.Errorf("failure at step %d path %q repeat %q, want step 2, path 2.2, repeat 4 of 24", f.Step, f.StepPath, f.Repeat)
	}
	line := 1 + strings.Count(suite[:strings.Index(suite, "- given: { In: false }")], "\n")
	if f.StepLine != line {
		t.Errorf("StepLine %d, want %d (the inner step as written)", f.StepLine, line)
	}
	if got := acceptance.FormatFailure(res[0]); !strings.Contains(got, "step 2.2 (line ") || !strings.Contains(got, "repeat 4 of 24") {
		t.Errorf("the report does not read 'step 2.2 (line N), repeat 4 of 24':\n%s", got)
	}
	if res[0].Scans > 9 {
		t.Errorf("ran %d scans: it should stop at the failing iteration (1 + 4 x 2 = 9), not run all 24", res[0].Scans)
	}
}

// A step after a group keeps the number it has in the file.
func TestStepsAfterAGroupKeepTheirIndex(t *testing.T) {
	res := runCounter(t, `
tests:
  - name: the last step fails
    steps:
      - repeat: 3
        steps:
          - { given: { In: true }, scans: 1 }
          - { given: { In: false }, scans: 1 }
      - { expect: { Count: 99 } }
`)
	f := res[0].Failure
	if f == nil || f.Step != 2 || f.StepPath != "" || f.Repeat != "" {
		t.Fatalf("failure %+v, want step 2 with no path and no repeat", f)
	}
}

func TestRepeatIsRefusedWhenItCannotWork(t *testing.T) {
	for _, tc := range []struct{ name, steps, want string }{
		{"zero", "- { repeat: 0, scans: 1 }", "repeat must be between 1 and"},
		{"negative", "- { repeat: -2, scans: 1 }", "repeat must be between 1 and"},
		{"huge", "- { repeat: 100001, scans: 1 }", "repeat must be between 1 and"},
		{"nothing to repeat", "- { repeat: 3 }", "needs something to repeat"},
		{"a group with its own keys", "- { repeat: 3, scans: 1, steps: [{ scans: 1 }] }", "is a group and holds only"},
		{"an empty group", "- { repeat: 3, steps: [] }", "`steps:` is empty"},
		{"a nest too big", "- repeat: 1000\n        steps:\n          - { repeat: 1000, scans: 1 }", "more than 100000 steps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := counterFS("tests:\n  - name: t\n    steps:\n      " + tc.steps + "\n")
			_, err := acceptance.LoadSuite(fsys, "counter_test.yaml")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want one containing %q", err, tc.want)
			}
		})
	}
}
