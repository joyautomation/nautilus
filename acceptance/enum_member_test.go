package acceptance_test

// #247: a test's given: by member path into an enumerated member — at any
// depth, in an array of structs too — takes the member NAME and lands as the
// named value, the same as a whole-tag given: and an operator's POST; a
// struct tag's init: seeds enum members by name the same way. A name that is
// no member is an error listing the members.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const recipeTypes = `TYPE
  Mode : (Idle, Run := 10, Fault) := Idle;
  Step : STRUCT
    Mode : Mode;
    Secs : REAL;
  END_STRUCT;
  Recipe : STRUCT
    Mode  : Mode;
    First : Step;
    Steps : ARRAY[1..3] OF Step;
  END_STRUCT;
END_TYPE
`

const recipeProgram = `PROGRAM Main
VAR_EXTERNAL
    R       : Recipe;
    Running : BOOL;
    Code    : INT;
END_VAR
Running := R.Steps[2].Mode = Mode#Run AND R.First.Mode = Mode#Fault;
Code := TO_INT(R.Steps[3].Mode);
END_PROGRAM`

func runRecipe(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - name: R
    role: state
    type: Recipe
    init: { Mode: Run, First: { Mode: Idle }, Steps: [ { Mode: Fault }, { Mode: Idle }, { Mode: 10 } ] }
  - { name: Running, role: state, init: false }
  - { name: Code, role: state, init: 0 }
`)},
		"types.st":         &fstest.MapFile{Data: []byte(recipeTypes)},
		"program.st":       &fstest.MapFile{Data: []byte(recipeProgram)},
		"recipe_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

func TestGivenEnumMemberByPath(t *testing.T) {
	// Block mappings: in a flow mapping ({ … }) YAML reads [ and ] as list
	// brackets, so an indexed key there must be quoted ("R.Steps[2].Mode").
	results, err := runRecipe(t, `
tests:
  - name: init seeds enum members by name, in nested structs and arrays of structs
    scans: 1
    expect:
      R.Mode: Run
      R.First.Mode: Idle
      R.Steps[1].Mode: Fault
      R.Steps[3].Mode: Run
      Running: false
      Code: 10

  - name: given by member path takes the member name, and the logic sees the member
    steps:
      - given:
          R.First.Mode: Fault
          R.Steps[2].Mode: run
          R.Steps[3].Mode: "Mode#Fault"
        scans: 1
        expect: { "R.Steps[2].Mode": Run, "R.Steps[3].Mode": Fault, Running: true, Code: 11 }
      - given: { "R.Steps[2].Mode": 0 }
        scans: 1
        expect: { "R.Steps[2].Mode": Idle, Running: false }

  - name: a whole-struct given merges, enums by name at every depth
    given: { R: { Steps: [ { Mode: Run }, { Mode: Run } ] }, R.First.Mode: Fault }
    scans: 1
    expect: { "R.Steps[1].Mode": Run, Running: true, R.Mode: Run }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("%d results, want 3", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

func TestGivenEnumNonMemberNamesMembers(t *testing.T) {
	results, err := runRecipe(t, `
tests:
  - name: a name that is no member
    given: { "R.Steps[2].Mode": Stop }
    scans: 1
`)
	msg := ""
	if err != nil {
		msg = err.Error()
	} else {
		for _, r := range results {
			if !r.Passed {
				msg += acceptance.FormatFailure(r)
			}
		}
	}
	if !strings.Contains(msg, `"Stop" is not a member of Mode (Idle, Run, Fault)`) {
		t.Errorf("want the member list, got: %q", msg)
	}
}
