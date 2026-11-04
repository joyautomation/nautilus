package optommp

import (
	"context"
	"net"
	"testing"
	"time"
)

// A standby changes nothing on the unit (#618): it clears no latch, and it
// never configures, takes or puts back the stream. The role follows the
// write gate, so a takeover moves the stream to the new leader.

// latchSet reports whether DI05's on-latch is set at the unit.
func latchSet(dev *fakeDevice) bool { return dev.U32(addrDigitalPacked+8)&(1<<5) != 0 }

// A standby polling a set latch leaves it set at the unit, and reads it
// set; the leader then sees it and clears it. The standby runs its own
// session (the standby branch, as `naut run` starts it); the leader's
// scans are driven by hand, one poll at a time.
func TestStandbyLeavesALatchSet(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Latches: true})
	standby := gatedTCP(t, m, dev.Listen(t), &gateWatch{}) // gate closed
	eventually(t, "the standby's polls", func() bool { return standby.Health().Reads > 3 })

	dev.SetDigital(0, 5, true) // the event the leader must see
	eventually(t, "the standby reading the latch", func() bool { return value(t, standby, "EPIC_M0_DI05_OnLatch") == true })
	reads := standby.Health().Reads
	eventually(t, "more standby scans", func() bool { return standby.Health().Reads > reads+5 })
	if !latchSet(dev) {
		t.Fatal("a standby's scans cleared the latch: the leader never sees the event")
	}

	leader, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	leader.SetWriteGate(func() bool { return true })
	a, b := net.Pipe()
	go dev.Serve(b)
	lc := NewClient(a, time.Second)
	t.Cleanup(func() { lc.Close() })
	if err := leader.poll(lc); err != nil {
		t.Fatal(err)
	}
	if value(t, leader, "EPIC_M0_DI05_OnLatch") != true {
		t.Fatal("the leader did not see the latch")
	}
	if latchSet(dev) {
		t.Error("the leader did not clear the latch it delivered")
	}
}

// A lone standby on a unit no replica has configured changes nothing: the
// custom data area stays empty, the stream off, the watchdog as found, and
// the power-up clear stays owed. When its gate opens it leads and does
// all of it.
func TestLoneStandbyLeavesABlankUnitBlank(t *testing.T) {
	dev := newBenchDevice()
	wd := 500 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond, Watchdog: &wd})
	dev.PutU32(addrPUCFlag, 1)
	watchdog := dev.U32(fakeWatchdog)
	g := &gateWatch{}
	drv := gatedTCP(t, m, dev.Listen(t), g)
	eventually(t, "the standby's polls", func() bool { return drv.Health().Reads > 10 })
	if p := dev.U32(fakeCustomConfig); p != 0 {
		t.Errorf("a standby wrote custom pointer 0: %08X", p)
	}
	if on := dev.U32(fakeStreamOn); on != 0 {
		t.Error("a standby turned the stream on")
	}
	if got := dev.U32(fakeWatchdog); got != watchdog {
		t.Errorf("a standby set the watchdog: %d ms, was %d", got, watchdog)
	}
	if got := dev.U32(addrPowerupClear); got != 0 {
		t.Error("a standby sent the power-up clear")
	}
	if h := drv.Health(); h.Mode != "poll" {
		t.Errorf("standby mode %q, want poll", h.Mode)
	}

	g.open.Store(true)
	eventually(t, "leading: streaming", func() bool { return drv.Health().Mode == "stream" && dev.U32(fakeStreamOn) != 0 })
	if got := dev.U32(fakeWatchdog); got != 500 {
		t.Errorf("leading: watchdog %d ms, want 500", got)
	}
	if got := dev.U32(addrPowerupClear); got != 1 {
		t.Error("leading: the power-up clear owed was not sent")
	}
	if dev.U32(fakeCustomConfig) == 0 {
		t.Error("leading: the custom data area was not configured")
	}
}

// gatedTCP runs a replica against the fake over loopback TCP on port, with
// write gate g.
func gatedTCP(t *testing.T, m Manifest, port int, g *gateWatch) *Driver {
	t.Helper()
	return gatedTCPAt(t, m, port, g, 5*time.Millisecond)
}

// gatedTCPAt is gatedTCP at scan rate every.
func gatedTCPAt(t *testing.T, m Manifest, port int, g *gateWatch, every time.Duration) *Driver {
	t.Helper()
	m.Host, m.Port = "127.0.0.1", port
	drv, err := New(m, WithScanRate(every))
	if err != nil {
		t.Fatal(err)
	}
	drv.retryMin = 10 * time.Millisecond
	drv.chassis = time.Hour
	drv.SetWriteGate(g.gate)
	drv.Start(context.Background())
	t.Cleanup(drv.Stop)
	return drv
}

// streamConf is the unit's stream configuration that a replica could
// change: on, interval, port and target #1.
type streamConf struct{ on, interval, port, target uint32 }

func streamOf(dev *fakeDevice) streamConf {
	return streamConf{dev.U32(fakeStreamOn), dev.U32(fakeStreamIntvl), dev.U32(fakeStreamPort), dev.U32(fakeStreamTarget)}
}

// steady fails the test if the stream configuration moves from want during
// d.
func steady(t *testing.T, dev *fakeDevice, want streamConf, d time.Duration, what string) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if got := streamOf(dev); got != want {
			t.Fatalf("%s: stream configuration moved from %+v to %+v", what, want, got)
		}
	}
}

