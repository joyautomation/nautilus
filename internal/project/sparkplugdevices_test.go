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
	p, err := Load(collectorFiles("", ""), "")
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

// writable: reaches the node: a malformed glob is refused when it is built.
func TestSparkplugWritableReachesTheNode(t *testing.T) {
	p, err := Load(collectorFiles("", "  writable: [\"M1_[\"]\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Sparkplug(rt); err == nil || !strings.Contains(err.Error(), `writable pattern "M1_["`) {
		t.Fatalf("err = %v, want the bad writable pattern named", err)
	}
}
