package main

// `naut check` and channels: (IEC 61131-5 READ/WRITE): an ID that names no
// declared channel is an error with the fix, and a malformed channels: entry
// is the load error it would be at run time.

import (
	"strings"
	"testing"
)

func channelProject(channels, id string) map[string]string {
	return map[string]string{
		"nautilus.yaml": "tasks:\n  - program: program.st\ntags:\n  - {name: Go, type: BOOL, role: setpoint}\n" + channels,
		"program.st": `PROGRAM Main
VAR_EXTERNAL Go : BOOL; END_VAR
VAR rd : READ; X : REAL; END_VAR
rd(REQ := Go, ID := '` + id + `', VAR_1 := 'Remote', RD_1 := X);
END_PROGRAM`,
	}
}

const line2 = "channels:\n  - name: line2\n    type: cip\n    host: 192.0.2.12\n    slot: 0\n    timeout: 2s\n"

func TestCheckChannels(t *testing.T) {
	if out, code := checkIn(t, channelProject(line2, "line2")); code != 0 {
		t.Fatalf("exit = %d, want 0 for a declared channel\n%s", code, out)
	}
	out, code := checkIn(t, channelProject(line2, "line3"))
	if code != 1 || !strings.Contains(out, `channel "line3"`) {
		t.Fatalf("exit = %d, want 1 naming the undeclared channel\n%s", code, out)
	}
	for _, c := range []struct{ channels, want string }{
		{"channels:\n  - name: a\n    type: cip\n", "host is required"},
		{"channels:\n  - name: a\n    type: modbus\n    host: x\n", "unknown type"},
		{"channels:\n  - type: cip\n    host: x\n", "name is required"},
		{"channels:\n  - {name: a, type: cip, host: x}\n  - {name: A, type: cip, host: y}\n", "another channel has this name"},
	} {
		out, code := checkIn(t, channelProject(c.channels, "a"))
		if code != 1 || !strings.Contains(out, c.want) {
			t.Errorf("%q: exit = %d, want 1 with %q\n%s", c.channels, code, c.want, out)
		}
	}
}
