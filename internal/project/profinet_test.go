package project

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/joyautomation/nautilus/profinet"
	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
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

// A manifest binding data to the PLC under a tag the project does not
// declare is refused: nothing would write it, and the PLC would read its
// zeros as valid once the program's first push validates the data.
func TestProfinetRefusesUndeclaredOutput(t *testing.T) {
	fs := profinetProject("")
	y := strings.Replace(profinetDeviceYAML, "inputs: 4", "inputs: 5", 1) + "  - {name: Extra, slot: 1, subslot: 1, dir: input, offset: 4, format: uint8}\n"
	fs["profinet_device.yaml"] = &fstest.MapFile{Data: []byte(y)}
	if _, err := Load(fs, ""); err == nil || !strings.Contains(err.Error(), "Extra") || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("got %v", err)
	}
}

// The validity rule under the real runtime: a project with two tasks
// loads, its driver serves an in-process PLC, real scans push the
// outputs, and the data the PLC reads turns valid with the first push,
// follows the program, goes invalid when the write gate closes and valid
// again with the push that answers the reopen.
func TestProfinetDeviceUnderTheRuntime(t *testing.T) {
	files := profinetProject("")
	files["nautilus.yaml"] = &fstest.MapFile{Data: []byte(`
name: pn
tasks:
  - program: program.st
    scan: 10ms
  - program: count.st
    scan: 15ms
tags:
  - { name: Bin1_Level, role: output, type: REAL }
  - { name: Bin1_Count, role: output, type: USINT }
  - { name: Bin1_Fill, role: input, type: BOOL }
  - { name: profinet_device__Online, role: input, type: BOOL }
driver:
  type: profinet-device
  manifest: profinet_device.yaml
`)}
	files["count.st"] = &fstest.MapFile{Data: []byte(`PROGRAM Count
VAR_EXTERNAL Bin1_Count : USINT; END_VAR
IF Bin1_Count < 200 THEN Bin1_Count := Bin1_Count + 1; END_IF;
END_PROGRAM`)}
	files["profinet_device.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(profinetDeviceYAML, "inputs: 4", "inputs: 5", 1) +
		"  - {name: Bin1_Count, slot: 1, subslot: 1, dir: input, offset: 4, format: uint8}\n")}
	p, err := Load(files, "")
	if err != nil {
		t.Fatal(err)
	}
	drv := p.Runtime.Driver.(*profinet.DeviceDriver)

	hub := transport.NewHub()
	rpc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: pntest.Loopback})
	if err != nil {
		t.Fatal(err)
	}
	devMAC := net.HardwareAddr{2, 0, 0, 0, 0xDE, 1}
	plc, err := pntest.NewController(hub, net.HardwareAddr{2, 0, 0, 0, 0xC0, 1}, devMAC, rpc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer plc.Close()
	var gate atomic.Bool
	gate.Store(true)
	drv.SetWriteGate(gate.Load)
	drv.Apply(
		profinet.WithDeviceOpener(func(string) (profinet.Opened, error) {
			return profinet.Opened{
				Port: hub.Port(devMAC, transport.Options{EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr}}),
				RPC:  rpc, Addrs: func() []net.IP { return []net.IP{pntest.Loopback} },
			}, nil
		}),
		profinet.WithDeviceControllerPort(plc.CMPort()))

	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drv.Start(ctx)
	defer drv.Stop()
	go rt.Run(ctx)

	wait := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (%+v)", what, drv.Health())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait("device up", func() bool { return drv.Health().State == "running" })
	m, _ := profinet.ParseDeviceManifest(files["profinet_device.yaml"].Data)
	if st, _, err := plc.Connect(m.Subslots(), false); err != nil || !st.IsOK() {
		t.Fatalf("connect %v %v", st, err)
	}
	if err := plc.PrmEnd(); err != nil {
		t.Fatal(err)
	}
	sub := device.Addr{Slot: 1, Subslot: 1}
	plc.StartOutputs(map[device.Addr][]byte{sub: {0x01}}, wire.DataStatusRunNormal) // Fill = TRUE

	// Valid with real pushes, following the program: Level climbs while
	// online and filling, and the second task's counter arrives too.
	var first []byte
	wait("valid data that follows the program", func() bool {
		b, iops, _, ok := plc.LastInput(sub)
		if !ok || iops != wire.IOxSGood || b[4] == 0 {
			return false
		}
		if first == nil {
			first = b
			return false
		}
		return !bytes.Equal(b[:4], first[:4])
	})

	// The gate closes: invalid without any help from the runtime.
	gate.Store(false)
	wait("invalid with the gate closed", func() bool {
		_, iops, _, _ := plc.LastInput(sub)
		return iops == wire.IOxSBad
	})
	// Reopened: the runtime's answering push makes it valid again.
	gate.Store(true)
	wait("valid after the reopen", func() bool {
		_, iops, _, _ := plc.LastInput(sub)
		return iops == wire.IOxSGood
	})
}
