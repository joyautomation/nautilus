package optommp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/runtime"
)

// #626: a standby holds no session with a unit whose communication
// watchdog the manifest sets. Any request from any client feeds that
// watchdog, so a standby that polled would keep it from tripping while a
// leader that cannot reach the unit keeps the lease.

// request is one OptoMMP request a replica sent, as its link saw it.
type request struct {
	write bool
	addr  uint32
	data  []byte
}

// link is one replica's way to the fake: every dial a fresh pipe, every
// request recorded, and a cut that closes the pipes and refuses dials (a
// network fault between this replica and the unit).
type link struct {
	dev   *fakeDevice
	mu    sync.Mutex
	reqs  []request
	dials int
	conns []net.Conn
	down  atomic.Bool
}

func newLink(dev *fakeDevice) *link { return &link{dev: dev} }

func (l *link) dial(context.Context) (net.Conn, error) {
	if l.down.Load() {
		return nil, errors.New("unreachable")
	}
	a, b := net.Pipe()
	go l.dev.Serve(b)
	l.mu.Lock()
	l.dials++
	l.conns = append(l.conns, a)
	l.mu.Unlock()
	return &tapConn{Conn: a, l: l}, nil
}

// cut is the fault: the replica's connections close and it cannot dial.
func (l *link) cut() {
	l.down.Store(true)
	l.mu.Lock()
	for _, c := range l.conns {
		c.Close()
	}
	l.mu.Unlock()
}

func (l *link) log() (dials int, reqs []request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dials, append([]request(nil), l.reqs...)
}

// tapConn records each request: the client writes every request (header,
// and a write's data) in one Write.
type tapConn struct {
	net.Conn
	l *link
}

func (c *tapConn) Write(p []byte) (int, error) {
	if len(p) >= 16 {
		r := request{write: p[3]>>4 == tcodeWriteBlockRequest, addr: binary.BigEndian.Uint32(p[8:12])}
		if r.write {
			r.data = append([]byte(nil), p[16:]...)
		}
		c.l.mu.Lock()
		c.l.reqs = append(c.l.reqs, r)
		c.l.mu.Unlock()
	}
	return c.Conn.Write(p)
}

// replica runs a driver for m over l with write gate g, scanning every
// 5 ms.
func replica(t *testing.T, m Manifest, l *link, g *gateWatch) *Driver {
	t.Helper()
	drv, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(l.dial))
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

// The case #626 is about: a leader that cannot reach the unit while its
// standby still could. The standby sends nothing, so once the leader goes
// silent the unit's watchdog trips and drives DO00 to its watchdog value.
func TestWatchdogStandbySendsNothingSoTheWatchdogTrips(t *testing.T) {
	dev := newBenchDevice()
	dev.SetWatchdogValue(1, 0, 1) // DO00 fails safe ON
	wd := 100 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: &wd})

	la, lb := newLink(dev), newLink(dev)
	ga, gb := &gateWatch{}, &gateWatch{}
	ga.open.Store(true)
	a := replica(t, m, la, ga)
	eventually(t, "the leader's scans and its watchdog", func() bool {
		return a.Health().Reads > 3 && dev.U32(fakeWatchdog) == 100
	})
	b := replica(t, m, lb, gb)
	gb.seen()
	time.Sleep(100 * time.Millisecond) // twenty of the standby's scans, were it polling

	if dials, reqs := lb.log(); dials != 0 || len(reqs) != 0 {
		t.Errorf("the standby dialled %d times and sent %d requests: it feeds the unit's watchdog", dials, len(reqs))
	}
	if h := b.Health(); h.Mode != "standby (watchdog)" || h.Connected || h.Reads != 0 {
		t.Errorf("standby: mode %q, connected %v, reads %d; want standby (watchdog), false, 0", h.Mode, h.Connected, h.Reads)
	}
	if on, _, _ := status(b); on {
		t.Error("the standby reports online without a session")
	}
	if do00(dev) {
		t.Fatal("DO00 on before the fault: the test proves nothing")
	}

	la.cut() // the leader keeps the lease and cannot reach the unit
	deadline := time.Now().Add(time.Second)
	for !dev.WatchdogTripped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !dev.WatchdogTripped() {
		_, reqs := lb.log()
		t.Fatalf("the unit's watchdog never tripped with the leader cut off (the standby sent %d requests)", len(reqs))
	}
	if !do00(dev) {
		t.Error("the watchdog tripped but DO00 is not at its watchdog value")
	}
}

