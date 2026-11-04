package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseCheck(t *testing.T) {
	out := `tank.ld:12:5: undeclared identifier "Hornn"
control.fbd:3:1: warning: local Level shadows the project tag Level — delete the VAR declaration
nautilus.yaml: warning: nautilus.yaml declares input "Sensor", which no program binds — remove it
nautilus.yaml: warning: the programs write "Horn", which nautilus.yaml declares no tag for — add one
x.ld:4:2: warning: Horn is driven by coils on 3 rungs [ld-multi-coil]
naut check: 3 file(s), 1 with errors, 3 warning(s)
`
	r := ParseCheck(out, 1)
	if r.Errors != 1 || r.Warnings != 4 {
		t.Fatalf("errors %d warnings %d, want 1 and 4", r.Errors, r.Warnings)
	}
	want := map[string]int{"shadowed-tag": 1, "unbound-tag": 1, "undeclared-write": 1, "ld-multi-coil": 1}
	if !reflect.DeepEqual(r.WarningIDs, want) {
		t.Errorf("ids %v, want %v", r.WarningIDs, want)
	}
	if d := r.Diagnostics[0]; d.File != "tank.ld" || d.Line != 12 || d.Severity != "error" {
		t.Errorf("first diagnostic %+v", d)
	}

	clean := ParseCheck("naut check: 2 file(s), 0 with errors\n", 0)
	if clean.Errors != 0 || clean.Warnings != 0 || clean.WarningIDs != nil {
		t.Errorf("clean output parsed as %+v", clean)
	}

	// A failure with no positioned line still counts.
	broken := ParseCheck("naut check: yaml: line 3: mapping values are not allowed\n", 1)
	if broken.Errors != 1 {
		t.Errorf("unpositioned failure: errors = %d, want 1", broken.Errors)
	}
}

func TestWarningID(t *testing.T) {
	cases := map[string]string{
		"ld-multi-coil: Horn has three writers":              "ld-multi-coil",
		"Horn has three writers [ld.multi-coil]":             "ld.multi-coil",
		"empty chart — no PROGRAM yet; open it as a diagram": "empty-chart",
		`rung "fill" has 9 elements in series — split it`:    "rung-has-elements-in-series",
	}
	for msg, want := range cases {
		if got := WarningID(msg); got != want {
			t.Errorf("WarningID(%q) = %q, want %q", msg, got, want)
		}
	}
}

func TestParseTestJSON(t *testing.T) {
	out := `{"suite":"h_test.yaml","name":"a","line":3,"passed":true,"scans":5,"elapsedMs":500}
{"suite":"h_test.yaml","name":"b","line":9,"passed":false,"scans":5,"failure":{"step":1},"elapsedMs":500}
not json
`
	r := ParseTestJSON(out)
	if r.Total != 2 || r.Passed != 1 || !reflect.DeepEqual(r.Failures, []string{"b"}) {
		t.Errorf("got %+v", r)
	}
}

// buildNaut builds the naut CLI once for the end-to-end scoring tests.
func buildNaut(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds naut; skipped with -short")
	}
	bin := filepath.Join(t.TempDir(), "naut")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/joyautomation/nautilus/cmd/naut")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build naut: %v\n%s", err, out)
	}
	return bin
}

