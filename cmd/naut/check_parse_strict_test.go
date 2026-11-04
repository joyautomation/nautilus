package main

import (
	"strings"
	"testing"
)

// Every language reaches ST's parser (LD, FBD and SFC transpile to it), so
// what it used to skip without a word now fails `naut check` in all four
// (nautilus#609): a POU with no END_PROGRAM and a (* comment that never
// closes. Two operands in a row fail wherever an ST expression is written:
// an ST body and an SFC action (LD and FBD already refused them in their
// own argument lists).
func TestCheckRejectsUnclosedPOUAndComment(t *testing.T) {
	manifest := func(file string) string {
		return "tasks:\n  - program: " + file + "\ntags:\n" +
			"  - { name: Sensor, role: input, type: REAL, init: 0.0 }\n" +
			"  - { name: Setpoint, role: setpoint, type: REAL, init: 50.0 }\n" +
			"  - { name: Actuator, role: output, type: REAL, init: 0.0 }\n" +
			"  - { name: Alarm, role: output, type: BOOL }\n"
	}
	srcs := map[string]string{
		"st":  "PROGRAM P\nActuator := Sensor;\nEND_PROGRAM\n",
		"ld":  "PROGRAM P\nLD\n  RUNG high\n    GT(Sensor, Setpoint) ( Alarm )\nEND_LD\nEND_PROGRAM\n",
		"fbd": "PROGRAM P\nFBD\n  Actuator := LIMIT(0.0, Sensor, 100.0)\nEND_FBD\nEND_PROGRAM\n",
		"sfc": "PROGRAM P\nSFC\n  INITIAL_STEP Track:\n    N  Upd;\n  END_STEP\n  ACTION Upd:\n    Actuator := Sensor;\n  END_ACTION\nEND_SFC\nEND_PROGRAM\n",
	}
	for _, lang := range []string{"st", "ld", "fbd", "sfc"} {
		file := "program." + lang
		src := srcs[lang]
		if out, code := checkIn(t, map[string]string{"nautilus.yaml": manifest(file), file: src}); code != 0 {
			t.Fatalf("%s control program rejected (exit %d):\n%s", lang, code, out)
		}
		cases := map[string]struct{ src, want string }{
			"no END_PROGRAM":   {strings.Replace(src, "END_PROGRAM\n", "", 1), "PROGRAM P has no END_PROGRAM"},
			"unclosed comment": {src + "(* open\n", "unterminated block comment opened"},
		}
		if lang == "st" || lang == "sfc" {
			cases["two operands"] = struct{ src, want string }{
				strings.Replace(src, "Actuator := Sensor;", "Actuator := Sensor Setpoint;", 1),
				`expected an operator or ';' after the expression, got "Setpoint"`,
			}
		}
		for name, tc := range cases {
			out, code := checkIn(t, map[string]string{"nautilus.yaml": manifest(file), file: tc.src})
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Errorf("%s, %s: exit %d, want 1 and %q:\n%s", lang, name, code, tc.want, out)
			}
		}
	}
}
