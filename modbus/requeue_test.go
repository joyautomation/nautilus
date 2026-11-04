package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// breakingConn records like recordingConn, and the first frame for which
// breakOn says true is not sent: the link is cut instead and onBreak runs,
// while the driver is still inside that flush.
type breakingConn struct {
	recordingConn
	breakOn func(fc byte, addr uint16) bool
	onBreak func()
	once    sync.Once
}

var errCut = errors.New("test: link cut mid-flush")

func (c *breakingConn) Write(b []byte) (int, error) {
	if len(b) >= 12 && c.breakOn(b[7], binary.BigEndian.Uint16(b[8:10])) {
		cut := false
		c.once.Do(func() { cut = true })
		if cut {
			c.rec.add(frameRec{fc: b[7], addr: binary.BigEndian.Uint16(b[8:10]), count: binary.BigEndian.Uint16(b[10:12])})
			_ = c.Conn.Close()
			c.onBreak()
			return 0, errCut
		}
	}
	return c.recordingConn.Write(b)
}

// A flush the link breaks partway through re-sends only the write that
// broke and the ones after it (issue #593). Writes the device already
// confirmed in that flush are not sent again after the reconnect, so a
// value another master wrote to those registers in between stands; a
// value queued while the flush was in flight beats the one it replaced.
func TestBrokenFlushResendsOnlyUnconfirmedRuns(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}

	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			// Send order in one flush: the bit RMW, then register runs by
			// address (100, 200, 300), then the coil run.
			{Name: "BIT", Source: "DEV", Table: TableHolding, Address: 50, Format: "bit:0", Writable: true},
			{Name: "A1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
			{Name: "A2", Source: "DEV", Table: TableHolding, Address: 101, Format: "uint16", Writable: true},
			{Name: "B1", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
			{Name: "B2", Source: "DEV", Table: TableHolding, Address: 201, Format: "uint16", Writable: true},
			{Name: "C1", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Writable: true},
			{Name: "C2", Source: "DEV", Table: TableHolding, Address: 301, Format: "uint16", Writable: true},
			{Name: "K1", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
			{Name: "K2", Source: "DEV", Table: TableCoil, Address: 11, Writable: true},
		},
	}

	var d *Driver
	var allow atomic.Bool // refuse dials until every command is queued
	var dials atomic.Int32
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		if !allow.Load() {
			return nil, errors.New("test: not yet")
		}
		var nd net.Dialer
		nc, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		if dials.Add(1) > 1 {
			return &recordingConn{Conn: nc, rec: rec}, nil
		}
		return &breakingConn{
			recordingConn: recordingConn{Conn: nc, rec: rec},
			// The second register run: the bit RMW and the run at 100 are
			// confirmed by then, the runs at 300 and the coils unattempted.
			breakOn: func(fc byte, addr uint16) bool { return fc == FCWriteMultipleRegisters && addr == 200 },
			onBreak: func() {
				// Another master changes what this flush already wrote.
				u.SetHolding(50, 0x8000)
				u.SetHolding(100, 4242)
				// The program moves on while the flush is in flight.
				if err := d.WriteOutputs(nio.Values{"B1": float64(2222), "C1": float64(3333)}); err != nil {
					t.Error(err)
				}
			},
		}, nil
	}

	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)

	// Baseline, then one snapshot of commands, all queued before the
	// first connection so they go out in one flush.
	base := nio.Values{"BIT": false, "A1": float64(0), "A2": float64(0), "B1": float64(0),
		"B2": float64(0), "C1": float64(0), "C2": float64(0), "K1": false, "K2": false}
	if err := d.WriteOutputs(base); err != nil {
		t.Fatal(err)
	}
	cmd := nio.Values{"BIT": true, "A1": float64(11), "A2": float64(12), "B1": float64(21),
		"B2": float64(22), "C1": float64(31), "C2": float64(32), "K1": true, "K2": true}
	if err := d.WriteOutputs(cmd); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all nine queued", func() bool {
		h := d.Health()
		return len(h.Sources) == 1 && h.Sources[0].QueuedWrites == 9
	})
	allow.Store(true)

	// The broken run and everything after it land after the reconnect,
	// with the values queued mid-flight.
	waitFor(t, "unconfirmed runs re-sent", func() bool {
		return dials.Load() >= 2 &&
			u.Holding(200) == 2222 && u.Holding(201) == 22 &&
			u.Holding(300) == 3333 && u.Holding(301) == 32 &&
			u.Coil(10) && u.Coil(11)
	})

	// Confirmed before the break: sent once, never again, so the other
	// master's values stand.
	if got := rec.writes(FCWriteSingleRegister, 50); len(got) != 1 {
		t.Errorf("confirmed bit RMW at 50 must be written once, got %d frames", len(got))
	}
	if got := rec.writes(FCWriteMultipleRegisters, 100); len(got) != 1 {
		t.Errorf("confirmed FC16 at 100 must be sent once, got %d frames", len(got))
	}
	if v := u.Holding(50); v != 0x8000 {
		t.Errorf("holding 50 = %#x: the other master's 0x8000 was overwritten by a re-sent RMW", v)
	}
	if v := u.Holding(100); v != 4242 {
		t.Errorf("holding 100 = %d: the other master's 4242 was overwritten by a re-sent run", v)
	}

	// The run that broke goes again (the device may not have applied it);
	// the unattempted ones go once.
	if got := rec.writes(FCWriteMultipleRegisters, 200); len(got) != 2 {
		t.Errorf("broken FC16 at 200 must be sent again after the reconnect, got %d frames", len(got))
	}
	if got := rec.writes(FCWriteMultipleRegisters, 300); len(got) != 1 {
		t.Errorf("unattempted FC16 at 300 must be sent once, got %d frames", len(got))
	}
	if got := rec.writes(FCWriteMultipleCoils, 10); len(got) != 1 {
		t.Errorf("unattempted FC15 at 10 must be sent once, got %d frames", len(got))
	}
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Errorf("queue must drain, %d left", q)
	}
}

