package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #893's repro: a tag with `init: 3` and no type is a REAL, and as a FOR's
// end it passed `naut check` and then ran the loop zero times. Check now
// refuses it at the bound, naming the conversion; typed DINT, it passes.
func TestCheckRefusesARealForBound(t *testing.T) {
	prog := "PROGRAM p\nVAR i : DINT; c : DINT; END_VAR\nc := 0;\nFOR i := 1 TO N DO\n    c := c + 1;\nEND_FOR;\nCount := c;\nEND_PROGRAM\n"
	for _, c := range []struct {
		nType string
		code  int
		want  string
	}{
		{"", 1, "p.st:4:15: FOR i end: N is REAL, and a FOR's bounds and step are integers like its variable; write REAL_TO_DINT(N)"},
		{", type: DINT", 0, "0 with errors"},
	} {
		dir := t.TempDir()
		manifest := "name: forreal\ntasks:\n  - program: p.st\n    scan: 100ms\ntags:\n" +
			"  - { name: N, role: setpoint, init: 3" + c.nType + " }\n" +
			"  - { name: Count, role: state, type: DINT, init: 0 }\n"
		for n, body := range map[string]string{"nautilus.yaml": manifest, "p.st": prog} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out, code := capture(t, func() int { return runCheck([]string{dir}) })
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("N%s: naut check = %d\n%s\nwant %d and %q", c.nType, code, out, c.code, c.want)
		}
	}
}
