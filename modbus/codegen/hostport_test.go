package codegen

import (
	"strings"
	"testing"
)

// F-18 (#426): a host that carries a port ("127.0.0.1:5020", the address
// `naut modbus serve` listens on) must land as host + port, not as a host the
// driver later dials as "127.0.0.1:5020:502".
func TestGenerateSplitsHostPort(t *testing.T) {
	src := `
devices:
  d:
    port: 1502
    registers:
      - {tag: A, address: 0}
instances:
  - {id: X, type: d, host: "127.0.0.1:5020"}
  - {id: Y, type: d, host: "[::1]:5021"}
  - {id: Z, type: d, host: "10.0.0.9"}
  - {id: W, type: d, host: "10.0.0.8:5022", port: 5022}
`
	out := generate(t, src, Options{})
	want := map[string]struct {
		host string
		port int
	}{"X": {"127.0.0.1", 5020}, "Y": {"::1", 5021}, "Z": {"10.0.0.9", 1502}, "W": {"10.0.0.8", 5022}}
	for _, s := range out.Manifest.Sources {
		w := want[s.ID]
		if s.Host != w.host || s.Port != w.port {
			t.Errorf("%s: got host %q port %d, want %q %d", s.ID, s.Host, s.Port, w.host, w.port)
		}
	}
}

func TestParseRejectsConflictingHostPort(t *testing.T) {
	src := `
devices:
  d:
    registers:
      - {tag: A, address: 0}
instances:
  - {id: X, type: d, host: "10.0.0.8:5022", port: 5023}
`
	if _, err := ParseDeviceMap([]byte(src)); err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("host:port disagreeing with port: was not refused: %v", err)
	}
}
