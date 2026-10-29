//go:build linux

package plcnext

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	nio "github.com/joyautomation/nautilus/io"
)

// fakeShim is rt/plcnext-io/nautilus_io's side of the page, cycle for
// cycle as the C does it: header once, then every period the inputs
// block under its seqlock (status, stamps, the input frame, the bytes it
// put in the output frame) after a bounded read of the driver's block,
// with the same watchdog rule.
type fakeShim struct {
	t   *testing.T
	mem []byte

	mu     sync.Mutex
	inputs [shmFrameLen]byte
	run    bool
	frame  [shmFrameLen]byte // the "bus" output frame

	stalled  atomic.Bool
	midWrite atomic.Bool // stalled with its sequence odd: descheduled inside a write
	cycles   uint64
	stop     chan struct{}
	done     chan struct{}
}

func startFakeShim(t *testing.T, path string) *fakeShim {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, shmSize), 0o666); err != nil {
		t.Fatal(err)
	}
	p, err := openShmPage(path)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeShim{t: t, mem: p.mem, run: true, stop: make(chan struct{}), done: make(chan struct{})}
	le := binary.LittleEndian
	le.PutUint32(s.mem[shmOffVersion:], shmVersion)
	le.PutUint32(s.mem[shmOffPeriodNs:], uint32(time.Millisecond))
	le.PutUint32(s.mem[shmOffFrameLen:], shmFrameLen)
	le.PutUint32(s.mem[shmOffPID:], 4242)
	le.PutUint64(s.mem[shmOffStartedNs:], uint64(monoNow()))
	le.PutUint32(s.mem[shmOffLateUs:], 200)
	atomic.StoreUint64((*uint64)(unsafe.Pointer(&s.mem[shmOffMagic])), shmMagic)
	go s.loop()
	t.Cleanup(func() { close(s.stop); <-s.done; p.Close() })
	return s
}

func (s *fakeShim) seq(off int) *uint32 { return (*uint32)(unsafe.Pointer(&s.mem[off])) }

func (s *fakeShim) loop() {
	defer close(s.done)
	le := binary.LittleEndian
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	var outLen, wdMs uint32
	var outNs int64
	var gen, applied uint64
	var outs [shmFrameLen]byte
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
		}
		if s.stalled.Load() {
			continue
		}
		if seq := s.seq(shmOffInSeq); s.midWrite.Load() {
			if q := atomic.LoadUint32(seq); q&1 == 0 {
				atomic.StoreUint32(seq, q+1)
			}
			continue
		} else if q := atomic.LoadUint32(seq); q&1 == 1 {
			atomic.StoreUint32(seq, q+1)
		}
		s.cycles++
		now := monoNow()
		// the driver's block, bounded
		for range 8 {
			s1 := atomic.LoadUint32(s.seq(shmOffOutSeq))
			if s1&1 == 1 {
				continue
			}
			l, ns, g, wd := le.Uint32(s.mem[shmOffOutLen:]), int64(le.Uint64(s.mem[shmOffOutNs:])), le.Uint64(s.mem[shmOffOutGen:]), le.Uint32(s.mem[shmOffWatchdogMs:])
			var b [shmFrameLen]byte
			copy(b[:], s.mem[shmOffOutputs:])
			if atomic.LoadUint32(s.seq(shmOffOutSeq)) == s1 {
				outLen, outNs, gen, wdMs, outs = min(l, shmFrameLen), ns, g, wd, b
				break
			}
		}
		s.mu.Lock()
		status := uint32(shmStPLC | shmStFrames | shmStRunKnown)
		if s.run {
			status |= shmStRun
		}
		wd := outLen > 0 && wdMs > 0 && now > outNs && time.Duration(now-outNs) > time.Duration(wdMs)*time.Millisecond
		s.frame = [shmFrameLen]byte{}
		if outLen > 0 && !wd {
			copy(s.frame[:outLen], outs[:outLen])
			applied = gen
		}
		if wd {
			status |= shmStWatchdog
		}
		in, echo := s.inputs, s.frame
		s.mu.Unlock()

		seq := s.seq(shmOffInSeq)
		q := atomic.LoadUint32(seq)
		atomic.StoreUint32(seq, q+1)
		le.PutUint32(s.mem[shmOffStatus:], status)
		le.PutUint64(s.mem[shmOffStampNs:], uint64(now))
		le.PutUint64(s.mem[shmOffFrameNs:], uint64(now))
		le.PutUint64(s.mem[shmOffCycles:], s.cycles)
		le.PutUint64(s.mem[shmOffApplied:], applied)
		copy(s.mem[shmOffInputs:], in[:])
		copy(s.mem[shmOffEcho:], echo[:])
		atomic.StoreUint32(seq, q+2)
	}
}

