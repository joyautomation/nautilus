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
// set; the leader then sees it and clears it. Scans are driven by hand,
// one poll at a time on one connection per replica.
func TestStandbyLeavesALatchSet(t *testing.T) {
	dev := newBenchDevice()
	m := benchManifest(t, dev, ManifestOptions{Latches: true})
	replica := func(open bool) (*Driver, *Client) {
		drv, err := New(m)
		if err != nil {
			t.Fatal(err)
		}
		drv.SetWriteGate(func() bool { return open })
		a, b := net.Pipe()
		go dev.Serve(b)
		c := NewClient(a, time.Second)
		t.Cleanup(func() { c.Close() })
		return drv, c
	}
	standby, sc := replica(false)
	leader, lc := replica(true)

	dev.SetDigital(0, 5, true) // the event the leader must see
	for i := 0; i < 3; i++ {
		if err := standby.poll(sc); err != nil {
			t.Fatal(err)
		}
	}
	if !latchSet(dev) {
		t.Fatal("a standby's scans cleared the latch: the leader never sees the event")
	}
	if value(t, standby, "EPIC_M0_DI05_OnLatch") != true {
		t.Error("the standby does not read the latch it left set")
	}

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

// gatedTCP runs a replica against the fake over loopback TCP on port, with
// write gate g.
func gatedTCP(t *testing.T, m Manifest, port int, g *gateWatch) *Driver {
	t.Helper()
	m.Host, m.Port = "127.0.0.1", port
	drv, err := New(m, WithScanRate(5*time.Millisecond))
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
