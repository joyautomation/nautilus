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
)

// #861 G2: an optommp or PLCnext driver is one connection, so its Sparkplug
// device births with the link and dies with it. Without a driverHealth case
// its health was nil, "always healthy", and a lost processor never sent a
// DDEATH.

// cuttableBench serves the in-process bench chassis on a listener the test
// can take down: cut closes it and every connection, so the driver loses
// its session and cannot get a new one.
func cuttableBench(t *testing.T) (port int, cut func()) {
	t.Helper()
	dev := optommptest.NewBench()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go dev.Serve(c)
		}
	}()
	cut = func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	}
	t.Cleanup(cut)
	return ln.Addr().(*net.TCPAddr).Port, cut
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
	port, cut := cuttableBench(t)
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
