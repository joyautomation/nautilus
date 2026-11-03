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

// A failing expectation on a TIME tag shows what the tag held as a T#
// literal, in the failure line and in its trace, the way the test wrote it:
// `Delay = T#2m`, not `Delay = 120000`.
func TestFailureShowsTimeAsLiteral(t *testing.T) {
	results, err := runDelay(t, `
tests:
  - name: a TIME expectation that fails
    given: { Delay: T#2m, Level: 0.0 }
    steps:
      - advance: 1s
        expect: { Delay: T#1m30s }
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Passed {
		t.Fatalf("want one failing result, got %+v", results)
	}
	out := acceptance.FormatFailure(results[0])
	if !strings.Contains(out, "Delay = T#2m, want T#1m30s ") {
		t.Errorf("failure line does not read Delay = T#2m, want T#1m30s:\n%s", out)
	}
	if strings.Contains(out, "120000") || strings.Contains(out, "90000") {
		t.Errorf("failure output shows Delay in milliseconds:\n%s", out)
	}
	trace := false
	for _, tr := range results[0].Failure.Trace {
		if tr.Name == "Delay" {
			trace = true
			for _, v := range tr.Values {
				if v != "T#2m" {
					t.Errorf("Delay trace value %q, want T#2m", v)
				}
			}
		}
	}
	if !trace {
		t.Errorf("no trace for Delay:\n%s", out)
	}
}

// The want side of a failing TIME expectation reads in the same units as
// the actual: a literal written the canonical way as itself, one written
// another way with what it was read as, and a number (milliseconds) or a
// numeric bound as a literal too.
func TestFailureWantOnATimeReadsAsALiteral(t *testing.T) {
	for _, tc := range []struct{ expect, detail string }{
		{`{ Delay: T#1m30s }`, `Delay = T#2m, want T#1m30s`},
		{`{ Delay: t#1M_30S }`, `Delay = T#2m, want T#1m30s`},
		{`{ Delay: 90s }`, `Delay = T#2m, want 90s (T#1m30s)`},
		{`{ Delay: 90000 }`, `Delay = T#2m, want T#1m30s`},
		{`{ Delay: { gt: 200000 } }`, `Delay = T#2m, want > T#3m20s`},
		{`{ Delay: { le: 60000 } }`, `Delay = T#2m, want <= T#1m`},
		{`{ Delay: { between: [1000, 2000] } }`, `Delay = T#2m, want between T#1s and T#2s`},
		{`{ Delay: { near: 60000, tol: 500 } }`, `Delay = T#2m, want T#1m ± T#500ms`},
	} {
		results, err := runDelay(t, "tests:\n  - name: fails\n    given: { Delay: T#2m, Level: 0.0 }\n    expect: "+tc.expect+"\n")
		if err != nil {
			t.Fatalf("%s: %v", tc.expect, err)
		}
		if len(results) != 1 || results[0].Passed || results[0].Failure == nil {
			t.Fatalf("%s: want one failing result, got %+v", tc.expect, results)
		}
		if got := results[0].Failure.Detail; got != tc.detail {
			t.Errorf("expect %s: failure reads %q, want %q", tc.expect, got, tc.detail)
		}
	}
}