// leaderAndStandby starts a streaming leader, waits for its stream, then a
// standby on the same unit, and waits for the standby's polls.
func leaderAndStandby(t *testing.T) (dev *fakeDevice, a, b *Driver, ga, gb *gateWatch, conf streamConf) {
	t.Helper()
	dev = newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Stream: 5 * time.Millisecond})
	port := dev.Listen(t)
	ga, gb = &gateWatch{}, &gateWatch{}
	ga.open.Store(true)
	a = gatedTCP(t, m, port, ga)
	eventually(t, "the leader's stream", func() bool { return a.Health().Reads > 3 && a.Health().Mode == "stream" })
	conf = streamOf(dev)
	if conf.on == 0 {
		t.Fatal("the leader is not streaming")
	}
	b = gatedTCP(t, m, port, gb)
	eventually(t, "the standby's polls", func() bool { return b.Health().Reads > 3 })
	return dev, a, b, ga, gb, conf
}

// One leader streaming and one standby on the same unit do not fight over
// stream target #1: the standby polls, and the leader's stream stays
// pointed at the leader and on time.
func TestStandbyLeavesTheStreamToTheLeader(t *testing.T) {
	dev, a, b, _, _, conf := leaderAndStandby(t)
	steady(t, dev, conf, 300*time.Millisecond, "a standby beside a streaming leader")
	if h := b.Health(); h.Mode != "poll" {
		t.Errorf("standby mode %q, want poll", h.Mode)
	}
	if h := a.Health(); h.Mode != "stream" || h.Late != 0 {
		t.Errorf("leader: mode %q, late %d; want stream, 0", h.Mode, h.Late)
	}
}

// A takeover moves the stream to the new leader: it configures the stream
// to itself, and the old leader stops using it and polls, leaving the
// unit's configuration to the new leader (it does not take it back).
func TestTakeoverMovesTheStream(t *testing.T) {
	dev, a, b, ga, gb, conf := leaderAndStandby(t)
	_, outA, _ := status(a)
	_, outB, _ := status(b)
	ga.open.Store(false)
	gb.open.Store(true)
	eventually(t, "the new leader streaming", func() bool {
		return b.Health().Mode == "stream" && streamOf(dev).port != conf.port
	})
	eventually(t, "the old leader polling", func() bool { return a.Health().Mode == "poll" })
	moved := streamOf(dev)
	if moved.on == 0 {
		t.Fatal("the stream is off after the takeover")
	}
	reads := b.Health().Reads
	steady(t, dev, moved, 300*time.Millisecond, "after the takeover")
	if b.Health().Reads <= reads+10 {
		t.Errorf("the new leader got %d datagrams in 300 ms", b.Health().Reads-reads)
	}
	if h := b.Health(); h.Late != 0 {
		t.Errorf("the new leader's stream went late %d times", h.Late)
	}
	// Both switched on the connection they had: no outage on either.
	if _, o, _ := status(a); o != outA {
		t.Errorf("the old leader counted %d outages in the switch", o-outA)
	}
	if _, o, _ := status(b); o != outB {
		t.Errorf("the new leader counted %d outages in the switch", o-outB)
	}
}

// A standby's clean stop puts nothing back: the leader's stream and custom
// data area stay as the leader configured them.
func TestStandbyStopLeavesTheStream(t *testing.T) {
	dev, a, b, _, _, conf := leaderAndStandby(t)
	pointer := dev.U32(fakeCustomConfig)
	b.Stop()
	if got := streamOf(dev); got != conf {
		t.Errorf("a standby's stop moved the stream configuration from %+v to %+v", conf, got)
	}
	if p := dev.U32(fakeCustomConfig); p != pointer {
		t.Errorf("a standby's stop changed custom pointer 0 from %08X to %08X", pointer, p)
	}
	reads := a.Health().Reads
	eventually(t, "the leader's stream going on", func() bool { return a.Health().Reads > reads+10 })
}

// An old leader stopped after it lost the lease, before its own scan has
// looked at the gate, does not put its configuration back over the new
// leader's: a restore runs only while the gate is open at the stop. Here
// the old leader scans every 900 ms, so the new leader (5 ms) takes over
// and configures the custom data area well before the old one looks.
func TestOldLeaderStopDoesNotRestoreUnderTheNewLeader(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{CustomArea: true})
	port := dev.Listen(t)
	ga, gb := &gateWatch{}, &gateWatch{}
	ga.open.Store(true)
	a := gatedTCPAt(t, m, port, ga, 900*time.Millisecond)
	eventually(t, "the old leader's first scan", func() bool { return a.Health().Reads > 0 && dev.U32(fakeCustomConfig) != 0 })
	b := gatedTCP(t, m, port, gb)
	eventually(t, "the standby's polls", func() bool { return b.Health().Reads > 3 })

	ga.open.Store(false)
	gb.open.Store(true)
	eventually(t, "the new leader", func() bool { return b.Health().Mode == "custom area" })
	pointer := dev.U32(fakeCustomConfig)
	a.Stop()
	if p := dev.U32(fakeCustomConfig); p != pointer {
		t.Fatalf("the old leader's stop changed custom pointer 0 from %08X to %08X under the new leader", pointer, p)
	}
	reads := b.Health().Reads
	eventually(t, "the new leader's scans", func() bool { return b.Health().Reads > reads+5 })
	if v := value(t, b, "EPIC_M0_DI03"); v != true {
		t.Errorf("the new leader reads DI03 = %v, want true (it is on at the unit)", v)
	}
}
