package profinet

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

const sampleManifest = `
interface: eth1
station: naut-1
vendor-id: 0x1234
device-id: 0x0001
slots:
  - slot: 1
    module-ident: 0x00000101
    subslots:
      - {subslot: 1, submodule-ident: 0x00000001, inputs: 6, outputs: 3}
tags:
  - {name: Level,   slot: 1, subslot: 1, dir: input,  offset: 0, format: float32}
  - {name: Running, slot: 1, subslot: 1, dir: input,  offset: 4, bit: 0, format: bool}
  - {name: Count,   slot: 1, subslot: 1, dir: input,  offset: 5, format: uint8}
  - {name: Fill,    slot: 1, subslot: 1, dir: output, offset: 0, bit: 3, format: bool}
  - {name: Speed,   slot: 1, subslot: 1, dir: output, offset: 1, format: int16}
`

var (
	devMAC  = net.HardwareAddr{0x02, 0, 0, 0, 0xDE, 0x01}
	ctrlMAC = net.HardwareAddr{0x02, 0, 0, 0, 0xC0, 0x01}
)

func TestParseDeviceManifest(t *testing.T) {
	m, err := ParseDeviceManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.VendorName != DefaultVendorName || m.OrderNumber != DefaultOrderNumber || time.Duration(m.MinCycle) != DefaultMinCycle {
		t.Fatalf("defaults %+v", m)
	}
	if got := strings.Join(m.InputNames(), ","); got != "Fill,Speed" {
		t.Fatalf("inputs %s (the PLC's outputs)", got)
	}
	if got := strings.Join(m.OutputNames(), ","); got != "Count,Level,Running" {
		t.Fatalf("outputs %s (the PLC's inputs)", got)
	}
	subs := m.Subslots()
	if len(subs) != 1 || subs[0].InputLen != 6 || subs[0].OutputLen != 3 || subs[0].ModuleIdent != 0x101 {
		t.Fatalf("subslots %+v", subs)
	}
}

