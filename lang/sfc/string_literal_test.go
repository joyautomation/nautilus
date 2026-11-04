package sfc

import "testing"

// A string in an action reads as in ST (nautilus#591): '…' or "…", and $'
// or $" is a quote inside it. It used to end at $', so the rest of the
// line opened a string that ran past END_ACTION ("missing END_ACTION").
func TestActionStringsReadAsInST(t *testing.T) {
	src := `PROGRAM P
VAR_EXTERNAL Msg : STRING; Note : STRING; END_VAR
SFC
  INITIAL_STEP Talk:
    N Say;
  END_STEP
  ACTION Say:
    Msg := 'it$'s; ok';
    Note := "say $"hi$" END_ACTION";
  END_ACTION
END_SFC
END_PROGRAM
`
	_, _, h, run := scanner(t, src)
	run(nil)
	if got := h.vals["Msg"].S; got != "it's; ok" {
		t.Errorf("Msg = %q, want %q", got, "it's; ok")
	}
	if got := h.vals["Note"].S; got != `say "hi" END_ACTION` {
		t.Errorf("Note = %q", got)
	}
}
