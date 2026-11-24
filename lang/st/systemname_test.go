package st

import (
	"strings"
	"testing"
)

// A POU may not be named as a nautilus system function (#710 (b)): a
// call would bind to it, and the program would read its own answer for
// the runtime's. FIRST_SCAN stays shadowable, as before.
func TestAPOUNamedAsASystemFunctionIsRefused(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		{"FUNCTION peer_ready : BOOL\npeer_ready := TRUE;\nEND_FUNCTION\nPROGRAM P\nVAR x : BOOL; END_VAR\nx := PEER_READY();\nEND_PROGRAM", `FUNCTION "peer_ready" has the name of a nautilus system function (PEER_READY())`},
		{"FUNCTION_BLOCK Takeovers\nVAR_INPUT a : BOOL; END_VAR\nEND_FUNCTION_BLOCK\nPROGRAM P\nVAR t : Takeovers; END_VAR\nt(a := TRUE);\nEND_PROGRAM", `FUNCTION_BLOCK "Takeovers" has the name of a nautilus system function (TAKEOVERS())`},
		{"PROGRAM Takeover_Scan\nVAR x : BOOL; END_VAR\nx := TRUE;\nEND_PROGRAM", `PROGRAM "Takeover_Scan" has the name of a nautilus system function (TAKEOVER_SCAN())`},
	} {
		_, err := lowerSrc(c.src)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q:\n got %v\nwant %q", strings.SplitN(c.src, "\n", 2)[0], err, c.want)
		}
	}
	if _, err := lowerSrc("FUNCTION first_scan : BOOL\nfirst_scan := TRUE;\nEND_FUNCTION\nPROGRAM P\nVAR x : BOOL; END_VAR\nx := FIRST_SCAN();\nEND_PROGRAM"); err != nil {
		t.Errorf("a user FIRST_SCAN, as before: %v", err)
	}
}
