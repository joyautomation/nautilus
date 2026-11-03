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
	waitFor(t, "connect", func() bool { return read()["DEV__Online"] == true })

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
