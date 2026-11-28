package project

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/devbind"
	"github.com/joyautomation/nautilus/wiring"
)

const wiringManifest = `name: wired
tasks: [{ name: main, program: main.st, scan: 100ms }]
`

const wiringPump = `wirings:
  - type: Pump
    params: { endpoint: null }
    sources:
      drive: { driver: modbus, endpoint: "{endpoint}" }
    members:
      Running:  { from: drive, table: coil, address: 0 }
      Cmd.Start: { to: drive, table: coil, address: 10 }
`

const wiringTypes = `TYPE PumpCmd : STRUCT
  Start : BOOL;
END_STRUCT; END_TYPE
TYPE Pump : STRUCT
  Running : BOOL;
  Cmd     : PumpCmd;
END_STRUCT; END_TYPE
TYPE Motor : STRUCT
  SpeedSP : REAL;
END_STRUCT; END_TYPE
`

func wiringFS(manifest string, files map[string]string) fstest.MapFS {
	fs := fstest.MapFS{
		"nautilus.yaml": {Data: []byte(manifest)},
		"main.st":       {Data: []byte("PROGRAM Main\nEND_PROGRAM\n")},
		"types.st":      {Data: []byte(wiringTypes)},
	}
	for n, c := range files {
		fs[n] = &fstest.MapFile{Data: []byte(c)}
	}
	return fs
}

func readWiring(t *testing.T, manifest string, files map[string]string) error {
	t.Helper()
	_, err := ReadManifest(wiringFS(manifest, files), "")
	return err
}

// A project with wiring that is wrong hears every mistake, with where; a
// project whose wiring is right gets its instance and the connection's
// companions as tags.
func TestWiringIsCheckedThenResolved(t *testing.T) {
	good := wiringManifest + wiringPump + "instances:\n  - { name: P1, type: Pump, params: { endpoint: 10.0.0.1 } }\n"
	m, err := ReadManifest(wiringFS(good, nil), "")
	if err != nil {
		t.Fatalf("a valid wiring: %v", err)
	}
	got := map[string]TagConfig{}
	for _, tc := range m.Tags {
		got[tc.Name] = tc
	}
	if p := got["P1"]; p.instance == nil || p.Type != "Pump" {
		t.Errorf("P1 must be an instance of Pump: %+v", p)
	}
	if got["P1__Online"].Role != "input" || got["P1__Outages"].Type != "DINT" {
		t.Errorf("the connection's companions: %+v", got)
	}

	bad := wiringManifest + wiringPump + "instances:\n  - { name: P1, type: Pump }\n"
	err = readWiring(t, bad, nil)
	if err == nil || !strings.Contains(err.Error(), `nautilus.yaml:12: instance P1 does not give param "endpoint"`) {
		t.Errorf("a missing param must name the manifest and its line, got %v", err)
	}
}

func TestWiringFiles(t *testing.T) {
	m := wiringManifest + "wiring-files: [wiring/pumps.yaml]\n"
	files := map[string]string{"wiring/pumps.yaml": wiringPump + "instances:\n  - { name: P1, type: Pump, params: { endpoint: a } }\n"}
	if err := readWiring(t, m, files); err != nil {
		t.Errorf("a valid include file must decode, check and resolve: %v", err)
	}

	// A mistake in the file names the file and its line.
	files["wiring/pumps.yaml"] = strings.Replace(files["wiring/pumps.yaml"], "from: drive,", "from: drve,", 1)
	err := readWiring(t, m, files)
	if err == nil || !strings.Contains(err.Error(), `wiring/pumps.yaml:7: member "Running": from: "drve" is not a source of this wiring (did you mean drive?)`) {
		t.Errorf("a mistake in a wiring file must name the file and line: %v", err)
	}

	// A key the format does not know is an error, not a dropped binding.
	files["wiring/pumps.yaml"] = "wirings:\n  - type: Pump\n    membres: {}\n"
	if err := readWiring(t, m, files); err == nil || !strings.Contains(err.Error(), "wiring/pumps.yaml") || !strings.Contains(err.Error(), "field membres not found") {
		t.Errorf("strict decoding: %v", err)
	}

	// Paths stay inside the project, as tag-files do; a missing file says so.
	for _, tc := range []struct{ list, want string }{
		{"[../outside.yaml]", "wiring-files: path"},
		{"[wiring/missing.yaml]", "wiring-files:"},
	} {
		err := readWiring(t, wiringManifest+"wiring-files: "+tc.list+"\n", nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("wiring-files %s: %v, want %q", tc.list, err, tc.want)
		}
	}
}

