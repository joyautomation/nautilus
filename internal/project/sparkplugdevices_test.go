package project

// One Sparkplug device per field connection (docs/design/edge-collector.md
// §3): a driver holding several sources makes one device per source, a
// driver that is one connection makes one named after it, and the memory
// loopback makes none. sparkplug.device, the one device every driver's
// tags rode on, is gone, and says so.

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
)

// collectorFiles is a project with a two-source modbus driver (two meters)
// and a sparkplug-host driver — every driver offline, as in CI.
func collectorFiles(hostName, sparkplug string) fstest.MapFS {
	files := fsys(multiBase() + `drivers:
  - type: modbus
    manifest: modbus_manifest.yaml
` + hostDriverYAML("  ", "central", "G", "host.yaml") + hostName + `
sparkplug:
  broker: tcp://mqtt.invalid:1883
  group-id: Site
` + sparkplug)
	files["host.yaml"] = &fstest.MapFile{Data: []byte(hostManifestYAML("G", "W1", "W1_Level"))}
	files["modbus_manifest.yaml"] = &fstest.MapFile{Data: []byte(`sources:
  - {id: M1, host: 192.0.2.1}
  - {id: M2, host: 192.0.2.2}
tags:
  - {name: M1_KW, source: M1, table: holding, address: 0, format: float32}
  - {name: M1_PF, source: M1, table: holding, address: 2, format: float32}
  - {name: M2_KW, source: M2, table: holding, address: 0, format: float32}
`)}
	return files
}