// Only a watchdog the manifest sets keeps the standby away: without
// `watchdog:`, or with 0s, the standby polls as #622 left it.
func TestStandbyPollsUnlessTheManifestSetsTheWatchdog(t *testing.T) {
	off, on := time.Duration(0), 100*time.Millisecond
	for _, tc := range []struct {
		name     string
		watchdog *time.Duration
		polls    bool
	}{
		{"no watchdog key", nil, true},
		{"watchdog 0s", &off, true},
		{"watchdog 100ms", &on, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dev := newBenchDevice()
			m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: tc.watchdog})
			l, g := newLink(dev), &gateWatch{}
			drv := replica(t, m, l, g)
			if tc.polls {
				eventually(t, "the standby's polls", func() bool { return drv.Health().Reads > 5 })
				if h := drv.Health(); h.Mode != "poll" || !h.Connected {
					t.Errorf("mode %q, connected %v; want poll, true", h.Mode, h.Connected)
				}
				return
			}
			g.seen()
			time.Sleep(100 * time.Millisecond)
			if dials, reqs := l.log(); dials != 0 || len(reqs) != 0 {
				t.Errorf("dialled %d times, sent %d requests; want none", dials, len(reqs))
			}
			if h := drv.Health(); h.Mode != "standby (watchdog)" {
				t.Errorf("mode %q, want standby (watchdog)", h.Mode)
			}
		})
	}
}

// followProg copies DI03 (on at the unit) to DO00: a scan that ran on
// inputs not read since the takeover would command DO00 off.
const followProg = `PROGRAM Main
VAR_EXTERNAL
  EPIC_M0_DI03 : BOOL;
  EPIC_M1_DO00 : BOOL;
END_VAR
EPIC_M1_DO00 := EPIC_M0_DI03;
END_PROGRAM`

// A watchdog standby's takeover connects, configures the watchdog, and
// reads the unit before it writes; and the first command it sends comes
// from a scan that read those inputs, not from the scans the runtime ran
// as leader before the driver had any.
func TestWatchdogStandbyTakeoverReadsBeforeItWrites(t *testing.T) {
	dev := newBenchDevice()
	wd := 100 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: &wd})
	l, g := newLink(dev), &gateWatch{}
	drv := replica(t, m, l, g)
	rt, err := runtime.New(runtime.Options{
		Program:     followProg,
		Driver:      drv,
		Coordinator: leaderFunc(g.open.Load),
		Tags: []runtime.TagDef{
			// Seeded (a comms input exists from scan one, as `naut run`
			// builds it): the runtime's first scans as leader run on FALSE.
			{Name: "EPIC_M0_DI03", Role: runtime.RoleInput, Type: "BOOL", Comms: true},
			{Name: "EPIC_M1_DO00", Role: runtime.RoleOutput, Type: "BOOL"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g.seen()
	rt.Scan() // standing by: no scan
	time.Sleep(50 * time.Millisecond)
	if dials, reqs := l.log(); dials != 0 || len(reqs) != 0 {
		t.Errorf("before the takeover the standby dialled %d times and sent %d requests; want none", dials, len(reqs))
	}

	g.open.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for !do00(dev) && time.Now().Before(deadline) {
		rt.Scan()
		time.Sleep(5 * time.Millisecond)
	}
	if !do00(dev) {
		t.Fatal("DO00 never followed DI03 after the takeover")
	}
	if got := dev.U32(fakeWatchdog); got != 100 {
		t.Errorf("the new leader left the watchdog at %d ms, want 100", got)
	}

	_, reqs := l.log()
	fetched := false // a read of one of the scan's packed areas
	for _, r := range reqs {
		if !r.write {
			for _, a := range drv.plan.areas {
				fetched = fetched || r.addr == a.base
			}
			continue
		}
		if r.addr == addrSetWatchdog {
			continue // configuring the unit, not a command
		}
		if !fetched {
			t.Fatalf("wrote 0x%08X before any scan read the unit", r.addr)
		}
		if r.addr >= addrDigitalMOMO && r.addr < addrDigitalMOMO+MaxModules*momoModuleStride {
			lo := int(r.addr-addrDigitalMOMO) / momoModuleStride
			if off := (1 - lo) * momoModuleStride; off >= 0 && off+8 <= len(r.data) &&
				binary.BigEndian.Uint32(r.data[off+4:])&1 != 0 {
				t.Fatal("the takeover commanded DO00 off: a scan computed it from inputs not read since the takeover")
			}
		}
	}
}
