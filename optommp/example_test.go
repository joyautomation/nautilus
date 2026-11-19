package optommp

import (
	"os"
	"testing"
	"time"
)

// The bench example drives real outputs, so it teaches the safe setting:
// the I/O unit's communication watchdog is on, and the outputs go to their
// watchdog values whenever nautilus stops writing them (a stop, a crash, a
// hung scan). The bench unit runs this manifest as it is.
func TestEPICBenchExampleSetsTheWatchdog(t *testing.T) {
	raw, err := os.ReadFile("../examples/epic-bench/optommp_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Watchdog == nil || time.Duration(*m.Watchdog) != time.Second {
		t.Errorf("examples/epic-bench watchdog = %v, want 1s", m.Watchdog)
	}
	if m.Host != "127.0.0.1" {
		t.Errorf("examples/epic-bench host = %q, want 127.0.0.1 (it runs on the EPIC itself)", m.Host)
	}
}
