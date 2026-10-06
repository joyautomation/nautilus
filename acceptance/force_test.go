package acceptance_test

// `force:` and `unforce:` — the controller's own force table, driven from a
// test. The heated tank simulates its plant in an IEC task, so a forced
// LevelPct has to beat a writer that integrates the level every scan: the
// same fight a forced input wins against a field driver.

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

// heatedTankWith is the heated-tank project with its own suite swapped for
// the one given.
func heatedTankWith(t *testing.T, suite string) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	ents, err := os.ReadDir(nogo)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasSuffix(e.Name(), "_test.yaml") {
			continue
		}
		b, err := os.ReadFile(nogo + "/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		m[e.Name()] = &fstest.MapFile{Data: b}
	}
	m["force_test.yaml"] = &fstest.MapFile{Data: []byte(suite)}
	return m
}

func runHeatedTank(t *testing.T, suite string) ([]acceptance.Result, error) {
	t.Helper()
	fsys := heatedTankWith(t, suite)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunDir(fsys, proj.Runtime)
}

func TestForceHoldsAgainstPlantAndLogic(t *testing.T) {
	results, err := runHeatedTank(t, `
tests:
  - name: a forced level beats the plant model, and the logic reacts to it
    steps:
      - force: { LevelPct: 20.0 }       # below the pump's start level
        advance: 3s
        expect:
          LevelPct: 20.0                # the sim integrates; the force wins
          PumpRun: true                 # the seal-in saw the forced level
      - force: { PumpRun: false }       # an output, against the logic
        advance: 2s
        expect:
          PumpRun: false
          LevelPct: 20.0
      - unforce: [PumpRun]
        scans: 2
        expect:
          PumpRun: true                 # the logic owns it again
      - unforce: all
        advance: 5s
        expect: "LevelPct > 20.0"       # the plant model owns the level again
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("%d results; want 1", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("FAIL %s\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}

func TestForceStepMistakes(t *testing.T) {
	for _, c := range []struct{ suite, want string }{
		{"tests:\n  - name: x\n    steps:\n      - unforce: [LevelPct]\n        scans: 1\n", "not forced"},
		{"tests:\n  - name: x\n    steps:\n      - force: { Nope: 1 }\n        scans: 1\n", "undefined tag Nope"},
		{"tests:\n  - name: x\n    steps:\n      - unforce: everything\n        scans: 1\n", "`all`"},
	} {
		_, err := runHeatedTank(t, c.suite)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("suite %q: err = %v, want %q", c.suite, err, c.want)
		}
	}
}
