package optommp

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// The fake's addresses, spelled out from form 1465 rather than taken from
// the driver's constants.
const (
	fakeWatchdog     = 0xF0300054
	fakeCustomConfig = 0xF0D50000
	fakeStreamOn     = 0xF03FFFD0
	fakeStreamIntvl  = 0xF03FFFD4
	fakeStreamPort   = 0xF03FFFD8
	fakeStreamTarget = 0xF03FFFE0
)

// startDriverTCP runs a driver that dials the fake over loopback TCP, as a
// stream needs a real address to be sent to. The chassis check is left at
// an hour so the only TCP traffic is what the test causes.
func startDriverTCP(t *testing.T, d *fakeDevice, m Manifest) *Driver {
	t.Helper()
	m.Host, m.Port = "127.0.0.1", d.Listen(t)
	drv, err := New(m, WithScanRate(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = time.Hour
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv
}

func TestDriverCustomAreaIsOneReadWithTheSameResult(t *testing.T) {
	o := ManifestOptions{Latches: true, Quality: true, ScratchBits: 40, ScratchInts: 2, ScratchFloats: 2}
	packedDev, customDev := newBenchDevice(), newBenchDevice()
	for _, dev := range []*fakeDevice{packedDev, customDev} {
		dev.PutU32(0xF0D80000, 1<<3) // scratch bit 35
		dev.PutU32(0xF0D80004, 1<<2) // scratch bit 2
	}
	packed := startDriver(t, packedDev, benchManifest(t, packedDev, o))
	o.CustomArea = true
	cm := benchManifest(t, customDev, o)
	if !cm.CustomArea {
		t.Fatal("GenerateManifest dropped custom-area")
	}
	custom := startDriver(t, customDev, cm)
	eventually(t, "both scanned", func() bool { return packed.Health().Reads > 1 && custom.Health().Reads > 1 })

	pv, _ := packed.ReadInputs()
	cv, _ := custom.ReadInputs()
	for k := range pv {
		if fmt.Sprint(pv[k]) != fmt.Sprint(cv[k]) { // NaN reads alike in both
			t.Errorf("%s: packed %v, custom area %v", k, pv[k], cv[k])
		}
	}
	if pq, cq := packed.Quality(), custom.Quality(); !reflect.DeepEqual(pq, cq) {
		t.Errorf("quality differs:\npacked %v\ncustom %v", pq, cq)
	}
	if cv["EPIC_SP_B35"] != true || cv["EPIC_SP_B2"] != true || cv["EPIC_SP_B3"] != false {
		t.Errorf("scratch bits through pointers: B35 %v B2 %v B3 %v", cv["EPIC_SP_B35"], cv["EPIC_SP_B2"], cv["EPIC_SP_B3"])
	}
	if h := custom.Health(); h.Requests != 1 || h.Mode != "custom area" {
		t.Errorf("custom area scan: %d requests, mode %q; want 1, \"custom area\"", h.Requests, h.Mode)
	}
	if h := packed.Health(); h.Requests < 4 || h.Mode != "poll" {
		t.Errorf("packed scan: %d requests, mode %q", h.Requests, h.Mode)
	}
	if p := customDev.U32(fakeCustomConfig); p != 0xF0260000+2*0x1000 {
		// the lowest word: slot 2 channel 0 through its expanded analog
		// read address, not the packed values
		t.Errorf("pointer 0 = %08X", p)
	}
}

func TestDriverRewritesCustomPointersAnotherClientChanged(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{CustomArea: true}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	want := dev.U32(fakeCustomConfig)
	dev.PutU32(fakeCustomConfig, 0xF0D82000)
	eventually(t, "the change noticed", func() bool { return strings.Contains(drv.Health().LastError, "changed under the driver") })
	eventually(t, "pointers restored", func() bool { return dev.U32(fakeCustomConfig) == want && drv.Health().Connected })
}

func TestDriverStreams(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond, Latches: true})
	if m.Stream == 0 || m.CustomArea {
		t.Fatalf("stream manifest: stream %s custom-area %v", time.Duration(m.Stream), m.CustomArea)
	}
	drv := startDriverTCP(t, dev, m)
	eventually(t, "first datagram", func() bool { return drv.Health().Reads > 0 })
	if v := value(t, drv, "EPIC_M0_DI03"); v != true {
		t.Errorf("DI03 = %v", v)
	}
	if q := drv.Quality(); q["EPIC_M0_DI03"] != nio.Good || q["EPIC_M2_AI01"] != nio.Bad {
		t.Errorf("quality over the stream: DI03 %s AI01 %s", q["EPIC_M0_DI03"], q["EPIC_M2_AI01"])
	}
	if dev.U32(fakeStreamTarget) != 0x7F000001 || dev.U32(fakeStreamIntvl) != 5 {
		t.Errorf("stream config: target %08X interval %d", dev.U32(fakeStreamTarget), dev.U32(fakeStreamIntvl))
	}

	// Inputs arrive with no request at all.
	reqs, reads := dev.Requests(), drv.Health().Reads
	dev.SetDigital(0, 5, true)
	eventually(t, "DI05 over the stream", func() bool { return value(t, drv, "EPIC_M0_DI05") == true })
	eventually(t, "more datagrams", func() bool { return drv.Health().Reads > reads+5 })
	// The latch the change set was cleared by one read-and-clear request.
	if got := dev.Requests() - reqs; got > 2 {
		t.Errorf("%d requests while streaming, want at most the latch clears", got)
	}

	// A command goes out over TCP and comes back on the stream.
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO04": true})
	eventually(t, "DO04 read back over the stream", func() bool { return value(t, drv, "EPIC_M1_DO04") == true })
	if h := drv.Health(); h.Mode != "stream" || h.Stream != 5*time.Millisecond {
		t.Errorf("health: mode %q stream %s", h.Mode, h.Stream)
	}
}

