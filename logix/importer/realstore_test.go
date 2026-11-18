package importer

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/logix/writer"
)

// #782 (held REAL half): a MOV of a DINT into a REAL writes the conversion
// IEC's table needs, and the writer folds it back into the MOV.
func TestDintIntoRealConverts(t *testing.T) {
	p := mustImport(t, miniL5X([]string{"D:DINT", "I:INT", "R:REAL"}, "MOV(D,R);", "MOV(I,R);"))
	src := string(p.Files["Main.ld"])
	for _, want := range []string{"{ R := DINT_TO_REAL(D) }", "{ R := I }"} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	for _, want := range []string{"MOVE(D,R);", "MOVE(I,R);"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("written back without %q", want)
		}
	}
}
