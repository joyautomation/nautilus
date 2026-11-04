package modbus

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// gateWatch is a test write gate that counts the driver's looks at it.
type gateWatch struct {
	open  atomic.Bool
	looks atomic.Int64
}

func (g *gateWatch) gate() bool { g.looks.Add(1); return g.open.Load() }

// seen waits until the driver has looked at the gate twice since the test
// moved it: with one source its loop is the only caller, so the second look
// started after the first finished, whatever that one found. A driver that
// never looks is given two seconds and fails on what it sends, not here.
func (g *gateWatch) seen() {
	n := g.looks.Load()
	for deadline := time.Now().Add(2 * time.Second); g.looks.Load() < n+2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
}

// A replica that starts as a standby has no start-up baseline (#606): its
// driver saw the write gate closed, so the takeover push commands every
// output with the program's value, the plain setpoint too. A plain start,
// leader from the first scan and never gated, keeps the baseline: the
// setpoint is recorded, not sent; only the keep-alive goes out.
func TestStandbyTakeoverCommandsEveryOutput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		standby bool
	}{{"starts as standby", true}, {"plain start", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newSlave(t)
			u := srv.Unit(1)
			rec := &recorder{}
			m := Manifest{
				Sources: []Source{fastSource("DEV", srv.Addr())},
				Tags: []TagBinding{
					{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
					{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, Rewrite: 30 * time.Millisecond},
				},
			}
			d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
			if err != nil {
				t.Fatal(err)
			}
			g := &gateWatch{}
			g.open.Store(!tc.standby)
			d.SetWriteGate(g.gate)
			startDriver(t, d)
			waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
			rt := newOutputsRuntime(t, d, leaderFunc(g.open.Load))
			rt.Tags().SetReal("SP", 77) // what the program holds when it leads

			if tc.standby {
				rt.Scan() // standing by: no scan
				g.seen()
				g.open.Store(true)
				g.seen()
			}
			rt.Scan() // the first scan as leader: a takeover
			waitFor(t, "the keep-alive", func() bool { return u.Holding(400) == 7 })
			if tc.standby {
				waitFor(t, "the setpoint commanded on takeover", func() bool { return u.Holding(500) == 77 })
				return
			}
			time.Sleep(60 * time.Millisecond)
			if n := len(rec.writes(FCWriteSingleRegister, 500)); n != 0 || u.Holding(500) != 0 {
				t.Errorf("a plain start sent its baseline setpoint: %d frames, holding 500 = %d", n, u.Holding(500))
			}
		})
	}
}

// A gate that reopens without a takeover, on a driver with no rewrite:
// binding and nothing queued, is seen by the source loop's own look at
// each wake: nothing else asks the gate then. The driver asks for the
// outputs, and the unchanged setpoint another master overwrote meanwhile
// goes back to the program's value on the next scan, once.
func TestGateReopenIsSeenByTheSourceLoop(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
		},
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	g := &gateWatch{}
	g.open.Store(true)
	d.SetWriteGate(g.gate)
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	rt := newOutputsRuntime(t, d, nil)
	rt.Scan() // baseline
	rt.Tags().SetReal("SP", 77)
	rt.Scan()
	waitFor(t, "the leader's write", func() bool { return u.Holding(500) == 77 })

	g.open.Store(false)
	g.seen()
	u.SetHolding(500, 5) // another master
	g.open.Store(true)
	g.seen()
	rt.Scan()
	rt.Scan()
	waitFor(t, "the unchanged setpoint back", func() bool { return u.Holding(500) == 77 })
	time.Sleep(60 * time.Millisecond)
	if n := len(rec.writes(FCWriteSingleRegister, 500)); n != 2 {
		t.Errorf("SP frames = %d, want 2: the leader's and one after the reopen", n)
	}
}

// A gate that reopens while the source is reconnecting, after the connect
// attempt's look and before the on-connect re-assert, is seen by the look
// at the top of prime: the keep-alive does not re-assert the previous
// term's value on the new connection. It waits for the push.
func TestGateReopenDuringTheDialIsSeenBeforeTheReassert(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, Rewrite: 30 * time.Millisecond},
		},
	}
	g := &gateWatch{}
	g.open.Store(true)
	var up, reopenOnDial atomic.Bool
	up.Store(true)
	var connMu sync.Mutex
	var cur net.Conn
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("test: source down")
		}
		var nd net.Dialer
		nc, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		if reopenOnDial.Load() {
			g.open.Store(true) // the gate reopens while the connect is in flight
		}
		connMu.Lock()
		cur = nc
		connMu.Unlock()
		return &recordingConn{Conn: nc, rec: rec}, nil
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(g.gate)
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	if err := d.WriteOutputs(nio.Values{"KA": float64(7)}); err != nil { // a keep-alive baseline is sent
		t.Fatal(err)
	}
	waitFor(t, "the leader's keep-alive", func() bool { return u.Holding(400) == 7 })

	// The link drops and stays down; the gate closes meanwhile.
	up.Store(false)
	g.open.Store(false)
	connMu.Lock()
	_ = cur.Close()
	connMu.Unlock()
	waitFor(t, "the source down", func() bool { return d.Health().Sources[0].State != "connected" })
	g.seen()
	u.SetHolding(400, 3) // another master's keep-alive

	reopenOnDial.Store(true)
	before := len(rec.writes(FCWriteSingleRegister, 400))
	up.Store(true)
	waitFor(t, "the reconnect", func() bool { return d.Health().Sources[0].State == "connected" })
	time.Sleep(100 * time.Millisecond) // three keep-alive periods, no push yet
	if got := rec.writes(FCWriteSingleRegister, 400)[before:]; len(got) != 0 || u.Holding(400) != 3 {
		t.Errorf("the keep-alive re-asserted the previous term's value on reconnect: %d frames (first value %d), holding 400 = %d",
			len(got), firstValue(got), u.Holding(400))
	}
	if !d.OutputsWanted() {
		t.Error("the driver did not ask for the outputs once the gate reopened")
	}
	if err := d.WriteOutputs(nio.Values{"KA": float64(8)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the program's keep-alive", func() bool { return u.Holding(400) == 8 })
}
