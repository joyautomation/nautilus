package acceptance_test

// The `alarms:` key and the operator verbs, end to end: the harness builds
// the engine from the SAME manifest that deploys, wires it to the same
// virtual clock the scans run on, and an on-delay measured in minutes
// elapses in microseconds.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const alarmLib = `TYPE
  AnalogInput : STRUCT
    PV : REAL;
    HH : BOOL;
    L  : BOOL;
  END_STRUCT;
END_TYPE
`

const alarmProgram = `PROGRAM Main
VAR_EXTERNAL
    FIT_001 : AnalogInput;
    Online  : BOOL;
END_VAR
FIT_001.HH := FIT_001.PV > 100.0;
FIT_001.L  := FIT_001.PV < 10.0;
END_PROGRAM`

const alarmManifest = `
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: FIT_001, role: state, type: AnalogInput, init: { PV: 50.0 }, desc: "Well flow" }
  - { name: Online, role: setpoint, init: true }
alarms:
  rules:
    - match: { type: AnalogInput, member: HH }
      name: "{desc} High High"
      priority: high
      on-delay: 5m
      enable: Online
    - match: { type: AnalogInput, member: L }
      name: "{desc} Low"
      priority: medium
`

func alarmProject(t *testing.T, suite string) (fstest.MapFS, *project.Project) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml":   &fstest.MapFile{Data: []byte(alarmManifest)},
		"blocks.st":       &fstest.MapFile{Data: []byte(alarmLib)},
		"program.st":      &fstest.MapFile{Data: []byte(alarmProgram)},
		"alarm_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return fsys, proj
}

func runAlarmSuite(t *testing.T, suite string) []acceptance.Result {
	t.Helper()
	fsys, proj := alarmProject(t, suite)
	results, err := acceptance.RunDir(fsys, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no tests discovered")
	}
	return results
}

func mustPass(t *testing.T, results []acceptance.Result) {
	t.Helper()
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// The headline: an on-delay is a real assertion because the engine's clock
// is the harness's clock. `advance: 4m` is short of it and `advance: 1m1s`
// crosses it, exactly, on every machine.
func TestAlarmOnDelayWalksInVirtualTime(t *testing.T) {
	mustPass(t, runAlarmSuite(t, `
tests:
  - name: the on-delay qualifies the condition
    steps:
      - given: { FIT_001.PV: 150.0 }
        advance: 4m
        expect: { FIT_001.HH: true }
        alarms: { active: [], unacked: 0 }
      - advance: 1m1s
        alarms:
          active: ["FIT_001.HH"]
          unacked: 1
          state: { FIT_001.HH: unack-active }
          priority: { high: 1 }
`))
}

// ack: is the operator's side, and it moves alarm state without touching
// the tag store — the claim the design makes about ack never reaching the
// edge, asserted here rather than argued.
func TestAlarmAckVerb(t *testing.T) {
	mustPass(t, runAlarmSuite(t, `
tests:
  - name: acking discharges the obligation and leaves the bit alone
    steps:
      - given: { FIT_001.PV: 5.0 }
        scans: 1
        alarms: { state: { FIT_001.L: unack-active } }
      - ack: { ids: ["FIT_001.L"], by: test }
        scans: 1
        expect: { FIT_001.L: true }
        alarms:
          state: { FIT_001.L: ack-active }
          unacked: 0
      - given: { FIT_001.PV: 50.0 }
        scans: 1
        alarms:
          active: []
          journal: [active, ack, rtn]
`))
}

func TestAlarmShelveAndUnshelveVerbs(t *testing.T) {
	mustPass(t, runAlarmSuite(t, `
tests:
  - name: a shelf silences, then expires into the state it interrupted
    steps:
      - given: { FIT_001.PV: 5.0 }
        scans: 1
        alarms: { active: ["FIT_001.L"] }
      - shelve: { id: FIT_001.L, for: 15m, by: test }
        scans: 1
        alarms: { active: [], shelved: ["FIT_001.L"] }
      - advance: 16m
        alarms:
          shelved: []
          state: { FIT_001.L: unack-active }
  - name: unshelving early restores it too
    steps:
      - given: { FIT_001.PV: 5.0 }
        scans: 1
        alarms: { active: ["FIT_001.L"] }
      - shelve: { id: FIT_001.L, for: 1h, by: test }
        scans: 1
        alarms: { shelved: ["FIT_001.L"] }
      - unshelve: { id: FIT_001.L, by: test }
        scans: 1
        alarms: { shelved: [], state: { FIT_001.L: unack-active } }
`))
}

// Each test gets a fresh engine, the way each gets a fresh runtime. An ack
// leaking into the next test would be the exact failure the harness's "no
// shared state" contract exists to prevent.
func TestAlarmStateDoesNotLeakBetweenTests(t *testing.T) {
	mustPass(t, runAlarmSuite(t, `
tests:
  - name: first test acks everything
    steps:
      - given: { FIT_001.PV: 5.0 }
        scans: 1
      - ack: { all: true, by: test }
        scans: 1
        alarms: { unacked: 0 }
  - name: second test starts clean
    steps:
      - given: { FIT_001.PV: 5.0 }
        scans: 1
        alarms:
          unacked: 1
          journal: [active]
`))
}

// A failing alarm assertion has to say what it saw, not just that it
// failed — the same standard `expect:` is held to.
func TestAlarmFailureNamesWhatItSaw(t *testing.T) {
	results := runAlarmSuite(t, `
tests:
  - name: this one is meant to fail
    given: { FIT_001.PV: 5.0 }
    scans: 1
    alarms: { active: ["FIT_001.HH"] }
`)
	if results[0].Passed {
		t.Fatal("an assertion naming the wrong alarm passed")
	}
	d := results[0].Failure.Detail
	if !strings.Contains(d, "missing: FIT_001.HH") || !strings.Contains(d, "unexpected: FIT_001.L") {
		t.Errorf("failure detail should say what is missing and what is extra, got:\n%s", d)
	}
}

// A test that talks about alarms against a project with no `alarms:`
// section is a malformed test, not a failed assertion — and it says which
// manifest key is missing.
func TestAlarmKeysWithoutAnEngineAreAnError(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - { name: FIT_001, role: state, type: AnalogInput, init: { PV: 50.0 } }
  - { name: Online, role: setpoint, init: true }
`)},
		"blocks.st":  &fstest.MapFile{Data: []byte(alarmLib)},
		"program.st": &fstest.MapFile{Data: []byte(alarmProgram)},
		"a_test.yaml": &fstest.MapFile{Data: []byte(`
tests:
  - name: talks about alarms
    scans: 1
    alarms: { unacked: 0 }
`)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = acceptance.RunDir(fsys, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
	if err == nil || !strings.Contains(err.Error(), "`alarms:`") {
		t.Errorf("want an error naming the missing manifest section, got %v", err)
	}
}

// `alarms:` is checked at the end of a step; `until:` polls `expect` every
// tick. Mixing them would need a second predicate and a second failure
// story, so the loader refuses it with the split spelled out.
func TestAlarmsAndUntilAreRefused(t *testing.T) {
	fsys, proj := alarmProject(t, `
tests:
  - name: mixes them
    until: 1s
    expect: { FIT_001.HH: false }
    alarms: { unacked: 0 }
`)
	_, err := acceptance.RunDir(fsys, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
	if err == nil || !strings.Contains(err.Error(), "two steps") {
		t.Errorf("want a refusal explaining the split, got %v", err)
	}
}