func TestDeviceManifestValidation(t *testing.T) {
	for name, tc := range map[string]struct{ edit, want string }{
		"unknown key":   {"interface: eth1\nbogus: 1\n", "bogus"},
		"station":       {strings.Replace(sampleManifest, "naut-1", "Naut_1", 1), "station"},
		"no vendor":     {strings.Replace(sampleManifest, "vendor-id: 0x1234\n", "", 1), "vendor-id"},
		"overlap":       {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: input, offset: 2, format: int16}\n", "overlaps"},
		"same bit":      {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: input, offset: 4, bit: 0, format: bool}\n", "overlaps"},
		"other bit ok":  {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: input, offset: 4, bit: 1, format: bool}\n", ""},
		"past the end":  {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: output, offset: 2, format: int16}\n", "does not fit"},
		"bool no bit":   {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: output, offset: 0, format: bool}\n", "bit"},
		"bit not bool":  {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: output, offset: 0, bit: 1, format: uint8}\n", "bool only"},
		"no subslot":    {sampleManifest + "  - {name: X, slot: 2, subslot: 1, dir: output, offset: 0, format: uint8}\n", "no subslot"},
		"bad dir":       {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: both, offset: 0, format: uint8}\n", "dir"},
		"bad format":    {sampleManifest + "  - {name: X, slot: 1, subslot: 1, dir: input, offset: 0, format: string}\n", "unknown format"},
		"duplicate tag": {sampleManifest + "  - {name: Level, slot: 1, subslot: 1, dir: output, offset: 2, format: uint8}\n", "twice"},
		"slot 0":        {strings.Replace(sampleManifest, "slot: 1\n    module", "slot: 0\n    module", 1), "slot 0"},
	} {
		_, err := ParseDeviceManifest([]byte(tc.edit))
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
}

type driverRig struct {
	drv  *DeviceDriver
	ctrl *pntest.Controller
	gate atomic.Bool
}

func newDriverRig(t *testing.T) *driverRig {
	t.Helper()
	m, err := ParseDeviceManifest([]byte(sampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	hub := transport.NewHub()
	rpc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: pntest.Loopback})
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := pntest.NewController(hub, ctrlMAC, devMAC, rpc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	opener := func(string) (transport.Port, net.PacketConn, func() []net.IP, error) {
		port := hub.Port(devMAC, transport.Options{EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr}})
		return port, rpc, func() []net.IP { return []net.IP{pntest.Loopback} }, nil
	}
	drv, err := NewDevice(m, WithDeviceOpener(opener), WithDeviceOnlineTag("pn__Online"),
		WithDeviceMinWatchdog(60*time.Millisecond), WithDeviceControllerPort(ctrl.CMPort()))
	if err != nil {
		t.Fatal(err)
	}
	r := &driverRig{drv: drv, ctrl: ctrl}
	r.gate.Store(true)
	drv.SetWriteGate(r.gate.Load)
	drv.Start(context.Background())
	t.Cleanup(func() { drv.Stop(); ctrl.Close() })
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var sub11 = device.Addr{Slot: 1, Subslot: 1}

func TestDeviceDriverEndToEnd(t *testing.T) {
	r := newDriverRig(t)
	in := nio.Values{}

	// Before any AR: the PLC's outputs have never arrived.
	r.drv.ReadInputsInto(in)
	if _, ok := in["Fill"]; ok || in["pn__Online"] != false {
		t.Fatalf("before an AR: %v", in)
	}
	if q := r.drv.Quality(); q["Fill"] != nio.NotConnected || q["Speed"] != nio.NotConnected {
		t.Fatalf("quality %v", q)
	}

	// The PLC connects and runs: Fill = bit 3 of byte 0, Speed = -2.
	waitFor(t, "device up", func() bool { return r.drv.Health().State == "running" })
	if st, _, err := r.ctrl.Connect(r.drv.m.Subslots(), false); err != nil || !st.IsOK() {
		t.Fatalf("connect %v %v", st, err)
	}
	if err := r.ctrl.PrmEnd(); err != nil {
		t.Fatal(err)
	}
	r.ctrl.StartOutputs(map[device.Addr][]byte{sub11: {0x08, 0xFF, 0xFE}}, wire.DataStatusRunNormal)
	waitFor(t, "PLC outputs", func() bool {
		r.drv.ReadInputsInto(in)
		return in["Fill"] == true && in["Speed"] == int64(-2) && in["pn__Online"] == true && len(r.drv.Quality()) == 0
	})

	// Our data is invalid to the PLC until the program's first full push.
	waitFor(t, "IOPS Bad before the push", func() bool {
		_, iops, _, ok := r.ctrl.LastInput(sub11)
		return ok && iops == wire.IOxSBad
	})
	// The runtime pushes every output at once; that push makes it valid.
	if err := r.drv.WriteOutputs(nio.Values{"Level": 1.5, "Running": true, "Count": int64(7)}); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x3F, 0xC0, 0, 0, 0x01, 0x07}
	waitFor(t, "our data at the PLC", func() bool {
		b, iops, _, ok := r.ctrl.LastInput(sub11)
		return ok && iops == wire.IOxSGood && bytes.Equal(b, want)
	})

	// The gate closes (a hung scan, a standby): invalid within a look,
	// without any call from the runtime.
	r.gate.Store(false)
	waitFor(t, "IOPS Bad with the gate closed", func() bool {
		_, iops, _, _ := r.ctrl.LastInput(sub11)
		return iops == wire.IOxSBad
	})
	r.gate.Store(true)
	waitFor(t, "outputs asked for", func() bool { return r.drv.OutputsWanted() })
	r.drv.WriteOutputs(nio.Values{"Level": 1.5, "Running": true, "Count": int64(7)})
	waitFor(t, "valid again", func() bool {
		_, iops, _, _ := r.ctrl.LastInput(sub11)
		return iops == wire.IOxSGood
	})

	// A value that does not fit is refused, not wrapped.
	if err := r.drv.WriteOutputs(nio.Values{"Count": int64(300)}); err == nil {
		t.Fatal("300 written into a uint8")
	}

	// The PLC in STOP: Bad, last value held.
	r.ctrl.SetOutputs(map[device.Addr][]byte{sub11: {0x00, 0, 0}}, wire.DataStatusNormal)
	waitFor(t, "Bad in STOP", func() bool {
		r.drv.ReadInputsInto(in)
		return r.drv.Quality()["Speed"] == nio.Bad && in["Speed"] == int64(-2)
	})

	// The PLC goes quiet: Stale, held, offline.
	r.ctrl.StopOutputs()
	waitFor(t, "Stale", func() bool {
		r.drv.ReadInputsInto(in)
		return r.drv.Quality()["Speed"] == nio.Stale && in["Speed"] == int64(-2) && in["pn__Online"] == false
	})
	if h := r.drv.Health(); h.Aborts != 1 || h.AR != "idle" {
		t.Fatalf("health %+v", h)
	}
}

func TestDeviceDriverWithoutCapability(t *testing.T) {
	m, _ := ParseDeviceManifest([]byte(sampleManifest))
	perm := &transport.PermissionError{Interface: "eth1", Err: errors.New("operation not permitted")}
	drv, err := NewDevice(m, WithDeviceRetry(10*time.Millisecond, 20*time.Millisecond),
		WithDeviceOpener(func(string) (transport.Port, net.PacketConn, func() []net.IP, error) { return nil, nil, nil, perm }))
	if err != nil {
		t.Fatal(err)
	}
	drv.Start(context.Background())
	defer drv.Stop()
	waitFor(t, "error row", func() bool {
		h := drv.Health()
		return h.State == "error" && strings.Contains(h.LastError, "CAP_NET_RAW")
	})
	in := nio.Values{}
	if err := drv.ReadInputsInto(in); err != nil || len(in) != 0 {
		t.Fatalf("read without a device: %v %v", in, err)
	}
	if err := drv.WriteOutputs(nio.Values{"Level": 1.0}); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceDriverOnlineTagClash(t *testing.T) {
	m, _ := ParseDeviceManifest([]byte(sampleManifest))
	if _, err := NewDevice(m, WithDeviceOnlineTag("Speed")); err == nil {
		t.Fatal("online tag equal to a bound tag accepted")
	}
}
