package project

import (
	"strings"
	"testing"
	"testing/fstest"
)

// EIPOnlineTag names the companion exactly as Load will: a lone driver: is
// "eip", a drivers: entry its name: or the deduped "eip-2", reduced to an
// identifier; the driver is found by host (with or without a port), then by
// manifest; one eip driver is used even when the host differs; none, or no
// nautilus.yaml, is the default.
func TestEIPOnlineTagResolution(t *testing.T) {
	two := "drivers:\n" +
		"  - { type: modbus, manifest: m.yaml }\n" +
		"  - { type: eip, host: 10.0.0.1, manifest: a.yaml }\n" +
		"  - { type: eip, host: '10.0.0.2:44818', manifest: b.yaml }\n" +
		"  - { type: eip, name: Line 3, host: 10.0.0.3, manifest: c.yaml }\n"
	for _, tc := range []struct {
		name, manifest, host, file, want string
	}{
		{"no nautilus.yaml", "", "10.0.0.1", "", "eip__Online"},
		{"no eip driver", "driver: { type: modbus, manifest: m.yaml }\n", "10.0.0.1", "", "eip__Online"},
		{"lone driver: matches", "driver: { type: eip, host: 10.0.0.1, manifest: e.yaml }\n", "10.0.0.1", "", "eip__Online"},
		{"lone named driver, other host", "driver: { type: eip, name: press, host: 10.0.0.1, manifest: e.yaml }\n", "10.9.9.9", "", "press__Online"},
		{"first of several", two, "10.0.0.1", "", "eip__Online"},
		{"deduped second, port on the driver", two, "10.0.0.2", "", "eip_2__Online"},
		{"named, reduced to an identifier", two, "10.0.0.3:44818", "", "Line_3__Online"},
		{"by manifest", two, "", "./b.yaml", "eip_2__Online"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			if tc.manifest != "" {
				fsys["nautilus.yaml"] = &fstest.MapFile{Data: []byte(tc.manifest)}
			}
			got, how, err := EIPOnlineTag(fsys, tc.host, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q (%s), want %q", got, how, tc.want)
			}
		})
	}

	fsys := fstest.MapFS{"nautilus.yaml": &fstest.MapFile{Data: []byte(two)}}
	_, _, err := EIPOnlineTag(fsys, "10.9.9.9", "")
	if err == nil || !strings.Contains(err.Error(), "eip-2 (host 10.0.0.2:44818)") || !strings.Contains(err.Error(), "--name") {
		t.Errorf("ambiguous project: err = %v, want the drivers listed and --name", err)
	}
}

// The name the import resolves is the name Load gives the driver's
// companion, so the generated tag file and the running driver agree.
func TestEIPOnlineTagAgreesWithLoad(t *testing.T) {
	files := fsys(multiBase() + "drivers:\n" +
		"  - { type: eip, host: 10.0.0.1, manifest: e.yaml }\n" +
		"  - { type: eip, host: 10.0.0.2, manifest: e2.yaml }\n")
	files["e.yaml"] = &fstest.MapFile{Data: []byte("tags:\n  - { name: A, device: A, type: REAL }\n")}
	files["e2.yaml"] = &fstest.MapFile{Data: []byte("tags:\n  - { name: B, device: B, type: REAL }\n")}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := EIPOnlineTag(files, "10.0.0.2", "")
	if err != nil {
		t.Fatal(err)
	}
	type owner interface{ InputNames() []string }
	found := false
	for _, n := range p.Runtime.Driver.(owner).InputNames() {
		found = found || n == want
	}
	if !found {
		t.Errorf("Load's drivers deliver %v; the import's %q is not among them", p.Runtime.Driver.(owner).InputNames(), want)
	}
}
