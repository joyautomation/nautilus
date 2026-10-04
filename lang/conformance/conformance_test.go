package main

// The corpus walker: every feature directory is a manifest project with an
// acceptance suite, and each one runs here under `go test`. A failure
// prints the acceptance harness's own failure text — step, virtual time,
// and the trace of what the tags did — so a red row reads the same way a
// `naut test` failure does.

import (
	"os"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

func TestConformance(t *testing.T) {
	feats, err := Features(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(feats) == 0 {
		t.Fatal("no feature directories found (a feature is a directory holding nautilus.yaml)")
	}
	for _, f := range feats {
		t.Run(f.Name, func(t *testing.T) {
			runFeature(t, f)
		})
	}
}

func runFeature(t *testing.T, f Feature) {
	t.Helper()
	fsys := os.DirFS(f.Dir)

	// Every language file must be a task in the manifest. A `<feature>.ld`
	// that nothing scans would show ✓ in the matrix while asserting
	// nothing, which is the worst kind of coverage.
	m, err := project.ReadManifest(fsys, "")
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	tasked := map[string]bool{}
	for _, task := range m.Tasks {
		tasked[task.Program] = true
	}
	for _, file := range f.LanguageFiles() {
		if !tasked[file] {
			t.Errorf("%s/%s exists but no task in nautilus.yaml runs it", f.Name, file)
		}
	}
	if len(f.LanguageFiles()) == 0 {
		t.Errorf("%s has no %s.{%s} program file", f.Name, f.Name, strings.Join(Languages, ","))
	}

	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime, acceptance.WithAlarms(proj.AlarmEngine))
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	if len(results) == 0 {
		t.Fatalf("%s: no acceptance tests (expected %s_test.yaml with at least one test)", f.Name, f.Name)
	}
	for _, r := range results {
		if r.Passed {
			t.Logf("ok   %s (%s, %d scans)", r.Name, r.Elapsed, r.Scans)
			continue
		}
		t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
	}
}

// TestMatrix pins the generator's shape on the real corpus: every feature
// appears, with at least one language and at least one test.
func TestMatrix(t *testing.T) {
	feats, err := Features(".")
	if err != nil {
		t.Fatal(err)
	}
	out := Matrix(feats)
	for _, f := range feats {
		if !strings.Contains(out, "`"+f.Name+"`") {
			t.Errorf("matrix lacks a row for %s:\n%s", f.Name, out)
		}
		if f.Tests == 0 {
			t.Errorf("%s has no tests", f.Name)
		}
		if len(f.LanguageFiles()) == 0 {
			t.Errorf("%s has no language files", f.Name)
		}
	}
	t.Log("\n" + out)
}
