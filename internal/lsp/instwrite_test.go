package lsp

import (
	"strings"
	"testing"
)

const instWriteManifest = `name: d
tasks:
  - program: %s
    scan: 100ms
wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}" }
    members:
      Running: { from: drive, table: coil, address: 0 }
      Start:   { to: drive, table: coil, address: 10 }
instances:
  - { name: P101, type: Pump, params: { endpoint: 192.0.2.1 } }
`

const instWriteTypes = "TYPE Pump : STRUCT\n  Running : BOOL;\n  SpeedSP : REAL := 50.0;\n  Start : BOOL;\nEND_STRUCT; END_TYPE\n"

const instWriteLib = "FUNCTION_BLOCK Reset\n  VAR_IN_OUT P : Pump; END_VAR\n  P.Running := FALSE;\nEND_FUNCTION_BLOCK\n"

// The editor shows the call-site write check on the line (#1458 slice 7):
// a direct assignment to a from: member, and a block that assigns one, are
// errors with the fix in the message; a command and the same block on a
// local struct are not. ST, FBD and LD sources land on their own lines.
func TestInstanceWriteDiagnostics(t *testing.T) {
	for _, c := range []struct {
		name, file, src string
		line            int
		want            string
	}{
		{"direct", "main.st", "PROGRAM main\nVAR r : BOOL; END_VAR\nP101.Start := TRUE;\nP101.Running := r;\nEND_PROGRAM\n", 3,
			"P101.Running is read from the device (from:), so the program cannot assign it; remove the assignment, or wire the member as memory:"},
		{"through a block", "main.st", "PROGRAM main\nVAR b : Reset; l : Pump; END_VAR\nb(P := l);\nb(P := P101);\nEND_PROGRAM\n", 3,
			"calling Reset with P101 would assign P101.Running, which is read from the device (from:): Reset assigns P.Running (|lib/reset.st:3)"},
		{"FBD", "main.fbd", "PROGRAM main\nVAR\n  r : BOOL;\nEND_VAR\nFBD\n  NETWORK 'x'\n  P101.Running := AND(r, TRUE)\nEND_FBD\nEND_PROGRAM\n", 6,
			"P101.Running is read from the device (from:)"},
		{"LD", "main.ld", "PROGRAM main\nVAR\n  r : BOOL;\nEND_VAR\nLD\n  RUNG one (* why *)\n    r ( P101.Running )\nEND_LD\nEND_PROGRAM\n", 5,
			"P101.Running is read from the device (from:)"},
	} {
		files := map[string]string{
			"nautilus.yaml": strings.Replace(instWriteManifest, "%s", c.file, 1),
			c.file:          c.src,
			"types.st":      instWriteTypes,
			"lib/reset.st":  instWriteLib,
		}
		_, paths := refProject(t, files)
		s := startSession(t)
		s.recvResponse(s.send("initialize", map[string]any{}, true))
		d := s.openDiags(pathToURI(paths[c.file]), c.src)
		var got []Diagnostic
		for _, x := range d {
			if x.Severity == SeverityError {
				got = append(got, x)
			}
		}
		if len(got) != 1 {
			t.Errorf("%s: diagnostics = %+v", c.name, d)
			continue
		}
		if got[0].Range.Start.Line != c.line {
			t.Errorf("%s: line %d, want %d: %q", c.name, got[0].Range.Start.Line, c.line, got[0].Message)
		}
		for _, frag := range strings.Split(c.want, "|") {
			if !strings.Contains(got[0].Message, frag) {
				t.Errorf("%s: message %q lacks %q", c.name, got[0].Message, frag)
			}
		}
	}
}
