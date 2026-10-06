package st

import (
	"strings"
	"testing"
)

// #198: SCL's `#` local prefix is rejected in every position with one
// message. It used to be dropped silently on an assignment target, so
// `#state := 10;` compiled and ran as `state := 10;`.
func TestHashPrefixRejectedEverywhere(t *testing.T) {
	const want = "the # prefix is Siemens SCL syntax; write the name without it"
	cases := map[string]string{
		"assignment target":   "PROGRAM P\nVAR state : INT; END_VAR\n#state := 10;\nEND_PROGRAM\n",
		"right-hand side":     "PROGRAM P\nVAR state, x : INT; END_VAR\nx := #state;\nEND_PROGRAM\n",
		"in an expression":    "PROGRAM P\nVAR state, x : INT; END_VAR\nx := 1 + #state * 2;\nEND_PROGRAM\n",
		"IF condition":        "PROGRAM P\nVAR b : BOOL; x : INT; END_VAR\nIF #b THEN x := 1; END_IF;\nEND_PROGRAM\n",
		"FB argument":         "PROGRAM P\nVAR t : TON; b : BOOL; END_VAR\nt(IN := #b, PT := T#1s);\nEND_PROGRAM\n",
		"FB output target":    "PROGRAM P\nVAR t : TON; q : BOOL; END_VAR\nt(IN := TRUE, PT := T#1s, Q => #q);\nEND_PROGRAM\n",
		"FOR variable":        "PROGRAM P\nVAR i : INT; END_VAR\nFOR #i := 1 TO 3 DO END_FOR;\nEND_PROGRAM\n",
		"in a FUNCTION_BLOCK": "FUNCTION_BLOCK Dosing\nVAR state : INT; END_VAR\nIF state = 0 THEN\n  #state := 10;\nEND_IF;\nEND_FUNCTION_BLOCK\n",
		"inside CASE":         "PROGRAM P\nVAR s : INT; END_VAR\nCASE s OF\n0: #s := 10;\nEND_CASE;\nEND_PROGRAM\n",
	}
	for name, src := range cases {
		_, err := Parse(src)
		if err == nil {
			t.Errorf("%s: compiled; want the SCL # diagnostic", name)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v; want %q", name, err, want)
		}
		if _, ok := ParseErrorPos(err); !ok {
			t.Errorf("%s: error carries no line: %v", name, err)
		}
	}
	_, err := Parse("PROGRAM P\nVAR state : INT; END_VAR\n#state := 10;\nEND_PROGRAM\n")
	if err == nil || !strings.Contains(err.Error(), "(state, not #state)") {
		t.Errorf("the message names the variable: %v", err)
	}
	if p, _ := ParseErrorPos(err); p.Line != 3 {
		t.Errorf("anchored on line %d, want 3", p.Line)
	}
}
