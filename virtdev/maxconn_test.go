package virtdev

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/devprofile"
)

// withLimit writes the built-in profile id into dir as file, documenting
// n Modbus connections.
func withLimit(t *testing.T, dir, id, file, doc string, n int) {
	t.Helper()
	raw, err := devprofile.BuiltinFile(id + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "modbus:\n  port: 502\n", "modbus:\n  port: 502\n  max-connections: "+string(rune('0'+n))+"\n  max-connections-doc: \""+doc+" §1\"\n", 1)
	edited = strings.Replace(edited, "simulator:", "#simulator:", 1)
	if edited == string(raw) {
		t.Fatal("the fixture edit did not apply")
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The wire face serves as many connections as the profile documents; a
// gateway's devices share the smallest of their limits, a device of its
// own keeps its own, and a device documenting none has no limit.
func TestWireFaceConnectionLimits(t *testing.T) {
	dir := t.TempDir()
	withLimit(t, dir, "abb-acs355-fmbt21", "three.yaml", "fena", 3)
	withLimit(t, dir, "asi-275m", "two.yaml", "measured", 2)
	writeSet(t, dir, `devices:
  - { name: A, profile: three.yaml, endpoint: 10.0.0.5, unit-id: 1 }
  - { name: B, profile: two.yaml,   endpoint: 10.0.0.5, unit-id: 2 }
  - { name: C, profile: three.yaml, endpoint: 10.0.0.6 }
  - { name: D, profile: asi-275m,   endpoint: 10.0.0.7 }
`)
	s, err := LoadSet(dir)
	if err != nil {
		t.Fatal(err)
	}
	run, err := Start(context.Background(), s, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Stop()
	got := map[string]int{}
	for _, e := range run.Endpoints {
		got[e.Device] = e.MaxConnections
	}
	if want := map[string]int{"A": 2, "B": 2, "C": 3, "D": 0}; !mapsEqual(got, want) {
		t.Errorf("limits %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
