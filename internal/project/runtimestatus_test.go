package project

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// #850: the `type: runtime` status driver.

func statusBinds(specs ...string) []RuntimeStatusConfig {
	var out []RuntimeStatusConfig
	for _, s := range specs {
		f := strings.Fields(s) // "name task status"
		out = append(out, RuntimeStatusConfig{Name: f[0], Task: f[1], Status: f[2]})
	}
	return out
}

// Every status reads its field of the task it names, main from the top of
// ScanStats and the others from Tasks; a task the stats lack reads 0 and is
// reported missing.
func TestRuntimeStatusValues(t *testing.T) {
	s := runtime.ScanStats{LogicErrors: 7, Faulting: true}
	s.Sched.Demoted = true
	s.Lateness.Late, s.Lateness.Overruns = 3, 2
	fast := runtime.TaskStats{Name: "Fast", LogicErrors: 1}
	fast.Lateness.Late, fast.Lateness.Overruns = 11, 13
	s.Tasks = []runtime.TaskStats{fast}

	vals, missing := statusValues(statusBinds(
		"MD main demoted", "MF main faulting", "ME main logic-errors", "ML main late", "MO main overruns",
		"FD fast demoted", "FF fast faulting", "FE fast logic-errors", "FL fast late", "FO fast overruns",
		"XL gone late",
	), s)
	want := nio.Values{
		"MD": true, "MF": true, "ME": int64(7), "ML": int64(3), "MO": int64(2),
		"FD": false, "FF": false, "FE": int64(1), "FL": int64(11), "FO": int64(13),
		"XL": int64(0),
	}
	for k, w := range want {
		if vals[k] != w {
			t.Errorf("%s = %#v, want %#v", k, vals[k], w)
		}
	}
	if len(missing) != 1 || !missing["XL"] {
		t.Errorf("missing = %v, want only XL (task gone)", missing)
	}
}

// From construction, before any stats: every binding is delivered, false
// or 0, and Good; a scan never sees it undelivered.
func TestRuntimeStatusDeliversBeforeBinding(t *testing.T) {
	d, err := newRuntimeStatus(DriverConfig{Type: "runtime", Tags: statusBinds("A main demoted", "B main late")})
	if err != nil {
		t.Fatal(err)
	}
	v, err := d.ReadInputs()
	if err != nil || v["A"] != false || v["B"] != int64(0) || len(v) != 2 {
		t.Fatalf("before binding: %v, %v; want A=false B=0", v, err)
	}
	if q := d.Quality(); q["A"] != nio.Good || q["B"] != nio.Good {
		t.Errorf("quality %v, want Good", q)
	}
	if d.InputType("a") != "BOOL" || d.InputType("B") != "UDINT" {
		t.Errorf("InputType A=%q B=%q, want BOOL and UDINT", d.InputType("a"), d.InputType("B"))
	}
	if err := d.WriteOutputs(nio.Values{"A": true}); err == nil {
		t.Error("a write to the status driver was accepted: it has no outputs")
	}
	// Started but never bound (naut check, naut test): still delivered.
	d.Start(context.Background())
	d.Stop()
	if v, _ := d.ReadInputs(); v["A"] != false {
		t.Errorf("started unbound: %v", v)
	}
}

func TestRuntimeStatusConfigRefusals(t *testing.T) {
	for _, c := range []struct {
		binds []RuntimeStatusConfig
		want  string
	}{
		{nil, "tags: is empty"},
		{statusBinds("A main sleepy"), `status "sleepy"`},
		{[]RuntimeStatusConfig{{Name: "A", Status: "late"}}, "task is required"},
		{[]RuntimeStatusConfig{{Task: "main", Status: "late"}}, "name is required"},
		{statusBinds("A main late", "a main overruns"), "a is bound twice"},
	} {
		if _, err := newRuntimeStatus(DriverConfig{Type: "runtime", Tags: c.binds}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.binds, err, c.want)
		}
	}
}

const statusMain = `PROGRAM Main
VAR_EXTERNAL Break : BOOL; END_VAR
VAR y : INT; END_VAR
IF Break THEN y := MUX(5, 1, 2); END_IF;
END_PROGRAM
`

func statusProject(drivers string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`name: health
tasks:
  - program: main.st
    scan: 5ms
  - program: fast.st
    scan: 5ms
tags:
  - {name: Break, role: setpoint, type: BOOL, init: false}
  - {name: BreakFast, role: setpoint, type: BOOL, init: false}
  - {name: Fast__Faulting, role: input, type: BOOL}
  - {name: Main__Faulting, role: input, type: BOOL}
  - {name: Main__LogicErrors, role: input, type: UDINT}
  - {name: Fast__Overruns, role: input, type: UDINT}
` + drivers)},
		"main.st": &fstest.MapFile{Data: []byte(statusMain)},
		"fast.st": &fstest.MapFile{Data: []byte("PROGRAM Fast\nVAR_EXTERNAL BreakFast : BOOL; END_VAR\nVAR z : INT; END_VAR\nIF BreakFast THEN z := MUX(5, 1, 2); END_IF;\nEND_PROGRAM\n")},
	}
}

const statusDrivers = `driver:
    name: health
    type: runtime
    scan-rate: 10ms
    tags:
      - {name: Main__Faulting, task: main, status: faulting}
      - {name: Main__LogicErrors, task: main, status: logic-errors}
      - {name: Fast__Overruns, task: fast, status: overruns}
      - {name: Fast__Faulting, task: fast, status: faulting}
`