// restart is what page_open does for a shim that comes back: a fresh
// start stamp and pid on the same page.
func (s *fakeShim) restart() {
	binary.LittleEndian.PutUint32(s.mem[shmOffPID:], 4243)
	binary.LittleEndian.PutUint64(s.mem[shmOffStartedNs:], uint64(monoNow()))
}

func (s *fakeShim) setInput(off int, b byte) { s.mu.Lock(); s.inputs[off] = b; s.mu.Unlock() }
func (s *fakeShim) setRun(v bool)            { s.mu.Lock(); s.run = v; s.mu.Unlock() }
func (s *fakeShim) out(off int) byte         { s.mu.Lock(); defer s.mu.Unlock(); return s.frame[off] }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyWithin(t, 3*time.Second, what, cond)
}

func eventuallyWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// The whole of transport shm against a shim that behaves like the C one:
// the handshake still goes over gRPC, then the bytes go through the page —
// inputs in, outputs out to the frame, RUN from the shim, Stale when the
// shim stops cycling, the watchdog field set from the manifest.
func TestDriverOverShm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nautilus-plcnext-io")
	shim := startFakeShim(t, path)
	f := newFakeGDS()
	addr := serve(t, f)
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\noutput-watchdog: 250ms\n" + benchManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(ctx context.Context) (dataAccess, error) { return Dial(ctx, addr) }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()

	shim.setInput(0, 0x80) // DI_15
	shim.setInput(1, 0x01) // DI_00
	shim.setInput(2, 0x04)
	shim.setInput(3, 0xD2) // AI_00 = 1.234 V
	eventually(t, "inputs never arrived through the page", func() bool {
		in, _ := d.ReadInputs()
		return in["DI_00"] == true && in["DI_15"] == true
	})
	if in, _ := d.ReadInputs(); in["AI_00"].(float64) < 1.2339 || in["AI_00"].(float64) > 1.2341 {
		t.Errorf("AI_00 = %v", in["AI_00"])
	}
	if q := d.Quality()["AI_00"]; q != nio.Good {
		t.Errorf("quality %v", q)
	}
	// The handshake went over gRPC, the image did not.
	if w := f.wrote(); !strings.Contains(w, "START_IO_REQ=true") || strings.Contains(w, DefaultOutputs) {
		t.Errorf("gRPC writes %q: want the handshake and no image", w)
	}
	h := d.Health()
	if h.Transport != TransportShm || h.Shm != path || !h.Connected || !h.Running || h.Shim == nil {
		t.Fatalf("health %+v", h)
	}
	if h.Shim.PID != 4242 || h.Shim.PeriodUs != 1000 || h.Shim.Cycles == 0 || !h.Shim.RunKnown {
		t.Errorf("shim health %+v", *h.Shim)
	}
	if wd := binary.LittleEndian.Uint32(shim.mem[shmOffWatchdogMs:]); wd != 250 {
		t.Errorf("watchdog in the page %d ms, want 250", wd)
	}

	// Outputs reach the shim's frame and come back as its echo.
	if err := d.WriteOutputs(nio.Values{"DO_15": true, "AO_00": 5.0}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "outputs never reached the frame", func() bool {
		return shim.out(0) == 0x80 && shim.out(2) == 0x13 && shim.out(3) == 0x88
	})
	eventually(t, "the shim's echo never reached Health", func() bool {
		h := d.Health()
		return h.Shim.Applied > 0 && h.Shim.Applied <= h.Shim.Written
	})

	// RUN from the shim: Bad while the bus is stopped, Good again after.
	shim.setRun(false)
	eventually(t, "bus stop not reflected", func() bool { return d.Quality()["DI_00"] == nio.Bad && !d.Health().Running })
	shim.setRun(true)
	eventually(t, "bus restart not reflected", func() bool { return d.Quality()["DI_00"] == nio.Good })

	// A shim that stops cycling: Stale, with the reason; back to Good when
	// it cycles again — on the same mapping, no reconnect.
	reads := d.Health().Reads
	shim.stalled.Store(true)
	eventually(t, "a stalled shim never made the inputs Stale", func() bool { return d.Quality()["DI_00"] == nio.Stale })
	if why := d.Health().Degraded; !strings.Contains(why, "has not cycled") {
		t.Errorf("degraded %q", why)
	}
	errs := d.Health().Errors
	time.Sleep(50 * time.Millisecond) // ten more scans of the same stall
	if e := d.Health().Errors; e != errs {
		t.Errorf("one stall counted %d more errors: logged every scan, not once", e-errs)
	}
	shim.stalled.Store(false)
	eventually(t, "a shim that resumed never made the inputs Good", func() bool { return d.Quality()["DI_00"] == nio.Good })
	if h := d.Health(); h.Degraded != "" || h.Reads <= reads {
		t.Errorf("after resume: %+v", h)
	}

	// A shim descheduled inside its write leaves the sequence odd: the
	// driver keeps its last copy (still Good, no error) until that copy is
	// as old as a stall, then it is one.
	errs = d.Health().Errors
	shim.midWrite.Store(true)
	eventually(t, "a torn page was never held", func() bool { return d.Health().Shim.Held > 0 })
	if h := d.Health(); h.Degraded == "" && h.Errors != errs {
		t.Errorf("a held read counted %d errors", h.Errors-errs)
	}
	eventually(t, "a shim stuck mid-write never made the inputs Stale", func() bool { return d.Quality()["DI_00"] == nio.Stale })
	if why := d.Health().Degraded; !strings.Contains(why, "has not cycled") {
		t.Errorf("stuck mid-write: degraded %q", why)
	}
	shim.midWrite.Store(false)
	eventually(t, "a shim that finished its write never made the inputs Good", func() bool { return d.Quality()["DI_00"] == nio.Good })

	// A shim that restarted (the runtime did) ends the session, so the
	// BusConductor handshake runs again on a fresh connection.
	f.mu.Lock()
	f.writes = nil
	f.bools["configured"], f.bools[DefaultRunPort] = false, false
	f.bools["BcComponent1/BusConductor.CONFIG_REQ"], f.bools["BcComponent1/BusConductor.START_IO_REQ"] = false, false
	f.mu.Unlock()
	shim.restart()
	eventuallyWithin(t, 10*time.Second, "a shim restart never re-ran the handshake", func() bool {
		return strings.Contains(f.wrote(), "START_IO_REQ=true") && d.Quality()["DI_00"] == nio.Good
	})

	// The driver stopping is the shim's watchdog: its outputs stop being
	// refreshed and the frame goes to zero.
	d.Stop()
	eventually(t, "the frame was never dropped after the driver stopped", func() bool { return shim.out(0) == 0 && shim.out(2) == 0 })
}

