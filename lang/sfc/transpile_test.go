package sfc

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// ─── test harness ────────────────────────────────────────────────────────────

// clockHost is an ir.Host backed by a tag map with an explicitly-drivable
// clock, so timer-based tests (mixT, Step.T) advance NowMs by hand.
type clockHost struct {
	vals map[string]ir.Value
	now  int64
}

func (h *clockHost) ReadGlobal(name string) (ir.Value, error) {
	if v, ok := h.vals[name]; ok {
		return v, nil
	}
	return ir.Value{}, nil
}
func (h *clockHost) WriteGlobal(name string, v ir.Value) error { h.vals[name] = v; return nil }
func (h *clockHost) NowMs() int64                              { return h.now }

func mustCompile(t *testing.T, src string) *ir.Program {
	t.Helper()
	prog, err := Compile(src)
	if err != nil {
		st, terr := Transpile(src)
		if terr != nil {
			st = "TRANSPILE ERROR: " + terr.Error()
		}
		t.Fatalf("compile: %v\n--- transpiled ST ---\n%s", err, st)
	}
	return prog
}

// slotIdx finds a slot index by case-insensitive name.
func slotIdx(prog *ir.Program, name string) int {
	if i, ok := prog.SlotIndex[name]; ok {
		return i
	}
	for i, s := range prog.Slots {
		if strings.EqualFold(s.Name, name) {
			return i
		}
	}
	return -1
}

func stepActive(t *testing.T, prog *ir.Program, frame *ir.Frame, step string) bool {
	t.Helper()
	i := slotIdx(prog, "_S_"+step+"_X")
	if i < 0 {
		t.Fatalf("no activity slot for step %q", step)
	}
	return frame.Slots[i].B
}

// ─── the §1.2 worked example ─────────────────────────────────────────────────

const tankBatchSrc = `PROGRAM TankBatch
VAR_EXTERNAL
  Start     : BOOL;
  Abort     : BOOL;
  Level     : REAL;
  TempC     : REAL;
  FillSP    : REAL;
  EmptySP   : REAL;
  HeatSP    : REAL;
  FillValve : BOOL;
  DrainValve: BOOL;
  Heater    : BOOL;
  Mixer     : BOOL;
  RunLamp   : BOOL;
  AbortLamp : BOOL;
  BatchCount: INT;
END_VAR
VAR
  mixT : TON;
END_VAR
SFC

  INITIAL_STEP Idle:
    N  RunLamp;
    R  AbortLamp;
  END_STEP

  STEP Fill:
    N  RunLamp;
    N  FillValve;
  END_STEP

  STEP Heat:
    N  RunLamp;
    N  Heater;
  END_STEP

  STEP Mix:
    N  RunLamp;
    N  Stir;
  END_STEP

  STEP Drain:
    N   RunLamp;
    N   DrainValve;
    P1  CountBatch;
  END_STEP

  TRANSITION t_start FROM Idle TO Fill := Start AND NOT Abort;
  END_TRANSITION

  TRANSITION t_abort FROM Fill TO Idle := Abort;
  END_TRANSITION
  TRANSITION t_full  FROM Fill TO (Heat, Mix) := Level >= FillSP;
  END_TRANSITION

  TRANSITION t_done FROM (Heat, Mix) TO Drain := (TempC >= HeatSP) AND mixT.Q;
  END_TRANSITION

  TRANSITION t_empty FROM Drain TO Idle := Level <= EmptySP;
  END_TRANSITION

  ACTION Stir:
    Mixer := Mix.X;
    mixT(IN := Mix.X, PT := T#30S);
  END_ACTION

  ACTION CountBatch:
    BatchCount := BatchCount + 1;
  END_ACTION

  ACTION HoldAbort:
  END_ACTION

END_SFC
END_PROGRAM`

