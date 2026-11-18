package modbus

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// addrCounter dials for real and counts, per address, the requests sent.
type addrCounter struct {
	mu sync.Mutex
	n  map[string]*atomic.Int64
}

func (c *addrCounter) count(addr string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v := c.n[addr]; v != nil {
		return v.Load()
	}
	return 0
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (cc countingConn) Write(b []byte) (int, error) { cc.n.Add(1); return cc.Conn.Write(b) }

func (c *addrCounter) dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.n == nil {
		c.n = map[string]*atomic.Int64{}
	}
	if c.n[addr] == nil {
		c.n[addr] = &atomic.Int64{}
	}
	n := c.n[addr]
	c.mu.Unlock()
	return countingConn{Conn: nc, n: n}, nil
}

// twoDevices is a driver over device A and device B (a value and a
// writable command each), started, with both online.
func twoDevices(t *testing.T, c *addrCounter) (*Driver, *slaveSet) {
	t.Helper()
	ss := &slaveSet{a: newSlave(t), b: newSlave(t), b2: newSlave(t)}
	ss.a.Unit(1).SetHolding(0, 11)
	ss.b.Unit(1).SetHolding(0, 22)
	ss.b2.Unit(1).SetHolding(5, 33) // the same tag at another address on the new device
	m := Manifest{
		Sources: []Source{fastSource("A", ss.a.Addr()), fastSource("B", ss.b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}
	opts := []Option{WithScanRate(10 * time.Millisecond)}
	if c != nil {
		opts = append(opts, WithDialer(c.dial))
	}
	d, err := New(m, opts...)
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "both devices online with values", func() bool {
		v, _ := d.ReadInputs()
		return v["A__Online"] == true && v["B__Online"] == true && v["A_V"] != nil && v["B_V"] != nil
	})
	return d, ss
}

type slaveSet struct{ a, b, b2 *slave.Server }

// newBTags are B's bindings on the new device: B_V moved from 0 to 5.
func newBTags() []TagBinding {
	return []TagBinding{
		{Name: "B_V", Source: "B", Table: TableHolding, Address: 5, Format: "uint16"},
		{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
	}
}

// #706: one device moves to another address and register while the other
// keeps polling. A's tags stay Good through the swap; B's read the new
// device; the driver asks for the outputs and the current command reaches
// the new device.
func TestReplaceSourceMovesOneDevice(t *testing.T) {
	d, ss := twoDevices(t, nil)
	if err := d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}); err != nil { // the baseline snapshot
		t.Fatal(err)
	}
	d.OutputsWanted() // clear any request from start-up

	stop := make(chan struct{})
	var aBad atomic.Int64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if q, ok := d.Quality()["A_V"]; ok && q != nio.Good {
				aBad.Add(1)
			}
			if v, _ := d.ReadInputs(); v["A__Online"] != true {
				aBad.Add(1)
			}
		}
	}()
	b2 := fastSource("B", ss.b2.Addr())
	if err := d.ReplaceSource(b2, newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B_V from the new device's register 5", func() bool {
		v, _ := d.ReadInputs()
		return v["B_V"] == int64(33)
	})
	close(stop)
	if n := aBad.Load(); n != 0 {
		t.Errorf("device A was not Good and online %d times during B's swap", n)
	}
	if !d.OutputsWanted() {
		t.Fatal("after the swap the driver should ask for the outputs")
	}
	if err := d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}); err != nil { // the runtime's answer
		t.Fatal(err)
	}
	u := ss.b2.Unit(1)
	waitFor(t, "the current command on the new device", func() bool { return u.Holding(10) == 7 })
	if h := d.Health(); len(h.Sources) != 2 || h.Sources[1].Addr != b2.Addr() {
		t.Errorf("Health after the swap: %+v", h.Sources)
	}
}

// The old source's loop ends: after the swap no more requests reach the
// device it polled.
func TestReplaceSourceStopsTheOldLoop(t *testing.T) {
	c := &addrCounter{}
	d, ss := twoDevices(t, c)
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	before := c.count(ss.b.Addr())
	time.Sleep(150 * time.Millisecond)
	if after := c.count(ss.b.Addr()); after != before {
		t.Errorf("the old device still got %d requests after the swap", after-before)
	}
	if c.count(ss.a.Addr()) == 0 || c.count(ss.b2.Addr()) == 0 {
		t.Error("device A or the new device is not being polled")
	}
}

