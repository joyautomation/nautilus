package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #203: VAR_TEMP is scratch for one execution. In a PROGRAM it starts every
// scan at its declared initial value (or zero) — the issue's probe counted
// 1,2,3,4,5 where IEC (and TIA) give 1 every time.
func TestVarTempResetsEveryScan(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL Out : INT; Seeded : INT; Total : INT; END_VAR
VAR_TEMP n : INT; s : INT := 40; buf : ARRAY[0..2] OF INT; END_VAR
VAR kept : INT; END_VAR
n := n + 1;
s := s + 2;
buf[1] := buf[1] + 5;
kept := kept + 1;
Out := n;
Seeded := s;
Total := buf[1] * 100 + kept;
END_PROGRAM`
	seed := map[string]ir.Value{"Out": ir.IntVal(0), "Seeded": ir.IntVal(0), "Total": ir.IntVal(0)}
	h, _, _ := scanN(t, src, 5, seed)
	if got := h.globals["Out"].I; got != 1 {
		t.Errorf("VAR_TEMP n after 5 scans = %d, want 1 (fresh every scan)", got)
	}
	if got := h.globals["Seeded"].I; got != 42 {
		t.Errorf("VAR_TEMP s := 40 after 5 scans = %d, want 42 (its initial value every scan)", got)
	}
	if got := h.globals["Total"].I; got != 505 {
		t.Errorf("Total = %d, want 505 (temp array element 5, VAR kept 5)", got)
	}
}

// In a FUNCTION_BLOCK a temp resets on every CALL, not once per scan: two
// calls in one scan each see it fresh, while VAR keeps counting.
func TestVarTempResetsEveryFBCall(t *testing.T) {
	src := `
FUNCTION_BLOCK Counter
VAR_OUTPUT tempOut : INT; keptOut : INT; END_VAR
VAR_TEMP n : INT; END_VAR
VAR k : INT; END_VAR
n := n + 1;
k := k + 1;
tempOut := n;
keptOut := k;
END_FUNCTION_BLOCK

PROGRAM P
VAR_EXTERNAL T1 : INT; K1 : INT; T2 : INT; K2 : INT; END_VAR
VAR c : Counter; END_VAR
c();
T1 := c.tempOut; K1 := c.keptOut;
c();
T2 := c.tempOut; K2 := c.keptOut;
END_PROGRAM`
	seed := map[string]ir.Value{"T1": ir.IntVal(0), "K1": ir.IntVal(0), "T2": ir.IntVal(0), "K2": ir.IntVal(0)}
	h, _, _ := scanN(t, src, 3, seed)
	for name, want := range map[string]int64{"T1": 1, "T2": 1, "K1": 5, "K2": 6} {
		if got := h.globals[name].I; got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

// A FUNCTION's VAR_TEMP (like all its locals) is fresh per call.
func TestVarTempInFunction(t *testing.T) {
	src := `
FUNCTION Bump : INT
VAR_INPUT x : INT; END_VAR
VAR_TEMP acc : INT := 100; END_VAR
acc := acc + x;
Bump := acc;
END_FUNCTION

PROGRAM P
VAR_EXTERNAL A : INT; B : INT; END_VAR
A := Bump(1);
B := Bump(2);
END_PROGRAM`
	h, _, _ := scanN(t, src, 3, map[string]ir.Value{"A": ir.IntVal(0), "B": ir.IntVal(0)})
	if h.globals["A"].I != 101 || h.globals["B"].I != 102 {
		t.Errorf("A, B = %d, %d; want 101, 102", h.globals["A"].I, h.globals["B"].I)
	}
}

// An online edit never carries a temp, and does not report it as reset.
func TestVarTempNotMigrated(t *testing.T) {
	src := `
PROGRAM P
VAR_TEMP n : INT; END_VAR
VAR k : INT; END_VAR
n := 7; k := k + 1;
END_PROGRAM`
	prog := lowerSource(t, src)
	frame := ir.NewFrame(prog)
	if err := ir.Run(prog, frame, newStubHost()); err != nil {
		t.Fatal(err)
	}
	next := lowerSource(t, src)
	nf, resets := ir.MigrateFrame(next, prog, frame)
	if len(resets) != 0 {
		t.Errorf("resets = %v, want none", resets)
	}
	if got := nf.Slots[next.SlotIndex["n"]].I; got != 0 {
		t.Errorf("migrated temp n = %d, want 0", got)
	}
	if got := nf.Slots[next.SlotIndex["k"]].I; got != 1 {
		t.Errorf("migrated VAR k = %d, want 1 (carried)", got)
	}
	if !next.Slots[next.SlotIndex["n"]].Temp || next.Slots[next.SlotIndex["k"]].Temp {
		t.Error("only n is marked Temp")
	}
}

func TestVarTempRejections(t *testing.T) {
	lowerExpectErr(t, "PROGRAM P\nVAR_TEMP RETAIN n : INT; END_VAR\nEND_PROGRAM\n", "VAR_TEMP RETAIN")
	lowerExpectErr(t, "PROGRAM P\nVAR_TEMP t : TON; END_VAR\nEND_PROGRAM\n", "a function-block instance cannot be a temporary")
	lowerExpectErr(t, "FUNCTION_BLOCK F\nVAR_TEMP t : TON; END_VAR\nEND_FUNCTION_BLOCK\n", "a function-block instance cannot be a temporary")
}
