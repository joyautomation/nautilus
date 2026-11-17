package project

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/profinet"
	"github.com/joyautomation/nautilus/runtime"
)

const profinetDeviceYAML = `interface: eth1
station: naut-1
vendor-id: 0x1234
device-id: 0x0001
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 4, outputs: 1}
tags:
  - {name: Bin1_Level, slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}
  - {name: Bin1_Fill,  slot: 1, subslot: 1, dir: output, offset: 0, bit: 0, format: bool}
`

func profinetProject(extra string) fstest.MapFS {
	return fstest.MapFS{
		"nautilus.yaml": &fstest.MapFile{Data: []byte(`
name: pn
tasks:
  - program: program.st
    scan: 50ms
tags:
  - { name: Bin1_Level, role: output, type: REAL }
  - { name: Bin1_Fill, role: input, type: BOOL }
  - { name: profinet_device__Online, role: input, type: BOOL }
driver:
  type: profinet-device
  manifest: profinet_device.yaml
` + extra)},
		"program.st": &fstest.MapFile{Data: []byte(`PROGRAM Main
VAR_EXTERNAL Bin1_Level : REAL; Bin1_Fill : BOOL; profinet_device__Online : BOOL; END_VAR
IF profinet_device__Online AND Bin1_Fill THEN Bin1_Level := Bin1_Level + 1.0; END_IF;
END_PROGRAM`)},
		"profinet_device.yaml": &fstest.MapFile{Data: []byte(profinetDeviceYAML)},
	}
}

// A profinet-device project loads and compiles with no NIC, no privilege
// and no PLC; its status row says it has not opened the interface yet.
func TestLoadProfinetDevice(t *testing.T) {
	p, err := Load(profinetProject(""), "")
	if err != nil {
		t.Fatalf("a profinet-device project must load anywhere: %v", err)
	}
	if _, ok := p.Runtime.Driver.(*profinet.DeviceDriver); !ok {
		t.Fatalf("driver = %T", p.Runtime.Driver)
	}
	if _, err := runtime.New(p.Runtime); err != nil {
		t.Fatalf("must compile: %v", err)
	}
	s := p.DriverStatus(nil, nil)()
	if len(s) != 1 || s[0].Kind != "profinet-device" || s[0].State != "connecting" || !strings.Contains(s[0].Detail, "naut-1") {
		t.Fatalf("status %+v", s)
	}
	if !commsDriver(p.Runtime.Driver) {
		t.Fatal("profinet-device is a comms driver: an undelivered input reads its init")
	}
}

// Redundancy needs a standby that goes silent on the PROFINET network;
// until that is designed, the combination is refused at load.
func TestProfinetRefusesRedundancy(t *testing.T) {
	_, err := Load(profinetProject("redundancy:\n  lease: pn-lease\n"), "")
	if err == nil || !strings.Contains(err.Error(), "redundancy") || !strings.Contains(err.Error(), "profinet") {
		t.Fatalf("got %v", err)
	}
}

func TestProfinetDeviceManifestErrors(t *testing.T) {
	fs := profinetProject("")
	fs["profinet_device.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(profinetDeviceYAML, "offset: 0, format: float32", "offset: 2, format: float32", 1))}
	if _, err := Load(fs, ""); err == nil || !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("got %v", err)
	}
}
