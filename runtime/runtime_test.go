package runtime_test

import (
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
