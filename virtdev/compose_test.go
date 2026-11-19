package virtdev

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/devprofile"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus"
)

// vendored writes a project folder with profile id vendored in profiles/
// (and its simulator), as naut devices add leaves it, and returns it.
func vendored(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, devprofile.ProjectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := devprofile.BuiltinFile(id + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := devprofile.ParseProfile(raw, id+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{id + ".yaml": raw}
	if p.Simulator != nil {
		if files[p.Simulator.Source], err = devprofile.BuiltinFile(p.Simulator.Source); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, devprofile.ProjectDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func conv() Instance {
	return Instance{Name: "CONV", Profile: "abb-acs355-fmbt21", Endpoint: "10.0.0.11"}
}

func writeSet(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A declared device is a virtual device without virtual-devices.yaml, and
// it simulates the project's vendored copy, not the built-in profile.
func TestComposeDeclaredSimulatesTheVendoredCopy(t *testing.T) {
	dir := vendored(t, "abb-acs355-fmbt21")
	pf := filepath.Join(dir, devprofile.ProjectFile("abb-acs355-fmbt21"))
	raw, err := os.ReadFile(pf)
	if err != nil {
		t.Fatal(err)
	}
	// The project's copy moved Actual1; the built-in one did not.
	edited := strings.Replace(string(raw), "table: holding, address: 4,", "table: holding, address: 40,", 1)
	if edited == string(raw) {
		t.Fatal("the fixture edit did not apply")
	}
	if err := os.WriteFile(pf, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Compose(dir, []Instance{conv()})
	if err != nil {
		t.Fatal(err)
	}
	p := s.Profile("CONV")
	if p.File() != pf {
		t.Errorf("CONV simulates %q, want the vendored %s", p.File(), pf)
	}
	pt, ok := p.Point("Actual1")
	if !ok || pt.Address != 40 {
		t.Errorf("Actual1 at %d, want the vendored copy's 40", pt.Address)
	}
	if ep, _ := s.Devices[0].modbusEndpoint(p); ep != "10.0.0.11:502" {
		t.Errorf("endpoint %q", ep)
	}
}

// virtual-devices.yaml adds listen and init to a declared device; every
// other entry is a device of its own, after the declared ones.
func TestComposeOverlay(t *testing.T) {
	dir := vendored(t, "abb-acs355-fmbt21")
	writeSet(t, dir, `devices:
  - { name: conv, listen: "127.0.0.1:15020", init: { Actual2: 7 } }
  - { name: RB1, profile: asi-275m, endpoint: 10.0.0.12 }
`)
	s, err := Compose(dir, []Instance{conv()})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 2 || s.Devices[0].Name != "CONV" || s.Devices[1].Name != "RB1" {
		t.Fatalf("devices: %+v", s.Devices)
	}
	d := s.Devices[0]
	if d.Listen != "127.0.0.1:15020" || d.Init["Actual2"] != 7 || d.Endpoint != "10.0.0.11" {
		t.Errorf("CONV: %+v", d)
	}
}

func TestComposeErrors(t *testing.T) {
	for _, tc := range []struct{ name, file, want string }{
		{"profile repeated", "devices: [{ name: CONV, profile: abb-acs355-fmbt21 }]\n", "may set only listen, init and opcua"},
		{"endpoint repeated", "devices: [{ name: CONV, endpoint: 10.0.0.99 }]\n", "may set only listen, init and opcua"},
		{"unit id repeated", "devices: [{ name: CONV, unit-id: 2 }]\n", "may set only listen, init and opcua"},
		{"params repeated", "devices: [{ name: CONV, params: {} }]\n", "may set only listen, init and opcua"},
		{"listed twice", "devices: [{ name: CONV, init: {} }, { name: CONV, listen: ':1' }]\n", "listed twice"},
		{"same endpoint as a declared device", "devices: [{ name: OTHER, profile: abb-acs355-fmbt21, endpoint: 10.0.0.11 }]\n", "both answer at 10.0.0.11:502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := vendored(t, "abb-acs355-fmbt21")
			writeSet(t, dir, tc.file)
			_, err := Compose(dir, []Instance{conv()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Compose: %v, want an error saying %q", err, tc.want)
			}
		})
	}
	t.Run("profile not vendored", func(t *testing.T) {
		if _, err := Compose(t.TempDir(), []Instance{conv()}); err == nil || !strings.Contains(err.Error(), "profiles/abb-acs355-fmbt21.yaml") {
			t.Errorf("Compose: %v", err)
		}
	})
	t.Run("profile is a path", func(t *testing.T) {
		d := conv()
		d.Profile = "../x/abb-acs355-fmbt21"
		if _, err := Compose(vendored(t, "abb-acs355-fmbt21"), []Instance{d}); err == nil || !strings.Contains(err.Error(), "is not a profile id") {
			t.Errorf("Compose: %v", err)
		}
	})
	t.Run("neither", func(t *testing.T) {
		if _, err := Compose(t.TempDir(), nil); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Compose: %v, want os.ErrNotExist", err)
		}
	})
	t.Run("an empty file without declared devices", func(t *testing.T) {
		dir := t.TempDir()
		writeSet(t, dir, "")
		if _, err := Compose(dir, nil); err == nil || !strings.Contains(err.Error(), "no devices") {
			t.Errorf("Compose: %v", err)
		}
	})
	t.Run("an empty file with declared devices", func(t *testing.T) {
		dir := vendored(t, "abb-acs355-fmbt21")
		writeSet(t, dir, "")
		if _, err := Compose(dir, []Instance{conv()}); err != nil {
			t.Errorf("Compose: %v", err)
		}
	})
}

// A declared device's vendored profile is the project's (the controller
// derives the device from it at load), so it is never a test fixture that
// naut build leaves out; its simulator, and a standalone device's own
// profile, are.
func TestComposeFilesKeepTheVendoredProfile(t *testing.T) {
	dir := vendored(t, "abb-acs355-fmbt21")
	own, err := devprofile.BuiltinFile("asi-275m.yaml")
	if err != nil {
		t.Fatal(err)
	}
	own = []byte(strings.Replace(string(own), "simulator:", "#simulator:", 1))
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), own, 0o644); err != nil {
		t.Fatal(err)
	}
	// A standalone device naming the same vendored file does not make it a fixture either.
	writeSet(t, dir, `devices:
  - { name: RB1, profile: mine.yaml, endpoint: 10.0.0.12 }
  - { name: SPARE, profile: profiles/abb-acs355-fmbt21.yaml, endpoint: 10.0.0.13 }
`)
	s, err := Compose(dir, []Instance{conv()})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range s.Files() {
		files[f] = true
	}
	if files[filepath.Join(dir, devprofile.ProjectFile("abb-acs355-fmbt21"))] {
		t.Errorf("the vendored profile is listed as a fixture: %v", s.Files())
	}
	for _, want := range []string{filepath.Join(dir, "mine.yaml"), filepath.Join(dir, devprofile.ProjectDir, "abb-drives.st"), filepath.Join(dir, FileName)} {
		if !files[want] {
			t.Errorf("%s is not listed as a fixture: %v", want, s.Files())
		}
	}
}

// wrapStub carries devices around a driver the in-process face cannot
// answer, or without the adapter's assembly.
type wrapStub struct {
	*nio.Memory
	inner nio.Driver
}

func (w wrapStub) Unwrap() nio.Driver { return w.inner }

type assemblingStub struct{ wrapStub }

func (assemblingStub) NewAssembly() func(nio.Values) { return func(nio.Values) {} }

// A driver that carries devices is answered only when it is a Modbus or
// OPC UA client driver under the device library's adapter: anything else is refused by
// name before a scan, not delivered without its structs.
func TestInProcessRefusesAnUnknownDevicesDriver(t *testing.T) {
	set, err := LoadSet(binFill)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := modbus.ParseManifest([]byte("sources: [{ id: CONV, host: 10.77.1.101, unitid: 1 }]\ntags: [{ name: S, source: CONV, table: holding, address: 3, format: uint16 }]\n"))
	if err != nil {
		t.Fatal(err)
	}
	md, err := modbus.New(mm)
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]nio.Driver{
		"not modbus underneath": assemblingStub{wrapStub{nio.NewMemory(), nio.NewMemory()}},
		"no assembly":           wrapStub{nio.NewMemory(), md},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewInProcess(set, acceptance.NewClock(), []nio.NamedDriver{{Name: "field", Driver: d}})
			if err == nil || !strings.Contains(err.Error(), "answer the devices: of a modbus or opcua-client driver only") {
				t.Errorf("NewInProcess: %v", err)
			}
		})
	}
}

// opcua: is an OPC UA device's simulation-only key: a declared device takes
// it from virtual-devices.yaml beside listen and init, and its wire face
// serves with that certificate store; a Modbus device has none.
func TestComposeOPCUAKeys(t *testing.T) {
	dir := vendored(t, "pepperl-fuchs-ice3")
	writeSet(t, dir, "devices:\n  - { name: PF1, opcua: { pki: server-pki } }\n")
	s, err := Compose(dir, []Instance{{Name: "PF1", Profile: "pepperl-fuchs-ice3", Endpoint: "10.0.0.21"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := opcuaPKI(s, s.Devices[0]); got != filepath.Join(dir, "server-pki") {
		t.Errorf("pki %q, want the project's server-pki", got)
	}
	dir = vendored(t, "abb-acs355-fmbt21")
	writeSet(t, dir, "devices:\n  - { name: CONV, opcua: { pki: server-pki } }\n")
	if _, err := Compose(dir, []Instance{conv()}); err == nil || !strings.Contains(err.Error(), "opcua: is an OPC UA device's") {
		t.Errorf("opcua: on a Modbus device: %v", err)
	}
}
