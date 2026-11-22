package acceptance_test

// Suppression by state (#715 PR 5) end to end, on the manifest that
// deploys: an alarm whose condition is a substituted input is suppressed
// (signal substituted), and one the program computes from a substituted
// input is suppressed by its own suppress-when on the input's
// __Substituted flag (the case the automatic link cannot see).

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const suppressManifest = `
name: suppress
tasks:
  - program: main.st
    scan: 100ms
tags:
  - {name: LSH, role: input, type: BOOL, init: false}
  - {name: Level, role: input, type: REAL, init: 0.0}
  - {name: HiLevel, role: state, init: false}
substitutes:
  defaults: {max: 1h}
  rules:
    - {match: LSH, substitute: false, max: 1h}
    - {match: Level, substitute: 99.0, max: 1h}
alarms:
  defs:
    - {id: LSH, tag: LSH}
    - {id: HiLevel, tag: HiLevel, suppress-when: Level__Substituted, suppress-reason: "level sensor substituted"}
`

const suppressProgram = `PROGRAM Main
VAR_EXTERNAL
  Level : REAL; HiLevel : BOOL;
END_VAR
HiLevel := Level > 90.0;
END_PROGRAM
`

func TestSubstitutedSignalsSuppressTheirAlarms(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml":      {Data: []byte(suppressManifest)},
		"main.st":            {Data: []byte(suppressProgram)},
		"suppress_test.yaml": {Data: []byte(suppressSuite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := acceptance.RunDir(fsys, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine),
		acceptance.WithSubstitutes(proj.AcceptanceSubstitutes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) == 0 {
		t.Fatal("no tests ran")
	}
	for _, r := range rs {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

const suppressSuite = `
tests:
  - name: a substituted condition is suppressed, and shows again after
    steps:
      - given: {LSH: true}
        advance: 300ms
        alarms: {state: {LSH: unack-active}}
      - substitute: {tag: LSH}
        advance: 300ms
        alarms: {state: {LSH: suppressed}}
      - restore: [LSH]
        advance: 300ms
        alarms: {state: {LSH: unack-active}}
  - name: a computed condition follows its suppress-when on the input's flag
    steps:
      - given: {Level: 95.0}
        advance: 300ms
        alarms: {state: {HiLevel: unack-active}}
      - substitute: {tag: Level}
        advance: 300ms
        expect: {HiLevel: true}
        alarms: {state: {HiLevel: suppressed}}
      - restore: [Level]
        advance: 300ms
        alarms: {state: {HiLevel: unack-active}}
`