// Load refuses a binding to a task the project lacks (an unnamed task is
// its program's basename, so "fast" is known); a lone status driver keeps
// the memory loopback a project with no driver has; a drivers: set of
// status drivers alone has no I/O and is refused.
func TestRuntimeStatusLoadChecks(t *testing.T) {
	p, err := Load(statusProject(statusDrivers), "")
	if err != nil {
		t.Fatalf("the good project: %v", err)
	}
	d, ok := p.Runtime.Driver.(*statusOverMemory)
	if !ok {
		t.Fatalf("a lone status driver is %T, want it over the memory loopback", p.Runtime.Driver)
	}
	if err := d.WriteOutputs(nio.Values{"Out": 3.0}); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.ReadInputs(); v["Out"] != 3.0 || v["Main__Faulting"] != false {
		t.Errorf("loopback + status: %v, want Out=3 read back beside Main__Faulting=false", v)
	}
	bad := strings.Replace(statusDrivers, "task: fast,", "task: slow,", 1)
	if _, err := Load(statusProject(bad), ""); err == nil || !strings.Contains(err.Error(), `task "slow"`) || !strings.Contains(err.Error(), "main, fast") {
		t.Errorf("an unknown task: %v", err)
	}
	only := `drivers:
  - {name: a, type: runtime, tags: [{name: Main__Faulting, task: main, status: faulting}]}
  - {name: b, type: runtime, tags: [{name: Main__LogicErrors, task: main, status: logic-errors}]}
`
	if _, err := Load(statusProject(only), ""); err == nil || !strings.Contains(err.Error(), "has no I/O") {
		t.Errorf("status drivers alone in a set: %v", err)
	}
}

// field is a stand-in field driver that owns one input.
type field struct{ *nio.Memory }

func (field) InputNames() []string  { return []string{"Level"} }
func (field) OutputNames() []string { return nil }

// In a drivers: set the runtime binds the status driver among the
// children, and the set's checks pass it.
func TestRuntimeStatusInADriverSet(t *testing.T) {
	rs, err := newRuntimeStatus(DriverConfig{Type: "runtime", ScanRate: Duration(10 * time.Millisecond), Tags: statusBinds("F main faulting")})
	if err != nil {
		t.Fatal(err)
	}
	m, err := nio.NewMulti(nio.NamedDriver{Name: "field", Driver: field{nio.NewMemory()}}, nio.NamedDriver{Name: "health", Driver: rs})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := pairRuntimeStatus(m, []string{"main"}); err != nil || d != m {
		t.Fatalf("pair: %v %v, want the set unchanged", d, err)
	}
	rt, err := runtime.New(runtime.Options{Program: statusMain, Scan: 5 * time.Millisecond, Driver: m,
		Tags: []runtime.TagDef{runtime.State("Break", true), runtime.Input("F")}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; m.Stop() })
	waitFor(t, "F TRUE: the set's status driver bound and polling", func() bool { return rt.Tags().Bool("F") })
}

// End to end through a running runtime: Run binds the lone driver (over
// the memory loopback), and the main task's logic fault episode reaches
// its tags and clears again.
func TestRuntimeStatusThroughRun(t *testing.T) {
	p, err := Load(statusProject(statusDrivers), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(interface {
		nio.Driver
		Start(context.Context)
		Stop()
	})
	ctx, cancel := context.WithCancel(context.Background())
	drv.Start(ctx)
	done := make(chan struct{})
	go func() { rt.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; drv.Stop() })

	tags := rt.Tags()
	count := func(name string) int64 {
		v, err := tags.ReadGlobal(name)
		if err != nil {
			return -1
		}
		return v.I
	}
	waitFor(t, "a few clean scans", func() bool { return rt.Stats().Count > 5 })
	if tags.Bool("Main__Faulting") || count("Main__LogicErrors") != 0 || count("Fast__Overruns") != 0 {
		t.Fatalf("clean: Faulting %v, LogicErrors %d, Fast__Overruns %d", tags.Bool("Main__Faulting"), count("Main__LogicErrors"), count("Fast__Overruns"))
	}

	tags.SetBool("Break", true)
	waitFor(t, "Main__Faulting TRUE while the main task faults", func() bool { return tags.Bool("Main__Faulting") })
	waitFor(t, "Main__LogicErrors counting", func() bool { return count("Main__LogicErrors") > 0 })

	tags.SetBool("Break", false)
	waitFor(t, "Main__Faulting FALSE once the scans are clean", func() bool { return !tags.Bool("Main__Faulting") })
	if n := count("Main__LogicErrors"); n <= 0 {
		t.Errorf("Main__LogicErrors = %d after the episode, want the count kept", n)
	}

	// Another task's episode reaches its own tag, not main's.
	tags.SetBool("BreakFast", true)
	waitFor(t, "Fast__Faulting TRUE while the fast task faults", func() bool { return tags.Bool("Fast__Faulting") })
	if tags.Bool("Main__Faulting") {
		t.Error("Main__Faulting TRUE for the fast task's fault")
	}
	tags.SetBool("BreakFast", false)
	waitFor(t, "Fast__Faulting FALSE once its scans are clean", func() bool { return !tags.Bool("Fast__Faulting") })
}
