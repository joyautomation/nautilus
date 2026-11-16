package ld

import (
	"strings"
	"testing"
)

// READ and WRITE in a rung (IEC 61131-5): the rung's power is REQ, and power
// passes through to the coils after the block, as it does past a Logix MSG.
func TestLadderReadWriteBlocks(t *testing.T) {
	src := `TYPE Pt : STRUCT A : INT; B : REAL; END_STRUCT; END_TYPE
PROGRAM Main
VAR_EXTERNAL Step : BOOL; Send : BOOL; Polled : BOOL; Sent : BOOL; END_VAR
VAR poll : READ; push : WRITE; Local : Pt; Out : Pt; END_VAR
LD
  RUNG rd   Step poll:READ(ID := 'line2', VAR_1 := 'Remote', RD_1 := Local) ( Polled )
  RUNG wr   Send push:WRITE(ID := 'line2', VAR_1 := 'Remote', SD_1 := Out) ( Sent )
END_LD
END_PROGRAM`
	got := toST(t, src)
	for _, want := range []string{
		"poll(REQ := Step, ID := 'line2', VAR_1 := 'Remote', RD_1 := Local);",
		"Polled := Step;",
		"push(REQ := Send, ID := 'line2', VAR_1 := 'Remote', SD_1 := Out);",
		"Sent := Send;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	compileST(t, got)
}

// A one-line VAR section declares its variables, as a one-line VAR_INPUT
// does: the Logix writer reads instances from here, and used to drop the
// call of a block declared `VAR t1 : TON; END_VAR` as undeclared.
func TestScanVarsOneLineVar(t *testing.T) {
	src := "PROGRAM Main\nVAR_EXTERNAL Go : BOOL; END_VAR\nVAR rd : READ; t1 : TON; END_VAR\nVAR RETAIN n : DINT; END_VAR\nVAR CONSTANT kMax : DINT := 10; END_VAR\nLD\n  RUNG r0\n    Go ( Go )\nEND_LD\nEND_PROGRAM\n"
	got := map[string]string{}
	for _, v := range scanVars(src) {
		got[v.Name] = v.Section + " " + v.Type
	}
	for name, want := range map[string]string{"rd": "VAR READ", "t1": "VAR TON", "n": "VAR DINT", "Go": "VAR_EXTERNAL BOOL", "kMax": "VAR CONSTANT DINT"} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q (all: %v)", name, got[name], want, got)
		}
	}
}
