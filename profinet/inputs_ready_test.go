package profinet

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// InputsReady (io.InputsReadier, #786): delivered is fresh cyclic data from
// the PLC; failed is an interface that would not open, or no controller
// within controllerWait; sticky.

func newReadyDevice(t *testing.T, wait time.Duration) (*DeviceDriver, *pntest.Controller) {
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
	opener := func(string) (Opened, error) {
		port := hub.Port(devMAC, transport.Options{EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr}})
		return Opened{Port: port, RPC: rpc, Addrs: func() []net.IP { return []net.IP{pntest.Loopback} }}, nil
	}
	d, err := NewDevice(m, WithDeviceOpener(opener), WithDeviceMinWatchdog(60*time.Millisecond), WithDeviceControllerPort(ctrl.CMPort()))
	if err != nil {
		t.Fatal(err)
	}
	d.controllerWait = wait
	return d, ctrl
}

func TestDeviceInputsReadyOnTheFirstPLCDataThenSticky(t *testing.T) {
	d, ctrl := newReadyDevice(t, time.Hour) // silence never counts as failed here
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	d.Start(context.Background())
	t.Cleanup(func() { d.Stop(); ctrl.Close() })
	waitFor(t, "device up", func() bool { return d.Health().State == "running" })
	if d.InputsReady() {
		t.Fatal("ready with no controller connected")
	}
	if st, _, err := ctrl.Connect(d.m.Subslots(), false); err != nil || !st.IsOK() {
		t.Fatalf("connect %v %v", st, err)
	}
	if err := ctrl.PrmEnd(); err != nil {
		t.Fatal(err)
	}
	ctrl.StartOutputs(map[device.Addr][]byte{sub11: {0x08, 0xFF, 0xFE}}, wire.DataStatusRunNormal)
	waitFor(t, "ready on the PLC's data", d.InputsReady)
	ctrl.StopOutputs()
	waitFor(t, "AR lost", func() bool { return d.Health().AR == "idle" })
	if !d.InputsReady() {
		t.Fatal("InputsReady went false after the AR was lost")
	}
}

func TestDeviceInputsReadyWhenNoControllerComes(t *testing.T) {
	d, ctrl := newReadyDevice(t, 150*time.Millisecond)
	d.Start(context.Background())
	t.Cleanup(func() { d.Stop(); ctrl.Close() })
	waitFor(t, "device up", func() bool { return d.Health().State == "running" })
	if d.InputsReady() {
		t.Fatal("ready before the controller wait is over")
	}
	waitFor(t, "ready after the controller wait", d.InputsReady)
}

func TestDeviceInputsReadyWhenTheInterfaceWillNotOpen(t *testing.T) {
	m, _ := ParseDeviceManifest([]byte(sampleManifest))
	perm := &transport.PermissionError{Interface: "eth1", Err: errors.New("operation not permitted")}
	d, err := NewDevice(m, WithDeviceRetry(10*time.Millisecond, 20*time.Millisecond),
		WithDeviceOpener(func(string) (Opened, error) { return Opened{}, perm }))
	if err != nil {
		t.Fatal(err)
	}
	d.controllerWait = time.Hour
	if d.InputsReady() {
		t.Fatal("ready before Start")
	}
	d.Start(context.Background())
	defer d.Stop()
	waitFor(t, "ready after the open failed", d.InputsReady)
}
