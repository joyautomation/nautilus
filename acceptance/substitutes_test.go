package acceptance_test

// `substitute:`, `restore:` and `ack-substitutes:` (#707): a sequence
// through a substituted sensor, on the project's own `substitutes:` rules
// and the test's virtual time.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const subManifest = `
name: subs
tasks:
  - program: main.st
    scan: 100ms
tags:
  - {name: Level, role: input, type: REAL, init: 0.0}
  - {name: Flow, role: input, type: REAL, init: 0.0}
  - {name: Mirror, role: state, init: 0.0}
  - {name: FlowMirror, role: state, init: 0.0}
  - {name: Permit, role: state, init: false}
  - {name: Flagged, role: state, init: false}
  - {name: SawExpired, role: state, init: false}
substitutes:
  defaults: {max: 1h}
  rules:
    - {match: Level, substitute: 95.0, max: 1h, ack-on-start: true}
    - {match: Flow, substitute: 1.0, max: 10m, operator-value: true}
`

const subProgram = `PROGRAM Main
VAR_EXTERNAL
  Level : REAL; Flow : REAL; Mirror : REAL; FlowMirror : REAL;
  Permit : BOOL; Flagged : BOOL; SawExpired : BOOL;
  Level__Substituted : BOOL; __SubstitutesUnacked : BOOL; __Substitute_Expired : BOOL;
END_VAR
Mirror := Level;
FlowMirror := Flow;
Flagged := Level__Substituted;
Permit := NOT __SubstitutesUnacked;
IF __Substitute_Expired THEN SawExpired := TRUE; END_IF;
END_PROGRAM
`

func runSubs(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml":  {Data: []byte(subManifest)},
		"main.st":        {Data: []byte(subProgram)},
		"subs_test.yaml": {Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime, acceptance.WithSubstitutes(proj.AcceptanceSubstitutes()))
}

func subMustPass(t *testing.T, suite string) {
	t.Helper()
	rs, err := runSubs(t, suite)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
	if len(rs) == 0 {
		t.Fatal("no tests ran")
	}
}

// The issue's acceptance test: a sequence through a substituted bad
// sensor; the substitute holds against the field, expiry restores the real
// value, the flag falls and __Substitute_Expired pulses.
func TestSubstituteSequenceExpiryRestoresTheRealValue(t *testing.T) {
	subMustPass(t, `
tests:
  - name: run on the substitute, then the sensor is back
    steps:
      - given: {Level: 10.0}
        advance: 300ms
        expect: {Mirror: 10.0, Flagged: false}
      - substitute: {tag: Level}        # the rule's value 95, its max 1h
        ack-substitutes: all
        advance: 300ms
        expect: {Mirror: 95.0, Flagged: true, Permit: true, Level__Substituted: true}
      - given: {Level: 12.0}            # the field moves on; the program does not see it
        advance: 59m
        expect: {Mirror: 95.0, Flagged: true}
      - advance: 2m                     # past the hour: expired
        expect: {Mirror: 12.0, Flagged: false, SawExpired: true}
`)
}

// A start that waits for an acknowledgement holds the program's start
// interlock until `ack-substitutes:`.
func TestSubstituteAckOnStartHoldsThePermissive(t *testing.T) {
	subMustPass(t, `
tests:
  - name: unacked until acknowledged
    steps:
      - given: {Level: 10.0}
        advance: 300ms
        expect: {Permit: true}
      - substitute: {tag: Level}
        advance: 300ms
        expect: {Permit: false}
      - ack-substitutes: [Level]
        advance: 300ms
        expect: {Permit: true}
  - name: restore puts the field back at once
    steps:
      - given: {Level: 10.0}
        advance: 300ms
      - substitute: {tag: Level}
        advance: 300ms
        expect: {Mirror: 95.0}
      - restore: [Level]
        advance: 300ms
        expect: {Mirror: 10.0, Flagged: false}
`)
}

// A step meets the same rules a person starting one through the API does.
func TestSubstituteStepRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ step, want string }{
		"no rule":           {"substitute: {tag: Mirror}", "not substitutable"},
		"an operator value": {"substitute: {tag: Level, value: 5.0}", "does not allow another"},
		"over the max":      {"substitute: {tag: Level, for: 2h}", "over the rule's max"},
		"a bad value":       {"substitute: {tag: Flow, value: high}", "substitute Flow"},
		"restore of none":   {"restore: [Level]", "is not substituted"},
	} {
		_, err := runSubs(t, "tests:\n  - name: x\n    steps:\n      - given: {Level: 1.0, Flow: 1.0}\n        advance: 300ms\n      - "+tc.step+"\n        advance: 100ms\n")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v; want an error containing %q", name, err, tc.want)
		}
	}
}

// An operator-value rule takes another value, and the harness without the
// rules says so rather than passing.
func TestSubstituteOperatorValueAndNoRules(t *testing.T) {
	subMustPass(t, `
tests:
  - name: the operator chooses
    steps:
      - given: {Flow: 3.0}
        advance: 300ms
      - substitute: {tag: Flow, value: 7.0, for: 5m}
        advance: 300ms
        expect: {FlowMirror: 7.0}
      - advance: 6m
        expect: {FlowMirror: 3.0}
`)
	fsys := fstest.MapFS{
		"nautilus.yaml": {Data: []byte(subManifest)},
		"main.st":       {Data: []byte(subProgram)},
		"x_test.yaml":   {Data: []byte("tests:\n  - name: x\n    steps:\n      - given: {Level: 1.0}\n        advance: 300ms\n      - substitute: {tag: Level}\n        advance: 100ms\n")},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptance.RunDir(fsys, proj.Runtime); err == nil || !strings.Contains(err.Error(), "no substitute rules") {
		t.Fatalf("without WithSubstitutes: %v", err)
	}
}

// `restore:` ends the tags it names and no others.
func TestSubstituteRestoreNamesTheTag(t *testing.T) {
	subMustPass(t, `
tests:
  - name: two substituted, one restored
    steps:
      - given: {Level: 10.0, Flow: 3.0}
        advance: 300ms
      - substitute: {tag: Level}
        advance: 100ms
      - substitute: {tag: Flow}
        advance: 300ms
        expect: {Mirror: 95.0, FlowMirror: 1.0}
      - restore: [Level]
        advance: 300ms
        expect: {Mirror: 10.0, FlowMirror: 1.0}     # Flow still on its substitute
      - restore: all
        advance: 300ms
        expect: {FlowMirror: 3.0}
`)
}
