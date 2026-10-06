package main

// Compile-time diagnostics. A program that must NOT compile cannot be a
// feature directory (the walker runs every feature's suite), so these build
// a tiny project in memory, as faults_test.go does, and assert the
// diagnostic a user running `naut test` (or `naut check`) is shown.
//
// They pin:
//   - #198: SCL's `#` local prefix is rejected in every position with one
//     message — it used to be silently dropped on an assignment target.
//   - #178: a parse error names tokens by their text, never a token-kind
//     number ("expected 79, got …"), including through the SFC header.
//   - #196: two CASE labels with one value (constants included) are an
//     error naming both.
//   - #176 / #238: writing a project constant or an enumeration member.

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// compileErr loads a one-task project (program file name decides the
// language) with an optional library, runs its trivial suite, and returns
// the load/compile error text ("" when it compiled).
func compileErr(t *testing.T, file, program, lib string) string {
	t.Helper()
	fsys := fstest.MapFS{
		"nautilus.yaml": {Data: []byte("name: diag\ntasks:\n  - program: " + file + "\n    scan: 10ms\ntags:\n  - { name: X, role: state, init: 0 }\ndriver:\n  type: memory\n")},
		file:            {Data: []byte(program)},
		"p_test.yaml":   {Data: []byte("tests:\n  - name: one scan\n    steps:\n      - scans: 1\n")},
	}
	if lib != "" {
		fsys["lib/lib.st"] = &fstest.MapFile{Data: []byte(lib)}
	}
	proj, err := project.Load(fsys, "")
	if err != nil {
		return err.Error()
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		return err.Error()
	}
	for _, r := range results {
		if !r.Passed {
			return r.Failure.Reason + ": " + r.Failure.Detail
		}
	}
	return ""
}

func wantCompileErr(t *testing.T, got, want string) {
	t.Helper()
	if got == "" {
		t.Fatalf("compiled; want an error containing %q", want)
	}
	if !strings.Contains(got, want) {
		t.Fatalf("error %q does not contain %q", got, want)
	}
}

func TestHashPrefixDiagnosed(t *testing.T) {
	const want = "the # prefix is Siemens SCL syntax; write the name without it"
	for name, body := range map[string]string{
		"target": "#X := 1;",
		"value":  "X := #X + 1;",
	} {
		t.Run(name, func(t *testing.T) {
			wantCompileErr(t, compileErr(t, "p.st", "PROGRAM P\nVAR_EXTERNAL X : INT; END_VAR\n"+body+"\nEND_PROGRAM\n", ""), want)
		})
	}
}

func TestParseErrorsNameTokens(t *testing.T) {
	number := regexp.MustCompile(`expected [0-9]+`)
	for name, c := range map[string]struct{ file, src string }{
		"stray comment close in an SFC header": {"p.sfc", "PROGRAM P\nVAR_EXTERNAL X : INT; END_VAR\nVAR\n  n : INT; *)\nEND_VAR\nSFC\n  INITIAL_STEP S:\n  END_STEP\nEND_SFC\nEND_PROGRAM\n"},
		"missing type in a VAR":                {"p.st", "PROGRAM P\nVAR n : ; END_VAR\nEND_PROGRAM\n"},
		"missing THEN":                         {"p.st", "PROGRAM P\nVAR_EXTERNAL X : INT; END_VAR\nIF X > 1 X := 2; END_IF;\nEND_PROGRAM\n"},
	} {
		t.Run(name, func(t *testing.T) {
			got := compileErr(t, c.file, c.src, "")
			if got == "" {
				t.Fatal("compiled; want a parse error")
			}
			if number.MatchString(got) {
				t.Fatalf("error leaks a token-kind number: %s", got)
			}
		})
	}
}

func TestCaseDuplicateConstantLabelDiagnosed(t *testing.T) {
	src := "PROGRAM P\nVAR_EXTERNAL X : INT; END_VAR\nVAR CONSTANT A : INT := 1; B : INT := 1; END_VAR\nCASE X OF\n  A: X := 10;\n  B: X := 20;\nEND_CASE;\nEND_PROGRAM\n"
	wantCompileErr(t, compileErr(t, "p.st", src, ""), "duplicate CASE label: B (= 1) has the same value as A (= 1)")
}

func TestConstantsAndMembersAreNotWritable(t *testing.T) {
	lib := "VAR_GLOBAL CONSTANT LIMIT : INT := 5; END_VAR\nTYPE Mode : (Idle, Run); END_TYPE\n"
	wantCompileErr(t, compileErr(t, "p.st", "PROGRAM P\nLIMIT := 6;\nEND_PROGRAM\n", lib),
		"LIMIT is a constant (VAR_GLOBAL CONSTANT) and cannot be written")
	wantCompileErr(t, compileErr(t, "p.st", "PROGRAM P\nRun := Idle;\nEND_PROGRAM\n", lib),
		"Run is a value of the enumeration Mode (Mode#Run), not a variable")
}

// The Codesys enumeration that produced #178's "expected 79" now compiles.
func TestCodesysEnumTypeCompiles(t *testing.T) {
	lib := "TYPE E_WashState : (IDLE := 0, FILL := 1, WASH := 2);\nEND_TYPE\n"
	src := "PROGRAM P\nVAR_EXTERNAL X : INT; END_VAR\nVAR s : E_WashState; END_VAR\ns := FILL;\nX := TO_INT(s);\nEND_PROGRAM\n"
	if got := compileErr(t, "p.st", src, lib); got != "" {
		t.Fatalf("did not compile: %s", got)
	}
}