func TestSparkplugDevicePerConnection(t *testing.T) {
	// The edge node keeps a device only when the project declares some of
	// its tags (TestSparkplugDeviceWithNothingDeclaredIsDropped).
	files := collectorFiles("", "tag-files: [tags/dev.yaml]\n")
	files["tags/dev.yaml"] = &fstest.MapFile{Data: []byte(`- { name: M1_KW, role: input }
- { name: M2_KW, role: input }
- { name: W1_Level, role: input }
`)}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	type dev struct {
		ID   string
		Tags []string
	}
	var got []dev
	for _, d := range devs {
		got = append(got, dev{d.ID, d.Tags})
		if d.Health == nil {
			t.Errorf("device %s has no health gate", d.ID)
		} else if d.Health() {
			t.Errorf("device %s reads healthy with nothing connected", d.ID)
		}
	}
	want := []dev{
		{"M1", []string{"M1_KW", "M1_PF", "M1__Online", "M1__Outages"}},
		{"M2", []string{"M2_KW", "M2__Online", "M2__Outages"}},
		{"sparkplug-host", []string{"W1_Level", "W1__LastBirthMs", "W1__Online", "W1__Outages"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devices =\n  %+v\nwant\n  %+v", got, want)
	}

	// And the edge node is built with them.
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	node, err := p.Sparkplug(rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range node.Status().Devices {
		ids = append(ids, d.ID)
	}
	if !reflect.DeepEqual(ids, []string{"M1", "M2", "sparkplug-host"}) {
		t.Fatalf("node devices = %v", ids)
	}
}

// A driver's name: is its device id.
func TestSparkplugDeviceNamedByDriverName(t *testing.T) {
	p, err := Load(collectorFiles("    name: central\n", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	if last := devs[len(devs)-1].ID; last != "central" {
		t.Fatalf("device id = %q, want the driver's name", last)
	}
}

// Two connections with one id would share a topic: refused, naming both.
func TestSparkplugDeviceCollision(t *testing.T) {
	p, err := Load(collectorFiles("    name: M2\n", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = sparkplugDevices(p.drivers)
	for _, want := range []string{`device "M2"`, `source "M2" of driver "modbus"`, `driver "M2"`, "rename one"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to contain %q", err, want)
		}
	}
}

// The memory loopback is no connection: no device, everything node level.
func TestSparkplugMemoryMakesNoDevice(t *testing.T) {
	p, err := Load(fsys(manifest+"sparkplug:\n  broker: tcp://mqtt.invalid:1883\n  group-id: Site\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	node, err := p.Sparkplug(rt)
	if err != nil {
		t.Fatal(err)
	}
	if devs := node.Status().Devices; len(devs) != 0 {
		t.Fatalf("devices = %+v, want none for the memory loopback", devs)
	}
}

func TestSparkplugDeviceKeyIsGone(t *testing.T) {
	_, err := Load(collectorFiles("", "  device: plc1\n"), "")
	for _, want := range []string{"sparkplug.device: removed", "its own Sparkplug device", "device: plc1", "name:"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to contain %q", err, want)
		}
	}
}

// writable: reaches the node. A malformed pattern is refused by Load
// (TestSparkplugWritableValidatedAtLoad), so the list is spoiled after Load
// here: the node, built from it, refuses it too, which only the list it
// was given can make it do.
func TestSparkplugWritableReachesTheNode(t *testing.T) {
	if _, err := Load(collectorFiles("", "  writable: [\"M1_[\"]\n"), ""); err == nil || !strings.Contains(err.Error(), `sparkplug.writable pattern "M1_["`) {
		t.Fatalf("Load: err = %v, want the bad writable pattern named", err)
	}
	p, err := Load(collectorFiles("", "  writable: [\"M1_*\"]\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	p.sparkplug.Writable = []string{"M1_["}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Sparkplug(rt); err == nil || !strings.Contains(err.Error(), `writable pattern "M1_["`) {
		t.Fatalf("err = %v, want the bad writable pattern named", err)
	}
}

// A device publishes only the tags the project declares. The runtime never
// writes an undeclared input into the tag store, so a companion no tag file
// names (M2__Online, the __Outages counters) would only hold the DBIRTH
// back for its wait and then be missing from it anyway. A companion the
// project does declare (M1__Online here) is published as before.
func TestSparkplugDeviceCarriesOnlyDeclaredTags(t *testing.T) {
	files := collectorFiles("", "tag-files: [tags/dev.yaml]\n")
	files["tags/dev.yaml"] = &fstest.MapFile{Data: []byte(`- { name: M1_KW, role: input }
- { name: M1_PF, role: input }
- { name: M1__Online, role: input }
- { name: M2_KW, role: input }
- { name: W1_Level, role: input }
`)}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, d := range declaredOnly(devs, declaredNames(p.Runtime, rt)) {
		got[d.ID] = d.Tags
	}
	want := map[string][]string{
		"M1":             {"M1_KW", "M1_PF", "M1__Online"},
		"M2":             {"M2_KW"},
		"sparkplug-host": {"W1_Level"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("device tags = %v, want %v", got, want)
	}

	// The edge node is built with the filtered lists.
	node, err := p.Sparkplug(rt)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, d := range node.Status().Devices {
		counts[d.ID] = d.Tags
	}
	if want := map[string]int{"M1": 3, "M2": 1, "sparkplug-host": 1}; !reflect.DeepEqual(counts, want) {
		t.Fatalf("node device tag counts = %v, want %v", counts, want)
	}
}

// edgeDeviceTags loads files, builds the runtime and the edge node, and
// returns each edge device's tags after the declared-only filter, plus the
// devices the built node reports.
func edgeDeviceTags(t *testing.T, files fstest.MapFS) (map[string][]string, []string) {
	t.Helper()
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, d := range declaredOnly(devs, declaredNames(p.Runtime, rt)) {
		got[d.ID] = d.Tags
	}
	node, err := p.Sparkplug(rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range node.Status().Devices {
		ids = append(ids, d.ID)
	}
	return got, ids
}

// A polled writable binding is one of its source's tags, and the project
// declares it role: output, not input. It stays on its device (DBIRTH,
// DCMD, dies with the device) rather than moving to node level.
func TestSparkplugDeviceKeepsADeclaredOutput(t *testing.T) {
	files := collectorFiles("", "tag-files: [tags/dev.yaml]\n")
	files["modbus_manifest.yaml"] = &fstest.MapFile{Data: []byte(`sources:
  - {id: M1, host: 192.0.2.1}
tags:
  - {name: M1_KW, source: M1, table: holding, address: 0, format: float32}
  - {name: M1_SP, source: M1, table: holding, address: 2, format: float32, writable: true}
`)}
	files["tags/dev.yaml"] = &fstest.MapFile{Data: []byte(`- { name: M1_KW, role: input }
- { name: M1_SP, role: output, init: 0.0 }
- { name: W1_Level, role: input }
`)}
	got, _ := edgeDeviceTags(t, files)
	if want := []string{"M1_KW", "M1_SP"}; !reflect.DeepEqual(got["M1"], want) {
		t.Fatalf("device M1 tags = %v, want %v", got["M1"], want)
	}
}

// A device none of whose tags the project declares is left off the edge
// node, as before the declared-only filter: its tags never reach the tag
// store, so it would only birth empty on every connect.
func TestSparkplugDeviceWithNothingDeclaredIsDropped(t *testing.T) {
	files := collectorFiles("", "tag-files: [tags/dev.yaml]\n")
	files["tags/dev.yaml"] = &fstest.MapFile{Data: []byte(`- { name: M1_KW, role: input }
- { name: W1_Level, role: input }
`)}
	got, ids := edgeDeviceTags(t, files)
	if _, ok := got["M2"]; ok {
		t.Fatalf("device tags = %v: M2 declares nothing and must be dropped", got)
	}
	if want := []string{"M1", "sparkplug-host"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("node devices = %v, want %v", ids, want)
	}
}

// Only a name the project declares as a tag, spelled as declared, stays on
// a device: a companion a program binds through VAR_EXTERNAL with no tag
// declaration, and a binding declared in another case, never reach the
// tag store, so either would hold the DBIRTH back for its whole delay.
func TestSparkplugDeviceIgnoresProgramOnlyAndCaseMismatchedNames(t *testing.T) {
	files := collectorFiles("", "tag-files: [tags/dev.yaml]\n")
	files["tags/dev.yaml"] = &fstest.MapFile{Data: []byte(`- { name: M1_KW, role: input }
- { name: m1_pf, role: input }
- { name: M2_KW, role: input }
- { name: W1_Level, role: input }
`)}
	files["program.fbd"] = &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL Sensor : REAL; Setpoint : REAL; Actuator : REAL; M1__Outages : DINT; END_VAR
FBD
  w_err = SUB(Setpoint, Sensor)
  Actuator := LIMIT(0.0, w_err, 100.0)
END_FBD
END_PROGRAM`)}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices(p.drivers)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range declaredOnly(devs, declaredNames(p.Runtime, rt)) {
		if d.ID == "M1" && !reflect.DeepEqual(d.Tags, []string{"M1_KW"}) {
			t.Fatalf("device M1 tags = %v, want [M1_KW] (M1__Outages is only a program global; M1_PF is declared as m1_pf)", d.Tags)
		}
	}
}
