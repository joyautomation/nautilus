package device_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/joyautomation/nautilus/profinet/device"
	"github.com/joyautomation/nautilus/profinet/pntest"
	"github.com/joyautomation/nautilus/profinet/transport"
	"github.com/joyautomation/nautilus/profinet/wire"
)

// f33Kind names a frame: LLDP, DCP, or RT (cyclic data and alarms).
func f33Kind(f transport.Frame) string {
	if f.EtherType == wire.EtherTypeLLDP {
		return "lldp"
	}
	if f.EtherType == wire.EtherTypePNIO && len(f.Payload) >= 2 {
		if id := binary.BigEndian.Uint16(f.Payload); id >= wire.FrameIDDCPHello && id <= wire.FrameIDDCPIdentResp {
			return "dcp"
		}
		return "rt"
	}
	return "other"
}

// Test plan F33 / design §3.4: the device reads Config.MaySend before
// EVERY frame it sends (cyclic RT, DCP, LLDP), not on a periodic look: in
// a running AR, with LLDP announcing, the gate closing stops each kind at
// once, though nothing else stops the device, and a DCP Identify after the
// close goes unanswered. Reading the gate only now and then (no gated
// port) turns it red.
func TestF33EveryFrameKindReadsTheGate(t *testing.T) {
	hub := transport.NewHub()
	devRPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loIP})
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := pntest.NewController(hub, ctrlMAC, devMAC, devRPC.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	dev, err := New(Config{
		Identity: Identity{Station: "dev-1", VendorName: "nautilus", VendorID: 0x1234, DeviceID: 0x0001, OrderNumber: "NAUT-PN"},
		Subslots: layout,
		Port: hub.Port(devMAC, transport.Options{
			EtherTypes: []uint16{wire.EtherTypePNIO}, Multicast: []net.HardwareAddr{wire.DCPMulticastAddr},
		}),
		RPC:              devRPC,
		ControllerCMPort: ctrl.CMPort(),
		InterfaceAddrs:   func() []net.IP { return []net.IP{loIP} },
		MinWatchdog:      60 * time.Millisecond,
		LLDPEvery:        30 * time.Millisecond,
		MaySend:          open.Load,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dev.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; ctrl.Close() })
	c := tctl{ctrl, t}

	var mu sync.Mutex
	var closedAt time.Time
	seen, after := map[string]int{}, map[string]int{}
	hub.Tap(func(f transport.Frame) {
		if !bytes.Equal(f.Src, devMAC) {
			return
		}
		k := f33Kind(f)
		mu.Lock()
		defer mu.Unlock()
		seen[k]++
		// A frame whose check passed just before the close may still
		// leave right after it (the stated residual: one per sending
		// goroutine, the send itself microseconds); none after that.
		if !closedAt.IsZero() && time.Since(closedAt) > 5*time.Millisecond {
			after[k]++
		}
	})
	if name, _ := c.identify("dev-1"); name != "dev-1" {
		t.Fatalf("identify: %q", name)
	}
	if diff := c.connect(t, layout, false); len(diff) != 0 {
		t.Fatalf("diff %+v", diff)
	}
	c.prmEnd(t)
	c.startOutputs(map[Addr][]byte{{1, 1}: {0xAB, 0xCD}}, wire.DataStatusRunNormal)
	eventually(t, "cyclic RT, DCP and LLDP from the device", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen["rt"] > 0 && seen["dcp"] > 0 && seen["lldp"] > 0
	})

	mu.Lock()
	closedAt = time.Now()
	mu.Unlock()
	open.Store(false)
	if name, _ := c.identify("dev-1"); name != "" {
		t.Error("the device answered a DCP Identify with the gate closed")
	}
	time.Sleep(200 * time.Millisecond) // several LLDP periods and many RT cycles
	mu.Lock()
	defer mu.Unlock()
	for k, n := range after {
		if n != 0 {
			t.Errorf("%d %s frame(s) after the gate closed; want none", n, k)
		}
	}
}
