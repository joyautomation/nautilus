package project

import (
	"strings"
	"testing"
)

// #1382: a tag with no type: and a number init: is a REAL. Passed to an
// INT input of a block it is refused, and the refusal names the fix: the
// tag's type:. A tag that states its type gets no such hint.
func TestInferredTagIntoAnIntInputNamesTheFix(t *testing.T) {
	files := func(tag string) map[string]string {
		return map[string]string{
			"nautilus.yaml": "tasks:\n  - program: main.st\ntags:\n  - " + tag + "\n",
			"main.st": "FUNCTION_BLOCK Outer\nVAR_INPUT x : INT; END_VAR\nEND_FUNCTION_BLOCK\n" +
				"PROGRAM Main\nVAR o : Outer; END_VAR\no(x := In);\nEND_PROGRAM\n",
		}
	}
	err := newRuntimeErr(t, files("{ name: In, role: state, init: 3 }"))
	want := "FB Outer input x: cannot pass REAL as INT; tag In has no type:, so it is REAL; give it type: INT"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("untyped tag: err = %v, want %q", err, want)
	}
	err = newRuntimeErr(t, files("{ name: In, role: state, type: REAL, init: 3 }"))
	if err == nil || !strings.Contains(err.Error(), "FB Outer input x: cannot pass REAL as INT") {
		t.Fatalf("typed REAL tag: err = %v, want the refusal", err)
	}
	if strings.Contains(err.Error(), "has no type:") {
		t.Errorf("typed REAL tag: %v names a missing type:, but the tag states one", err)
	}
	if err := newRuntimeErr(t, files("{ name: In, role: state, type: INT, init: 3 }")); err != nil {
		t.Errorf("INT tag: %v", err)
	}
}