// A wiring or instance declared in the manifest and a file, or two files,
// is one namespace: an error naming both, never last-wins.
func TestWiringSharesOneNamespace(t *testing.T) {
	m := wiringManifest + "wiring-files: [wiring/a.yaml, wiring/b.yaml]\n"
	files := map[string]string{"wiring/a.yaml": wiringPump, "wiring/b.yaml": wiringPump}
	err := readWiring(t, m, files)
	if err == nil || !strings.Contains(err.Error(), `wiring/b.yaml:2: wiring "Pump" is declared again (first at wiring/a.yaml:2)`) {
		t.Errorf("two files declaring one wiring: %v", err)
	}
	inline := wiringManifest + "wiring-files: [wiring/a.yaml]\n" + wiringPump
	err = readWiring(t, inline, map[string]string{"wiring/a.yaml": wiringPump})
	if err == nil || !strings.Contains(err.Error(), `wiring "Pump" is declared again (first at wiring/a.yaml:2)`) {
		t.Errorf("the manifest and a file declaring one wiring: %v", err)
	}
}

// An instance is a tag of its type, so it shares the tags' namespace.
func TestInstanceNameClashesWithATag(t *testing.T) {
	m := wiringManifest + "tags:\n  - { name: P1, role: state, type: BOOL }\n" + wiringPump +
		"instances:\n  - { name: p1, type: Pump, params: { endpoint: a } }\n"
	err := readWiring(t, m, nil)
	if err == nil || !strings.Contains(err.Error(), `instance "p1" has the name of the tag P1`) {
		t.Errorf("an instance named like a tag: %v", err)
	}
}

// A source's driver resolves through the manifest's drivers: by name, and
// both: is refused on a field device whatever its entry is called.
func TestWiringResolvesDriversByName(t *testing.T) {
	both := func(driverEntry, driverName string) string {
		return wiringManifest + driverEntry + `wirings:
  - type: Motor
    sources: { plc: { driver: ` + driverName + ` } }
    members:
      SpeedSP: { both: plc }
`
	}
	// A modbus entry named line-plc is still a field device.
	err := readWiring(t, both("drivers:\n  - { type: modbus, name: line-plc }\n", "line-plc"), nil)
	if err == nil || !strings.Contains(err.Error(), `source "plc" is a modbus field device`) {
		t.Errorf("both: on a modbus entry named like a controller must be refused: %v", err)
	}
	// An eip entry passes the format's checks, and this build refuses it
	// as not wired yet (slice 12), both: with it.
	for _, tc := range []struct{ entry, name string }{
		{"drivers:\n  - { type: eip, name: line-plc, host: 192.0.2.1, manifest: eip.yaml }\n", "line-plc"},
		{"", "eip"}, // a bare driver type needs no entry
	} {
		err = readWiring(t, both(tc.entry, tc.name), nil)
		if err == nil || !strings.Contains(err.Error(), `source "plc" uses the eip driver: this build runs struct wiring on Modbus only`) ||
			!strings.Contains(err.Error(), `member "SpeedSP": both: is for a peer controller, which this build does not wire yet`) {
			t.Errorf("both: on eip (%s): %v", tc.name, err)
		}
	}
	// A name that is neither is named.
	err = readWiring(t, both("", "nope"), nil)
	if err == nil || !strings.Contains(err.Error(), `driver "nope" is not a drivers: entry or a driver type`) {
		t.Errorf("an unknown driver: %v", err)
	}
}

// Nothing about a manifest without wiring changes.
func TestNoWiringIsUntouched(t *testing.T) {
	if _, err := ReadManifest(wiringFS(wiringManifest, nil), ""); err != nil {
		t.Errorf("a manifest with no wiring: %v", err)
	}
}

// A devices: row is a tag too, so an instance may not take its name.
func TestInstanceNameClashesWithADevice(t *testing.T) {
	m := &Manifest{Devices: []devbind.Device{{Name: "P1"}}}
	f := &wiring.File{Instances: []wiring.Instance{{Name: "p1", Type: "Pump", Pos: wiring.Pos{File: "nautilus.yaml", Line: 9}}}}
	ps := instanceTagClashes([]*wiring.File{f}, m)
	if len(ps) != 1 || !strings.Contains(ps[0].Error(), `nautilus.yaml:9: instance "p1" has the name of the tag P1 (a devices: row)`) {
		t.Errorf("an instance named like a device: %v", ps)
	}
}

// A devices: key in a wiring file says when it will be allowed.
func TestDevicesInAWiringFileSaysWhen(t *testing.T) {
	files := map[string]string{"wiring/a.yaml": "devices:\n  - { name: D }\n"}
	err := readWiring(t, wiringManifest+"wiring-files: [wiring/a.yaml]\n", files)
	if err == nil || !strings.Contains(err.Error(), "devices: rows in a wiring file arrive with slice 10") {
		t.Errorf("devices: in a wiring file: %v", err)
	}
}
