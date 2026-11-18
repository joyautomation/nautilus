package optommp

import (
	"context"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// #865: without `redundancy:`, `naut run` gives optommp its liveness gate
// (io.SetPlainGate), open while the scans are live. A plain gate is no
// redundancy lease: there is no other replica, so a closed gate is a hung
// or starved scan, and the driver lets the unit's watchdog act (with
// `watchdog:`) or keeps reading and writes nothing (without).

// plainReplica runs a driver for m over l with the plain gate g, installed
// the way naut run does (io.SetPlainGate), scanning every 5 ms.
func plainReplica(t *testing.T, m Manifest, l *link, g *gateWatch) *Driver {
	t.Helper()
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(l.dial))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = time.Hour
	if n := nio.SetPlainGate(drv, g.gate); n != 1 {
		t.Fatalf("SetPlainGate took %d drivers, want optommp's 1", n)
	}
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv
}

// The point of #865: a hung scan closes the gate, the driver goes silent,
// the unit's watchdog takes DO00 to its watchdog value (off), and when the
// scans are live again DO00 is commanded back and the trip is counted.
func TestPlainGateWithWatchdogGoesSilentSoTheUnitTrips(t *testing.T) {
	dev := newBenchDevice()
	dev.SetWatchdogValue(1, 0, 0) // DO00 fails safe OFF
	wd := 100 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: &wd})
	l, g := newLink(dev), &gateWatch{}
	g.open.Store(true)
	drv := plainReplica(t, m, l, g)
	eventually(t, "the scans and the unit's watchdog", func() bool {
		return drv.Health().Reads > 3 && dev.U32(fakeWatchdog) == 100
	})
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true})
	eventually(t, "DO00 commanded on", func() bool { return do00(dev) })

	g.open.Store(false) // the scans stopped: the liveness gate closes
	g.seen()
	time.Sleep(20 * time.Millisecond) // a request already on the wire lands
	_, before := l.log()
	deadline := time.Now().Add(time.Second)
	for !dev.WatchdogTripped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// A hang lasts the gate's lease (4 s) at least: well past the
	// watchdog, as the silence here is once it has tripped.
	time.Sleep(3 * wd)
	if _, after := l.log(); len(after) != len(before) {
		t.Errorf("%d requests after the gate closed: they feed the unit's watchdog", len(after)-len(before))
	}
	if !dev.WatchdogTripped() || do00(dev) {
		t.Fatalf("watchdog tripped %v, DO00 %v: want tripped and DO00 at its watchdog value (off)", dev.WatchdogTripped(), do00(dev))
	}
	if h := drv.Health(); h.Mode != "held (scans not live)" || h.Online {
		t.Errorf("while held: mode %q, online %v; want held (scans not live), false", h.Mode, h.Online)
	}

	g.open.Store(true) // the scans are live again
	deadline = time.Now().Add(2 * time.Second)
	for !do00(dev) && time.Now().Before(deadline) {
		// What the runtime does each scan: read, then hand over every
		// output when the driver asks for them.
		drv.ReadInputs()
		if drv.OutputsWanted() {
			drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true})
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !do00(dev) {
		t.Fatal("DO00 was not commanded back once the scans were live again")
	}
	if h := drv.Health(); h.WatchdogTrips == 0 {
		t.Error("the trip was not counted (WatchdogTrips 0): a program's hold-until-acknowledged never sees it")
	}
}

// Without `watchdog:` there is nothing to starve: the driver keeps leading
// the unit (here through the custom data area, which a redundancy standby
// would give up for the packed areas) and reading it, and writes nothing
// until the gate reopens.
func TestPlainGateWithoutWatchdogReadsOnAndWritesNothing(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, CustomArea: true})
	l, g := newLink(dev), &gateWatch{}
	g.open.Store(true)
	drv := plainReplica(t, m, l, g)
	eventually(t, "the scans", func() bool { return drv.Health().Reads > 3 })
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": true})
	eventually(t, "DO00 commanded on", func() bool { return do00(dev) })

	if v, _ := drv.ReadInputs(); v["EPIC_M0_DI05"] != false {
		t.Fatalf("DI05 reads %v before the test sets it: the test proves nothing", v["EPIC_M0_DI05"])
	}
	g.open.Store(false)
	g.seen()
	reads := drv.Health().Reads
	dev.SetDigital(0, 5, true)
	eventually(t, "a changed input read while the gate is closed", func() bool {
		v, _ := drv.ReadInputs()
		return v["EPIC_M0_DI05"] == true
	})
	eventually(t, "scans going on while the gate is closed", func() bool { return drv.Health().Reads > reads+3 })
	if h := drv.Health(); h.Mode != "custom area" || !h.Online {
		t.Errorf("while closed: mode %q, online %v; want still leading the custom area, online", h.Mode, h.Online)
	}
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": false})
	time.Sleep(50 * time.Millisecond)
	if !do00(dev) {
		t.Fatal("DO00 written while the gate was closed")
	}

	g.open.Store(true)
	eventually(t, "the driver to ask for every output", func() bool {
		drv.ReadInputs()
		return drv.OutputsWanted()
	})
	drv.WriteOutputs(nio.Values{"EPIC_M1_DO00": false})
	eventually(t, "DO00 commanded off once the gate reopened", func() bool { return !do00(dev) })
}

// A clean stop puts the custom data area back even while the plain gate is
// closed: no other replica's configuration can be under it.
func TestPlainGateStopRestoresWhileClosed(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, CustomArea: true})
	l, g := newLink(dev), &gateWatch{}
	g.open.Store(true)
	drv := plainReplica(t, m, l, g)
	eventually(t, "the custom area in use", func() bool { return drv.Health().Reads > 3 && dev.U32(fakeCustomConfig) != 0 })
	if drv.replicated() {
		t.Error("a plain gate counted as a redundant set (replicated)")
	}
	g.open.Store(false)
	g.seen()
	drv.Stop()
	if p := dev.U32(fakeCustomConfig); p != 0 {
		t.Errorf("after a stop with the plain gate closed, pointer 0 is %08X; want it put back as found (0)", p)
	}
}

// The redundancy lease is untouched: a gate installed with SetWriteGate
// alone is still a replica's, so with `watchdog:` the closed gate is the
// #626 standby, not held.
func TestSetWriteGateAloneIsStillARedundancyLease(t *testing.T) {
	dev := newBenchDevice()
	wd := 100 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: &wd})
	l, g := newLink(dev), &gateWatch{}
	drv := replica(t, m, l, g)
	g.seen()
	time.Sleep(50 * time.Millisecond)
	if h := drv.Health(); h.Mode != "standby (watchdog)" {
		t.Errorf("mode %q, want standby (watchdog)", h.Mode)
	}
	if drv.plain.Load() || !drv.replicated() {
		t.Error("a redundancy lease was taken for a plain gate")
	}
}
