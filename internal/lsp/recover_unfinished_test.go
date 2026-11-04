package lsp

import "testing"

// A file typed top to bottom has no END_PROGRAM yet, or a (* comment not
// yet closed. st.Parse refuses both (nautilus#609); the skeleton still
// parses, so hover and "settle." completion keep their declarations.
func TestDeclarationsRecoveredInAnUnfinishedFile(t *testing.T) {
	for name, src := range map[string]string{
		"no END_PROGRAM yet":  typingHead + "Full := settle.",
		"(* still open":       typingHead + "(* explain the settle timer\nFull := settle.Q;\nEND_PROGRAM\n",
		"both":                typingHead + "Full := settle.Q; (* a note",
		"FUNCTION_BLOCK open": "FUNCTION_BLOCK Filler\nVAR\n  settle : TON;\nEND_VAR\nsettle.",
		"FUNCTION, then (* ":  "FUNCTION Twice : REAL\nVAR_INPUT\n  x : REAL;\nEND_VAR\nVAR\n  settle : TON;\nEND_VAR\n(* todo",
	} {
		a := analyze(src, "", 0)
		if len(a.Diags) != 1 {
			t.Errorf("%s: diags = %+v, want the one parse error", name, a.Diags)
		}
		sym := a.lookup("settle", 10)
		if sym == nil || sym.Datatype != "TON" {
			t.Errorf("%s: settle = %+v, want the TON declaration recovered", name, sym)
		}
	}
}
