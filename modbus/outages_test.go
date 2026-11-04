package modbus

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// link is a dialer the test can cut and restore: a cut closes every open
// connection and refuses new dials until restore. It stands in for a
// pulled cable without stopping the slave, so no port is ever reused.
type link struct {
	mu    sync.Mutex
	up    bool
	conns []net.Conn
}

func (l *link) dial(ctx context.Context, addr string) (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.up {
		return nil, errors.New("link down")
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	l.conns = append(l.conns, nc)
	return nc, nil
}

func (l *link) cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.up = false
	for _, c := range l.conns {
		_ = c.Close()
	}
	l.conns = nil
}

func (l *link) restore() {
	l.mu.Lock()
	l.up = true
	l.mu.Unlock()
}

// <source>__Outages counts the times __Online has gone false: 0 after the
// first connect, then one more for each fall, as a DINT (int64). Both
// ReadInputs and ReadInputsInto deliver it, and InputNames, InputType and
// Connections declare it.
func TestOutagesCountsEachFallNotTheFirstConnect(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 7)
	ln := &link{up: true}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.InputType("DEV__Outages"); got != "DINT" {
		t.Errorf("InputType(DEV__Outages) = %q, want DINT", got)
	}
	if !contains(d.InputNames(), "DEV__Outages") {
		t.Errorf("InputNames = %v, want DEV__Outages in it", d.InputNames())
	}
	if c := d.Connections(); len(c) != 1 || !contains(c[0].Inputs, "DEV__Outages") {
		t.Errorf("Connections = %+v, want DEV__Outages on source DEV", c)
	}
	startDriver(t, d)

	// outages reads the counter through both delivery paths and checks they
	// agree.
	outages := func() any {
		v, err := d.ReadInputs()
		if err != nil {
			t.Fatal(err)
		}
		dst := nio.Values{}
		if err := d.ReadInputsInto(dst); err != nil {
			t.Fatal(err)
		}
		if v["DEV__Outages"] != dst["DEV__Outages"] {
			t.Fatalf("ReadInputs says %v, ReadInputsInto says %v", v["DEV__Outages"], dst["DEV__Outages"])
		}
		return v["DEV__Outages"]
	}
	online := func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == true }

	waitFor(t, "first connect", func() bool { v, _ := d.ReadInputs(); return online() && v["PV"] == int64(7) })
	if got := outages(); got != int64(0) {
		t.Fatalf("after the first connect __Outages = %#v, want int64(0)", got)
	}

	for want := int64(1); want <= 2; want++ {
		ln.cut()
		waitFor(t, "source offline", func() bool { return !online() })
		if got := outages(); got != want {
			t.Fatalf("after fall %d __Outages = %#v, want int64(%d)", want, got, want)
		}
		ln.restore()
		waitFor(t, "source back online", online)
		if got := outages(); got != want {
			t.Fatalf("reconnecting changed __Outages to %#v, want int64(%d)", got, want)
		}
	}
	if q := d.Quality(); q["DEV__Outages"] != nio.Good {
		t.Errorf("__Outages quality = %v, want Good", q["DEV__Outages"])
	}
}

// A source that never connects has had no outage: __Outages is delivered
// as 0 from Start, and failed dials do not count.
func TestOutagesStartingOfflineIsNotAnOutage(t *testing.T) {
	ln := &link{} // down from the start
	m := Manifest{
		Sources: []Source{fastSource("DEV", "127.0.0.1:502")},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "several failed dials", func() bool { return d.Health().Sources[0].Retries >= 3 })
	v, err := d.ReadInputs()
	if err != nil {
		t.Fatal(err)
	}
	if v["DEV__Online"] != false || v["DEV__Outages"] != int64(0) {
		t.Fatalf("never connected: __Online = %#v, __Outages = %#v, want false and int64(0)", v["DEV__Online"], v["DEV__Outages"])
	}
}

// Parking a source with its enable: tag takes __Online false, so it counts
// as an outage; waking it does not.
func TestOutagesParkingCounts(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 3)
	src := fastSource("DEV", srv.Addr())
	src.Enable = "DEV_En"
	m := Manifest{
		Sources: []Source{src},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "first read", func() bool { return read()["PV"] == int64(3) })

	if err := d.WriteOutputs(nio.Values{"DEV_En": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "parked", func() bool { return d.Health().Sources[0].State == "parked" })
	if v := read(); v["DEV__Online"] != false || v["DEV__Outages"] != int64(1) {
		t.Fatalf("parked: __Online = %#v, __Outages = %#v, want false and int64(1)", v["DEV__Online"], v["DEV__Outages"])
	}
	if err := d.WriteOutputs(nio.Values{"DEV_En": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unparked", func() bool { return read()["DEV__Online"] == true })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("waking the source changed __Outages to %#v, want int64(1)", got)
	}
}

// A gateway that accepts TCP and never answers is not a device that went
// away: the driver reconnects to it over and over, and none of those
// connections counts as an outage.
func TestOutagesSilentGatewayDoesNotCount(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	accepted := 0
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			t.Cleanup(func() { _ = c.Close() }) // read nothing, answer nothing
		}
	}()

	src := fastSource("DEV", ln.Addr().String())
	src.Timeout = 30 * time.Millisecond
	m := Manifest{
		Sources: []Source{src},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "several connections to the silent gateway", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return accepted >= 4
	})
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(0) {
		t.Fatalf("a gateway that never answered: __Outages = %#v, want int64(0)", v["DEV__Outages"])
	}
}

