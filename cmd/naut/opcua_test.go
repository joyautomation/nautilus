package main

import (
	"strings"
	"testing"
)

// `naut check` refuses an opcua: section it could not serve, naming the
// key — the same place a bad sparkplug: or driver: section is caught.
func TestCheckRefusesBadOPCUAEndpoint(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML + "opcua:\n  endpoint: http://localhost:4840\n",
		"program.st":    programBinding("", ""),
	})
	if code == 0 {
		t.Fatalf("check passed a bad opcua endpoint:\n%s", out)
	}
	if !strings.Contains(out, "opcua.endpoint") {
		t.Errorf("check does not name the key:\n%s", out)
	}
	out, code = checkIn(t, map[string]string{
		"nautilus.yaml": checkManifestYAML + "opcua:\n  endpoint: opc.tcp://0.0.0.0:4840\n",
		"program.st":    programBinding("", ""),
	})
	if code != 0 {
		t.Fatalf("check refused a good opcua endpoint:\n%s", out)
	}
}
