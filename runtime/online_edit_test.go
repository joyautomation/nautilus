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

// FBD sources are first-class programs: the runtime detects the netlist
// block, transpiles through lang/fbd, and keeps the ORIGINAL .fbd text as
// the program of record — so online edits and hashes speak .fbd end to end.

const fbdV1 = `PROGRAM Main
VAR_EXTERNAL
  In1 : REAL;
  Out1 : REAL;
END_VAR
VAR
  integral : REAL;
END_VAR
FBD
  integral := ADD(integral, In1)
  Out1 := integral
END_FBD
END_PROGRAM`

// fbdV2 keeps integral (retained across the swap) and scales the output.
const fbdV2 = `PROGRAM Main
VAR_EXTERNAL
  In1 : REAL;
  Out1 : REAL;
END_VAR
VAR
  integral : REAL;
END_VAR
FBD
  integral := ADD(integral, In1)
  Out1 := MUL(integral, 2.0)
END_FBD
END_PROGRAM`

func TestLanguageDetection(t *testing.T) {
	if l := Language(fbdV1); l != "fbd" {
		t.Errorf("Language(fbd source) = %q, want fbd", l)
	}
	if l := Language(editV1); l != "st" {
		t.Errorf("Language(st source) = %q, want st", l)
	}
}

func TestSwapWarmFBDCarriesRetainedState(t *testing.T) {
	p, err := Compile(fbdV1)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source() != fbdV1 {
		t.Fatal("Source() must be the original .fbd text, not the transpiled ST")
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

	report, err := p.SwapWarm(fbdV2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resets) != 0 {
		t.Errorf("resets = %v, want none (integral carries)", report.Resets)
	}
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	// integral was 5, +1 = 6, doubled = 12: the edit went live warm.
	if got := tags.Real("Out1"); got != 12.0 {
		t.Fatalf("Out1 after warm fbd edit = %v, want 12", got)
	}
	if !p.Dirty() || p.Source() != fbdV2 {
		t.Error("swap must mark dirty and report the new .fbd source")
	}

	// Rollback restores the old program AND its pre-swap state snapshot
	// (integral back to 5), so the next scan integrates to 6, unscaled.
	if _, err := p.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
	if got := tags.Real("Out1"); got != 6.0 {
		t.Fatalf("Out1 after rollback = %v, want 6", got)
	}
	if p.Source() != fbdV1 {
		t.Error("rollback must restore the original .fbd source")
	}
}

func TestSwapWarmFBDCompileErrorLeavesRunning(t *testing.T) {
	p, err := Compile(fbdV1)
	if err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(fbdV1, "Out1 := integral", "Out1 := bogus_wire", 1)
	if _, err := p.SwapWarm(bad); err == nil {
		t.Fatal("expected compile error")
	}
	if p.Source() != fbdV1 {
		t.Error("failed swap must leave the running program untouched")
	}
	tags := NewTags()
	tags.SetReal("In1", 1.0)
	tags.SetReal("Out1", 0)
	if err := p.Run(tags); err != nil {
		t.Fatal(err)
	}
}
