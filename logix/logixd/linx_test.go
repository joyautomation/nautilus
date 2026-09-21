package logixd

import (
	"os"
	"testing"
)

// The fixture is a real FactoryTalk Linx configuration with the client's
// controller name and addresses replaced. The topology — which is the part
// under test — is untouched: an Ethernet driver, a device on it, an
// emulated 1756 backplane, and a controller in a slot.
func TestParseLinxConfigFindsTheController(t *testing.T) {
	raw, err := os.ReadFile("testdata/RSLinxNG.xml")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := ParseLinxConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no comm paths found in a configuration that has one")
	}

	var found *CommPath
	for i := range paths {
		t.Logf("  %-40s %s", paths[i].Path, paths[i].Controller)
		if paths[i].Controller == "DemoLine" {
			found = &paths[i]
		}
	}
	if found == nil {
		t.Fatal("the controller in the backplane slot was not discovered")
	}

	// This exact string is what SetCommunicationsPath takes. It was verified
	// against a live controller — a wrong one does not fail cleanly, it
	// fails as "cannot go online", which reads like a controller fault.
	if want := `AB_ETH-1\10.0.0.5\Backplane\0`; found.Path != want {
		t.Errorf("path = %q, want %q", found.Path, want)
	}
	if found.Driver != "AB_ETH-1" {
		t.Errorf("driver = %q", found.Driver)
	}
}

// A driver with nothing browsed on it is a configured driver, not somewhere
// you can go online. Listing it would send someone down a dead end.
func TestParseLinxConfigSkipsEmptyDrivers(t *testing.T) {
	raw, err := os.ReadFile("testdata/RSLinxNG.xml")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := ParseLinxConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if p.Controller == "" {
			t.Errorf("emitted a path with no controller on it: %q", p.Path)
		}
		if p.Driver == "" {
			t.Errorf("emitted a path with no driver: %q", p.Path)
		}
	}
	// AB_ETHIP-1 and EmulateEthernet are configured but empty in this
	// fixture; neither should appear.
	for _, p := range paths {
		if p.Driver == "AB_ETHIP-1" || p.Driver == "EmulateEthernet" {
			t.Errorf("empty driver %q was listed as reachable", p.Driver)
		}
	}
}

// FT Linx writes UTF-16LE with a BOM and an encoding declaration Go's XML
// decoder will not honour. Both the conversion and the declaration rewrite
// have to happen or the file does not parse at all.
func TestParseLinxConfigHandlesUTF16(t *testing.T) {
	raw, err := os.ReadFile("testdata/RSLinxNG.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 2 || raw[0] != 0xff || raw[1] != 0xfe {
		t.Fatal("fixture lost its UTF-16 BOM; the test no longer covers what it claims")
	}
	if _, err := ParseLinxConfig(raw); err != nil {
		t.Fatalf("UTF-16: %v", err)
	}
}

func TestParseLinxConfigRejectsGarbage(t *testing.T) {
	if _, err := ParseLinxConfig([]byte{0xff, 0xfe, 0x01}); err == nil {
		t.Error("a truncated UTF-16 file should be an error")
	}
}
