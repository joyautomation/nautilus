package acceptance_test

// A FUNCTION_BLOCK's declared initial values — a VAR CONSTANT most visibly —
// used to be dropped when an instance was allocated, so the constant read
// zero. In the lift station's physics block that zeroed every term it
// scaled, and the block's VAR_IN_OUT and `=>`-captured outputs looked like
// they were never written back. This drives the original shape end to end:
// project.Load, then virtual-time scans.

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const fbConstLib = `FUNCTION_BLOCK Integrator
VAR_INPUT  Rate : REAL; Dt : REAL; END_VAR
VAR_IN_OUT Acc  : REAL; END_VAR
VAR_OUTPUT Doubled : REAL; END_VAR
VAR CONSTANT Gain : REAL := 2.0; END_VAR
Acc := Acc + Rate * Gain * Dt;
Doubled := Acc * Gain;
END_FUNCTION_BLOCK
`

const fbConstProgram = `PROGRAM Main
VAR_EXTERNAL Total : REAL; Twice : REAL; END_VAR
VAR i : Integrator; END_VAR
i(Rate := 1.0, Dt := 0.1, Acc := Total, Doubled => Twice);
END_PROGRAM`

func TestFBVarConstantWritesBack(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - { name: Total, role: state, init: 1.0 }
  - { name: Twice, role: state, init: 0.0 }
`)},
		"integrator.st": &fstest.MapFile{Data: []byte(fbConstLib)},
		"program.st":    &fstest.MapFile{Data: []byte(fbConstProgram)},
		"fb_test.yaml": &fstest.MapFile{Data: []byte(`
tests:
  - name: the in-out pin and the captured output both see the constant
    scans: 5
    expect:
      Total: { near: 2.0, tol: 0.000001 }
      Twice: { near: 4.0, tol: 0.000001 }
`)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no tests discovered")
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}
