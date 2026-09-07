package acceptance_test

// Regression coverage for a bug in applyField (now applyFields): a `given:`
// map that set two dotted fields of the SAME struct tag raced. Each field
// write independently re-read the tag store as its base, but an INPUT tag's
// writes land in the driver's image, not the tag store, until the next scan
// promotes it — so both reads saw the same stale (usually zero-of-type)
// base, and the driver's last write (whichever key sorted last) replaced
// the whole tag, silently discarding the other edit. See
// ~/Development/pomona/wrd/host/host_test.yaml and its README "Tests"
// section, which found this while testing fleet.st.
//
// The fix gathers every field a given map addresses on one root tag, applies
// them all to a single copy of the current value, and writes once.

import (
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// TestGivenComposesTwoFieldsOfSameStructInOneMap is the direct repro: two
// dotted fields into the SAME input struct tag, given in one map. Before
// the fix, "Running" (given true) was silently lost — only "Speed" (sorted
// last) survived the race.
func TestGivenComposesTwoFieldsOfSameStructInOneMap(t *testing.T) {
	for _, r := range runUDT(t, `
tests:
  - name: two dotted fields into one struct compose
    given: { P101.Running: true, P101.Speed: 12.5 }
    scans: 1
    expect:
      P101.Running: true
      P101.Speed: 12.5
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// TestGivenSingleFieldStillZeroFillsTheRest is the pre-existing, single-
// field behaviour the fix must not disturb: one dotted field is still
// enough to seed the whole struct zero-of-type.
func TestGivenSingleFieldStillZeroFillsTheRest(t *testing.T) {
	for _, r := range runUDT(t, `
tests:
  - name: one dotted field zero-fills its siblings
    given: { P101.Speed: 12.5 }
    scans: 1
    expect:
      P101.Running: false
      P101.Speed: 12.5
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// ─── a nested-struct fixture, for a nested field + a sibling field ────────

const nestedLib = `TYPE
  Drv : STRUCT
    Speed : REAL;
    Fault : BOOL;
  END_STRUCT;
  Motor2 : STRUCT
    Running : BOOL;
    Drive   : Drv;
  END_STRUCT;
END_TYPE
`

const nestedProgram = `PROGRAM Main
VAR_EXTERNAL
    M    : Motor2;
    Mode : REAL;
END_VAR
VAR
    err : REAL;
END_VAR
err := M.Drive.Speed;
END_PROGRAM`

func nestedFS(suite string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - { name: M, role: input, type: Motor2 }
  - { name: Mode, role: state, init: 0.0 }
`)},
		"motor2.st":        &fstest.MapFile{Data: []byte(nestedLib)},
		"program.st":       &fstest.MapFile{Data: []byte(nestedProgram)},
		"nested_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
}

func runNested(t *testing.T, suite string) []acceptance.Result {
	t.Helper()
	fsys := nestedFS(suite)
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
	return results
}

// TestGivenNestedFieldAndSiblingFieldCompose covers a nested field
// (Drive.Speed, two segments deep) and a top-level sibling field (Running)
// of the same struct tag, given together in one map.
func TestGivenNestedFieldAndSiblingFieldCompose(t *testing.T) {
	for _, r := range runNested(t, `
tests:
  - name: a nested field and a sibling field compose
    given: { M.Running: true, M.Drive.Speed: 7.5 }
    scans: 1
    expect:
      M.Running: true
      M.Drive.Speed: 7.5
      M.Drive.Fault: false
      main.err: 7.5
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// TestGivenScalarAndStructFieldInSameStep covers a plain scalar tag and a
// dotted struct field on a different tag in the same `given:` map — the
// grouping-by-root-tag in apply() must not disturb the ordinary whole-tag
// path.
func TestGivenScalarAndStructFieldInSameStep(t *testing.T) {
	for _, r := range runNested(t, `
tests:
  - name: a scalar and a struct field in the same given
    given: { Mode: 3.0, M.Drive.Speed: 4.25 }
    scans: 1
    expect:
      Mode: 3.0
      M.Drive.Speed: 4.25
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

// TestGivenComposesAfterZeroScans checks the compose fix is visible
// immediately — no scan required — for a tag whose `given` writes land
// straight in the tag store (a non-input struct field would also work, but
// Mode alone already proves the whole-tag path was never routed through the
// (now-removed) per-field re-read).
func TestGivenComposesAfterZeroScans(t *testing.T) {
	for _, r := range runNested(t, `
tests:
  - name: a scalar given is visible before any scan
    given: { Mode: 9.0 }
    scans: 0
    expect:
      Mode: 9.0
`) {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}