// Refused: an unknown source, other tags (more, fewer, writable or not),
// another enable tag, a binding for another source, an invalid binding, an
// undefined scan class. A refusal leaves the running source as it was.
func TestReplaceSourceRefuses(t *testing.T) {
	d, ss := twoDevices(t, nil)
	b2 := fastSource("B", ss.b2.Addr())
	extra := append(newBTags(), TagBinding{Name: "B_X", Source: "B", Table: TableHolding, Address: 20, Format: "uint16"})
	ro := newBTags()
	ro[1].Writable = false
	enabled := b2
	enabled.Enable = "B_Enable"
	for _, c := range []struct {
		name string
		src  Source
		tags []TagBinding
		want string
	}{
		{"unknown source", fastSource("C", ss.b2.Addr()), newBTags(), "no such source"},
		{"an extra tag", b2, extra, "cannot change live"},
		{"a tag fewer", b2, newBTags()[:1], "cannot change live"},
		{"no longer writable", b2, ro, "cannot change live"},
		{"another enable tag", enabled, newBTags(), "enable tag"},
		{"a binding for another source", b2, []TagBinding{{Name: "B_V", Source: "A", Table: TableHolding, Address: 5, Format: "uint16"}}, "names source"},
		{"an invalid format", b2, []TagBinding{{Name: "B_V", Source: "B", Table: TableHolding, Address: 5, Format: "nope"}, newBTags()[1]}, "nope"},
		{"an undefined scan class", b2, []TagBinding{{Name: "B_V", Source: "B", Table: TableHolding, Address: 5, Format: "uint16", ScanClass: "turbo"}, newBTags()[1]}, "turbo"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := d.ReplaceSource(c.src, c.tags, ReplaceOptions{}); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ReplaceSource: %v, want %q", err, c.want)
			}
			if h := d.Health(); h.Sources[1].Addr != ss.b.Addr() {
				t.Errorf("a refused replace moved B to %s", h.Sources[1].Addr)
			}
		})
	}
	v, _ := d.ReadInputs()
	if v["B_V"] != int64(22) {
		t.Errorf("after refusals B_V = %v, want the old device's 22", v["B_V"])
	}
}