// TestTranspileShape asserts the generated ST parses + lowers and structurally
// matches the §3.1 excerpt (slots, guards, wrappers) — key lines, not bytes.
func TestTranspileShape(t *testing.T) {
	stSrc, err := Transpile(tankBatchSrc)
	if err != nil {
		t.Fatalf("transpile: %v", err)
	}
	prog, err := st.Parse(stSrc)
	if err != nil {
		t.Fatalf("generated ST does not parse: %v\n%s", err, stSrc)
	}
	if _, err := st.Lower(prog); err != nil {
		t.Fatalf("generated ST does not lower: %v\n%s", err, stSrc)
	}
	want := []string{
		"_S_Idle_X : BOOL := TRUE;",                                            // cold-start initial step
		"_S_Fill_X : BOOL;",                                                    // other steps default FALSE
		"_en_t_full := _S_Fill_X AND (Level >= FillSP)",                        // enabled = source .X AND cond
		"_f_t_full := _en_t_full AND NOT (_f_t_abort);",                        // firing-based alt guard
		"_en_t_done := _S_Heat_X AND _S_Mix_X AND ((TempC >= HeatSP) AND mixT.Q)", // convergence: both sources ANDed
		"IF _S_Idle_X OR _S_Fill_X OR _S_Heat_X OR _S_Mix_X OR _S_Drain_X THEN RunLamp := TRUE; END_IF;", // N OR-combine, held while active
		"IF _act_FillValve_lvl AND NOT (_S_Fill_X) THEN FillValve := FALSE; END_IF;",                   // N final scan
		"IF (_S_Idle_X AND NOT _S_Idle_prev) THEN AbortLamp := FALSE; END_IF;",                          // R acts once, on activation
		"_act_Stir_prev THEN",                     // final-scan wrapper
		"Mixer := _S_Mix_X;",                      // body .X rewrite
		"mixT(IN := _S_Mix_X, PT := T#30S);",      // body .X rewrite in FB call
		"_S_Drain_X AND NOT _S_Drain_prev", // P1 pulse guard, no final scan
		"BatchCount := BatchCount + 1;",
	}
	for _, w := range want {
		if !strings.Contains(stSrc, w) {
			t.Errorf("generated ST missing %q\n--- ST ---\n%s", w, stSrc)
		}
	}
	// A P1 body must NOT get a final-scan memory.
	if strings.Contains(stSrc, "_act_CountBatch_prev") {
		t.Errorf("P1 body action must not have a final-scan memory:\n%s", stSrc)
	}
}

