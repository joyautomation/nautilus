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
	if err := d.ReplaceSource(b2, newBTags()); err != nil {
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
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), newBTags()); err != nil {
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
			if err := d.ReplaceSource(c.src, c.tags); err == nil || !strings.Contains(err.Error(), c.want) {
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
	if err := d.ReplaceSource(fastSource("B", b2.Addr()), newBTags()); err != nil {
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
		if err := d.ReplaceSource(fastSource("B", addrs[i%2]), tags); err != nil {
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
	if err := d.ReplaceSource(fastSource("B", ss.b2.Addr()), tags); err != nil {
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
	if err := d.ReplaceSource(nb, newBTags()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if st := d.Health().Sources[1].State; st != "parked" {
		t.Errorf("the replaced device is %s, want still parked", st)
	}
}