// flushStep is one device write in a flush's send order: its write frame,
// another master's value for it, and how to read back what landed.
type flushStep struct {
	name     string
	fc       byte
	addr     uint16
	interfer func(u *slave.Unit)      // another master writes over it
	foreign  func(u *slave.Unit) bool // the other master's value is there
	landed   func(u *slave.Unit) bool // this flush's command is there
}

// The link cut at every point of one flush (issue #593): every write
// before the cut goes out once and another master's value on it stands,
// the write that broke goes out twice (a cut on an RMW read: once, its
// write never left), and every write after it goes out once.
func TestBrokenFlushAtEveryPoint(t *testing.T) {
	tags := []TagBinding{
		{Name: "BA", Source: "DEV", Table: TableHolding, Address: 50, Format: "bit:0", Writable: true},
		{Name: "BB", Source: "DEV", Table: TableHolding, Address: 60, Format: "bit:0", Writable: true},
		{Name: "A1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
		{Name: "A2", Source: "DEV", Table: TableHolding, Address: 101, Format: "uint16", Writable: true},
		{Name: "B1", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
		{Name: "B2", Source: "DEV", Table: TableHolding, Address: 201, Format: "uint16", Writable: true},
		{Name: "C1", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Writable: true},
		{Name: "C2", Source: "DEV", Table: TableHolding, Address: 301, Format: "uint16", Writable: true},
		{Name: "K1", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
		{Name: "K2", Source: "DEV", Table: TableCoil, Address: 11, Writable: true},
		{Name: "K3", Source: "DEV", Table: TableCoil, Address: 20, Writable: true},
	}
	base := nio.Values{"BA": false, "BB": false, "A1": float64(0), "A2": float64(0),
		"B1": float64(0), "B2": float64(0), "C1": float64(0), "C2": float64(0),
		"K1": false, "K2": false, "K3": false}
	cmd := nio.Values{"BA": true, "BB": true, "A1": float64(11), "A2": float64(12),
		"B1": float64(21), "B2": float64(22), "C1": float64(31), "C2": float64(32),
		"K1": true, "K2": true, "K3": true}

	bitStep := func(name string, addr uint16) flushStep {
		return flushStep{name: name, fc: FCWriteSingleRegister, addr: addr,
			interfer: func(u *slave.Unit) { u.SetHolding(addr, 0x8000) },
			foreign:  func(u *slave.Unit) bool { return u.Holding(addr) == 0x8000 },
			landed:   func(u *slave.Unit) bool { return u.Holding(addr)&1 == 1 },
		}
	}
	runStep := func(name string, addr, v0, v1 uint16) flushStep {
		return flushStep{name: name, fc: FCWriteMultipleRegisters, addr: addr,
			interfer: func(u *slave.Unit) { u.SetHolding(addr, 4242) },
			foreign:  func(u *slave.Unit) bool { return u.Holding(addr) == 4242 },
			landed:   func(u *slave.Unit) bool { return u.Holding(addr) == v0 && u.Holding(addr+1) == v1 },
		}
	}
	coilStep := func(name string, fc byte, addr uint16) flushStep {
		return flushStep{name: name, fc: fc, addr: addr,
			interfer: func(u *slave.Unit) { u.SetCoil(addr, false) },
			foreign:  func(u *slave.Unit) bool { return !u.Coil(addr) },
			landed:   func(u *slave.Unit) bool { return u.Coil(addr) },
		}
	}
	// Send order: bit RMWs, register runs by address, coil runs by address.
	steps := []flushStep{
		bitStep("RMW 50", 50),
		bitStep("RMW 60", 60),
		runStep("FC16 100", 100, 11, 12),
		runStep("FC16 200", 200, 21, 22),
		runStep("FC16 300", 300, 31, 32),
		coilStep("FC15 10", FCWriteMultipleCoils, 10),
		coilStep("FC5 20", FCWriteSingleCoil, 20),
	}

	cuts := []struct {
		name string
		fc   byte
		addr uint16
		step int  // the step the cut frame belongs to
		sent bool // the cut frame is that step's write, not its RMW read
	}{
		{"RMW read at 50", FCReadHoldingRegisters, 50, 0, false},
		{"RMW write at 50", FCWriteSingleRegister, 50, 0, true},
		{"RMW write at 60", FCWriteSingleRegister, 60, 1, true},
		{"FC16 at 100", FCWriteMultipleRegisters, 100, 2, true},
		{"FC16 at 200", FCWriteMultipleRegisters, 200, 3, true},
		{"FC16 at 300", FCWriteMultipleRegisters, 300, 4, true},
		{"first coil run FC15 at 10", FCWriteMultipleCoils, 10, 5, true},
		{"last coil FC5 at 20 with nothing after", FCWriteSingleCoil, 20, 6, true},
	}

	for _, cut := range cuts {
		t.Run(cut.name, func(t *testing.T) {
			srv := newSlave(t)
			u := srv.Unit(1)
			rec := &recorder{}

			var allow atomic.Bool // refuse dials until every command is queued
			var dials atomic.Int32
			dialer := func(ctx context.Context, addr string) (net.Conn, error) {
				if !allow.Load() {
					return nil, errors.New("test: not yet")
				}
				var nd net.Dialer
				nc, err := nd.DialContext(ctx, "tcp", addr)
				if err != nil {
					return nil, err
				}
				if dials.Add(1) > 1 {
					return &recordingConn{Conn: nc, rec: rec}, nil
				}
				return &breakingConn{
					recordingConn: recordingConn{Conn: nc, rec: rec},
					// prime flushes before it polls, so on the first
					// connection the first matching frame is the flush's
					// own, an RMW read included.
					breakOn: func(fc byte, addr uint16) bool { return fc == cut.fc && addr == cut.addr },
					onBreak: func() {
						for _, st := range steps[:cut.step] {
							st.interfer(u)
						}
					},
				}, nil
			}

			m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: tags}
			d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
			if err != nil {
				t.Fatal(err)
			}
			startDriver(t, d)
			if err := d.WriteOutputs(base); err != nil {
				t.Fatal(err)
			}
			if err := d.WriteOutputs(cmd); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "all commands queued", func() bool {
				h := d.Health()
				return len(h.Sources) == 1 && h.Sources[0].QueuedWrites == len(cmd)
			})
			allow.Store(true)

			// The flush after the reconnect sends in the same order, so
			// once every re-sent step has landed and the queue is empty, a
			// re-send of an earlier step would have been recorded already.
			waitFor(t, "the re-sent steps land", func() bool {
				if dials.Load() < 2 || d.Health().Sources[0].QueuedWrites != 0 {
					return false
				}
				for _, st := range steps[cut.step:] {
					if !st.landed(u) {
						return false
					}
				}
				return true
			})

			for i, st := range steps {
				want := 1
				if i == cut.step && cut.sent {
					want = 2
				}
				if got := len(rec.writes(st.fc, int(st.addr))); got != want {
					t.Errorf("%s: %d write frames, want %d", st.name, got, want)
				}
				if i < cut.step && !st.foreign(u) {
					t.Errorf("%s: confirmed before the cut, yet the other master's value was overwritten", st.name)
				}
			}
		})
	}
}
