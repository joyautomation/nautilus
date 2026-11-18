package importer

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/logix/writer"
)

// #893: a Logix ST FOR, whose operands Logix types SINT/INT/DINT, imports
// verbatim with integer bounds and lowers in nautilus; one with a REAL
// bound is refused at check with the conversion named, not run zero times.
func TestLogixSTForImportsWithIntegerBounds(t *testing.T) {
	for _, c := range []struct {
		nType, want string
	}{
		{"DINT", ""},
		{"REAL", "FOR i end: N is REAL, and a FOR's bounds and step are integers like its variable; write REAL_TO_DINT(N)"},
	} {
		src := "PROGRAM Loop\nVAR\n    i : DINT;\nEND_VAR\nVAR_EXTERNAL\n    c : DINT;\n    N : " + c.nType + ";\nEND_VAR\n" +
			"c := 0;\nFOR i := 1 TO N BY 1 DO\n    c := c + 1;\nEND_FOR;\nEND_PROGRAM\n"
		l5x, diags, err := writer.WriteST(src, writer.Options{Controller: "Mini"})
		if err != nil || len(diags) > 0 {
			t.Fatalf("N %s: writing the Logix project: %v %v", c.nType, err, diags)
		}
		if !strings.Contains(string(l5x), "FOR i := 1 TO N BY 1 DO") {
			t.Fatalf("N %s: the written routine has no FOR:\n%s", c.nType, l5x)
		}
		p := mustImport(t, l5x)
		imported := string(p.Files["Loop.st"])
		if !strings.Contains(imported, "FOR i := 1 TO N BY 1 DO") || !strings.Contains(imported, "N : "+c.nType+";") {
			t.Fatalf("N %s: the import is not the routine as written:\n%s", c.nType, imported)
		}
		prog, err := st.Parse(imported)
		if err == nil {
			_, err = st.Lower(prog)
		}
		if c.want == "" && err != nil {
			t.Errorf("N DINT: the imported FOR does not lower: %v\n%s", err, imported)
		}
		if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("N REAL: err = %v, want %q", err, c.want)
		}
	}
}
