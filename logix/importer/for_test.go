package importer

import (
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/logix/writer"
)

// #893: a Logix ST FOR, whose operands Logix types SINT/INT/DINT, imports
// verbatim with integer bounds and lowers in nautilus. A routine whose
// bound is a REAL tag is refused at check with the conversion named,
// rather than run zero times.
func TestLogixSTForImportsWithIntegerBounds(t *testing.T) {
	src := "PROGRAM Loop\nVAR\n    i : DINT;\nEND_VAR\nVAR_EXTERNAL\n    c : DINT;\n    N : DINT;\nEND_VAR\n" +
		"c := 0;\nFOR i := 1 TO N BY 1 DO\n    c := c + 1;\nEND_FOR;\nEND_PROGRAM\n"
	doc, diags, err := writer.WriteST(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writing the Logix project: %v %v", err, diags)
	}
	if !strings.Contains(string(doc), "FOR i := 1 TO N BY 1 DO") {
		t.Fatalf("the written routine has no FOR:\n%s", doc)
	}
	// The same project with the N controller tag a REAL, as an L5X could
	// carry it: built from the L5X, not the writer.
	nTag := regexp.MustCompile(`(<Tag Name="N"[^>]*DataType=")DINT(")`)
	realDoc := nTag.ReplaceAll(doc, []byte("${1}REAL${2}"))
	if string(realDoc) == string(doc) {
		t.Fatalf("no N tag to retype in:\n%s", doc)
	}
	for _, c := range []struct {
		nType string
		doc   []byte
		want  string
	}{
		{"DINT", doc, ""},
		{"REAL", realDoc, "FOR i end: N is REAL, and a FOR's bounds and step are integers like its variable; write REAL_TO_DINT(N)"},
	} {
		p := mustImport(t, c.doc)
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
