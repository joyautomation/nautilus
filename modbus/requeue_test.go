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
