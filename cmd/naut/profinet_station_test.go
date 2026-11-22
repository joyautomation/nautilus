package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/siemens/station"
	"github.com/joyautomation/nautilus/siemens/tiad"
	"github.com/joyautomation/nautilus/siemens/tiad/tiadtest"
)

// benchDevice is a profinet_device.yaml with two slots. Slot 2's subslots
// are listed out of order, so the lowest one must be found, not the first.
const benchDevice = `interface: eth1
station: naut-1
vendor-id: 0x1234
device-id: 0x0001
min-cycle: 8ms
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 4, outputs: 1}
  - slot: 2
    module-ident: 0x00000102
    subslots:
      - {subslot: 3, submodule-ident: 0x00000003, inputs: 2}
      - {subslot: 2, submodule-ident: 0x00000002, outputs: 2}
tags:
  - {name: Level, slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}
  - {name: Fill,  slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool}
`

var stationDate = time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)

func writeManifest(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func benchCPU() station.CPU {
	return station.CPU{Name: "PLC_1", Order: "6ES7 510-1DK03-0AB0", Firmware: "V3.1",
		Address: "192.0.2.10", SubnetMask: "255.255.255.0", ProfinetName: "plc-1"}
}

func tiadCPU(c station.CPU) tiad.CPU { return stationCPU(c) }

// TestExpandProfinetReference expands a reference into exactly the device
// #814's mapping names, and the GSDML it installs is the one
// `naut profinet gsdml` writes.
func TestExpandProfinetReference(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "devices/profinet_device.yaml", benchDevice)
	p := &station.Profinet{UpdateTime: "32ms", Devices: []station.Device{
		{ProfinetManifest: "devices/profinet_device.yaml", Address: "192.0.2.20"},
		{GSDML: "GSDML-V2.4-Other-Io-20261117.xml", DAP: "DAP_1", Name: "other-io", Address: "192.0.2.30",
			UpdateTime: "64ms", Modules: []station.DeviceModule{{Slot: 1, Subslot: 1, Module: "ID_X", InputStart: intp(7)}}},
	}}
	got, files, err := expandProfinet(dir, benchCPU(), p, stationDate)
	if err != nil {
		t.Fatal(err)
	}
	const name = "GSDML-V2.35-nautilus-naut-1-20261117.xml"
	want := &tiad.Profinet{UpdateTime: "32ms", Devices: []tiad.Device{
		{GSDML: name, DAP: "IDD_1", Name: "naut-1", Address: "192.0.2.20", UpdateTime: "32ms",
			Modules: []tiad.DeviceModule{{Slot: 1, Subslot: 1, Module: "IDM_Mod1"}, {Slot: 2, Subslot: 2, Module: "IDM_Mod2"}}},
		{GSDML: "GSDML-V2.4-Other-Io-20261117.xml", DAP: "DAP_1", Name: "other-io", Address: "192.0.2.30",
			UpdateTime: "64ms", Modules: []tiad.DeviceModule{{Slot: 1, Subslot: 1, Module: "ID_X", InputStart: intp(7)}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded:\n got %+v\nwant %+v", got, want)
	}

	if len(files) != 1 || files[0].Name != name {
		t.Fatalf("files: %+v", files)
	}
	out := t.TempDir()
	if code := runProfinet([]string{"gsdml", "-o", out, "--date", "20261117", filepath.Join(dir, "devices/profinet_device.yaml")}); code != 0 {
		t.Fatalf("naut profinet gsdml exit %d", code)
	}
	byHand, err := os.ReadFile(filepath.Join(out, name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files[0].Body, byHand) {
		t.Fatal("the deploy's GSDML differs from the one naut profinet gsdml writes")
	}
	for _, id := range []string{`ID="IDD_1"`, `ID="IDM_Mod1"`, `ID="IDM_Mod2"`} {
		if !bytes.Contains(files[0].Body, []byte(id)) {
			t.Errorf("GSDML lacks %s, which the station names", id)
		}
	}
}

// TestExpandedStationThroughTiad walks the bench's P1-P3 against the tiad
// fake: the expanded station is one tiad accepts, its GSDML installs, and
// it compiles and dry-runs, loading nothing.
func TestExpandedStationThroughTiad(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeManifest(t, dir, "profinet_device.yaml", benchDevice)
	cpu := benchCPU()
	pn, files, err := expandProfinet(dir, cpu, &station.Profinet{UpdateTime: "16ms", Devices: []station.Device{
		{ProfinetManifest: "profinet_device.yaml", Address: "192.0.2.20"},
	}}, stationDate)
	if err != nil {
		t.Fatal(err)
	}
	st := tiad.Station{CPU: tiadCPU(cpu), Profinet: pn}
	if err := st.Validate(); err != nil {
		t.Fatalf("tiad refuses the expanded station: %v", err)
	}

	f := tiadtest.New()
	f.Targets = []string{cpu.Address}
	_, c := f.Serve(t)
	for _, g := range files { // P1
		if _, err := c.PutFile(ctx, "gsd/"+g.Name, g.Body); err != nil {
			t.Fatal(err)
		}
		if _, err := c.InstallGSD(ctx, "gsd/"+g.Name); err != nil {
			t.Fatal(err)
		}
	}
	// The default protects the configuration: its password comes from
	// the secret store, here a test value.
	if st.CPU.ConfigurationProtection != tiad.ProtectPassword {
		t.Fatalf("a CPU naming no configuration protection expands to %q, want password", st.CPU.ConfigurationProtection)
	}
	sec := tiad.Secrets{ConfigurationPassword: tiadtest.BenchConfigurationPassword, FullAccessPassword: tiadtest.BenchFullAccessPassword}
	if _, err := c.CreateProject(ctx, "bench", st, tiad.Secrets{FullAccessPassword: tiadtest.BenchFullAccessPassword}); tiad.Kind(err) != tiad.KindBadRequest ||
		!strings.Contains(err.Error(), "secrets.configurationPassword is empty") {
		t.Fatalf("a protected station without its password: %v", err)
	}
	s, err := c.CreateProject(ctx, "bench", st, sec) // P2
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if held, _ := f.Station(s.ID); !reflect.DeepEqual(held, st) {
		t.Fatalf("tiad holds %+v, want %+v", held, st)
	}
	if r, err := s.Compile(ctx, true, true); err != nil || r.Result != "success" { // P3
		t.Fatalf("compile: %+v %v", r, err)
	}
	dr, err := s.Download(ctx, tiad.DownloadRequest{Address: cpu.Address, Hardware: true, Announced: true, DryRun: true, Secrets: &sec})
	if err != nil || !dr.DryRun || dr.Downloaded || len(f.Downloads()) != 0 {
		t.Fatalf("dry run: %+v %v, downloads %d", dr, err, len(f.Downloads()))
	}
}

// TestExpandProfinetRefuses covers the rules only an expansion can see.
// Every case also proves the problem is reported against its device.
func TestExpandProfinetRefuses(t *testing.T) {
	ref := func(path, addr string) station.Device {
		return station.Device{ProfinetManifest: path, Address: addr}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, dir string)
		p     station.Profinet
		want  string
	}{
		{name: "outside the project",
			p:    station.Profinet{Devices: []station.Device{ref("../profinet_device.yaml", "192.0.2.20")}},
			want: `profinet.devices[0].profinet-manifest: "../profinet_device.yaml" is outside the project`},
		{name: "a symlink out of the project",
			setup: func(t *testing.T, dir string) {
				outside := t.TempDir()
				writeManifest(t, outside, "profinet_device.yaml", benchDevice)
				if err := os.Symlink(filepath.Join(outside, "profinet_device.yaml"), filepath.Join(dir, "link.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			p: station.Profinet{Devices: []station.Device{ref("link.yaml", "192.0.2.20")}},
			// The target is a valid manifest, so only the escape can refuse it.
			want: "profinet.devices[0].profinet-manifest: "},
		{name: "a manifest that is not there",
			p:    station.Profinet{Devices: []station.Device{ref("missing.yaml", "192.0.2.20")}},
			want: "profinet.devices[0].profinet-manifest: open"},
		{name: "a manifest that does not parse",
			setup: func(t *testing.T, dir string) { writeManifest(t, dir, "bad.yaml", "station: naut-1\nbogus: 1\n") },
			p:     station.Profinet{Devices: []station.Device{ref("bad.yaml", "192.0.2.20")}},
			want:  "profinet.devices[0].profinet-manifest: bad.yaml: profinet-device: manifest:"},
		{name: "a station name the Siemens rules forbid",
			setup: func(t *testing.T, dir string) {
				writeManifest(t, dir, "ip.yaml", strings.Replace(benchDevice, "station: naut-1", "station: 1.2.3.4", 1))
			},
			p:    station.Profinet{Devices: []station.Device{ref("ip.yaml", "192.0.2.20")}},
			want: `profinet.devices[0]: ip.yaml: station "1.2.3.4": "1.2.3.4" has the form n.n.n.n`},
		{name: "the CPU's name of station",
			setup: func(t *testing.T, dir string) {
				writeManifest(t, dir, "cpu.yaml", strings.Replace(benchDevice, "station: naut-1", "station: plc-1", 1))
			},
			p:    station.Profinet{Devices: []station.Device{ref("cpu.yaml", "192.0.2.20")}},
			want: `profinet.devices[0]: name of station "plc-1" is already the CPU's`},
		{name: "an explicit device's name",
			p: station.Profinet{Devices: []station.Device{
				{GSDML: "GSDML-V2.4-X-Y-20261117.xml", DAP: "DAP_1", Name: "naut-1", Address: "192.0.2.30"},
				ref("profinet_device.yaml", "192.0.2.20"),
			}},
			want: `profinet.devices[1]: name of station "naut-1" is already profinet.devices[0]'s`},
		{name: "the same manifest twice",
			p: station.Profinet{Devices: []station.Device{
				ref("profinet_device.yaml", "192.0.2.20"), ref("./profinet_device.yaml", "192.0.2.21"),
			}},
			want: `profinet.devices[1]: name of station "naut-1" is already profinet.devices[0]'s`},
		{name: "the CPU's address",
			p:    station.Profinet{Devices: []station.Device{ref("profinet_device.yaml", "192.0.2.10")}},
			want: "profinet.devices[0]: address 192.0.2.10 is already the CPU's"},
		{name: "an update time below the device's min-cycle",
			setup: func(t *testing.T, dir string) {
				writeManifest(t, dir, "slow.yaml", strings.Replace(benchDevice, "min-cycle: 8ms", "min-cycle: 32ms", 1))
			},
			p:    station.Profinet{UpdateTime: "16ms", Devices: []station.Device{ref("slow.yaml", "192.0.2.20")}},
			want: "profinet.devices[0].update-time: 16ms is below the device's min-cycle 32ms"},
		{name: "an update time TIA cannot set",
			p:    station.Profinet{Devices: []station.Device{{ProfinetManifest: "profinet_device.yaml", Address: "192.0.2.20", UpdateTime: "20ms"}}},
			want: "profinet.devices[0].update-time: 20ms is not an update time the device offers"},
		{name: "an update time above the ladder",
			p:    station.Profinet{Devices: []station.Device{{ProfinetManifest: "profinet_device.yaml", Address: "192.0.2.20", UpdateTime: "1024ms"}}},
			want: "profinet.devices[0].update-time: 1.024s is not an update time the device offers"},
		{name: "two stations that share a GSDML file name",
			setup: func(t *testing.T, dir string) {
				writeManifest(t, dir, "dotted.yaml", strings.Replace(benchDevice, "station: naut-1", "station: naut.1", 1))
				writeManifest(t, dir, "plain.yaml", strings.Replace(benchDevice, "station: naut-1", "station: naut1", 1))
			},
			p:    station.Profinet{Devices: []station.Device{ref("dotted.yaml", "192.0.2.20"), ref("plain.yaml", "192.0.2.21")}},
			want: "profinet.devices[1]: its GSDML file name GSDML-V2.35-nautilus-naut1-20261117.xml is already profinet.devices[0]'s"},
		{name: "an update time below 16 ms",
			p:    station.Profinet{Devices: []station.Device{{ProfinetManifest: "profinet_device.yaml", Address: "192.0.2.20", UpdateTime: "8ms"}}},
			want: "profinet.devices[0].update-time: 8ms is below the 16ms minimum"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeManifest(t, dir, "profinet_device.yaml", benchDevice)
			if tc.setup != nil {
				tc.setup(t, dir)
			}
			got, files, err := expandProfinet(dir, benchCPU(), &tc.p, stationDate)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\nwant it to contain %q", err, tc.want)
			}
			if got != nil || files != nil {
				t.Fatalf("a refused expansion returned %+v, %d files", got, len(files))
			}
		})
	}
}

// TestReferenceUpdateTime: the device's own time wins over the IO
// system's, and with neither the time is explicit and never below 16 ms or
// the device's min-cycle.
func TestReferenceUpdateTime(t *testing.T) {
	for _, tc := range []struct {
		own, system string
		minCycle    time.Duration
		want        string
	}{
		{"64ms", "32ms", 8 * time.Millisecond, "64ms"},
		{"", "32ms", 8 * time.Millisecond, "32ms"},
		{"", "", 8 * time.Millisecond, "16ms"},
		{"", "", 32 * time.Millisecond, "32ms"},
		{"", "", 20 * time.Millisecond, "32ms"}, // rounded up to the ladder
		{"", "", 300 * time.Millisecond, "512ms"},
		{"32ms", "", 32 * time.Millisecond, "32ms"},
	} {
		got, err := referenceUpdateTime(tc.own, tc.system, tc.minCycle)
		if err != nil || got != tc.want {
			t.Errorf("referenceUpdateTime(%q, %q, %s) = %q, %v; want %q", tc.own, tc.system, tc.minCycle, got, err, tc.want)
		}
	}
}

func TestExpandProfinetNone(t *testing.T) {
	got, files, err := expandProfinet(t.TempDir(), benchCPU(), nil, stationDate)
	if got != nil || files != nil || err != nil {
		t.Fatalf("no IO system: %+v %v %v", got, files, err)
	}
}

func intp(i int) *int { return &i }

// TestStationCPUDefaults: the manifest's empty configuration protection
// is the protected default, and any value it names goes through as is.
func TestStationCPUDefaults(t *testing.T) {
	for in, want := range map[string]string{"": "password", "password": "password", "none": "none", "keep": "keep"} {
		c := benchCPU()
		c.ConfigurationProtection = in
		if got := stationCPU(c).ConfigurationProtection; got != want {
			t.Errorf("configuration-protection %q expands to %q, want %q", in, got, want)
		}
	}
}

// TestStationCPUAccessControl: the manifest's empty access control is the
// default, a Full access user (#789: access control on for stations naut
// makes), and its kebab-case values become the wire's.
func TestStationCPUAccessControl(t *testing.T) {
	for in, want := range map[string]string{"": "fullAccessUser", "full-access-user": "fullAccessUser", "keep": "keep"} {
		c := benchCPU()
		c.AccessControl = in
		if got := stationCPU(c).AccessControl; got != want {
			t.Errorf("access-control %q expands to %q, want %q", in, got, want)
		}
	}
}
