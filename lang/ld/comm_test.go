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