// Before Start a replace just changes the configuration; Start polls the
// new device.
func TestReplaceSourceBeforeStart(t *testing.T) {
	a, b, b2 := newSlave(t), newSlave(t), newSlave(t)
	b2.Unit(1).SetHolding(5, 44)
	d, err := New(Manifest{
		Sources: []Source{fastSource("A", a.Addr()), fastSource("B", b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceSource(fastSource("B", b2.Addr()), newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "B_V from the new device", func() bool {
		v, _ := d.ReadInputs()
		return v["B_V"] == int64(44)
	})
}

// Reads, writes, quality and health go on while a source is replaced
// again and again (run with -race: the source lock is what makes it safe).
func TestReplaceSourceUnderConcurrentUse(t *testing.T) {
	d, ss := twoDevices(t, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = d.ReadInputs()
				_ = d.WriteOutputs(nio.Values{"B_Cmd": uint16(1)})
				_ = d.Quality()
				_ = d.Health()
				_ = d.Connections()
				_ = d.ConnectionUp("B")
			}
		}()
	}
	addrs := []string{ss.b2.Addr(), ss.b.Addr()}
	for i := 0; i < 20; i++ {
		tags := newBTags()
		if i%2 == 1 {
			tags[0].Address = 0
		}
		if err := d.ReplaceSource(fastSource("B", addrs[i%2]), tags, ReplaceOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

// A replaced source's loop, winding down, encodes writes with its own
// bindings, never the replacement's: B_Cmd moved from register 10 to 12,
// and the old source still writes 10 (to the old device) while the new one
// writes 12.
func TestReplaceSourceOldLoopKeepsItsBindings(t *testing.T) {
	d, ss := twoDevices(t, nil)
	d.srcMu.RLock()
	old := d.bySource["B"]
	d.srcMu.RUnlock()
	tags := newBTags()
	tags[1].Address = 12
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), tags, ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	d.srcMu.RLock()
	ns := d.bySource["B"]
	d.srcMu.RUnlock()
	regsOld, _, _ := d.stageWrites(old, map[string]any{"B_Cmd": uint16(7)})
	regsNew, _, _ := d.stageWrites(ns, map[string]any{"B_Cmd": uint16(7)})
	if len(regsOld) != 1 || regsOld[0].addr != 10 {
		t.Errorf("the old source encodes B_Cmd as %+v, want register 10 (its own binding)", regsOld)
	}
	if len(regsNew) != 1 || regsNew[0].addr != 12 {
		t.Errorf("the new source encodes B_Cmd as %+v, want register 12", regsNew)
	}
}

// A device parked by its Enable tag stays parked when it is replaced.
func TestReplaceSourceKeepsAParkedDeviceParked(t *testing.T) {
	a, b, b2 := newSlave(t), newSlave(t), newSlave(t)
	bs := fastSource("B", b.Addr())
	bs.Enable = "B_Enable"
	d, err := New(Manifest{
		Sources: []Source{fastSource("A", a.Addr()), bs},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	if err := d.WriteOutputs(nio.Values{"B_Enable": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B parked", func() bool { return d.Health().Sources[1].State == "parked" })
	nb := fastSource("B", b2.Addr())
	nb.Enable = "B_Enable"
	if err := d.ReplaceSource(nb, newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if st := d.Health().Sources[1].State; st != "parked" {
		t.Errorf("the replaced device is %s, want still parked", st)
	}
}

// #880 review: the write gate reopening (a takeover) while a replace waits
// for the source lock must not hang the scan's WriteOutputs. reopenHook
// widens the window: it starts the replace from inside the gate's look,
// where the old code held the source lock and re-entered it.
func TestReplaceSourceDuringATakeoverDoesNotHang(t *testing.T) {
	d, ss := twoDevices(t, nil)
	var open atomic.Bool
	d.SetWriteGate(open.Load)
	d.WriteOutputs(nio.Values{"B_Cmd": uint16(1)}) // a look that finds it closed
	open.Store(true)                               // the takeover
	var once sync.Once
	replaced := make(chan error, 1)
	reopenHook = func() {
		once.Do(func() {
			go func() { replaced <- d.ReplaceSource(fastSource("B", ss.b2.Addr()), newBTags(), ReplaceOptions{}) }()
			time.Sleep(50 * time.Millisecond) // the replace now waits for the lock, if anything holds it
		})
	}
	t.Cleanup(func() { reopenHook = nil })
	wrote := make(chan struct{})
	go func() { d.WriteOutputs(nio.Values{"B_Cmd": uint16(2)}); close(wrote) }()
	select {
	case <-wrote:
	case <-time.After(3 * time.Second):
		t.Fatal("WriteOutputs hung: the gate reopened while a replace waited for the source lock")
	}
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the replace hung")
	}
}

// timeline records, per address, when a connection was dialled, when it
// last wrote, and when it closed.
type timeline struct {
	mu                 sync.Mutex
	dialed, lastW, cls map[string]time.Time
	frames             map[string][]frameRec
}

type tlConn struct {
	net.Conn
	tl   *timeline
	addr string
}

func (c tlConn) Write(b []byte) (int, error) {
	c.tl.mu.Lock()
	c.tl.lastW[c.addr] = time.Now()
	if len(b) >= 12 {
		c.tl.frames[c.addr] = append(c.tl.frames[c.addr], frameRec{fc: b[7], addr: uint16(b[8])<<8 | uint16(b[9])})
	}
	c.tl.mu.Unlock()
	return c.Conn.Write(b)
}

func (c tlConn) Close() error {
	c.tl.mu.Lock()
	c.tl.cls[c.addr] = time.Now()
	c.tl.mu.Unlock()
	return c.Conn.Close()
}

func (tl *timeline) dial(ctx context.Context, addr string) (net.Conn, error) {
	var nd net.Dialer
	nc, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	tl.mu.Lock()
	if _, ok := tl.dialed[addr]; !ok {
		tl.dialed[addr] = time.Now()
	}
	tl.mu.Unlock()
	return tlConn{Conn: nc, tl: tl, addr: addr}, nil
}

func newTimeline() *timeline {
	return &timeline{dialed: map[string]time.Time{}, lastW: map[string]time.Time{}, cls: map[string]time.Time{}, frames: map[string][]frameRec{}}
}

// Break before make: with the old device slow (a request in flight at the
// replace), the new device is dialled only after the old connection has
// sent its last request and closed, and the old device never gets the new
// register address (B_Cmd moved from 10 to 12) from the winding-down loop.
func TestReplaceSourceBreaksBeforeMaking(t *testing.T) {
	tl := newTimeline()
	a, b, b2 := newSlave(t), newSlave(t), newSlave(t)
	for _, srv := range []*slave.Server{a, b, b2} {
		srv.Unit(1).SetHolding(0, 1) // unit 1 answers
	}
	b.SetLatency(150 * time.Millisecond)
	d, err := New(Manifest{
		Sources: []Source{fastSource("A", a.Addr()), fastSource("B", b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, WithScanRate(10*time.Millisecond), WithDialer(tl.dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "B online", func() bool { v, _ := d.ReadInputs(); return v["B__Online"] == true })
	d.WriteOutputs(nio.Values{"B_Cmd": uint16(3)}) // baseline
	d.WriteOutputs(nio.Values{"B_Cmd": uint16(4)}) // a command queued on the old device
	tags := newBTags()
	tags[1].Address = 12
	if err := d.ReplaceSource(fastSource("B", b2.Addr()), tags, ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new device dialled", func() bool { tl.mu.Lock(); defer tl.mu.Unlock(); _, ok := tl.dialed[b2.Addr()]; return ok })
	tl.mu.Lock()
	defer tl.mu.Unlock()
	newDial := tl.dialed[b2.Addr()]
	if c, ok := tl.cls[b.Addr()]; !ok || newDial.Before(c) {
		t.Errorf("the new device was dialled (%v) before the old connection closed (%v, closed: %v)", newDial, c, ok)
	}
	if w := tl.lastW[b.Addr()]; newDial.Before(w) {
		t.Errorf("the old device was still being written (%v) after the new one was dialled (%v)", w, newDial)
	}
	for _, f := range tl.frames[b.Addr()] {
		if f.addr == 12 {
			t.Errorf("the old device got a request at the new address 12: %+v", f)
		}
	}
}

// __Outages counts since the driver started: a replace is not an outage,
// and the count carries over.
func TestReplaceSourceCarriesOutages(t *testing.T) {
	a, b, b2 := newSlave(t), newSlave(t), newSlave(t)
	for _, srv := range []*slave.Server{a, b, b2} {
		srv.Unit(1).SetHolding(0, 1) // unit 1 answers
	}
	d, err := New(Manifest{
		Sources: []Source{fastSource("A", a.Addr()), fastSource("B", b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "B online", func() bool { v, _ := d.ReadInputs(); return v["B__Online"] == true })
	b.Stop()
	waitFor(t, "one outage of B", func() bool { v, _ := d.ReadInputs(); return v["B__Outages"] == int64(1) })
	if err := d.ReplaceSource(fastSource("B", b2.Addr()), newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B online on the new device", func() bool { v, _ := d.ReadInputs(); return v["B__Online"] == true })
	if v, _ := d.ReadInputs(); v["B__Outages"] != int64(1) {
		t.Errorf("B__Outages = %v after the replace, want the 1 it had", v["B__Outages"])
	}
}

// A tag's type (DINT, REAL, BOOL) cannot change live: the runtime fixed it.
func TestReplaceSourceRefusesARetype(t *testing.T) {
	d, ss := twoDevices(t, nil)
	b2 := fastSource("B", ss.b2.Addr())
	for _, c := range []struct {
		name string
		b    TagBinding
	}{
		{"DINT to REAL by format", TagBinding{Name: "B_V", Source: "B", Table: TableHolding, Address: 5, Format: "float32"}},
		{"DINT to REAL by scale", TagBinding{Name: "B_V", Source: "B", Table: TableHolding, Address: 5, Format: "uint16", Scale: 0.1}},
		{"DINT to BOOL", TagBinding{Name: "B_V", Source: "B", Table: TableCoil, Address: 5}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := d.ReplaceSource(b2, []TagBinding{c.b, newBTags()[1]}, ReplaceOptions{}); err == nil || !strings.Contains(err.Error(), "type cannot change") {
				t.Errorf("ReplaceSource: %v, want a refused retype", err)
			}
		})
	}
}

// The manifest a caller handed New is not written through by a replace,
// and after Stop a replace starts no loop.
func TestReplaceSourceLeavesTheCallersManifestAndRespectsStop(t *testing.T) {
	a, b, b2 := newSlave(t), newSlave(t), newSlave(t)
	m := Manifest{
		Sources: []Source{fastSource("A", a.Addr()), fastSource("B", b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}
	c := &addrCounter{}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(c.dial))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	d.Stop()
	if err := d.ReplaceSource(fastSource("B", b2.Addr()), newBTags(), ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	if m.Sources[1].Port != fastSource("B", b.Addr()).Port {
		t.Error("the replace wrote into the caller's manifest")
	}
	time.Sleep(100 * time.Millisecond)
	if n := c.count(b2.Addr()); n != 0 {
		t.Errorf("after Stop a replace started a loop: %d requests to the new device", n)
	}
}

// A driver stopped mid-apply may be replaced again (a rollback or a retry):
// the replacement recorded after Stop has no loop, so the second replace
// must neither cancel a nil func nor wait on a nil done (#880 review: it
// panicked). The next Start polls the last replacement.
func TestReplaceSourceTwiceAfterStop(t *testing.T) {
	a, b, b2, b3 := newSlave(t), newSlave(t), newSlave(t), newSlave(t)
	b3.Unit(1).SetHolding(5, 55)
	d, err := New(Manifest{
		Sources: []Source{fastSource("A", a.Addr()), fastSource("B", b.Addr())},
		Tags: []TagBinding{
			{Name: "A_V", Source: "A", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_V", Source: "B", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "B_Cmd", Source: "B", Table: TableHolding, Address: 10, Format: "uint16", Writable: true},
		},
	}, WithScanRate(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Start(ctx)
	d.Stop()
	replaced := make(chan error, 1)
	go func() {
		if err := d.ReplaceSource(fastSource("B", b2.Addr()), newBTags(), ReplaceOptions{}); err != nil {
			replaced <- err
			return
		}
		replaced <- d.ReplaceSource(fastSource("B", b3.Addr()), newBTags(), ReplaceOptions{})
	}()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second replace after Stop hung (waiting on a loop that never started)")
	}
	d.Start(ctx)
	t.Cleanup(d.Stop)
	waitFor(t, "B_V from the last replacement after a restart", func() bool {
		v, _ := d.ReadInputs()
		return v["B_V"] == int64(55)
	})
}

// ReplaceOptions.HoldOutputs (the runtime's HoldOutputsOnSwap): the
// driver still asks for the outputs, but the replaced source records that
// snapshot as its baseline and writes only once a command changes. The
// other source's outputs are not held. Without it (the default), the
// current command is sent: TestReplaceSourceMovesOneDevice.
func TestReplaceSourceHoldOutputs(t *testing.T) {
	d, ss := twoDevices(t, nil)
	if err := d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}); err != nil { // the baseline snapshot
		t.Fatal(err)
	}
	d.OutputsWanted()
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), newBTags(), ReplaceOptions{HoldOutputs: true}); err != nil {
		t.Fatal(err)
	}
	if !d.OutputsWanted() {
		t.Fatal("a held replace should still ask for the outputs (to record them)")
	}
	if err := d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}); err != nil {
		t.Fatal(err)
	}
	u := ss.b2.Unit(1)
	waitFor(t, "B_V from the new device", func() bool {
		v, _ := d.ReadInputs()
		return v["B_V"] == int64(33)
	})
	time.Sleep(100 * time.Millisecond) // several polls: a queued write would have gone by now
	if got := u.Holding(10); got != 0 {
		t.Fatalf("held: the new device got the unchanged command %d", got)
	}
	if err := d.WriteOutputs(nio.Values{"B_Cmd": uint16(8)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a changed command on the new device", func() bool { return u.Holding(10) == 8 })
}

// A held replace on a standby: the snapshots it sees while the gate is
// closed use up the hold, so the takeover's push is a command and reaches
// the new device (a takeover must never start from a swallowed push).
func TestReplaceSourceHeldOnAStandbyStillPushesAtTakeover(t *testing.T) {
	d, ss := twoDevices(t, nil)
	var open atomic.Bool
	d.SetWriteGate(open.Load)
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), newBTags(), ReplaceOptions{HoldOutputs: true}); err != nil {
		t.Fatal(err)
	}
	d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}) // standby: dropped
	open.Store(true)                               // the takeover
	d.WriteOutputs(nio.Values{"B_Cmd": uint16(7)}) // its push: the first snapshot with the gate open
	u := ss.b2.Unit(1)
	waitFor(t, "the current command on the new device after the takeover", func() bool { return u.Holding(10) == 7 })
}
