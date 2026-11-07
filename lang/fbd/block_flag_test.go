package fbd

import "testing"

// Model.Block marks a FUNCTION_BLOCK body, whose chips are the block's pins
// and locals (no live tag actions on them), and only that: a PROGRAM, also
// one after an ST library block in the same file, is not one.
func TestModelBlockFlag(t *testing.T) {
	cases := []struct {
		name, src, pou string
		block          bool
	}{
		{"program", "PROGRAM p\nVAR_EXTERNAL Run : BOOL; Done : BOOL; END_VAR\nFBD\n  Done := Run\nEND_FBD\nEND_PROGRAM\n", "p", false},
		{"function block", "FUNCTION_BLOCK Seal\nVAR_INPUT x : BOOL; END_VAR\nVAR_OUTPUT y : BOOL; END_VAR\nFBD\n  y := x\nEND_FBD\nEND_FUNCTION_BLOCK\n", "Seal", true},
		{"lower-case keyword", "function_block Seal\nVAR_INPUT x : BOOL; END_VAR\nVAR_OUTPUT y : BOOL; END_VAR\nFBD\n  y := x\nEND_FBD\nEND_FUNCTION_BLOCK\n", "Seal", true},
		{"program after an ST block", "FUNCTION_BLOCK Helper\nVAR_INPUT x : BOOL; END_VAR\nVAR_OUTPUT y : BOOL; END_VAR\ny := x;\nEND_FUNCTION_BLOCK\n\nPROGRAM p\nVAR_EXTERNAL Run : BOOL; Done : BOOL; END_VAR\nFBD\n  Done := Run\nEND_FBD\nEND_PROGRAM\n", "p", false},
		{"commented-out keyword", "(* FUNCTION_BLOCK Old *)\nPROGRAM p\nVAR_EXTERNAL Run : BOOL; Done : BOOL; END_VAR\nFBD\n  Done := Run\nEND_FBD\nEND_PROGRAM\n", "p", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mustGraph(t, c.src)
			if m.Name != c.pou || m.Block != c.block {
				t.Fatalf("Name %q Block %v, want %q %v", m.Name, m.Block, c.pou, c.block)
			}
		})
	}
}
