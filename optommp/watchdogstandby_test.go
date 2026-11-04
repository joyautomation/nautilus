package optommp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
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
// network fault between this replica and the unit). delay is how long a
// dial takes (a real connect is not instant; a pipe is).
type link struct {
	dev   *fakeDevice
	delay time.Duration
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
	time.Sleep(l.delay)
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

// A polling standby on a unit whose watchdog is on though the manifest
// does not set it (set in groov Manage, or left armed after `watchdog:`
// was deleted) feeds it: Health says so, and leading clears it.
func TestPollingStandbyReportsAWatchdogTheManifestDoesNotName(t *testing.T) {
	dev := newBenchDevice()
	dev.PutU32(fakeWatchdog, 500)
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond})
	l, g := newLink(dev), &gateWatch{}
	drv := replica(t, m, l, g)
	eventually(t, "the standby's polls", func() bool { return drv.Health().Reads > 3 })
	if h := drv.Health(); h.FedWatchdogMs != 500 {
		t.Errorf("standby: FedWatchdogMs %d, want 500", h.FedWatchdogMs)
	}
	g.open.Store(true)
	eventually(t, "leading", func() bool { return drv.Health().FedWatchdogMs == 0 })
}

// takeoverRig is a standby with the watchdog set, its link taking 50 ms to
// dial, and a runtime over drv (or over a set holding it) that leads when
// the gate is open.
type takeoverRig struct {
	dev *fakeDevice
	l   *link
	g   *gateWatch
	drv *Driver
}

func newTakeoverRig(t *testing.T) *takeoverRig {
	t.Helper()
	dev := newBenchDevice()
	wd := 100 * time.Millisecond
	m := benchManifest(t, dev, ManifestOptions{ScanRate: 5 * time.Millisecond, Watchdog: &wd})
	l, g := newLink(dev), &gateWatch{}
	l.delay = 50 * time.Millisecond
	return &takeoverRig{dev: dev, l: l, g: g, drv: replica(t, m, l, g)}
}

