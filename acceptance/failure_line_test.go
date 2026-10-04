package acceptance_test

// Where a failure points. An editor anchors the failure at Failure.Line,
// so it must be the assertion that broke — the tag key inside `expect:`,
// the expression, the `alarms:` key — and not the top of the step, which
// is usually its `given:` (issue #145). The step's own line rides along
// as StepLine for the person reading the log.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const pumpProgram = `PROGRAM Main
VAR_EXTERNAL
    LevelPct : REAL;
    PumpRun  : BOOL;
END_VAR
PumpRun := LevelPct > 50.0;
END_PROGRAM`

func runPump(t *testing.T, suite string) []acceptance.Result {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
tasks:
  - program: program.st
tags:
  - { name: LevelPct, role: input, init: 0.0 }
  - { name: PumpRun, role: output, init: false }
`)},
		"program.st":     &fstest.MapFile{Data: []byte(pumpProgram)},
		"pump_test.yaml": &fstest.MapFile{Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	return results
}

// The suite text starts with a newline, so line N here is line N of the
// file: `tests:` is line 2.
func TestFailureLinePointsAtTheAssertion(t *testing.T) {
	results := runPump(t, `
tests:
  - name: flow-mapping expect
    steps:
      - given: { LevelPct: 80.0 }
        scans: 1
        expect: { PumpRun: false }
  - name: block-mapping expect names the tag's own line
    steps:
      - given: { LevelPct: 80.0 }
        scans: 1
        expect:
          LevelPct: { gt: 50 }
          PumpRun: false
  - name: expression
    steps:
      - given: { LevelPct: 10.0 }
        scans: 1
        expect:
          - LevelPct < 50.0
          - PumpRun
  - name: always
    steps:
      - given: { LevelPct: 80.0 }
        scans: 2
        always: { PumpRun: false }
  - name: until
    steps:
      - given: { LevelPct: 10.0 }
        until: 1s
        expect: { PumpRun: true }
  - name: single-step shorthand
    given: { LevelPct: 80.0 }
    scans: 1
    expect: { PumpRun: false }
`)
	want := map[string]struct{ line, stepLine int }{
		"flow-mapping expect":                           {7, 5},
		"block-mapping expect names the tag's own line": {14, 10},
		"expression":            {21, 17},
		"always":                {26, 24},
		"until":                 {31, 29},
		"single-step shorthand": {35, 32},
	}
	if len(results) != len(want) {
		t.Fatalf("ran %d tests, want %d", len(results), len(want))
	}
	for _, r := range results {
		w := want[r.Name]
		if r.Passed || r.Failure == nil {
			t.Errorf("%s: passed, but every test here is meant to fail", r.Name)
			continue
		}
		if r.Failure.Line != w.line || r.Failure.StepLine != w.stepLine {
			t.Errorf("%s: line %d, stepLine %d; want line %d, stepLine %d\n%s",
				r.Name, r.Failure.Line, r.Failure.StepLine, w.line, w.stepLine, acceptance.FormatFailure(r))
		}
	}
}

// The JSON event carries both lines, and the human header leads with the
// assertion's location while still naming the step and where it starts.
func TestFailureReportsBothLines(t *testing.T) {
	results := runPump(t, `
tests:
  - name: pump runs on a high level
    steps:
      - given: { LevelPct: 80.0 }
        scans: 1
        expect: { PumpRun: false }
`)
	var buf bytes.Buffer
	acceptance.ReportJSON(&buf, results)
	var ev struct {
		Failure struct {
			Step     int    `json:"step"`
			Line     int    `json:"line"`
			StepLine int    `json:"stepLine"`
			Detail   string `json:"detail"`
		} `json:"failure"`
	}
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Fatalf("decode %s: %v", buf.String(), err)
	}
	if f := ev.Failure; f.Step != 1 || f.Line != 7 || f.StepLine != 5 || f.Detail != "PumpRun = true, want false" {
		t.Errorf("json failure = %+v", f)
	}

	head, _, _ := strings.Cut(acceptance.FormatFailure(results[0]), "\n")
	if want := "pump_test.yaml:7 — step 1 (line 5), t="; !strings.HasPrefix(head, want) {
		t.Errorf("header %q, want prefix %q", head, want)
	}
}
