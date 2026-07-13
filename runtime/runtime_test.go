package runtime_test

import (
	"errors"
	"testing"

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
