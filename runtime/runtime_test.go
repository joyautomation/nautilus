package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

const prog = `PROGRAM Main
VAR_EXTERNAL
	LevelPct : REAL; TempC : REAL; ScanDtS : REAL;
	TempSP : REAL; Kp : REAL; Ki : REAL;
	PumpStartLevel : REAL; PumpStopLevel : REAL;
	PumpRun : BOOL; Heater : REAL;
END_VAR
VAR integral : REAL; err : REAL; END_VAR
IF LevelPct <= PumpStartLevel THEN PumpRun := TRUE;
ELSIF LevelPct >= PumpStopLevel THEN PumpRun := FALSE; END_IF;
err := TempSP - TempC;
integral := integral + Ki * err * ScanDtS;
integral := LIMIT(0.0, integral, 100.0);
Heater := LIMIT(0.0, Kp * err + integral, 100.0);
END_PROGRAM`

func newRT(t *testing.T, drv nio.Driver) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program: prog, Driver: drv,
		Inputs:  []string{"LevelPct", "TempC"},
		Outputs: []string{"PumpRun", "Heater"},
		DtTag:   "ScanDtS",
		Seed: nio.Values{
			"TempSP": 65.0, "Kp": 12.0, "Ki": 0.15,
			"PumpStartLevel": 40.0, "PumpStopLevel": 75.0,
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return rt
}

// The runtime binds a Driver's inputs, runs the program, and writes outputs.
func TestRuntimeDrivesOutputs(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"LevelPct": 30.0, "TempC": 55.0}) // below start, below setpoint
	rt := newRT(t, drv)

	for i := 0; i < 20; i++ {
		rt.Scan()
	}

	out, _ := drv.ReadInputs()
	if out["PumpRun"] != true {
		t.Fatalf("pump should start below the start level, got %v", out["PumpRun"])
	}
	if h, _ := out["Heater"].(float64); h <= 0 {
		t.Fatalf("heater should drive up when cold, got %v", h)
	}
	if n := rt.Stats().Count; n != 20 {
		t.Fatalf("expected 20 scans, got %d", n)
	}
}