func TestDriverStreamLateIsStaleThenTimesOut(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond})
	m.Timeout = Duration(400 * time.Millisecond)
	drv := startDriverTCP(t, dev, m)
	eventually(t, "first datagram", func() bool { return drv.Health().Reads > 0 })

	// A short gap: Stale, then Good again on the same session.
	dev.PauseStream(true)
	eventually(t, "Stale", func() bool { return drv.Quality()["EPIC_M0_DI03"] == nio.Stale })
	dev.PauseStream(false)
	eventually(t, "Good again", func() bool { return drv.Quality()["EPIC_M0_DI03"] == nio.Good })
	if h := drv.Health(); h.Late != 1 || !h.Connected || h.LastError != "" {
		t.Errorf("after a short gap: late %d connected %v error %q", h.Late, h.Connected, h.LastError)
	}

	// A long one ends the session; the reconnect streams again.
	dev.PauseStream(true)
	eventually(t, "timeout", func() bool { return strings.Contains(drv.Health().LastError, "missed incoming stream") })
	dev.PauseStream(false)
	eventually(t, "Good after reconnect", func() bool {
		return drv.Health().Connected && drv.Quality()["EPIC_M0_DI03"] == nio.Good
	})
}

func TestDriverStopPutsTheStreamConfigBack(t *testing.T) {
	dev := newBenchDevice()
	drv := startDriverTCP(t, dev, benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond}))
	eventually(t, "first datagram", func() bool { return drv.Health().Reads > 0 })
	if dev.U32(fakeStreamOn) == 0 {
		t.Fatal("streaming is not on")
	}
	drv.Stop()
	if p := dev.U32(fakeCustomConfig); p != 0 {
		t.Errorf("after Stop pointer 0 is %08X, want it cleared as found", p)
	}
	if on, iv, port, tgt := dev.U32(fakeStreamOn), dev.U32(fakeStreamIntvl), dev.U32(fakeStreamPort), dev.U32(fakeStreamTarget); on != 0 || iv != 1000 || port != 5001 || tgt != 0 {
		t.Errorf("after Stop: on %d interval %d port %d target %08X; want 0 1000 5001 0", on, iv, port, tgt)
	}
}

func TestDriverWatchdog(t *testing.T) {
	wd := 150 * time.Millisecond
	dev := newBenchDevice()
	dev.PutU32(0xF0100000+1*0x3000+4*0xC0+0x28, 1) // DO04 has a watchdog value enabled
	drv := startDriverTCP(t, dev, benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond, Watchdog: &wd}))
	eventually(t, "first datagram", func() bool { return drv.Health().Reads > 0 })
	if got := dev.U32(fakeWatchdog); got != 150 {
		t.Fatalf("watchdog = %d ms, want 150", got)
	}
	// Streaming sends no requests of its own; the driver keeps the
	// watchdog fed with nothing to write.
	time.Sleep(5 * wd)
	if dev.WatchdogTripped() {
		t.Error("the watchdog tripped while the driver was running")
	}
	if h := drv.Health(); h.WatchdogMs != 150 || h.Armed != 1 || h.Armable != 12 {
		t.Errorf("health: watchdog %d armed %d of %d", h.WatchdogMs, h.Armed, h.Armable)
	}
	drv.Stop()
	time.Sleep(2 * wd)
	if !dev.WatchdogTripped() {
		t.Error("the watchdog did not trip after the driver stopped")
	}
}

func TestDriverWatchdogOffAndLeftAlone(t *testing.T) {
	off := time.Duration(0)
	dev := newBenchDevice()
	dev.PutU32(fakeWatchdog, 500)
	drv := startDriver(t, dev, benchManifest(t, dev, ManifestOptions{Watchdog: &off}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if got := dev.U32(fakeWatchdog); got != 0 {
		t.Errorf("watchdog: 0s left it at %d ms", got)
	}

	dev = newBenchDevice()
	dev.PutU32(fakeWatchdog, 700)
	drv = startDriver(t, dev, benchManifest(t, dev, ManifestOptions{}))
	eventually(t, "first scan", func() bool { return drv.Health().Reads > 0 })
	if got, h := dev.U32(fakeWatchdog), drv.Health(); got != 700 || h.WatchdogMs != 700 {
		t.Errorf("no watchdog key: unit %d ms, health %d; want both 700", got, h.WatchdogMs)
	}
}
