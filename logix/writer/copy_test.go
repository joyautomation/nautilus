package writer

import (
	"strings"
	"testing"
)

const copyTypes = "TYPE Pt : STRUCT x : DINT; y : REAL; END_STRUCT; END_TYPE\n"

// Logix assigns neither a whole array nor a structure (MOV takes one value,
// ST rejects `B := A`), so the writer copies them with COP, the length
// being the destination's element count. A run the import marked with a
// `@logix COP`/`CPS` pragma folds back into its one instruction; an
// unmarked element assignment stays a MOVE.
func TestCopiesWriteAsCOP(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
    Go : BOOL;
    A : ARRAY [0..3] OF DINT;
    B : ARRAY [0..3] OF DINT;
END_VAR
VAR
    p : Pt;
    q : Pt;
END_VAR
LD
  RUNG whole (* a whole array *)
    Go { B := A }
  RUNG strct (* a structure *)
    Go { q := p }
  RUNG sync (* a whole array, synchronous *)
    Go { B := A (* @logix CPS *) }
  RUNG slice (* the import's slice *)
    Go { B[1] := A[0]; B[2] := A[1] (* @logix COP 2 *) }
  RUNG mixed (* a move, then a one-element CPS, in one box *)
    Go { B[0] := A[3]; B[3] := A[2] (* @logix CPS *) }
END_LD
END_PROGRAM
`
	f := mustWrite(t, src, Options{Controller: "C", Libs: []string{copyTypes}})
	var got []string
	for _, r := range f.Controller.Programs[0].Routines[0].Rungs {
		got = append(got, r.Text)
	}
	want := []string{
		"XIC(Go)COP(A,B,4);",
		"XIC(Go)COP(p,q,1);",
		"XIC(Go)CPS(A,B,4);",
		"XIC(Go)COP(A[0],B[1],2);",
		"XIC(Go)MOVE(A[3],B[0])CPS(A[2],B[3],1);",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rungs:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The ST writer: the same whole copies are COP statements.
func TestSTCopiesWriteAsCOP(t *testing.T) {
	src := `PROGRAM Main
VAR
    A : ARRAY [0..3] OF DINT;
    B : ARRAY [0..3] OF DINT;
    p : Pt;
    q : Pt;
    n : DINT;
END_VAR
B := A;
q := p;
n := A[1];
END_PROGRAM
`
	doc, diags, err := WriteST(src, Options{Controller: "C", Libs: []string{copyTypes}})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, joinDiags(diags))
	}
	text := string(doc)
	for _, want := range []string{"COP(A, B, 4);", "COP(p, q, 1);", "n := A[1];"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
}

// Only what a pragma marks is folded: a move before a one-element CPS
// stays a MOVE even when its indexes line up, and a pragma whose count the
// box does not bear out (an edit broke the run) is a diagnostic, not a
// guess.
func TestCopyRunsFoldOnlyWhatIsMarked(t *testing.T) {
	head := `PROGRAM Main
VAR_EXTERNAL
    Go : BOOL;
    A : ARRAY [0..3] OF DINT;
    B : ARRAY [0..3] OF DINT;
END_VAR
LD
  RUNG r (* a copy *)
    Go { `
	tail := ` }
END_LD
END_PROGRAM
`
	f := mustWrite(t, head+"B[2] := A[1]; B[3] := A[2] (* @logix CPS *)"+tail, Options{Controller: "C"})
	if got := f.Controller.Programs[0].Routines[0].Rungs[0].Text; got != "XIC(Go)MOVE(A[1],B[2])CPS(A[2],B[3],1);" {
		t.Errorf("rung %s", got)
	}
	for body, want := range map[string]string{
		"B[2] := A[1]; B[3] := A[3] (* @logix COP 2 *)": "is not 2 element copies",
		"B[3] := A[2] (* @logix COP 2 *)":               "marks 2 assignments",
	} {
		_, diags, err := Write(head+body+tail, Options{Controller: "C"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(joinDiags(diags), want) {
			t.Errorf("%s: want a diagnostic naming %q, got %s", body, want, joinDiags(diags))
		}
	}
}
