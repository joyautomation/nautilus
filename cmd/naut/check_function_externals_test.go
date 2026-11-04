package main

import (
	"strings"
	"testing"
)

// funcBlocks binds every tag through user FUNCTIONs' VAR_EXTERNAL: Scale
// reads the input and the setpoint and writes the output, and calls Offset,
// which reads Bias.
const funcBlocks = `FUNCTION Offset : REAL
VAR_EXTERNAL Bias : REAL; END_VAR
Offset := Bias;
END_FUNCTION

FUNCTION Scale : BOOL
VAR_EXTERNAL RawLevel : REAL; Gain : REAL; PumpCmd : REAL; END_VAR
PumpCmd := RawLevel * Gain + Offset();
Scale := TRUE;
END_FUNCTION
`

const funcProgram = `PROGRAM Main
VAR ok : BOOL; END_VAR
ok := Scale();
END_PROGRAM`

// A tag bound only inside a user FUNCTION is bound by the program that
// calls it (nautilus#590): no "no program binds" warning, and a read of an
// undeclared tag inside the FUNCTION is the same error as one in the
// program's own body.
func TestCheckSeesTagsBoundThroughAFunction(t *testing.T) {
	const manifest = `
tasks:
  - program: program.st
tags:
  - { name: RawLevel, role: input }
  - { name: Gain, role: setpoint, init: 2.0 }
  - { name: Bias, role: setpoint, init: 0.0 }
  - { name: PumpCmd, role: output }
`
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": manifest,
		"blocks.st":     funcBlocks,
		"program.st":    funcProgram,
	})
	if code != 0 || strings.Contains(out, ": warning:") || strings.Contains(out, ": error:") {
		t.Errorf("tags bound through a FUNCTION were reported (exit %d):\n%s", code, out)
	}

	// Bias is read inside Offset, two calls down, and declared nowhere.
	out, code = checkIn(t, map[string]string{
		"nautilus.yaml": strings.Replace(manifest, "  - { name: Bias, role: setpoint, init: 0.0 }\n", "", 1),
		"blocks.st":     funcBlocks,
		"program.st":    funcProgram,
	})
	if code != 1 || !strings.Contains(out, `error: the programs read "Bias", which`) {
		t.Errorf("an undeclared tag read inside a FUNCTION was not an error (exit %d):\n%s", code, out)
	}
}

// An FB instance a FUNCTION declares binds its VAR_EXTERNAL for the
// program that calls the FUNCTION; and a FUNCTION declaring the program's
// tag with another type is an error naming both.
func TestCheckFollowsAnFBInsideAFunction(t *testing.T) {
	blocks := `FUNCTION_BLOCK Holder
VAR_EXTERNAL Gain : REAL; END_VAR
VAR_OUTPUT Q : REAL; END_VAR
Q := Gain;
END_FUNCTION_BLOCK

FUNCTION Wrap : REAL
VAR h : Holder; END_VAR
h();
Wrap := h.Q;
END_FUNCTION
`
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML + "  - { name: Gain, role: setpoint, init: 2.0 }\n",
		"blocks.st":     blocks,
		"program.st":    programBinding("", "Actuator := Wrap();\n"),
	})
	if code != 0 || strings.Contains(out, ": warning:") || strings.Contains(out, ": error:") {
		t.Errorf("a tag bound by an FB inside a FUNCTION was reported (exit %d):\n%s", code, out)
	}

	out, code = checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML,
		"blocks.st": `FUNCTION Peek : INT
VAR_EXTERNAL Sensor : INT; END_VAR
Peek := Sensor;
END_FUNCTION
`,
		"program.st": programBinding("", "Actuator := INT_TO_REAL(Peek());\n"),
	})
	if code != 1 || !strings.Contains(out, "tag Sensor is REAL in Main but INT in Peek") {
		t.Errorf("a FUNCTION declaring Sensor INT against the program's REAL was not an error (exit %d):\n%s", code, out)
	}
}

// A FUNCTION nothing calls binds nothing: its tag is still reported.
func TestCheckWarnsWhenFunctionNotCalled(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML + "  - { name: Gain, role: setpoint, init: 2.0 }\n",
		"blocks.st": `FUNCTION Scale : REAL
VAR_EXTERNAL Gain : REAL; END_VAR
Scale := Gain;
END_FUNCTION
`,
		"program.st": programBinding("", ""),
	})
	if code != 0 || !strings.Contains(out, `"Gain", which no program binds`) {
		t.Errorf("an uncalled FUNCTION's tag was not reported unbound (exit %d):\n%s", code, out)
	}
}
