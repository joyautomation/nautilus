package importer

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/logix/writer"
)

// COP and CPS import as nautilus copies and write back as the instruction
// they were: a whole array as an assignment (the writer's COP), a slice or
// one element as the element-by-element run with its `@logix` pragma, a
// CPS always with `@logix CPS`.
func TestCopiesImportAndWriteBack(t *testing.T) {
	tags := []string{"go:BOOL", "A:DINT[4]", "B:DINT[4]", "C:DINT[8]", "n:DINT", "m:DINT"}
	rungs := []string{
		"XIC(go)COP(A,B,4);",
		"XIC(go)CPS(A,B,4);",
		"XIC(go)COP(A[1],C[4],3);",
		"COP(n,m,1);",
		"XIC(go)CPS(A[0],C[0],1);",
	}
	p := mustImport(t, miniL5X(tags, rungs...))
	for _, n := range p.Notes {
		if n.Rung >= 0 {
			t.Errorf("note: %s", n)
		}
	}
	src := string(p.Files["Main.ld"])
	for _, want := range []string{
		"go { B := A }",
		"go { B := A (* @logix CPS *) }",
		"go { C[4] := A[1]; C[5] := A[2]; C[6] := A[3] (* @logix COP *) }",
		"{ m := n (* @logix COP *) }",
		"go { C[0] := A[0] (* @logix CPS *) }",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("missing %q in\n%s", want, src)
		}
	}
	out, diags, err := writer.Write(src, writer.Options{Controller: "Mini"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("writer: %v %v\n%s", err, diags, src)
	}
	back, err := l5x.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := rungTexts(back, "Main", "MainRoutine"); strings.Join(got, "\n") != strings.Join(rungs, "\n") {
		t.Errorf("written back:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(rungs, "\n  "))
	}
}

// The copies whose meaning is memory, not values, stay refused, each with
// its reason: a length past either end, an overlap in one array (the
// result depends on the direction Logix copies in), a computed length, and
// bytes reinterpreted between types.
func TestCopiesRefusedByReason(t *testing.T) {
	tags := []string{"go:BOOL", "A:DINT[4]", "C:DINT[8]", "n:DINT", "bytes:SINT[4]"}
	cases := map[string]string{
		"COP(A,C,8);":        "runs past the end of A",
		"COP(C[0],C[1],3);":  "overlap in C",
		"COP(A[0],C[0],n);":  "a length that is not a constant",
		"CPS(n,bytes[0],4);": "reinterprets bytes",
		"COP(A[n],C[0],1);":  "an element index that is not a constant",
		"COP(n,A[0],2);":     "from or into a single DINT",
	}
	for rung, want := range cases {
		p := mustImport(t, miniL5X(tags, "XIC(go)OTE(a);", rung))
		src := string(p.Files["Main.ld"])
		if !strings.Contains(src, "// not imported (") || !strings.Contains(src, want) {
			t.Errorf("%s: want a refusal naming %q:\n%s", rung, want, src)
		}
	}
}