// The scorer, end to end: every reference solution is Good, and a known
// mistake in each part of the verdict is caught.
func TestScoreReferencesAndMutants(t *testing.T) {
	naut := buildNaut(t)
	s := &Scorer{Naut: naut}
	tasks, err := LoadTasks("tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		sc, err := s.ScoreProject(filepath.Join(task.Dir, "reference"), task)
		if err != nil {
			t.Fatal(err)
		}
		if !sc.Verdict.Good {
			t.Errorf("%s reference: %v (check %+v, hidden %+v)", task.ID, sc.Verdict.Reasons, sc.Check, sc.Hidden)
		}
	}

	byID := map[string]*Task{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	mutants := []struct {
		name, task, file, old, new string
		want                       Verdict
	}{
		{"motor without its seal-in", "motor-seal-in", "motor.ld",
			"[ StartPB | Motor_Run ] StopPB", "StartPB StopPB",
			Verdict{Clean: true, Structured: true}},
		{"tank with a 3 s high-high delay", "tank-fill", "tank.ld",
			"T#2S", "T#3S",
			Verdict{Clean: true, Structured: true}},
		{"pid acting the wrong way", "analog-pid", "heater.fbd",
			"DIRECT := FALSE", "DIRECT := TRUE",
			Verdict{Clean: true, Structured: true}},
		{"traffic light with a 25 s green", "traffic-light", "traffic.ld",
			"LT(cycTmr.ET, T#20S)", "LT(cycTmr.ET, T#25S)",
			Verdict{Clean: true, Structured: true}},
		{"conveyor jam that never latches", "conveyor-fbd", "conveyor.fbd",
			"JamAlm := AND(OR(jamTmr.Q, JamAlm), NOT resetOk)", "JamAlm := jamTmr.Q",
			Verdict{Clean: true, Structured: true}},
		{"totalizer counting levels, not edges", "totalizer-st", "dosing.st",
			"IF pulseEdge.Q THEN", "IF FT201_Pulse THEN",
			Verdict{Clean: true, Structured: true}},
		{"batch whose transitions test a step number", "batch-sfc", "batch.sfc",
			"TRANSITION t_start FROM Idle TO Fill := StartPB;", "TRANSITION t_start FROM Idle TO Fill := StartPB OR BatchCount = 99;\n  END_TRANSITION\n  TRANSITION t_odd FROM Idle TO Fill := BatchCount = 98;",
			Verdict{Correct: true, Clean: true}},
		{"motor with a check warning", "motor-seal-in", "nautilus.yaml",
			"  - program: motor.ld", "  - name: motor\n    program: motor.ld",
			Verdict{Correct: true, Structured: true}},
		{"motor written in ST", "motor-seal-in", "nautilus.yaml",
			"program: motor.ld", "program: motor.st",
			Verdict{}},
	}
	for _, m := range mutants {
		task := byID[m.task]
		dir := t.TempDir()
		if err := copyProject(filepath.Join(task.Dir, "reference"), dir); err != nil {
			t.Fatal(err)
		}
		if m.name == "motor written in ST" {
			st := "PROGRAM Motor\nIF OL_Trip THEN Motor_Fault := TRUE; END_IF;\nIF ResetPB AND NOT OL_Trip THEN Motor_Fault := FALSE; END_IF;\n" +
				"Motor_Run := (StartPB OR Motor_Run) AND StopPB AND NOT OL_Trip AND NOT Motor_Fault;\nEND_PROGRAM\n"
			if err := os.WriteFile(filepath.Join(dir, "motor.st"), []byte(st), 0o644); err != nil {
				t.Fatal(err)
			}
			os.Remove(filepath.Join(dir, "motor.ld"))
		}
		p := filepath.Join(dir, m.file)
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), m.old) {
			t.Fatalf("%s: %q not in %s", m.name, m.old, m.file)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(raw), m.old, m.new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		sc, err := s.ScoreProject(dir, task)
		if err != nil {
			t.Fatal(err)
		}
		got := sc.Verdict
		got.Reasons = nil
		if m.name == "motor written in ST" {
			// Correct behaviour, wrong language: only the language fails it.
			if got.Correct || got.Good || len(sc.MissingLanguages) != 1 || sc.Hidden.Passed != sc.Hidden.Total {
				t.Errorf("%s: %+v missing %v hidden %+v", m.name, sc.Verdict, sc.MissingLanguages, sc.Hidden)
			}
			continue
		}
		if !reflect.DeepEqual(got, m.want) {
			t.Errorf("%s: got %+v, want %+v (reasons %v)", m.name, got, m.want, sc.Verdict.Reasons)
		}
	}
}
