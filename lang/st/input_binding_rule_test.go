package st

import (
	"strings"
	"testing"
)

const inputDecls = `
FUNCTION_BLOCK H
VAR_INPUT i : INT; d : DINT; r : REAL; b : BOOL; t : TIME; w : WORD; END_VAR
VAR_IN_OUT io : INT; ior : REAL; ioa : ARRAY[0..2] OF INT; END_VAR
END_FUNCTION_BLOCK
FUNCTION F : INT
VAR_INPUT i : INT; d : DINT; r : REAL; b : BOOL; t : TIME; w : WORD; END_VAR
F := 0;
END_FUNCTION
PROGRAM P
VAR h : H; tn : TON; cu : CTU; c : PID; n : INT; nd : DINT; ns : SINT; nl : LINT; nr : REAL; nlr : LREAL; nb : BOOL; nt : TIME; nw : WORD; ns2 : STRING;
    a : ARRAY[0..2] OF INT; ad : ARRAY[0..2] OF DINT; x : INT; END_VAR
`

const inouts = ", io := n, ior := nr, ioa := a"

// #1382: an FB input takes a value exactly where a FUNCTION argument of the
// same type does, and is refused in the same words. A REAL or a BOOL into
// an INT input used to pass.
func TestFBInputFollowsTheArgumentRule(t *testing.T) {
	for _, tc := range []struct{ pin, value string }{
		{"i", "nr"}, {"i", "nb"}, {"i", "nt"}, {"i", "ns2"}, {"d", "nlr"},
		{"r", "nb"}, {"r", "nt"}, {"b", "n"}, {"b", "nr"}, {"t", "nr"}, {"t", "nb"}, {"w", "nr"},
		{"r", "nlr"}, {"i", "nd"}, // the REAL table and the integer table: refused before, same words now
	} {
		_, ferr := lowerSrc(inputDecls + "x := F(" + tc.pin + " := " + tc.value + ");\nEND_PROGRAM")
		_, berr := lowerSrc(inputDecls + "h(" + tc.pin + " := " + tc.value + inouts + ");\nEND_PROGRAM")
		if ferr == nil {
			t.Fatalf("F(%s := %s): the FUNCTION argument is not refused; the test's premise is wrong", tc.pin, tc.value)
		}
		if berr == nil {
			t.Errorf("h(%s := %s): not refused, while F(%s := %s) is (%v)", tc.pin, tc.value, tc.pin, tc.value, ferr)
			continue
		}
		fmsg := linePrefix.ReplaceAllString(ferr.Error(), "")
		want := strings.Replace(fmsg, "FUNCTION F arg "+tc.pin+": ", "FB H input "+tc.pin+": ", 1)
		if want == fmsg {
			t.Fatalf("F(%s := %s): unexpected message shape %q", tc.pin, tc.value, fmsg)
		}
		if got := linePrefix.ReplaceAllString(berr.Error(), ""); got != want {
			t.Errorf("h(%s := %s):\n got %q\nwant %q", tc.pin, tc.value, got, want)
		}
	}
	// Standard blocks: a REAL or an INT where a BOOL or a TIME pin is, and
	// a BOOL into a numeric pin.
	for _, tc := range []struct{ body, want string }{
		{"tn(IN := nr, PT := T#1s);", "FB TON input IN: cannot pass REAL as BOOL"},
		{"tn(IN := nb, PT := 1.5);", "FB TON input PT: cannot pass REAL as TIME"},
		{"cu(CU := nb, PV := nr);", "FB CTU input PV: cannot pass REAL as INT"},
		{"cu(CU := n, PV := 3);", "FB CTU input CU: cannot pass INT as BOOL"},
		{"c(PV := nb, SP := nr);", "FB PID input PV: cannot pass BOOL as REAL"},
	} {
		_, err := lowerSrc(inputDecls + tc.body + "\nEND_PROGRAM")
		if err == nil || !strings.HasSuffix(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.body, err, tc.want)
		}
	}
	// What a FUNCTION argument takes, an input takes; an integer into a
	// TIME stays a warning (TestIntegerWhereTimeIsExpectedWarns), and a
	// standard block's numeric pins stay generic.
	if _, err := lowerSrc(inputDecls + `
h(i := ns, d := n, r := n, b := nb, t := nt, w := nw` + inouts + `);
h(t := 5000, d := nd, r := ns` + inouts + `);
h(t := nd` + inouts + `);
tn(IN := nb, PT := 5000); tn(IN := nb, PT := nt);
cu(CU := nb, PV := nd); c(PV := nlr, SP := nr); c(PV := n, SP := nd);
END_PROGRAM`); err != nil {
		t.Errorf("what a FUNCTION argument takes: %v", err)
	}
}

// #1382: a VAR_IN_OUT is passed by reference, so the bound variable must
// have the pin's width too: an INT pin bound to a DINT used to pass.
func TestInOutNeedsTheSameWidth(t *testing.T) {
	for _, tc := range []struct{ binds, want string }{
		{"io := nd, ior := nr, ioa := a", `VAR_IN_OUT "io": needs a INT variable, got DINT (an IN_OUT is passed by reference`},
		{"io := ns, ior := nr, ioa := a", `VAR_IN_OUT "io": needs a INT variable, got SINT`},
		{"io := n, ior := nlr, ioa := a", `VAR_IN_OUT "ior": needs a REAL variable, got LREAL`},
		{"io := n, ior := nr, ioa := ad", `VAR_IN_OUT "ioa": needs a ARRAY OF INT variable, got ARRAY OF DINT`},
		{"io := nr, ior := nr, ioa := a", `VAR_IN_OUT "io": needs a INT variable, got REAL`}, // refused before too
	} {
		_, err := lowerSrc(inputDecls + "h(" + tc.binds + ");\nEND_PROGRAM")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("h(%s): err = %v, want %q", tc.binds, err, tc.want)
		}
	}
	if _, err := lowerSrc(inputDecls + "h(io := n, ior := nr, ioa := a);\nEND_PROGRAM"); err != nil {
		t.Errorf("same widths: %v", err)
	}
}