// TestScanTrace is the acceptance oracle: every row of the §9 trace.
func TestScanTrace(t *testing.T) {
	prog := mustCompile(t, tankBatchSrc)
	h := &clockHost{vals: map[string]ir.Value{
		"FillSP":  ir.RealVal(100),
		"HeatSP":  ir.RealVal(80),
		"EmptySP": ir.RealVal(5),
	}}
	frame := ir.NewFrame(prog)

	mixT := slotIdx(prog, "mixT")
	mixQ := func() bool { return frame.Slots[mixT].FB.Slots[2].B }
	mixET := func() int64 { return frame.Slots[mixT].FB.Slots[3].I }
	g := func(n string) ir.Value { return h.vals[n] }
	boolG := func(n string) bool { return h.vals[n].B }

	scan := func(n int, inputs map[string]ir.Value) {
		t.Helper()
		for k, v := range inputs {
			h.vals[k] = v
		}
		h.now = 1000 + int64(n)*20000
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatalf("scan %d run: %v", n, err)
		}
	}
	activeSet := func() string {
		var out []string
		for _, s := range []string{"Idle", "Fill", "Heat", "Mix", "Drain"} {
			if stepActive(t, prog, frame, s) {
				out = append(out, s)
			}
		}
		return strings.Join(out, ",")
	}
	assertActive := func(n int, want string) {
		t.Helper()
		if got := activeSet(); got != want {
			t.Errorf("scan %d: active steps = %q, want %q", n, got, want)
		}
	}

	// Scan 1: Start=F → idle.
	scan(1, map[string]ir.Value{"Start": ir.BoolVal(false), "Abort": ir.BoolVal(false)})
	assertActive(1, "Idle")
	if !boolG("RunLamp") {
		t.Error("scan 1: RunLamp should be TRUE (Idle N)")
	}
	if boolG("FillValve") {
		t.Error("scan 1: FillValve should be FALSE")
	}

	// Scan 2: Start=T → Idle→Fill.
	scan(2, map[string]ir.Value{"Start": ir.BoolVal(true)})
	assertActive(2, "Fill")
	if !boolG("RunLamp") || !boolG("FillValve") {
		t.Error("scan 2: RunLamp and FillValve should be TRUE")
	}

	// Scan 3: Level=40 → still filling.
	scan(3, map[string]ir.Value{"Start": ir.BoolVal(false), "Level": ir.RealVal(40)})
	assertActive(3, "Fill")

	// Scan 4: Level=100 → simultaneous divergence Fill→(Heat,Mix).
	scan(4, map[string]ir.Value{"Level": ir.RealVal(100)})
	assertActive(4, "Heat,Mix")
	if boolG("FillValve") {
		t.Error("scan 4: FillValve should drop to FALSE")
	}
	if !boolG("Heater") {
		t.Error("scan 4: Heater should be TRUE")
	}
	if !boolG("Mixer") {
		t.Error("scan 4: Mixer should be TRUE (Stir started)")
	}
	if mixQ() {
		t.Error("scan 4: mixT.Q should be FALSE (just started)")
	}

	// Scan 5: TempC=60, mixT<30s → convergence waits.
	scan(5, map[string]ir.Value{"TempC": ir.RealVal(60)})
	assertActive(5, "Heat,Mix")
	if !boolG("Mixer") {
		t.Error("scan 5: Mixer should stay TRUE")
	}

	// Scan 6: TempC=85 but mixT<30s → still waits (all conditions, not just steps).
	scan(6, map[string]ir.Value{"TempC": ir.RealVal(85)})
	assertActive(6, "Heat,Mix")

	// Scan 7: mixT.Q now TRUE (≥30s) → simultaneous convergence (Heat,Mix)→Drain.
	scan(7, nil)
	assertActive(7, "Drain")
	if boolG("Heater") {
		t.Error("scan 7: Heater should drop to FALSE")
	}
	if boolG("Mixer") {
		t.Error("scan 7: Mixer should drop to FALSE (Stir final scan)")
	}
	if mixET() != 0 || mixQ() {
		t.Errorf("scan 7: mixT should reset on the final scan (ET=%d Q=%v)", mixET(), mixQ())
	}
	if !boolG("DrainValve") {
		t.Error("scan 7: DrainValve should be TRUE")
	}
	if g("BatchCount").I != 1 {
		t.Errorf("scan 7: BatchCount = %d, want 1 (P1 CountBatch fired)", g("BatchCount").I)
	}

	// Scan 8: Level=60 → draining; P1 does not re-run.
	scan(8, map[string]ir.Value{"Level": ir.RealVal(60)})
	assertActive(8, "Drain")
	if g("BatchCount").I != 1 {
		t.Errorf("scan 8: BatchCount = %d, want 1 (pulse fired once)", g("BatchCount").I)
	}
	if boolG("Mixer") || mixET() != 0 {
		t.Error("scan 8: Mixer/mixT should stay reset")
	}

	// Scan 9: Level=4 → Drain→Idle.
	scan(9, map[string]ir.Value{"Level": ir.RealVal(4)})
	assertActive(9, "Idle")
	if boolG("DrainValve") {
		t.Error("scan 9: DrainValve should drop to FALSE")
	}
	if boolG("AbortLamp") {
		t.Error("scan 9: AbortLamp should be FALSE (R on Idle)")
	}
	if g("BatchCount").I != 1 {
		t.Errorf("scan 9: BatchCount = %d, want 1", g("BatchCount").I)
	}
}

// TestAltDivergenceAbort replays the §9 alternative-divergence trace: at the
// Fill fork with Abort=T, only the higher-priority t_abort fires even though
// Level>=FillSP also holds.
func TestAltDivergenceAbort(t *testing.T) {
	prog := mustCompile(t, tankBatchSrc)
	h := &clockHost{vals: map[string]ir.Value{
		"FillSP": ir.RealVal(100), "HeatSP": ir.RealVal(80), "EmptySP": ir.RealVal(5),
	}}
	frame := ir.NewFrame(prog)
	step := func(inputs map[string]ir.Value, now int64) {
		for k, v := range inputs {
			h.vals[k] = v
		}
		h.now = now
		if err := ir.Run(prog, frame, h); err != nil {
			t.Fatal(err)
		}
	}
	step(map[string]ir.Value{"Start": ir.BoolVal(true), "Abort": ir.BoolVal(false)}, 1000) // Idle→Fill
	if !stepActive(t, prog, frame, "Fill") {
		t.Fatal("expected Fill active")
	}
	// Abort AND Level>=FillSP both true: only t_abort (declared first) fires.
	step(map[string]ir.Value{"Abort": ir.BoolVal(true), "Level": ir.RealVal(100), "Start": ir.BoolVal(false)}, 2000)
	if !stepActive(t, prog, frame, "Idle") {
		t.Error("abort should route Fill→Idle")
	}
	if stepActive(t, prog, frame, "Heat") || stepActive(t, prog, frame, "Mix") {
		t.Error("t_full must be suppressed by higher-priority t_abort")
	}
}