// TagDefs declare each tag's role, seed, and HMI meta in ONE place and
// expand into the flat Options fields — same controller, one entry per tag.
func TestTagDefs(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"LevelPct": 30.0, "TempC": 55.0})
	rt, err := runtime.New(runtime.Options{
		Program: prog, Driver: drv, DtTag: "ScanDtS",
		Tags: []runtime.TagDef{
			runtime.Input("LevelPct", runtime.Desc("Tank level"), runtime.Unit("%")),
			runtime.Input("TempC", runtime.Unit("°C")),
			runtime.Setpoint("TempSP", 65.0, runtime.Unit("°C")),
			runtime.Setpoint("Kp", 12.0),
			runtime.Setpoint("Ki", 0.15),
			runtime.Setpoint("PumpStartLevel", 40.0),
			runtime.Setpoint("PumpStopLevel", 75.0),
			runtime.Output("PumpRun", runtime.Init(false), runtime.Desc("Pump run command")),
			runtime.Output("Heater", runtime.Unit("%")),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Init seeds exist before the first scan — the read-before-write guard.
	if _, err := rt.Tags().ReadGlobal("PumpRun"); err != nil {
		t.Fatalf("Init must seed the output before scan one: %v", err)
	}
	if v := rt.Tags().Real("TempSP"); v != 65.0 {
		t.Fatalf("setpoint seed missing, got %v", v)
	}
	// Meta flowed into the HMI map.
	if m := rt.Meta(); m["LevelPct"].Unit != "%" || m["PumpRun"].Desc == "" {
		t.Fatalf("tag meta not expanded: %+v", m)
	}

	for i := 0; i < 20; i++ {
		rt.Scan()
	}
	out, _ := drv.ReadInputs()
	if out["PumpRun"] != true {
		t.Fatalf("pump should start below the start level, got %v", out["PumpRun"])
	}
	if h, _ := out["Heater"].(float64); h <= 0 {
		t.Fatalf("heater should drive up when cold, got %v", h)
	}
	if rt.Stats().LogicErrors != 0 {
		t.Fatalf("scans must run clean, got %d logic errors", rt.Stats().LogicErrors)
	}
}

// Libraries compose user FUNCTION_BLOCKs ahead of the program — here an
// ST-authored FB invoked from an FBD program, the cross-language reuse
// story: author blocks once, call them from any IEC language.
func TestLibrariesUserFBFromFBD(t *testing.T) {
	lib := `FUNCTION_BLOCK Hysteresis
VAR_INPUT
  IN : REAL;
  HI : REAL;
  LO : REAL;
END_VAR
VAR_OUTPUT
  Q : BOOL;
END_VAR
IF IN >= HI THEN Q := TRUE;
ELSIF IN <= LO THEN Q := FALSE;
END_IF;
END_FUNCTION_BLOCK`
	fbdProg := `PROGRAM Main
VAR_EXTERNAL
  x : REAL;
  Out : BOOL;
END_VAR
FBD
  h1 : Hysteresis(IN := x, HI := 10.0, LO := 5.0)
  Out := h1.Q
END_FBD
END_PROGRAM`

	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program:   fbdProg,
		Libraries: []string{lib},
		Driver:    drv,
		Tags: []runtime.TagDef{
			runtime.Input("x"),
			runtime.Output("Out", runtime.Init(false)),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	step := func(x float64) bool {
		_ = drv.WriteOutputs(nio.Values{"x": x})
		rt.Scan()
		out, _ := drv.ReadInputs()
		b, _ := out["Out"].(bool)
		return b
	}
	if step(12) != true {
		t.Fatal("above HI must latch on")
	}
	if step(7) != true {
		t.Fatal("inside the band must hold state")
	}
	if step(3) != false {
		t.Fatal("below LO must drop out")
	}
	if rt.Stats().LogicErrors != 0 {
		t.Fatalf("scans must run clean, got %d logic errors", rt.Stats().LogicErrors)
	}
}

// Tasks: additional programs on their own scan rates against the shared
// tag store — the IEC resource/task model. The main task owns field I/O;
// a task computes on the store (here: a totalizer integrating a rate).
func TestTasks(t *testing.T) {
	mainProg := `PROGRAM Main
VAR_EXTERNAL RateLps : REAL; Doubled : REAL; END_VAR
Doubled := RateLps * 2.0;
END_PROGRAM`
	totalizer := `PROGRAM Totals
VAR_EXTERNAL RateLps : REAL; TotalL : REAL; TotDtS : REAL; END_VAR
TotalL := TotalL + RateLps * TotDtS;
END_PROGRAM`

	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"RateLps": 2.0})
	rt, err := runtime.New(runtime.Options{
		Program: mainProg,
		Driver:  drv,
		Tags: []runtime.TagDef{
			runtime.Input("RateLps"),
			runtime.Output("Doubled"),
			runtime.State("TotalL", 0.0),
		},
		Tasks: []runtime.Task{
			{Name: "totals", Program: totalizer, Scan: 500 * time.Millisecond, DtTag: "TotDtS"},
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	rt.Scan() // main: reads RateLps, writes Doubled
	if got := rt.Tags().Real("Doubled"); got != 4.0 {
		t.Fatalf("main task: Doubled = %v, want 4", got)
	}
	// Drive the task deterministically. First scan uses its target dt.
	if err := rt.ScanTask("totals"); err != nil {
		t.Fatal(err)
	}
	if got := rt.Tags().Real("TotalL"); got != 1.0 { // 2 L/s × 0.5 s
		t.Fatalf("task scan 1: TotalL = %v, want 1", got)
	}
	if err := rt.ScanTask("nope"); err == nil {
		t.Fatal("unknown task name must error")
	}

	st := rt.Stats()
	if len(st.Tasks) != 1 || st.Tasks[0].Name != "totals" || st.Tasks[0].Count != 1 {
		t.Fatalf("task stats missing: %+v", st.Tasks)
	}
	if st.Tasks[0].LogicErrors != 0 {
		t.Fatalf("task must scan clean: %+v", st.Tasks[0])
	}

	// A broken task program fails composition with the task named.
	_, err = runtime.New(runtime.Options{
		Program: mainProg,
		Tasks:   []runtime.Task{{Name: "bad", Program: "PROGRAM x nonsense"}},
	})
	if err == nil || !strings.Contains(err.Error(), "task bad") {
		t.Fatalf("bad task program must fail with its name, got %v", err)
	}
}

// TestDtTagSeededBeforeFirstScan is a regression test: a dt-tag used to
// come into existence only after the first scan wrote it (see the scan
// loop's tags.SetReal(dtTag, dt) after logic runs), so a snapshot taken
// between New() and the first scan — exactly what a Sparkplug birth reads —
// was missing it. Across many tasks that showed up as a spurious second
// NBIRTH one scan later, once every dt-tag finally existed. New() now seeds
// every dt-tag (main task and additional tasks) at REAL 0.0 up front, the
// same as a declared `state` tag, so the tag store's shape is stable from
// construction.
func TestDtTagSeededBeforeFirstScan(t *testing.T) {
	mainProg := `PROGRAM Main
VAR_EXTERNAL ScanDtS : REAL; Doubled : REAL; END_VAR
Doubled := ScanDtS;
END_PROGRAM`
	totalizer := `PROGRAM Totals
VAR_EXTERNAL TotDtS : REAL; END_VAR
END_PROGRAM`

	rt, err := runtime.New(runtime.Options{
		Program: mainProg,
		DtTag:   "ScanDtS",
		Tasks: []runtime.Task{
			{Name: "totals", Program: totalizer, DtTag: "TotDtS"},
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	snap := rt.Tags().Snapshot() // before ANY scan, main or task
	for _, name := range []string{"ScanDtS", "TotDtS"} {
		if _, ok := snap[name]; !ok {
			t.Fatalf("dt-tag %s must exist before the first scan", name)
		}
	}
	if got := rt.Tags().Real("ScanDtS"); got != 0.0 {
		t.Fatalf("ScanDtS must seed at 0.0, got %v", got)
	}
	if got := rt.Tags().Real("TotDtS"); got != 0.0 {
		t.Fatalf("TotDtS must seed at 0.0, got %v", got)
	}

	// An explicitly declared dt-tag (e.g. `state: {init: 0.25}`, the
	// workaround this fix makes unnecessary) still wins — auto-seeding
	// must not clobber an operator-visible initial value.
	rt2, err := runtime.New(runtime.Options{
		Program: mainProg,
		DtTag:   "ScanDtS",
		Tags: []runtime.TagDef{
			runtime.State("ScanDtS", 0.25),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := rt2.Tags().Real("ScanDtS"); got != 0.25 {
		t.Fatalf("explicit dt-tag init must not be overwritten, got %v", got)
	}
}

// Run schedules every task concurrently; scans serialize on the shared
// store (this test's value is mostly under -race).
func TestTasksRunConcurrently(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"x": 1.0})
	rt, err := runtime.New(runtime.Options{
		Program: `PROGRAM Main
VAR_EXTERNAL x : REAL; y : REAL; END_VAR
y := x + 1.0;
END_PROGRAM`,
		Driver: drv,
		Scan:   2 * time.Millisecond,
		// y is seeded: Aux reads it and may win the startup race against
		// Main's first scan, which is what creates it — the tag model's
		// read-before-first-write case, whose documented fix is a seed.
		Tags:   []runtime.TagDef{runtime.Input("x"), runtime.Output("y", runtime.Init(0.0)), runtime.State("z", 0.0)},
		Tasks: []runtime.Task{{
			Name: "aux",
			Scan: 3 * time.Millisecond,
			Program: `PROGRAM Aux
VAR_EXTERNAL y : REAL; z : REAL; END_VAR
z := z + y;
END_PROGRAM`,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	rt.Run(ctx)

	st := rt.Stats()
	if st.Count == 0 {
		t.Fatal("main task never scanned")
	}
	if len(st.Tasks) != 1 || st.Tasks[0].Count == 0 {
		t.Fatalf("aux task never scanned: %+v", st.Tasks)
	}
	if st.LogicErrors != 0 || st.Tasks[0].LogicErrors != 0 {
		t.Fatalf("scans must run clean: main=%d aux=%+v", st.LogicErrors, st.Tasks[0])
	}
}

// Ladder Diagram programs run end to end: rung text → FBD netlist → ST →
// IR, with the original .ld source as the program of record.
func TestLadderProgram(t *testing.T) {
	ladder := `PROGRAM Main
VAR_EXTERNAL
  Start : BOOL; Stop : BOOL; Run : BOOL;
END_VAR
LD
  RUNG seal
    [ Start | Run ] /Stop ( Run )
END_LD
END_PROGRAM`

	drv := nio.NewMemory()
	rt, err := runtime.New(runtime.Options{
		Program: ladder,
		Driver:  drv,
		Tags: []runtime.TagDef{
			runtime.Input("Start"),
			runtime.Input("Stop"),
			runtime.Output("Run", runtime.Init(false)),
		},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	step := func(start, stop bool) bool {
		_ = drv.WriteOutputs(nio.Values{"Start": start, "Stop": stop})
		rt.Scan()
		return rt.Tags().Bool("Run")
	}
	if step(true, false) != true {
		t.Fatal("Start must energize the seal-in")
	}
	if step(false, false) != true {
		t.Fatal("the seal must hold after Start drops")
	}
	if step(false, true) != false {
		t.Fatal("Stop must drop the rung out")
	}
	if rt.Stats().LogicErrors != 0 {
		t.Fatalf("scans must run clean: %d errors", rt.Stats().LogicErrors)
	}
	if src := rt.Program().Source(); !strings.Contains(src, "RUNG seal") {
		t.Fatal("the ORIGINAL .ld source must be the program of record")
	}
}

// Pump hysteresis: latched off above the stop level.
func TestPumpHysteresis(t *testing.T) {
	drv := nio.NewMemory()
	_ = drv.WriteOutputs(nio.Values{"LevelPct": 80.0, "TempC": 65.0}) // above stop
	rt := newRT(t, drv)
	rt.Scan()
	if out, _ := drv.ReadInputs(); out["PumpRun"] != false {
		t.Fatalf("pump should stop above the stop level, got %v", out["PumpRun"])
	}
}

// A compile error leaves New returning an error, not a broken runtime.
func TestBadProgram(t *testing.T) {
	if _, err := runtime.New(runtime.Options{Program: "PROGRAM x\nnonsense @#$\nEND_PROGRAM"}); err == nil {
		t.Fatal("expected a compile error")
	}
}

// failingDriver errors on ReadInputs to exercise the IO fault counters.
type failingDriver struct{ fail bool }

func (d *failingDriver) ReadInputs() (nio.Values, error) {
	if d.fail {
		return nil, errFail
	}
	return nio.Values{"LevelPct": 50.0, "TempC": 60.0}, nil
}
func (d *failingDriver) WriteOutputs(nio.Values) error { return nil }

var errFail = errors.New("fieldbus down")

func TestScanStatsDiagnostics(t *testing.T) {
	drv := &failingDriver{}
	rt := newRT(t, drv)
	for i := 0; i < 200; i++ {
		rt.Scan()
	}
	s := rt.Stats()
	if s.Count != 200 {
		t.Errorf("Count = %d, want 200", s.Count)
	}
	if s.TargetMs != 100 {
		t.Errorf("TargetMs = %v, want 100 (default scan)", s.TargetMs)
	}
	if s.MinMs <= 0 || s.MaxMs < s.MinMs || s.AvgMs <= 0 {
		t.Errorf("min/max/avg wrong: %v/%v/%v", s.MinMs, s.MaxMs, s.AvgMs)
	}
	if s.ExecUs <= 0 {
		t.Errorf("ExecUs = %v, want > 0", s.ExecUs)
	}
	// History is capped at 180; periods lag by the first scan.
	if len(s.Recent) != 180 || len(s.Periods) != 180 {
		t.Errorf("history lengths = %d/%d, want 180/180", len(s.Recent), len(s.Periods))
	}
	total := 0
	for _, n := range s.Histogram {
		total += n
	}
	if len(s.Histogram) != 15 || total != 200 {
		t.Errorf("histogram: %d buckets, %d samples (want 15, 200)", len(s.Histogram), total)
	}
	if !s.IOHealthy || s.IOErrors != 0 {
		t.Errorf("healthy driver misreported: healthy=%v errors=%d", s.IOHealthy, s.IOErrors)
	}

	// Fieldbus failure: counted, flagged, and recovery restores health.
	drv.fail = true
	rt.Scan()
	s = rt.Stats()
	if s.IOHealthy || s.IOErrors != 1 || s.LastIOError == "" {
		t.Errorf("failed read misreported: healthy=%v errors=%d lastErr=%q",
			s.IOHealthy, s.IOErrors, s.LastIOError)
	}
	drv.fail = false
	rt.Scan()
	if s = rt.Stats(); !s.IOHealthy {
		t.Error("recovery should restore IOHealthy")
	}
}

func TestStatsReturnsCopies(t *testing.T) {
	rt := newRT(t, nio.NewMemory())
	rt.Scan()
	a := rt.Stats()
	a.Recent[0] = -1
	a.Histogram[0] = -1
	if b := rt.Stats(); b.Recent[0] == -1 || b.Histogram[0] == -1 {
		t.Error("Stats must return copies, not aliases")
	}
}