// With no BusConductor to nudge, transport shm needs no gRPC at all.
func TestShmWithoutBusConductorNeverDials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page")
	shim := startFakeShim(t, path)
	shim.setInput(1, 0x01)
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\nbus-conductor: \"\"\ntags:\n  - {name: DI_00, dir: in, offset: 0, format: bit:0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond), WithDialer(func(context.Context) (dataAccess, error) {
		t.Error("dialed gRPC with no BusConductor")
		return nil, errors.New("no")
	}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	eventually(t, "input never arrived", func() bool { in, _ := d.ReadInputs(); return in["DI_00"] == true })
}

// No page, no shim: the session fails with a message that says so, and
// the inputs stay NotConnected.
func TestShmMissingPage(t *testing.T) {
	m, err := ParseManifest([]byte("transport: shm\nshm: /nonexistent/page\nbus-conductor: \"\"\ntags:\n  - {name: DI_00, dir: in, offset: 0, format: bit:0}\n"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	defer d.Stop()
	eventually(t, "missing page never reported", func() bool { return d.Health().Errors > 0 })
	if le := d.Health().LastError; !strings.Contains(le, "nautilus_io") {
		t.Errorf("last error %q does not point at the shim", le)
	}
	if q := d.Quality()["DI_00"]; q != nio.NotConnected {
		t.Errorf("quality %v", q)
	}
}

// statusOf is the driver's status bindings as a program reads them.
func statusOf(d *Driver) (online bool, outages, trips int64) {
	v, _ := d.ReadInputs()
	online, _ = v["AXC__Online"].(bool)
	outages, _ = v["AXC__Outages"].(int64)
	trips, _ = v["AXC__WatchdogTrips"].(int64)
	return online, outages, trips
}

const statusManifest = `bus-conductor: ""
output-watchdog: 150ms
tags:
  - {name: DI_00, dir: in, offset: 0, format: "bit:0"}
  - {name: DO_00, dir: out, offset: 0, format: "bit:0"}
  - {name: AXC__Online, status: online}
  - {name: AXC__Outages, status: outages}
  - {name: AXC__WatchdogTrips, status: watchdog-trips}
`

func startShmDriver(t *testing.T, path string) *Driver {
	t.Helper()
	m, err := ParseManifest([]byte("transport: shm\nshm: " + path + "\n" + statusManifest))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(m, WithScanRate(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	d.Start(context.Background())
	t.Cleanup(d.Stop)
	return d
}

// What a program sees of the shim: online while the image is fresh, an
// outage for a stall, a watchdog trip when this driver's outputs went
// stale in the page (its own stall), and one more for a shim restart.
func TestShmStatusForTheProgram(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page")
	shim := startFakeShim(t, path)
	d := startShmDriver(t, path)
	eventually(t, "never online", func() bool { on, _, _ := statusOf(d); return on })
	if err := d.WriteOutputs(nio.Values{"DO_00": true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "DO_00 never reached the frame", func() bool { return shim.out(1) == 0x01 })
	if q, said := d.Quality()["AXC__Online"]; said {
		t.Errorf("a status binding has quality %s; it is the driver's own and always Good", q)
	}

	// The shim stalls: an outage, no trip (it is not applying anything).
	shim.stalled.Store(true)
	eventually(t, "a stall never took the driver offline", func() bool { on, _, _ := statusOf(d); return !on })
	shim.stalled.Store(false)
	eventually(t, "never online after the stall", func() bool { on, _, _ := statusOf(d); return on })
	if _, outages, trips := statusOf(d); outages != 1 || trips != 0 {
		t.Errorf("after a stall: outages %d trips %d; want 1 0", outages, trips)
	}

	// This driver stalls (its lock held, so no scan refreshes the page)
	// past the watchdog: the shim zeroes the frame, and on resume the
	// driver counts the trip once and puts DO_00 back.
	d.mu.Lock()
	eventually(t, "the shim's watchdog never fired", func() bool { return shim.out(1) == 0 })
	d.mu.Unlock()
	eventually(t, "DO_00 never came back", func() bool { return shim.out(1) == 0x01 })
	time.Sleep(20 * time.Millisecond)
	if _, outages, trips := statusOf(d); outages != 1 || trips != 1 {
		t.Errorf("after the driver's own stall: outages %d trips %d; want 1 1", outages, trips)
	}

	// The shim restarts (the runtime under it did): an outage and a trip.
	shim.restart()
	eventuallyWithin(t, 10*time.Second, "never online after the shim restarted", func() bool {
		on, outages, _ := statusOf(d)
		return on && outages == 2
	})
	if _, _, trips := statusOf(d); trips != 2 {
		t.Errorf("after a shim restart: trips %d, want 2", trips)
	}
	if h := d.Health(); !h.Online || h.Outages != 2 || h.WatchdogTrips != 2 {
		t.Errorf("health: online %v outages %d trips %d", h.Online, h.Outages, h.WatchdogTrips)
	}
}

// A naut that was killed and restarted finds the shim's watchdog bit set
// for the outputs its predecessor left: a program that holds on the count
// sees "restarted after a fault" from its first scans. A clean first start
// on a fresh page is no trip.
func TestShmRestartAfterAFaultIsATrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page")
	shim := startFakeShim(t, path)
	first := startShmDriver(t, path)
	if on, outages, trips := statusOf(first); on || outages != 0 || trips != 0 {
		t.Errorf("before the first read: online %v outages %d trips %d", on, outages, trips)
	}
	eventually(t, "first driver never online", func() bool { on, _, _ := statusOf(first); return on })
	if _, _, trips := statusOf(first); trips != 0 {
		t.Fatalf("a clean start on a fresh page counted %d trips", trips)
	}
	first.WriteOutputs(nio.Values{"DO_00": true})
	eventually(t, "DO_00 never reached the frame", func() bool { return shim.out(1) == 0x01 })
	first.Stop()
	eventually(t, "the frame was never dropped after the driver stopped", func() bool { return shim.out(1) == 0 })

	second := startShmDriver(t, path)
	eventually(t, "the restart's trip never showed", func() bool { _, _, trips := statusOf(second); return trips == 1 })
	second.WriteOutputs(nio.Values{"DO_00": true})
	eventually(t, "DO_00 never came back", func() bool { return shim.out(1) == 0x01 })
	time.Sleep(20 * time.Millisecond)
	if on, outages, trips := statusOf(second); !on || outages != 0 || trips != 1 {
		t.Errorf("after the restart: online %v outages %d trips %d; want true 0 1", on, outages, trips)
	}
}
