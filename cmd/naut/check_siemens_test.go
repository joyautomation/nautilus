package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

const siemensCheckManifest = `name: s7
tasks:
  - program: main.st
    scan: 100ms
tags:
  - {name: Level, role: input, type: REAL}
target:
  siemens:
    agent: AGENT
    station:
      cpu: {name: PLC_1, order: 6ES7 510-1DK03-0AB0, firmware: V3.1, address: 192.0.2.10, subnet-mask: 255.255.255.0, profinet-name: NAME}
`

const siemensCheckProgram = "PROGRAM main\nVAR x : REAL; END_VAR\nx := Level;\nEND_PROGRAM\n"

// TestCheckReportsSiemensStationProblems: an unfinished station loads (so
// the rest of check still runs) and each problem is an error with its
// YAML path. A refused agent URL never echoes its credentials.
func TestCheckReportsSiemensStationProblems(t *testing.T) {
	m := strings.NewReplacer("AGENT", "https://u:hunter2@tiad:8190", "NAME", "PLC_1").Replace(siemensCheckManifest)
	out, code := checkIn(t, map[string]string{"nautilus.yaml": m, "main.st": siemensCheckProgram})
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"error: target.siemens.agent: carries user info",
		"error: target.siemens.station.cpu.profinet-name:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Fatalf("the password was echoed:\n%s", out)
	}
}

func TestCheckPassesAGoodSiemensStation(t *testing.T) {
	m := strings.NewReplacer("AGENT", "http://127.0.0.1:8190", "NAME", "plc-1").Replace(siemensCheckManifest)
	out, code := checkIn(t, map[string]string{"nautilus.yaml": m, "main.st": siemensCheckProgram})
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

var goodSiemensManifest = strings.NewReplacer("AGENT", "http://127.0.0.1:8190", "NAME", "plc-1").Replace(siemensCheckManifest)

// A construct the SCL writer refuses (VAR CONSTANT, on line 2).
const siemensRefusedProgram = "PROGRAM main\nVAR CONSTANT k : REAL := 2.0; END_VAR\nVAR x : REAL; END_VAR\nx := Level * k;\nEND_PROGRAM\n"

// TestCheckSiemensTargetRunsTheWritersRules: a project with a
// target.siemens section is checked against the writer without a flag,
// and the writer's refusal is an error at the source line, with its rule.
func TestCheckSiemensTargetRunsTheWritersRules(t *testing.T) {
	out, code := checkIn(t, map[string]string{"nautilus.yaml": goodSiemensManifest, "main.st": siemensRefusedProgram})
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out, "main.st:2:1: siemens target: VAR CONSTANT is not in the first subset [siemens/var]") {
		t.Fatalf("no writer diagnostic at main.st:2:\n%s", out)
	}
}

// TestCheckSiemensTargetReportsProjectRules: a rule with no source line
// (the first subset writes one task) is reported against the manifest
// check reads, here one named with -m.
func TestCheckSiemensTargetReportsProjectRules(t *testing.T) {
	m := strings.Replace(goodSiemensManifest, "    scan: 100ms\n", "    scan: 100ms\n  - program: other.st\n    scan: 50ms\n", 1)
	dir := writeProject(t, map[string]string{
		"s7.yaml":  m,
		"main.st":  siemensCheckProgram,
		"other.st": "PROGRAM other\nVAR y : REAL; END_VAR\ny := Level;\nEND_PROGRAM\n",
	})
	out, code := capture(t, func() int { return runCheck([]string{"-m", "s7.yaml", dir}) })
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if !strings.Contains(out, "s7.yaml: siemens target: 2 tasks:") || !strings.Contains(out, "[siemens/task]") {
		t.Fatalf("no task diagnostic against s7.yaml:\n%s", out)
	}
}

// TestCheckTargetSiemensFlag: --target siemens checks a project that
// declares no target, and without the flag that project is not checked
// against the writer.
func TestCheckTargetSiemensFlag(t *testing.T) {
	m := "name: s7\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n  - {name: Level, role: input, type: REAL}\n"
	dir := writeProject(t, map[string]string{"nautilus.yaml": m, "main.st": siemensRefusedProgram})
	out, code := capture(t, func() int { return runCheck([]string{dir}) })
	if code != 0 {
		t.Fatalf("without the flag: exit %d, want 0:\n%s", code, out)
	}
	out, code = capture(t, func() int { return runCheck([]string{"--target", "siemens", dir}) })
	if code != 1 || !strings.Contains(out, "[siemens/var]") {
		t.Fatalf("with --target siemens: exit %d, want 1 with the writer's rule:\n%s", code, out)
	}
}

