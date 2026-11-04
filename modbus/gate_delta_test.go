package modbus

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// closableDialer dials for real, records frames, and keeps the latest
// connection so a test can cut it; up false makes dials fail.
type closableDialer struct {
	rec   *recorder
	up    atomic.Bool
	dials atomic.Int32
	mu    sync.Mutex
	cur   net.Conn
}

func (cd *closableDialer) dial(ctx context.Context, addr string) (net.Conn, error) {
	if !cd.up.Load() {
		return nil, errors.New("test: source down")
	}
	var nd net.Dialer
	nc, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	cd.mu.Lock()
	cd.cur = nc
	cd.mu.Unlock()
	cd.dials.Add(1)
	return &recordingConn{Conn: nc, rec: cd.rec}, nil
}

func (cd *closableDialer) cut() {
	cd.mu.Lock()
	_ = cd.cur.Close()
	cd.mu.Unlock()
}

// The reopen is handled atomically (#600): while one looker forgets the
// previous term's written, another that finds the gate open waits for it.
// Here the push from the test is the looker that handles the reopen, held
// inside it (reopenHook) for several keep-alive periods; source B's
// re-assert fires meanwhile and must not send the previous term's 7.
func TestReopenForgetsBeforeAnotherLookerReasserts(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("A", srv.Addr()), fastSource("B", srv.Addr())},
		Tags: []TagBinding{
			{Name: "SP", Source: "A", Table: TableHolding, Address: 500, Format: "uint16", Writable: true, WriteOnly: true},
			{Name: "KB", Source: "B", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, WriteOnly: true, Rewrite: 30 * time.Millisecond},
		},
	}
	var holding atomic.Bool
	reopenHook = func() {
		if holding.CompareAndSwap(true, false) {
			time.Sleep(150 * time.Millisecond) // five keep-alive periods
		}
	}
	t.Cleanup(func() { reopenHook = nil }) // runs after the driver stops
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	g := &gateWatch{}
	g.open.Store(true)
	d.SetWriteGate(g.gate)
	startDriver(t, d)
	waitFor(t, "connect", func() bool {
		h := d.Health()
		return h.Sources[0].State == "connected" && h.Sources[1].State == "connected"
	})
	if err := d.WriteOutputs(nio.Values{"SP": float64(0), "KB": float64(7)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the leader's keep-alive", func() bool { return u.Holding(400) == 7 })

	g.open.Store(false)
	n := g.looks.Load()
	waitFor(t, "looks at the closed gate", func() bool { return g.looks.Load() >= n+4 })
	time.Sleep(40 * time.Millisecond) // a keep-alive period: B has seen it closed
	u.SetHolding(400, 3)              // another master

	before := len(rec.writes(FCWriteSingleRegister, 400))
	holding.Store(true)
	g.open.Store(true)
	if err := d.WriteOutputs(nio.Values{"SP": float64(0), "KB": float64(8)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the program's keep-alive", func() bool { return u.Holding(400) == 8 })
	for _, f := range rec.writes(FCWriteSingleRegister, 400)[before:] {
		if f.count == 7 {
			t.Fatalf("source B re-asserted the previous term's keep-alive (7) while the reopen was being handled")
		}
	}
}

// A standby's reconnect re-asserts nothing (prime looks first): with the
// gate closed no keep-alive is queued, so nothing is dropped, no drop
// warning is logged, and the driver does not ask for the outputs.
func TestStandbyReconnectQueuesNothing(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	cd := &closableDialer{rec: &recorder{}}
	cd.up.Store(true)
	logs := &logRec{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, Rewrite: 30 * time.Millisecond},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(cd.dial), WithLogger(slog.New(logs)))
	if err != nil {
		t.Fatal(err)
	}
	g := &gateWatch{}
	g.open.Store(true)
	d.SetWriteGate(g.gate)
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	if err := d.WriteOutputs(nio.Values{"KA": float64(7)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the keep-alive", func() bool { return u.Holding(400) == 7 })

	g.open.Store(false)
	g.seen()
	_ = d.OutputsWanted() // nothing asked so far counts
	dials := cd.dials.Load()
	cd.cut()
	waitFor(t, "the reconnect", func() bool {
		return cd.dials.Load() > dials && d.Health().Sources[0].State == "connected"
	})
	time.Sleep(60 * time.Millisecond)
	if n, _ := logs.count("modbus: write gate closed; queued writes dropped", ""); n != 0 {
		t.Errorf("a standby's reconnect queued a re-assert and dropped it (%d drop warnings)", n)
	}
	if d.OutputsWanted() {
		t.Error("a standby's reconnect asked for the outputs")
	}
}

// A driver restarted after it has seen the gate closed is still a standby:
// Start does not re-arm the baseline, so its first push once the gate
// opens commands (#606).
func TestRestartAfterStandbyKeepsNoBaseline(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	g := &gateWatch{}
	d.SetWriteGate(g.gate) // closed: a standby
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.Start(ctx)
	g.seen()
	d.Stop()

	d.Start(ctx) // restarted, gate still closed
	t.Cleanup(d.Stop)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	g.open.Store(true)
	g.seen()
	if err := d.WriteOutputs(nio.Values{"SP": float64(77)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first push commands", func() bool { return u.Holding(500) == 77 })
}

// A write-only source has no polls to wake its loop: the stuck command's
// deadline wakes it, so it is retried every RetryMin, not every idle check
// (a second).
func TestNotRunWriteIsRetriedAtRetryMinOnAWriteOnlySource(t *testing.T) {
	const retryMin = 100 * time.Millisecond
	r := newBackoffRigWith(t, retryMin, true)
	r.inject(0x06)
	start := time.Now()
	r.rt.Tags().SetReal("SP", 21)
	r.scanFor(700 * time.Millisecond)
	elapsed := time.Since(start)
	time.Sleep(20 * time.Millisecond)
	n := len(r.frames())
	if limit := int(elapsed/retryMin) + 2; n > limit {
		t.Errorf("%d writes in %v: retried faster than RetryMin (limit %d)", n, elapsed, limit)
	}
	if n < 4 {
		t.Errorf("%d writes in %v with RetryMin %v: the deadline must wake a write-only source", n, elapsed, retryMin)
	}
}

// A refusal ends a stuck write: the program's next value, landing first
// time, is not reported as delivered after attempts that did not run.
func TestRefusalEndsTheStuckWrite(t *testing.T) {
	r := newBackoffRig(t, 3*time.Second)
	r.inject(0x06)
	r.rt.Tags().SetReal("SP", 21)
	r.rt.Scan()
	waitFor(t, "the 0x06", func() bool { return r.excs() == 1 })
	r.inject(0x02) // the changed value is refused
	r.rt.Tags().SetReal("SP", 22)
	r.rt.Scan()
	waitFor(t, "the 0x02", func() bool { return r.excs() == 2 })
	r.inject(0)
	r.rt.Tags().SetReal("SP", 23)
	r.rt.Scan()
	waitFor(t, "the next value lands", func() bool { return r.u.Holding(500) == 23 })
	time.Sleep(20 * time.Millisecond)
	if n, _ := r.logs.count(msgDelivered, ""); n != 0 {
		t.Errorf("%q logged after a refusal ended the stuck write", msgDelivered)
	}
}
