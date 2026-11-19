package main

import (
	"strings"
	"testing"
)

// A plcnext manifest whose outputs have no safe state when naut stops gets
// a warning from check, never an error. The bench showed the firmware does
// not zero the outputs when naut's session drops (#863): only the shim's
// output watchdog, on transport shm, does. So transport grpc with an output
// binding, or shm with output-watchdog: 0, is warned about. shm with the
// default 1 s watchdog, and a manifest with no outputs, stay quiet.
func TestCheckWarnsOnPLCnextOutputsWithoutASafeState(t *testing.T) {
	const warn = "if naut stops, the 1 output binding(s) HOLD their last values"
	for _, tc := range []struct {
		name, route string
		outputs     bool
		want        string // "" = no warning
	}{
		{"grpc with an output", "transport: grpc\n", true, "transport grpc: " + warn},
		{"shm with watchdog 0", "transport: shm\noutput-watchdog: 0s\n", true, "transport shm with output-watchdog 0s: " + warn},
		{"shm with the default watchdog", "transport: shm\n", true, ""},
		{"grpc with no outputs", "transport: grpc\n", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := "  - { name: DI_00, dir: in, offset: 0, format: \"ch:1\" }\n"
			tags := "  - { name: DI_00, role: input }\n"
			body := "X := DI_00;"
			vars := "DI_00 : BOOL; X : BOOL;"
			if tc.outputs {
				bindings += "  - { name: DO_00, dir: out, offset: 0, format: \"ch:1\" }\n"
				tags += "  - { name: DO_00, role: output }\n"
				body = "DO_00 := DI_00;"
				vars = "DI_00 : BOOL; DO_00 : BOOL;"
			} else {
				tags += "  - { name: X, role: state, init: false }\n"
			}
			out, code := checkIn(t, map[string]string{
				"nautilus.yaml":         "tasks:\n  - program: program.st\ndriver:\n  type: plcnext\n  manifest: plcnext_manifest.yaml\ntags:\n" + tags,
				"plcnext_manifest.yaml": tc.route + "tags:\n" + bindings,
				"program.st":            "PROGRAM Main\nVAR_EXTERNAL " + vars + " END_VAR\n" + body + "\nEND_PROGRAM",
			})
			if code != 0 {
				t.Fatalf("a safe-state warning must not fail the check (%d):\n%s", code, out)
			}
			got := strings.Contains(out, "HOLD their last values")
			switch {
			case tc.want != "" && !strings.Contains(out, "warning: "+tc.want):
				t.Errorf("want the warning %q:\n%s", tc.want, out)
			case tc.want == "" && got:
				t.Errorf("want no safe-state warning:\n%s", out)
			}
		})
	}
}