// TestCheckTargetSiemensNeedsAProject: the writer works on a whole
// project, so the flag on a lone file is a usage error naming the fix.
func TestCheckTargetSiemensNeedsAProject(t *testing.T) {
	dir := writeProject(t, map[string]string{"main.st": siemensCheckProgram})
	code := runCheck([]string{"--target", "siemens", filepath.Join(dir, "main.st")})
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

// TestSiemensWriterInput: the manifest reaches the writer as the runtime
// reads it: task names and the default scan, one program per file, tag
// types from type: or init:, real seeds as real literals, and retain
// exactly where the runtime keeps a tag.
func TestSiemensWriterInput(t *testing.T) {
	m := `name: s7
retain: {}
tasks:
  - program: main.st
  - program: pump.st
tags:
  - {name: Level, role: input, type: REAL}
  - {name: SP, role: setpoint, type: REAL, init: 5}
  - {name: Starts, role: state, type: DINT, retain: true}
  - {name: Run, role: state, init: true}
  - {name: Count, role: state, type: DINT, init: 3}
  - {name: Delay, role: state, type: TIME, init: 5000}
  - {name: Label, role: state, type: STRING, init: "a$N'b"}
`
	dir := writeProject(t, map[string]string{
		"nautilus.yaml": m,
		"main.st":       siemensCheckProgram,
		"pump.st":       "PROGRAM pump\nVAR y : REAL; END_VAR\ny := Level;\nEND_PROGRAM\n",
	})
	in, err := siemensWriterInput(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Programs) != 2 || in.Programs[1].Path != filepath.Join(dir, "pump.st") {
		t.Fatalf("programs: %+v", in.Programs)
	}
	// The first task is always runtime.MainTaskName; a later unnamed one
	// takes its file's base name, as project.Load names it.
	if len(in.Tasks) != 2 || in.Tasks[0].Name != "main" || in.Tasks[1].Name != "pump" ||
		in.Tasks[0].Program != "main" || in.Tasks[1].Program != "pump" || in.Tasks[0].Interval != 100*time.Millisecond {
		t.Fatalf("tasks: %+v", in.Tasks)
	}
	got := map[string]swriter.Global{}
	for _, g := range in.Globals {
		got[g.Name] = g
	}
	want := map[string]swriter.Global{
		"Level":  {Name: "Level", Type: "REAL"},
		"SP":     {Name: "SP", Type: "REAL", Init: "5.0", Retain: true},
		"Starts": {Name: "Starts", Type: "DINT", Retain: true},
		"Run":    {Name: "Run", Type: "BOOL", Init: "TRUE"},
		"Count":  {Name: "Count", Type: "DINT", Init: "3"},
		"Delay":  {Name: "Delay", Type: "TIME", Init: "T#5000ms"},
		"Label":  {Name: "Label", Type: "STRING", Init: "'a$$N$'b'"},
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %+v, want %+v", name, got[name], w)
		}
	}

	// Without a retain: section the runtime keeps nothing, setpoints
	// included.
	m2 := "name: s7\ntasks:\n  - program: main.st\n    scan: 20ms\ntags:\n  - {name: Level, role: input, type: REAL}\n  - {name: SP, role: setpoint, type: REAL, init: 5.5}\n"
	dir2 := writeProject(t, map[string]string{"nautilus.yaml": m2, "main.st": siemensCheckProgram})
	in2, err := siemensWriterInput(dir2, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range in2.Globals {
		if g.Retain {
			t.Errorf("%s retained without a retain: section", g.Name)
		}
		if g.Name == "SP" && g.Init != "5.5" {
			t.Errorf("SP init %q, want 5.5", g.Init)
		}
	}
	if in2.Tasks[0].Interval != 20*time.Millisecond {
		t.Errorf("interval %s, want 20ms", in2.Tasks[0].Interval)
	}

	// A fraction cannot seed an integer tag.
	m3 := "name: s7\ntasks:\n  - program: main.st\ntags:\n  - {name: Level, role: input, type: REAL}\n  - {name: N, role: state, type: DINT, init: 2.5}\n"
	dir3 := writeProject(t, map[string]string{"nautilus.yaml": m3, "main.st": siemensCheckProgram})
	if _, err := siemensWriterInput(dir3, ""); err == nil || !strings.Contains(err.Error(), "tag N: init 2.5 is not a whole number") {
		t.Errorf("DINT init 2.5: err %v", err)
	}
}

// TestSiemensWriterInputRefusesWhatTheRuntimeRefuses: two tasks running
// one PROGRAM, and a later task named like the main task, are refused
// with the runtime's own words, so check never passes a project naut run
// would not start.
func TestSiemensWriterInputRefusesWhatTheRuntimeRefuses(t *testing.T) {
	head := "name: s7\ntags:\n  - {name: Level, role: input, type: REAL}\ntasks:\n  - program: main.st\n"
	for name, tc := range map[string]struct{ tasks, want string }{
		"same program": {"  - program: main.st\n    name: second\n", "task second: PROGRAM main collides with task main"},
		"main name":    {"  - program: pump.st\n    name: main\n", `task name "main" is reserved for the main task`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeProject(t, map[string]string{
				"nautilus.yaml": head + tc.tasks,
				"main.st":       siemensCheckProgram,
				"pump.st":       "PROGRAM pump\nVAR y : REAL; END_VAR\ny := Level;\nEND_PROGRAM\n",
			})
			if _, err := siemensWriterInput(dir, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}
