package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/runtime"
)

func pp(name string, init, val any) runtime.Parameter {
	return runtime.Parameter{Name: name, Init: init, Value: val, State: runtime.ParamPlantAhead}
}

func planFor(t *testing.T, files map[string]string, pend ...runtime.Parameter) *paramPlan {
	t.Helper()
	read := func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
	plan, err := planParamWriteBack(read, "nautilus.yaml", pend)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

const planManifest = `# the plant
name: x
tasks:
  - program: main.st
tag-files:
  - tags/gen.yaml
tags:
  # the setpoint an operator tunes
  - name: SP        # degrees
    role: setpoint
    init: 65
    min: 10
  - {name: Flow, role: setpoint, init: 1.5}
  - name: Mode
    role: setpoint
    init: false
  - name: Quoted
    role: setpoint
    init: "7"
  - name: Hold
    role: setpoint
    init: T#5s
  - name: Ratio
    role: setpoint
    init: 2.0
  - name: Plain
    role: state
    init: 0
`

// Each tuned init is replaced in place and nothing else of the file moves:
// comments, flow style and neighbours stay byte for byte.
func TestParamPlanEditsOnlyTheScalar(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": planManifest,
		"tags/gen.yaml": "- name: Gen\n  role: setpoint\n  init: 3\n",
		"main.st":       "PROGRAM P\nEND_PROGRAM\n",
	}
	plan := planFor(t, files, pp("SP", 65, 70.5), pp("Flow", 1.5, 2.25), pp("Mode", false, true), pp("Ratio", 2.0, 3.0), pp("Gen", 3, 4))
	if len(plan.Unwritable) != 0 {
		t.Fatalf("unwritable: %+v", plan.Unwritable)
	}
	got := map[string]string{}
	for _, f := range plan.Files {
		got[f.path] = f.run
	}
	want := planManifest
	for _, r := range [][2]string{{"init: 65\n", "init: 70.5\n"}, {"init: 1.5}", "init: 2.25}"}, {"init: false", "init: true"}, {"init: 2.0\n", "init: 3.0\n"}} {
		if !strings.Contains(want, r[0]) {
			t.Fatalf("bad fixture %q", r[0])
		}
		want = strings.Replace(want, r[0], r[1], 1)
	}
	if got["nautilus.yaml"] != want {
		t.Errorf("manifest:\n%s\nwant:\n%s", got["nautilus.yaml"], want)
	}
	if got["tags/gen.yaml"] != "- name: Gen\n  role: setpoint\n  init: 4\n" {
		t.Errorf("tag file: %q", got["tags/gen.yaml"])
	}
	if plan.Written["SP"] != 70.5 || len(plan.Lines) != 5 {
		t.Errorf("written %v lines %v", plan.Written, plan.Lines)
	}
}

// What cannot be edited in place is reported, with why, and never written.
func TestParamPlanUnwritable(t *testing.T) {
	files := map[string]string{"nautilus.yaml": planManifest, "tags/gen.yaml": "[]\n", "main.st": "x"}
	plan := planFor(t, files,
		pp("Quoted", 7, 8.0),    // a quoted scalar
		pp("Hold", 5000, 6000),  // a text literal
		pp("Nope", 1, 2),        // not written anywhere (a device profile's)
		pp("SP", 60, 70.0),      // the file's init (65) is not what runs (60)
		pp("Mode", false, true), // fine
	)
	why := map[string]string{}
	for _, u := range plan.Unwritable {
		why[u.Name] = u.Why
	}
	for name, sub := range map[string]string{
		"Quoted": "plain number or boolean", "Hold": "not a number or boolean",
		"Nope": "not written in the manifest", "SP": "repo moved",
	} {
		if !strings.Contains(why[name], sub) {
			t.Errorf("%s: %q, want %q", name, why[name], sub)
		}
	}
	if len(plan.Written) != 1 || plan.Written["Mode"] != true {
		t.Errorf("written: %v", plan.Written)
	}
}

// Names match as IEC identifiers do, and a CRLF file keeps its endings.
func TestParamPlanCaseAndCRLF(t *testing.T) {
	m := strings.ReplaceAll("name: x\ntasks:\n  - program: main.st\ntags:\n  - name: Setpt\n    role: setpoint\n    init: 1\n", "\n", "\r\n")
	plan := planFor(t, map[string]string{"nautilus.yaml": m, "main.st": "x"}, pp("SETPT", 1, 2))
	if len(plan.Files) != 1 || plan.Files[0].run != strings.Replace(m, "init: 1", "init: 2", 1) {
		t.Fatalf("%+v %+v", plan.Unwritable, plan.Files)
	}
}

// A non-ASCII comment before the scalar on its line must not shift the edit.
func TestParamPlanMultibyteLine(t *testing.T) {
	m := "name: x\ntasks:\n  - program: main.st\ntags:\n  - {name: SP, role: setpoint, desc: \"température\", init: 1}\n"
	plan := planFor(t, map[string]string{"nautilus.yaml": m, "main.st": "x"}, pp("SP", 1, 9))
	if len(plan.Files) != 1 || plan.Files[0].run != strings.Replace(m, "init: 1}", "init: 9}", 1) {
		t.Fatalf("%+v %+v", plan.Unwritable, plan.Files)
	}
}

var _ = filepath.Join