// runtime builds a runtime over driver with the program and tags; DI03 is
// a seeded comms input, as `naut run` builds it, so a scan before the unit
// is read runs on FALSE.
func (k *takeoverRig) runtime(t *testing.T, driver nio.Driver, prog string, wait time.Duration, tags ...runtime.TagDef) *runtime.Runtime {
	t.Helper()
	rt, err := runtime.New(runtime.Options{
		Program:            prog,
		Driver:             driver,
		Coordinator:        leaderFunc(k.g.open.Load),
		TakeoverInputsWait: wait,
		Tags: append([]runtime.TagDef{
			{Name: "EPIC_M0_DI03", Role: runtime.RoleInput, Type: "BOOL", Comms: true},
		}, tags...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// takeOver stands by (checking nothing reaches the unit), opens the gate,
// and scans every 5 ms until the driver has made n scans of the unit.
func (k *takeoverRig) takeOver(t *testing.T, rt *runtime.Runtime, n uint64) {
	t.Helper()
	k.g.seen()
	rt.Scan() // standing by: no scan
	time.Sleep(50 * time.Millisecond)
	if dials, reqs := k.l.log(); dials != 0 || len(reqs) != 0 {
		t.Errorf("before the takeover the standby dialled %d times and sent %d requests; want none", dials, len(reqs))
	}
	k.g.open.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for k.drv.Health().Reads < n && time.Now().Before(deadline) {
		rt.Scan()
		time.Sleep(5 * time.Millisecond)
	}
	for range 5 {
		rt.Scan()
		time.Sleep(5 * time.Millisecond)
	}
}

// commandedDO00 lists the DO00 commands in the link's must-on/must-off
// writes, in order (true: on).
func (k *takeoverRig) commandedDO00() []bool {
	_, reqs := k.l.log()
	var out []bool
	for _, r := range reqs {
		if !r.write || r.addr < addrDigitalMOMO || r.addr >= addrDigitalMOMO+MaxModules*momoModuleStride {
			continue
		}
		lo := int(r.addr-addrDigitalMOMO) / momoModuleStride
		off := (1 - lo) * momoModuleStride
		if off < 0 || off+8 > len(r.data) {
			continue
		}
		if binary.BigEndian.Uint32(r.data[off:])&1 != 0 {
			out = append(out, true)
		}
		if binary.BigEndian.Uint32(r.data[off+4:])&1 != 0 {
			out = append(out, false)
		}
	}
	return out
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

var do00Tag = runtime.TagDef{Name: "EPIC_M1_DO00", Role: runtime.RoleOutput, Type: "BOOL"}

// A watchdog standby's takeover connects, configures the watchdog, and
// reads the unit before it writes; and the first command it sends comes
// from a scan that read those inputs, not from the scans the runtime ran
// as leader before the driver had any.
func TestWatchdogStandbyTakeoverReadsBeforeItWrites(t *testing.T) {
	k := newTakeoverRig(t)
	rt := k.runtime(t, k.drv, followProg, 0, do00Tag)
	k.takeOver(t, rt, 5)
	if !do00(k.dev) {
		t.Fatal("DO00 never followed DI03 after the takeover")
	}
	if got := k.dev.U32(fakeWatchdog); got != 100 {
		t.Errorf("the new leader left the watchdog at %d ms, want 100", got)
	}
	_, reqs := k.l.log()
	fetched := false // a read of one of the scan's packed areas
	for _, r := range reqs {
		if !r.write {
			for _, a := range k.drv.plan.areas {
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
	}
	if got := k.commandedDO00(); len(got) == 0 || !got[0] {
		t.Errorf("DO00 commands %v: the first must be on (DI03 is on at the unit)", got)
	}
}

// If the unit is not live within TakeoverInputsWait, the takeover runs on
// the inputs the driver holds (here, none: DI03 seeded FALSE), but the
// driver's own hold still keeps those scans' commands off the unit: the
// first DO00 command is on, from a scan that read it.
func TestWatchdogStandbyTakeoverPastTheWaitStillHoldsStaleCommands(t *testing.T) {
	k := newTakeoverRig(t)
	k.l.delay = 150 * time.Millisecond
	rt := k.runtime(t, k.drv, followProg, 10*time.Millisecond, do00Tag)
	k.takeOver(t, rt, 5)
	if got := k.commandedDO00(); len(got) == 0 || !got[0] {
		t.Errorf("DO00 commands %v: the first must be on; the scans that ran before the unit was read are not commands", got)
	}
}

// latchProg latches a fault the first scan DI03 reads off, and drives DO00
// with it: a warm start on the standby's view (DI03 seeded FALSE) latches
// it, though DI03 is on at the unit.
const latchProg = `PROGRAM Main
VAR_EXTERNAL
  EPIC_M0_DI03 : BOOL;
  EPIC_M1_DO00 : BOOL;
END_VAR
VAR
  Fault : BOOL;
END_VAR
IF NOT EPIC_M0_DI03 THEN Fault := TRUE; END_IF;
EPIC_M1_DO00 := Fault;
END_PROGRAM`

// The takeover's frame reset and the logic wait for live inputs: the
// warm start runs on the unit, not on the standby's view, so no fault
// latches and DO00 is never commanded on.
func TestWatchdogStandbyTakeoverWarmStartsOnLiveInputs(t *testing.T) {
	k := newTakeoverRig(t)
	rt := k.runtime(t, k.drv, latchProg, 0, do00Tag)
	k.takeOver(t, rt, 10)
	if got := k.commandedDO00(); slices.Contains(got, true) || do00(k.dev) {
		t.Errorf("DO00 commands %v, on at the unit %v: the warm start latched a fault from inputs not read since the takeover", got, do00(k.dev))
	}
}

// sibling is another driver in the same set: one output, every value it
// is handed recorded.
type sibling struct {
	mu  sync.Mutex
	got []bool
}

func (s *sibling) InputNames() []string            { return nil }
func (s *sibling) OutputNames() []string           { return []string{"SIB_OUT"} }
func (s *sibling) ReadInputs() (nio.Values, error) { return nio.Values{}, nil }
func (s *sibling) values() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.got...)
}
func (s *sibling) WriteOutputs(v nio.Values) error {
	if b, ok := v["SIB_OUT"].(bool); ok {
		s.mu.Lock()
		s.got = append(s.got, b)
		s.mu.Unlock()
	}
	return nil
}

// Every driver's outputs wait for the EPIC's live inputs, not only the
// EPIC's own: a sibling in the same io.Multi set that copies DI03 is never
// handed FALSE.
func TestWatchdogStandbyTakeoverHoldsSiblingOutputs(t *testing.T) {
	k := newTakeoverRig(t)
	sib := &sibling{}
	set, err := nio.NewMulti(nio.NamedDriver{Name: "epic", Driver: k.drv}, nio.NamedDriver{Name: "sib", Driver: sib})
	if err != nil {
		t.Fatal(err)
	}
	prog := `PROGRAM Main
VAR_EXTERNAL
  EPIC_M0_DI03 : BOOL;
  SIB_OUT : BOOL;
END_VAR
SIB_OUT := EPIC_M0_DI03;
END_PROGRAM`
	rt := k.runtime(t, set, prog, 0, runtime.TagDef{Name: "SIB_OUT", Role: runtime.RoleOutput, Type: "BOOL"})
	k.takeOver(t, rt, 5)
	got := sib.values()
	if len(got) == 0 || slices.Contains(got, false) {
		t.Errorf("the sibling was handed %v: every value must be TRUE (DI03 is on at the unit)", got)
	}
}
