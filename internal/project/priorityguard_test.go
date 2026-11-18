package project

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
)

func guardProject(mainGuard, taskGuard string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: guard
tasks:
  - program: main.st
    scan: 5ms
    priority: 40
` + mainGuard + `
  - name: fast
    program: fast.st
    scan: 1ms
    priority: 30
` + taskGuard + `
`)},
		"main.st": &fstest.MapFile{Data: []byte("PROGRAM Main\nVAR x : INT; END_VAR\nx := x + 1;\nEND_PROGRAM\n")},
		"fast.st": &fstest.MapFile{Data: []byte("PROGRAM Fast\nVAR y : INT; END_VAR\ny := y + 1;\nEND_PROGRAM\n")},
	}
}

// priority-guard: reaches the runtime for the main task and for a task,
// and a task at a priority without one runs under the defaults; the
// runtime shows the bound in each task's scan.sched.
func TestPriorityGuardFromTheManifest(t *testing.T) {
	p, err := Load(guardProject(
		"    priority-guard: {overruns: 7, scan-multiple: 30}",
		"    priority-guard: {scan-multiple: 5}"), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	s := r.Stats()
	if g := s.Sched.Guard; g == nil || *g != (runtime.PriorityGuard{Overruns: 7, ScanMultiple: 30}) {
		t.Errorf("main task guard %+v, want overruns 7, scan-multiple 30", g)
	}
	if g := s.Tasks[0].Sched.Guard; g == nil || *g != (runtime.PriorityGuard{Overruns: runtime.DefaultGuardOverruns, ScanMultiple: 5}) {
		t.Errorf("task guard %+v, want the default overruns and scan-multiple 5", g)
	}

	p, err = Load(guardProject("", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = runtime.New(p.Runtime)
	want := runtime.PriorityGuard{Overruns: runtime.DefaultGuardOverruns, ScanMultiple: runtime.DefaultGuardScanMultiple}
	if g := r.Stats().Sched.Guard; g == nil || *g != want {
		t.Errorf("no priority-guard: main guard %+v, want the defaults %+v", g, want)
	}
}

// Out-of-range bounds are refused at load: below 2 periods ordinary jitter
// would trip it, and a huge, infinite or NaN multiple would make no bound.
func TestPriorityGuardManifestRefusals(t *testing.T) {
	for _, c := range []struct {
		guard, want string
	}{
		{"    priority-guard: {overruns: -1}", "overruns -1"},
		{"    priority-guard: {overruns: 100001}", "overruns 100001"},
		{"    priority-guard: {scan-multiple: 1.5}", "scan-multiple 1.5"},
		{"    priority-guard: {scan-multiple: 1e9}", "scan-multiple 1e+09"},
		{"    priority-guard: {scan-multiple: .inf}", "scan-multiple +Inf"},
		{"    priority-guard: {scan-multiple: .nan}", "scan-multiple NaN"},
	} {
		if _, err := Load(guardProject("", c.guard), ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want a refusal naming %q", c.guard, err, c.want)
		}
		if _, err := Load(guardProject(c.guard, ""), ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("main task %s: got %v, want a refusal naming %q", c.guard, err, c.want)
		}
	}
	if _, err := Load(guardProject("    priority-guard: {overruns: 1, scan-multiple: 2}", ""), ""); err != nil {
		t.Errorf("the smallest bounds must load: %v", err)
	}
}
