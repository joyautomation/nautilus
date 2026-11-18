package project

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/optommp"
	"github.com/joyautomation/nautilus/optommp/optommptest"
	"github.com/joyautomation/nautilus/plcnext"
	"github.com/joyautomation/nautilus/plcnext/plcnexttest"
)

// #861 G2: an optommp or PLCnext driver is one connection, so its Sparkplug
// device births with the link and dies with it. Without a driverHealth case
// its health was nil, "always healthy", and a lost processor never sent a
// DDEATH.

// cuttableBench serves the in-process bench chassis on a listener the test
// can take down and bring back: cut closes it and every connection, so the
// driver loses its session and cannot get a new one; restore listens on
// the same port again, as a processor back on the network.
func cuttableBench(t *testing.T) (port int, cut, restore func()) {
	t.Helper()
	dev := optommptest.NewBench()
	var mu sync.Mutex
	var ln net.Listener
	var conns []net.Conn
	listen := func(addr string) {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		ln = l
		mu.Unlock()
		go func() {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				conns = append(conns, c)
				mu.Unlock()
				go dev.Serve(c)
			}
		}()
	}
	listen("127.0.0.1:0")
	port = ln.Addr().(*net.TCPAddr).Port
	cut = func() {
		mu.Lock()
		defer mu.Unlock()
		ln.Close()
		for _, c := range conns {
			c.Close()
		}
		conns = nil
	}
	restore = func() { listen(fmt.Sprintf("127.0.0.1:%d", port)) }
	t.Cleanup(cut)
	return port, cut, restore
}

func waitHealth(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOptommpSparkplugDeviceDiesWithItsProcessor(t *testing.T) {
	port, cut, restore := cuttableBench(t)
	m, err := optommp.ParseManifest([]byte(fmt.Sprintf(`host: 127.0.0.1
port: %d
modules:
  - { slot: 0, part: "GRV-IACDCTTL-24", type: 0x50000005, channels: 24 }
tags:
  - { name: EPIC_M0_DI03, point: di, slot: 0, channel: 3 }
`, port)))
	if err != nil {
		t.Fatal(err)
	}
	drv, err := optommp.New(m, optommp.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices([]nio.NamedDriver{{Name: "epic", Driver: drv}})
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices %+v, %v: want one, the driver", devs, err)
	}
	dev := devs[0]
	if dev.Health == nil {
		t.Fatal("the optommp device has no health: it would never send DDEATH")
	}
	if dev.Health() {
		t.Error("healthy before the driver has connected")
	}
	drv.Start(context.Background())
	defer drv.Stop()
	waitHealth(t, "the device to read healthy once connected", 5*time.Second, dev.Health)
	cut()
	waitHealth(t, "the device to read unhealthy once the processor is gone", 5*time.Second, func() bool { return !dev.Health() })
	// Back on the network: the driver's next redial (1 s, then backing off)
	// finds it, and the device births again.
	restore()
	waitHealth(t, "the device to read healthy again once the processor is back", 10*time.Second, dev.Health)
}

func TestPLCnextSparkplugDeviceIsDeadWithoutItsController(t *testing.T) {
	raw, err := os.ReadFile("../../examples/plcnext-bench/plcnext_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := plcnext.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	// No controller: the firmware's socket is a path nothing listens on.
	m.Transport, m.Socket = plcnext.TransportGRPC, filepath.Join(t.TempDir(), "no-grpc.sock")
	drv, err := plcnext.New(m, plcnext.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices([]nio.NamedDriver{{Name: "axc", Driver: drv}})
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices %+v, %v: want one, the driver", devs, err)
	}
	if devs[0].Health == nil {
		t.Fatal("the plcnext device has no health: it would never send DDEATH")
	}
	drv.Start(context.Background())
	defer drv.Stop()
	time.Sleep(300 * time.Millisecond)
	if devs[0].Health() {
		t.Error("healthy with no controller to talk to")
	}
}

// With a controller (plcnexttest's): the device births once the driver's
// BusConductor handshake brings the inputs Good, dies on a stopped
// Axioline bus (a live socket, stale bytes) and when the controller goes,
// and births again each time it comes back.
func TestPLCnextSparkplugDeviceLivesAndDiesWithItsController(t *testing.T) {
	raw, err := os.ReadFile("../../examples/plcnext-bench/plcnext_manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := plcnext.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	axc := plcnexttest.New()
	m.Transport, m.Socket = plcnext.TransportGRPC, axc.Listen(t)
	drv, err := plcnext.New(m, plcnext.WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	devs, err := sparkplugDevices([]nio.NamedDriver{{Name: "axc", Driver: drv}})
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices %+v, %v: want one, the driver", devs, err)
	}
	dev := devs[0]
	if dev.Health == nil {
		t.Fatal("the plcnext device has no health: it would never send DDEATH")
	}
	if dev.Health() {
		t.Error("healthy before the driver has connected")
	}
	drv.Start(context.Background())
	defer drv.Stop()
	waitHealth(t, "the device to read healthy once the bus runs", 5*time.Second, dev.Health)

	axc.SetRun(false)
	waitHealth(t, "the device to read unhealthy on a stopped bus", 5*time.Second, func() bool { return !dev.Health() })
	axc.SetRun(true)
	waitHealth(t, "the device to read healthy once the bus runs again", 5*time.Second, dev.Health)

	axc.Drop()
	waitHealth(t, "the device to read unhealthy once the controller is gone", 5*time.Second, func() bool { return !dev.Health() })
	// A gRPC client redials by itself: restore only once the session ended.
	waitHealth(t, "the driver's session to end", 5*time.Second, func() bool { return !drv.Health().Connected })
	axc.Restore()
	waitHealth(t, "the device to read healthy again once the controller is back", 10*time.Second, dev.Health)
}
