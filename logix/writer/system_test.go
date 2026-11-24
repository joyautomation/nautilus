package writer

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/l5x"
)

// The nautilus system functions (#710 (b)) read a nautilus pair's state.
// A Logix controller runs none: in ST they are constants, FALSE (0) and 0
// takeovers, with no refusal.
func TestSTSystemFunctionsAreConstants(t *testing.T) {
	src := "PROGRAM P\nVAR\n  a, b, c, d : BOOL; n : DINT;\nEND_VAR\n" +
		"a := TAKEOVER_SCAN();\nb := HOT_TAKEOVER();\nc := PEER_READY();\nd := TAKEOVER_HOLD();\nn := TAKEOVERS();\nEND_PROGRAM\n"
	doc, diags, err := WriteST(src, Options{Controller: "C"})
	if err != nil || len(diags) > 0 {
		t.Fatalf("%v %v", err, joinDiags(diags))
	}
	f, err := l5x.Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	text := f.Controller.Programs[0].Routines[0].Text
	for _, want := range []string{"a := 0;", "b := 0;", "c := 0;", "d := 0;", "n := 0;"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}
