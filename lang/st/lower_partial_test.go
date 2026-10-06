package st

import (
	"testing"

	"github.com/joyautomation/nautilus/lang/ir"
)

// #222: the IEC partial access w.%X3 (a bit), w.%B1 (a byte), w.%W0 (a
// word), w.%D1 (a double word), read and written, alongside Logix's w.3.
func TestPartialAccessReadWrite(t *testing.T) {
	src := `
PROGRAM P
VAR_EXTERNAL Alarms : DINT; B3 : BOOL; Hi : BYTE; Lo : WORD; L : LWORD; D1 : DWORD; END_VAR
B3 := Alarms.%X3;
Alarms.%x5 := TRUE;
Alarms.%X0 := FALSE;
Hi := Alarms.%B1;
Lo := Alarms.%W0;
Alarms.%B3 := 16#AB;
L.%D1 := 16#12345678;
D1 := L.%D1;
L.%W0 := 16#FFFF;
L.%X63 := FALSE;
END_PROGRAM`
	h, _, _ := scanN(t, src, 1, map[string]ir.Value{"Alarms": ir.IntVal(0x1209), "L": ir.IntVal(0)})
	if !h.globals["B3"].B {
		t.Errorf("Alarms.%%X3 of 16#1209 must read TRUE")
	}
	if got := h.globals["Hi"].I; got != 0x12 {
		t.Errorf("Alarms.%%B1 = %#x, want 0x12", got)
	}
	if got := h.globals["Lo"].I; got != 0x1228 {
		t.Errorf("Alarms.%%W0 (after the two bit writes) = %#x, want 0x1228", got)
	}
	if got := h.globals["Alarms"].I; got != 0xAB001228 {
		t.Errorf("Alarms = %#x, want 0xAB001228 (bit 5 set, bit 0 cleared, byte 3 = AB)", got)
	}
	if got := h.globals["D1"].I; got != 0x12345678 {
		t.Errorf("L.%%D1 = %#x", got)
	}
	if got := h.globals["L"].I; got != 0x123456780000FFFF {
		t.Errorf("L = %#x", got)
	}
}

// Parts are bounded by the declared type, and every message names the
// declared type (DINT, WORD), not the canonical INT the value runs as.
func TestPartialAccessErrorsNameDeclaredType(t *testing.T) {
	head := "PROGRAM P\nVAR d : DINT; w : WORD; s : SINT; r : REAL; b : BOOL; m : Mode; END_VAR\n"
	types := "TYPE Mode : (A, B); END_TYPE\n"
	cases := map[string]string{
		"b := d.%X32;": "d.%X32: bit 32 is out of range for DINT (bits 0..31)",
		"b := w.16;":   "w.16: bit 16 is out of range for WORD (bits 0..15)",
		"d := w.%B2;":  "w.%B2: byte 2 is out of range for WORD (bytes 0..1)",
		"d := s.%W0;":  "s.%W0: a SINT has 8 bits, fewer than one 16-bit part",
		"b := r.%X1;":  "r.%X1: partial access needs an integer variable (ANY_BIT/ANY_INT), and r is a REAL",
		"b := m.%X0;":  "and m is a Mode",
		"b := d.foo;":  "member access on non-struct type DINT (d.foo)",
		"b := w.Q;":    "member access on non-struct type WORD (w.Q)",
		"d := 'x';":    "cannot assign STRING to DINT",
	}
	for body, want := range cases {
		lowerExpectErr(t, types+head+body+"\nEND_PROGRAM\n", want)
	}
}