// Stopping the driver takes __Online false, but a stop is not an outage.
func TestOutagesStopIsNotAnOutage(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 3)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "first read", func() bool { v, _ := d.ReadInputs(); return v["PV"] != nil })
	d.Stop()
	v, _ := d.ReadInputs()
	if v["DEV__Online"] != false || v["DEV__Outages"] != int64(0) {
		t.Fatalf("after Stop: __Online = %#v, __Outages = %#v, want false and int64(0)", v["DEV__Online"], v["DEV__Outages"])
	}
}

// A write-only source (a keep-alive word re-asserted every rewrite:,
// nothing polled) answers with write confirmations, not reads. A confirmed
// write takes the source online, so losing it is an outage.
func TestOutagesWriteOnlySourceCounts(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(8, 0)
	ln := &link{up: true}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true, Rewrite: 20 * time.Millisecond}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	// Nothing polled: __Online waits for the first confirmed write (#539).
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	if err := d.WriteOutputs(nio.Values{"CMD": int64(5)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "write confirmed", func() bool { return srv.Unit(1).Holding(8) == 5 && read()["DEV__Online"] == true })

	ln.cut()
	waitFor(t, "source offline", func() bool { return read()["DEV__Online"] == false })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("write-only source lost after a confirmed write: __Outages = %#v, want int64(1)", got)
	}
}

// A write-only source with no rewrite: has nothing to ask the device, so
// its loop is idle between commands. A link cut while it is idle is still
// noticed, within idleCheckEvery, with no command pending (#552).
func TestOutagesIdleWriteOnlySourceNoticesCut(t *testing.T) {
	srv := newSlave(t)
	ln := &link{up: true}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "online on connect", func() bool { return read()["DEV__Online"] == true })

	ln.cut()
	cut := time.Now()
	waitFor(t, "source offline", func() bool { return read()["DEV__Online"] == false })
	if el := time.Since(cut); el > idleCheckEvery+time.Second {
		t.Errorf("noticed the cut after %v, want within %v", el, idleCheckEvery)
	}
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("idle write-only source lost its link: __Outages = %#v, want int64(1)", got)
	}
	ln.restore()
	waitFor(t, "back online", func() bool { return read()["DEV__Online"] == true })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("reconnecting changed __Outages to %#v, want int64(1)", got)
	}
}

// The same, when the device end hangs up (a reboot, a gateway dropping
// the socket): the peer's FIN is read while idle, not at the next command.
func TestOutagesIdleWriteOnlySourceNoticesPeerClose(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	t.Cleanup(func() {
		for {
			select {
			case c := <-accepted:
				_ = c.Close()
			default:
				return
			}
		}
	})
	m := Manifest{
		Sources: []Source{fastSource("DEV", l.Addr().String())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true}},
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "online on connect", func() bool { return read()["DEV__Online"] == true })

	var first net.Conn
	select {
	case first = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted")
	}
	_ = first.Close()
	waitFor(t, "an outage counted", func() bool { return read()["DEV__Outages"] == int64(1) })
}

// Watching an idle link puts nothing on the wire: no Modbus request the
// manifest did not ask for.
func TestIdleWriteOnlySourceSendsNothing(t *testing.T) {
	srv := newSlave(t)
	rec := &recorder{}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true}},
	}
	d, err := New(m, WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "connected", func() bool { return d.Health().Sources[0].State == "connected" })
	time.Sleep(idleCheckEvery + idleCheckEvery/2) // at least one idle check
	rec.mu.Lock()
	n := len(rec.frames)
	rec.mu.Unlock()
	if n != 0 {
		t.Fatalf("idle write-only source sent %d frames, want 0", n)
	}
	if h := d.Health().Sources[0]; h.State != "connected" {
		t.Fatalf("idle check dropped a healthy link: state %q, error %q", h.State, h.LastError)
	}
}

// Whether the device answered is per connection: once it has answered on
// one connection and that one falls (one outage), later connections it
// never answers on do not count.
func TestOutagesAnsweredResetsPerConnection(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 7)
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = silent.Close() })
	var mu sync.Mutex
	accepted := 0
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted++
			mu.Unlock()
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	// The first dial reaches the slave; every later one the silent listener.
	var first net.Conn
	dials := 0
	dial := func(ctx context.Context, _ string) (net.Conn, error) {
		mu.Lock()
		dials++
		n := dials
		mu.Unlock()
		addr := silent.Addr().String()
		if n == 1 {
			addr = srv.Addr()
		}
		var dl net.Dialer
		c, err := dl.DialContext(ctx, "tcp", addr)
		if n == 1 && err == nil {
			mu.Lock()
			first = c
			mu.Unlock()
		}
		return c, err
	}
	src := fastSource("DEV", srv.Addr())
	src.Timeout = 30 * time.Millisecond
	m := Manifest{
		Sources: []Source{src},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "first read", func() bool { v, _ := d.ReadInputs(); return v["PV"] == int64(7) })
	mu.Lock()
	_ = first.Close()
	mu.Unlock()
	waitFor(t, "several silent connections", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return accepted >= 4
	})
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(1) {
		t.Fatalf("one answered connection lost, then silent ones: __Outages = %#v, want int64(1)", v["DEV__Outages"])
	}
}

// A binding may not take the companion's name: the driver owns it.
func TestOutagesCompanionCollidesWithABinding(t *testing.T) {
	m := Manifest{
		Sources: []Source{fastSource("DEV", "127.0.0.1:502")},
		Tags:    []TagBinding{{Name: "DEV__Outages", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	_, err := New(m)
	if err == nil || !strings.Contains(err.Error(), `"DEV__Outages" collides`) {
		t.Fatalf("New = %v, want a collision on DEV__Outages", err)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
