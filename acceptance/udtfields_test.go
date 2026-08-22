package acceptance_test

// Once a tag can be a UDT, a dotted name in a test is ambiguous: `P101.Speed`
// is a field, `main.err` is a task local. Before this, value() cut on the
// first dot and looked up a TASK, so `P101.Speed` reported "no task P101" —
// a message about the wrong namespace entirely.
//
// The order is fixed and tag-first: exact tag, then <tag>.<field>, then
// <task>.<local>, then an error naming both namespaces.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const udtLib = `TYPE
  Motor : STRUCT
    Running : BOOL;
    Speed   : REAL;
  END_STRUCT;
END_TYPE
`

const udtProgram = `PROGRAM Main
VAR_EXTERNAL
    P101 : Motor;
    Duty : REAL;
END_VAR
VAR
    err : REAL;
END_VAR
err := P101.Speed;
Duty := err * 2.0;
END_PROGRAM`

func udtFS(suite string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - { name: P101, role: input, type: Motor }
  - { name: Duty, role: state, init: 0.0 }
`)},
		"motor.st":      &fstest.MapFile{Data: []byte(udtLib)},
		"program.st":    &fstest.MapFile{Data: []byte(udtProgram)},
		"udt_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
}

func runUDT(t *testing.T, suite string) []acceptance.Result {
	t.Helper()
	results, err := tryUDT(t, suite)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no tests discovered")
	}
	return results
}

// tryUDT keeps the error, for the cases where an unresolvable NAME is the
// thing under test. A name that resolves to nothing is a malformed test
// rather than a failed assertion, so the runner reports it as an error.
func tryUDT(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := udtFS(suite)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

// given sets one field of a driver-fed UDT input; expect reads it back and
// reads the value the logic derived from it. This is the whole feature.
func TestGivenAndExpectAddressFields(t *testing.T) {
	for _, r := range runUDT(t, `
tests:
  - name: a field given reaches the logic
    given: { P101.Speed: 12.5 }
    scans: 2
    expect:
      P101.Speed: 12.5
      Duty: 25.0
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// TestExpressionAddressesStructField is a regression test: an ST boolean
// expression like "WEL15_FIT_001.VALUE < WellFlowGpm + 5.0" used to fail
// with `undeclared identifier "WEL15_FIT_001"`, because ExternalsOf left
// every struct-typed (UDT) tag out of the expression's generated
// VAR_EXTERNAL block entirely — only the matcher form
// (`P101.Speed: {gt: 800}`) reached a struct field. A struct-typed tag now
// declares as its own TYPE, the same way the project's own program does.
func TestExpressionAddressesStructField(t *testing.T) {
	for _, r := range runUDT(t, `
tests:
  - name: an expression can reach a struct field
    given: { P101.Speed: 12.5 }
    scans: 2
    expect: "P101.Speed < Duty + 1.0"
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// A task local is still reachable, and still by the same syntax — the point
// of fixing the ORDER rather than inventing a second one.
func TestTaskLocalStillResolves(t *testing.T) {
	for _, r := range runUDT(t, `
tests:
  - name: task locals still work
    given: { P101.Speed: 3.0 }
    scans: 2
    expect:
      main.err: 3.0
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// A wrong field name must say so — and say what the struct does have. The
// old code could only report that there was no such TASK.
func TestUnknownFieldNamesTheStruct(t *testing.T) {
	_, err := tryUDT(t, `
tests:
  - name: misspelled field
    given: { P101.Speed: 1.0 }
    scans: 2
    expect:
      P101.Sped: 1.0
`)
	if err == nil {
		t.Fatal("a misspelled field passed")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Sped") {
		t.Errorf("failure does not name the bad field:\n%s", msg)
	}
	if !strings.Contains(msg, "Running") || !strings.Contains(msg, "Speed") {
		t.Errorf("failure should list the struct's real fields:\n%s", msg)
	}
	if strings.Contains(msg, "no task") {
		t.Errorf("failure still reports the task namespace:\n%s", msg)
	}
}

// A head that names neither namespace must say both, since the writer cannot
// tell from the name alone which one they meant.
func TestUnknownHeadNamesBothNamespaces(t *testing.T) {
	_, err := tryUDT(t, `
tests:
  - name: unknown head
    scans: 1
    expect:
      Nope.Field: 1.0
`)
	if err == nil {
		t.Fatal("an unresolvable name passed")
	}
	msg := err.Error()
	if !strings.Contains(msg, "tags:") || !strings.Contains(msg, "tasks:") {
		t.Errorf("failure should list both namespaces:\n%s", msg)
	}
}
