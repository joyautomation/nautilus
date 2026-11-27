package main

import (
	"strings"
	"testing"
)

// naut check says, in one warning, that an auth: block with an OPC UA
// server and anonymous: none leaves an OPC UA client with no user or
// mapped certificate reading nothing (#1131), and how to bring it back.
func TestCheckWarnsWhenOPCUAClientsLoseAnonymousReads(t *testing.T) {
	files := func(opcua, authBlock string) map[string]string {
		return map[string]string{
			"nautilus.yaml": "tasks:\n  - program: program.st\n" + opcua + authBlock + "tags:\n  - { name: Level, role: input, type: REAL }\n",
			"program.st":    "PROGRAM Main\nVAR_EXTERNAL Level : REAL; END_VAR\nEND_PROGRAM",
		}
	}
	const ua = "opcua:\n  endpoint: opc.tcp://127.0.0.1:0\n"
	const warn = "OPC UA server needs `read`"
	out, code := checkIn(t, files(ua, "auth:\n  token-role: operator\n"))
	if code != 0 || !strings.Contains(out, warn) || !strings.Contains(out, "anonymous: viewer") || !strings.Contains(out, "auth.opcua-certificates") {
		t.Errorf("auth + OPC UA + anonymous none (%d):\n%s", code, out)
	}
	for name, f := range map[string]map[string]string{
		"anonymous: viewer": files(ua, "auth:\n  anonymous: viewer\n"),
		"no OPC UA server":  files("", "auth:\n  token-role: operator\n"),
		"no auth block":     files(ua, ""),
	} {
		if out, _ := checkIn(t, f); strings.Contains(out, warn) {
			t.Errorf("%s warned:\n%s", name, out)
		}
	}
}
