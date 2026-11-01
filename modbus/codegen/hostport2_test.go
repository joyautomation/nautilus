package codegen

import (
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/modbus"
)

// A bracketed IPv6 host with no port is the bare address, and the manifest
// written for it reads back to the same source (an unquoted [::1] is a YAML
// list).
func TestGenerateBracketedHostRoundTrips(t *testing.T) {
	src := `
devices:
  d:
    registers:
      - {tag: A, address: 0}
instances:
  - {id: X, type: d, host: "[::1]"}
  - {id: Y, type: d, host: "::1", port: 5021}
`
	out := generate(t, src, Options{})
	for _, s := range out.Manifest.Sources {
		if s.Host != "::1" {
			t.Errorf("%s: host %q, want ::1", s.ID, s.Host)
		}
	}
	yml := string(ManifestYAML(out.Manifest, "naut modbus import"))
	if strings.Contains(yml, "host: [") {
		t.Errorf("host written unquoted:\n%s", yml)
	}
	back, err := modbus.ParseManifest([]byte(yml))
	if err != nil {
		t.Fatalf("rendered manifest does not read back: %v\n%s", err, yml)
	}
	for _, s := range back.Sources {
		if s.Host != "::1" {
			t.Errorf("round trip %s: host %q", s.ID, s.Host)
		}
	}
	if got := back.Sources[0].Addr(); got != "[::1]:502" {
		t.Errorf("Addr = %q, want [::1]:502", got)
	}
}
