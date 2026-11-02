package acceptance_test

// F-07: a test's given: and force: convert a value to the tag's declared
// type by the rule its init: seeds with, so `T#8h` on a TIME tag is eight
// hours and not the STRING "T#8h" — which a TON took as a zero preset and
// timed out on the first scan. A value that does not convert is the test's
// error, naming the tag. F-08: expect: names a TIME by the same literal.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const delayProgram = `PROGRAM Main
VAR_EXTERNAL
    Level : REAL;
    Delay : TIME;
    Alm   : BOOL;
END_VAR
VAR
    ton1 : TON;
END_VAR
ton1(IN := Level >= 90.0, PT := Delay);
Alm := ton1.Q;
END_PROGRAM`

func runDelay(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: Level, role: input,    type: REAL }
  - { name: Delay, role: setpoint, type: TIME, init: T#30s }
  - { name: Alm,   role: state,    type: BOOL, init: false }
`)},
		"program.st":      &fstest.MapFile{Data: []byte(delayProgram)},
		"delay_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

func TestGivenTimeLiteralIsATime(t *testing.T) {
	results, err := runDelay(t, `
tests:
  - name: a T# literal given to a TIME tag is that duration
    given: { Delay: T#8h, Level: 95.0 }
    steps:
      - advance: 31s
        expect: { Alm: false, Delay: T#8h }
      - expect: "Delay = T#8h"

  - name: the same duration without its prefix
    given: { Delay: 45s, Level: 95 }
    steps:
      - advance: 44s
        expect: { Alm: false }
      - advance: 2s
        expect: { Alm: true }

  - name: a force on a TIME tag takes the literal too
    steps:
      - force: { Delay: T#1m }
        given: { Level: 95.0 }
        advance: 31s
        expect: { Alm: false, Delay: 60000 }

  - name: expect names the seeded TIME by its literal
    expect: { Delay: T#30s }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("%d results, want 4", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

func TestGivenValueNotOfTheTagsTypeIsAnError(t *testing.T) {
	for _, tc := range []struct{ given, want string }{
		{`{ Delay: soon }`, `Delay: want TIME, got "soon"`},
		{`{ Alm: 1 }`, `Alm: want BOOL, got a number`},
		{`{ Level: high }`, `Level: want REAL, got a string`},
	} {
		_, err := runDelay(t, "tests:\n  - name: bad given\n    given: "+tc.given+"\n    scans: 1\n")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("given %s: want an error containing %q, got %v", tc.given, tc.want, err)
		}
	}
}
