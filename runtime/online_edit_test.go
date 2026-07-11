package runtime

import (
	"strings"
	"testing"
)

const editV1 = `PROGRAM Main
VAR_EXTERNAL
  In1 : REAL;
  Out1 : REAL;
END_VAR
VAR
  Total : REAL;
  Count : INT;
  Debounce : TON;
END_VAR
Total := Total + In1;
Count := Count + 1;
Out1 := Total;
END_PROGRAM`

// editV2 keeps Total and Debounce, drops Count, adds Gain — a typical
// online tweak.
const editV2 = `PROGRAM Main
VAR_EXTERNAL
  In1 : REAL;
  Out1 : REAL;
END_VAR
VAR
  Total : REAL;
  Gain : REAL := 2.0;
  Debounce : TON;
END_VAR
Total := Total + In1;
Out1 := Total * Gain;
END_PROGRAM`

func TestSwapWarmCarriesRetainedState(t *testing.T) {
	p, err := Compile(editV1)
	if err != nil {
		t.Fatal(err)
	}
	tags := NewTags()
	tags.SetReal("In1", 1.0)
	tags.SetReal("Out1", 0)
	for i := 0; i < 5; i++ {
		if err := p.Run(tags); err != nil {
			t.Fatal(err)
		}
	}
	if got := tags.Real("Out1"); got != 5.0 {
		t.Fatalf("Out1 after 5 scans = %v, want 5", got)
	}
	if p.Dirty() {
		t.Fatal("program dirty before any edit")
	}

	report, err := p.SwapWarm(editV2)
	if err != nil {
		t.Fatalf("warm swap: %v", err)
	}
	if !p.Dirty() {
		t.Error("program should be dirty after an online edit")
	}
	// Gain is new → reset; Count vanished (not reported: it has no slot in
	// the new program); Total and Debounce carried.
	if len(report.Resets) != 1 || report.Resets[0] != "Gain" {
		t.Errorf("resets = %v, want [Gain]", report.Resets)
	}

	// Total carried its 5.0 across the swap: next scan yields (5+1)*2.
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	if got := tags.Real("Out1"); got != 12.0 {
		t.Errorf("Out1 after warm swap scan = %v, want 12 (Total carried)", got)
	}

	// Rollback restores V1 program AND its state (Total keeps counting
	// from where V2 left it? No — rollback restores the pre-swap frame:
	// Total is 5 again, and one scan makes it 6).
	if !p.CanRollback() {
		t.Fatal("rollback should be available")
	}
	if _, err := p.Rollback(); err != nil {
		t.Fatal(err)
	}
	if p.Dirty() {
		t.Error("rolled-back program should match boot source")
	}
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	if got := tags.Real("Out1"); got != 6.0 {
		t.Errorf("Out1 after rollback scan = %v, want 6 (pre-swap frame restored)", got)
	}
	if p.CanRollback() {
		t.Error("rollback should be single-shot")
	}
}

func TestSwapWarmTypeChangeResets(t *testing.T) {
	p, err := Compile(editV1)
	if err != nil {
		t.Fatal(err)
	}
	tags := NewTags()
	tags.SetReal("In1", 1)
	tags.SetReal("Out1", 0)
	_ = p.Run(tags)

	// Total becomes INT: incompatible, must reset and be reported.
	v3 := strings.Replace(editV1, "Total : REAL;", "Total : INT;", 1)
	v3 = strings.Replace(v3, "Total := Total + In1;", "Total := Total + 1;", 1)
	v3 = strings.Replace(v3, "Out1 := Total;", "Out1 := 0.0;", 1)
	report, err := p.SwapWarm(v3)
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	found := false
	for _, r := range report.Resets {
		if r == "Total" {
			found = true
		}
	}
	if !found {
		t.Errorf("Total type change not reported in resets: %v", report.Resets)
	}
}

func TestSwapWarmCompileErrorLeavesRunning(t *testing.T) {
	p, err := Compile(editV1)
	if err != nil {
		t.Fatal(err)
	}
	hash := p.Hash()
	if _, err := p.SwapWarm("PROGRAM Broken\nOut1 := ;\nEND_PROGRAM"); err == nil {
		t.Fatal("expected compile error")
	}
	if p.Hash() != hash {
		t.Error("failed swap must not change the running program")
	}
	tags := NewTags()
	tags.SetReal("In1", 1)
	tags.SetReal("Out1", 0)
	if err := p.Run(tags); err != nil {
		t.Errorf("old program no longer runs: %v", err)
	}
}
