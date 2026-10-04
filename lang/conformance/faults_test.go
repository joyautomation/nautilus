package main

// Scan faults. The acceptance harness fails any YAML test whose scan
// faulted, so "this input must fault the scan" cannot be written in a
// feature's *_test.yaml. These cases build a tiny project in memory, run it
// through the same harness, and assert that the test FAILS with the fault's
// own message — which is how a fault is reported to a user running
// `naut test`.
//
// They pin documented behaviour:
//   - MUX with K outside 0..n faults the scan (docs/functions.md
//     "Selection"; st-mux-fault pins the in-range half).
//   - STRING_TO_INT / STRING_TO_REAL / STRING_TO_BOOL on text that does not
//     parse fault the scan (docs/functions.md "Type conversions";
//     fn-conversions pins the valid half).

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// runFaulting runs the ST program as the main task and returns the first failing result, or nil when every test passed.
func runFaulting(t *testing.T, tags, program, suite string) *acceptance.Result {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": {Data: []byte("name: faults\ntasks:\n  - program: p.st\n    scan: 10ms\ntags:\n" + tags + "\ndriver:\n  type: memory\n")},
		"p.st":          {Data: []byte(program)},
		"p_test.yaml":   {Data: []byte(suite)},
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no tests ran")
	}
	for i := range results {
		if !results[i].Passed {
			return &results[i]
		}
	}
	return nil
}

// wantFault asserts the run failed because the scan faulted, with msg in
// the harness's report.
func wantFault(t *testing.T, r *acceptance.Result, msg string) {
	t.Helper()
	if r == nil {
		t.Fatalf("test passed; expected the scan to fault with %q", msg)
	}
	f := r.Failure
	if !strings.Contains(f.Reason, "logic error") || !strings.Contains(f.Detail, msg) {
		t.Fatalf("expected a logic error mentioning %q, got reason %q detail %q", msg, f.Reason, f.Detail)
	}
}

const muxProgram = `PROGRAM P
VAR_EXTERNAL K : INT; Out : INT; END_VAR
Out := MUX(K, 10, 20, 30);
END_PROGRAM
`

const muxTags = "  - { name: K, role: state, init: 0 }\n  - { name: Out, role: state, init: 0 }\n"

func TestMuxOutOfRangeFaults(t *testing.T) {
	for _, tc := range []struct{ name, k, msg string }{
		{"above the last input", "3", "MUX selector 3 out of range 0..2"},
		{"negative", "-1", "MUX selector -1 out of range 0..2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runFaulting(t, muxTags, muxProgram, "tests:\n  - name: k out of range\n    given: { K: "+tc.k+" }\n    steps:\n      - scans: 1\n")
			wantFault(t, r, tc.msg)
		})
	}
	t.Run("in range does not fault", func(t *testing.T) {
		r := runFaulting(t, muxTags, muxProgram, "tests:\n  - name: k in range\n    given: { K: 2 }\n    steps:\n      - scans: 1\n        expect: { Out: 30 }\n")
		if r != nil {
			t.Fatalf("unexpected failure:\n%s", acceptance.FormatFailure(*r))
		}
	})
}

func TestStringParseFaults(t *testing.T) {
	for _, tc := range []struct{ fn, typ, bad, msg string }{
		{"STRING_TO_INT", "INT", "abc", `STRING_TO_INT: "abc" is not an integer`},
		{"STRING_TO_INT", "INT", "2.5", `STRING_TO_INT: "2.5" is not an integer`},
		{"STRING_TO_REAL", "REAL", "x1", `STRING_TO_REAL: "x1" is not a number`},
		{"STRING_TO_BOOL", "BOOL", "maybe", "STRING_TO_BOOL"},
	} {
		t.Run(tc.fn+"/"+tc.bad, func(t *testing.T) {
			init := map[string]string{"INT": "0", "REAL": "0.0", "BOOL": "false"}[tc.typ]
			tags := "  - { name: S, role: state, init: \"0\" }\n  - { name: Out, role: state, init: " + init + " }\n"
			prog := "PROGRAM P\nVAR_EXTERNAL S : STRING; Out : " + tc.typ + "; END_VAR\nOut := " + tc.fn + "(S);\nEND_PROGRAM\n"
			r := runFaulting(t, tags, prog, "tests:\n  - name: unparseable\n    given: { S: \""+tc.bad+"\" }\n    steps:\n      - scans: 1\n")
			wantFault(t, r, tc.msg)
		})
	}
}
