package modbus

import (
	"context"
	"errors"
	"io"
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

// idleCloser stands in for a device with a server-side idle timeout: a
// listener that closes each connection once the client has sent nothing
// for idle. With a target it forwards to that slave, so polls and writes
// still get answers. refuse closes the listener: later dials are refused.
type idleCloser struct {
	l      net.Listener
	target string
	idle   time.Duration

	mu      sync.Mutex
	accepts int
	frames  int // client writes forwarded (one per request)
	conns   []net.Conn
}

func newIdleCloser(t *testing.T, target string, idle time.Duration) *idleCloser {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ic := &idleCloser{l: l, target: target, idle: idle}
	t.Cleanup(func() {
		ic.refuse()
		ic.mu.Lock()
		defer ic.mu.Unlock()
		for _, c := range ic.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			ic.mu.Lock()
			ic.accepts++
			ic.conns = append(ic.conns, c)
			ic.mu.Unlock()
			go ic.handle(c)
		}
	}()
	return ic
}

func (ic *idleCloser) handle(c net.Conn) {
	defer c.Close()
	var up net.Conn
	if ic.target != "" {
		var err error
		if up, err = net.Dial("tcp", ic.target); err != nil {
			return
		}
		defer up.Close()
		ic.mu.Lock()
		ic.conns = append(ic.conns, up)
		ic.mu.Unlock()
		go func() { _, _ = io.Copy(c, up) }()
	}
	buf := make([]byte, 512)
	for {
		_ = c.SetReadDeadline(time.Now().Add(ic.idle))
		n, err := c.Read(buf)
		if err != nil {
			return // idle too long (or the client went): hang up
		}
		ic.mu.Lock()
		ic.frames++
		ic.mu.Unlock()
		if up != nil {
			if _, err := up.Write(buf[:n]); err != nil {
				return
			}
		}
	}
}

func (ic *idleCloser) addr() string { return ic.l.Addr().String() }
func (ic *idleCloser) refuse()      { _ = ic.l.Close() }
func (ic *idleCloser) counts() (accepts, frames int) {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.accepts, ic.frames
}

// watchOnline samples __Online and __Outages for d, every few ms, for dur
// and fails on any sample that is not online with no outage.
func watchOnline(t *testing.T, d *Driver, dur time.Duration) {
	t.Helper()
	for end := time.Now().Add(dur); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		v, _ := d.ReadInputs()
		if v["DEV__Online"] != true || v["DEV__Outages"] != int64(0) {
			t.Fatalf("__Online = %#v, __Outages = %#v, want true and int64(0) throughout",
				v["DEV__Online"], v["DEV__Outages"])
		}
	}
}

// A device that closes connections it has been idle on (a server-side idle
// timeout) is not having outages. The idle check reads the close, the
// driver reconnects at once with no backoff, and the source stays online
// with nothing counted, however often it happens. Commands still go out.
func TestOnlineIdleTimeoutIsNotAnOutage(t *testing.T) {
	srv := newSlave(t)
	ic := newIdleCloser(t, srv.Addr(), 200*time.Millisecond)
	src := fastSource("DEV", ic.addr())
	src.RetryMin, src.RetryMax = 3*time.Second, 3*time.Second // a backoff wait would show
	m := Manifest{
		Sources: []Source{src},
		Tags: []TagBinding{{Name: "CMD", Source: "DEV", Table: TableHolding, Address: 8,
			Format: "uint16", Writable: true, WriteOnly: true}},
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "online on connect", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == true })
	// The first snapshot after Start is the baseline, not a command.
	if err := d.WriteOutputs(nio.Values{"CMD": int64(1)}); err != nil {
		t.Fatal(err)
	}

	watchOnline(t, d, 3*idleCheckEvery+idleCheckEvery/2)
	if accepts, _ := ic.counts(); accepts < 3 {
		t.Fatalf("%d connections, want the device's idle closes followed by reconnects (3 or more)", accepts)
	}
	if h := d.Health().Sources[0]; h.State != "connected" || h.Retries != 0 {
		t.Fatalf("idle closes counted as failures: state %q, retries %d, error %q", h.State, h.Retries, h.LastError)
	}
	// Send a command on a connection the device has closed but the next
	// idle check has not looked at yet: 400 ms after a reconnect (closed at
	// 200 ms, next check near 1 s). It goes out on a fresh connection.
	before, _ := ic.counts()
	waitFor(t, "a reconnect", func() bool { a, _ := ic.counts(); return a > before })
	time.Sleep(400 * time.Millisecond)
	if err := d.WriteOutputs(nio.Values{"CMD": int64(5)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "command delivered", func() bool { return srv.Unit(1).Holding(8) == 5 })
	if v, _ := d.ReadInputs(); v["DEV__Online"] != true || v["DEV__Outages"] != int64(0) {
		t.Fatalf("after a command on a link the device had closed: __Online = %#v, __Outages = %#v, want true and int64(0)",
			v["DEV__Online"], v["DEV__Outages"])
	}
}

// The same for a polled source whose device closes between slow polls: no
// outage, and the reconnect keeps the poll schedule rather than re-reading
// everything, so the device is not read more often than configured.
func TestOnlinePolledIdleTimeoutIsNotAnOutage(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(0, 7)
	ic := newIdleCloser(t, srv.Addr(), 200*time.Millisecond)
	m := Manifest{
		Sources: []Source{fastSource("DEV", ic.addr())},
		Tags:    []TagBinding{{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}},
	}
	const scan = 2500 * time.Millisecond
	d, err := New(m, WithScanRate(scan))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "first read", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == true && v["PV"] == int64(7) })

	watchOnline(t, d, 4*time.Second) // the prime, then one poll at 2.5 s
	accepts, frames := ic.counts()
	if accepts < 3 {
		t.Fatalf("%d connections, want the device's idle closes followed by reconnects (3 or more)", accepts)
	}
	if frames > 2 {
		t.Fatalf("%d reads in 4 s at a %v scan, want 2: a reconnect after an idle close must not re-prime", frames, scan)
	}
}

// A device that hangs up while idle and then refuses the reconnect (a
// reboot, a gateway going down) is an outage: exactly one, noticed while
// idle rather than at the next command.
func TestOutagesIdleCloseThenRefusedCountsOnce(t *testing.T) {
	ic := newIdleCloser(t, "", time.Hour)
	m := Manifest{
		Sources: []Source{fastSource("DEV", ic.addr())},
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
	waitFor(t, "connection accepted", func() bool { a, _ := ic.counts(); return a == 1 })

	ic.refuse()
	ic.mu.Lock()
	for _, c := range ic.conns {
		_ = c.Close()
	}
	ic.mu.Unlock()
	waitFor(t, "source offline", func() bool { return read()["DEV__Online"] == false })
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("after the close and a refused reconnect __Outages = %#v, want int64(1)", got)
	}
	time.Sleep(300 * time.Millisecond) // more refused dials
	if got := read()["DEV__Outages"]; got != int64(1) {
		t.Fatalf("refused dials changed __Outages to %#v, want int64(1)", got)
	}
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
