package importer

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/logix/writer"
)

// #777: an operand reached through an index or a member is typed too, so
// its store converts: an array element, a counter's ACC, a
// timer's PRE (a TIME in nautilus), and the writer folds each back.
func TestStoreConversionThroughIndexesAndMembers(t *testing.T) {
	p := mustImport(t, miniL5X([]string{"A:DINT[4]", "AI:INT[4]", "C1:COUNTER=5", "T1:TIMER=1000", "T3:TIMER=500", "I:INT", "R:REAL"},
		"MOV(A[1],I);", "MOV(A[1],AI[2]);", "MOV(C1.ACC,I);", "MOV(T1.ACC,I);", "MOV(R,AI[0]);",
		"MOV(T1.PRE,T3.PRE);", "MOV(T1.ACC,T3.ACC);"))
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"{ I := DINT_TO_INT(A[1]) }",
		"{ AI[2] := DINT_TO_INT(A[1]) }",
		"{ I := DINT_TO_INT(C1.CV) }",
		"{ I := TIME_TO_INT(T1.ET) }",
		"{ AI[0] := REAL_TO_INT(R) }",
		// Timer to timer: TIME to TIME, no conversion (no TON runs on
		// T3, so its PRE stays .PT).
		"{ T3.PT := T1.PT }",
		"{ T3.ET := T1.ET }",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	for _, want := range []string{"MOVE(A[1],I);", "MOVE(A[1],AI[2]);", "MOVE(C1.ACC,I);", "MOVE(T1.ACC,I);", "MOVE(R,AI[0]);", "MOVE(T1.PRE,T3.PRE);", "MOVE(T1.ACC,T3.ACC);"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("written back without %q", want)
		}
	}
}
