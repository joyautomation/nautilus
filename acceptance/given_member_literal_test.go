package acceptance_test

// #498: given:, force: and expect: convert a struct MEMBER's value by its
// declared type, as #497 made them do for a whole scalar tag. A T# literal
// on a TIME member is that duration; an enumeration member by name; a
// whole-struct mapping lands member by member (it used to vanish without an
// error, taking every member it named with it).

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const memberLib = `TYPE
  Mode : (Idle, Run := 10) := Idle;
  Pump : STRUCT
    Speed : REAL;
    Hold  : TIME;
    Mode  : Mode;
    Drive : STRUCT_Drive;
  END_STRUCT;
  STRUCT_Drive : STRUCT
    Ramp : TIME;
  END_STRUCT;
END_TYPE
`

const memberProgram = `PROGRAM Main
VAR_EXTERNAL
    P   : Pump;
    Alm : BOOL;
END_VAR
VAR
    ton1 : TON;
END_VAR
ton1(IN := P.Speed > 1.0, PT := P.Hold);
Alm := ton1.Q;
END_PROGRAM`

func runMember(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: P,   role: state, type: Pump }
  - { name: Alm, role: state, type: BOOL, init: false }
`)},
		"lib/pump.st":    &fstest.MapFile{Data: []byte(memberLib)},
		"program.st":     &fstest.MapFile{Data: []byte(memberProgram)},
		"pump_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

func TestMemberLiteralsConvertByDeclaredType(t *testing.T) {
	results, err := runMember(t, `
tests:
  - name: a whole-struct given lands member by member
    given: { P: { Speed: 3.5, Hold: T#1s, Mode: Run, Drive: { Ramp: T#2s } } }
    steps:
      - advance: 900ms
        expect: { Alm: false, P.Speed: 3.5, P.Hold: T#1s, P.Mode: Run, P.Drive.Ramp: T#2s }
      - advance: 200ms
        expect: { Alm: true }

  - name: a dotted given takes a T# literal
    given: { P.Speed: 2.0, P.Hold: T#8h }
    steps:
      - advance: 1s
        expect: { Alm: false, P.Hold: T#8h }
      - expect: "P.Hold = T#8h"

  - name: a force on a TIME member takes the literal too
    given: { P.Speed: 2.0 }
    steps:
      - force: { P.Hold: T#500ms }
        advance: 600ms
        expect: { Alm: true, P.Hold: 500 }
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

func TestMemberGivenNotOfItsTypeIsAnError(t *testing.T) {
	for _, tc := range []struct{ given, want string }{
		{`{ P.Hold: soon }`, `P.Hold: want TIME, got "soon"`},
		{`{ P: { Hold: soon } }`, `P.Hold: want TIME, got "soon"`},
		{`{ P: { Mode: Walk } }`, `Walk`},
	} {
		_, err := runMember(t, "tests:\n  - name: bad given\n    given: "+tc.given+"\n    scans: 1\n")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("given %s: want an error containing %q, got %v", tc.given, tc.want, err)
		}
	}
}
