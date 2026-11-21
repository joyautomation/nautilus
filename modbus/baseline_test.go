package modbus

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/modbus/slave"
)

// The start-up baseline decides each output against what the DEVICE holds,
// at the first live read of its coil/register (#1123, as opcua's #1121).

func baselineDriver(t *testing.T, tags []TagBinding, opts ...Option) (*Driver, *recorder, *slave.Unit) {
	t.Helper()
	srv := newSlave(t)
	rec := &recorder{}
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: tags}
	opts = append([]Option{WithScanRate(20 * time.Millisecond), WithDialer(recordingDialer(rec))}, opts...)
	d, err := New(m, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return d, rec, srv.Unit(1)
}

func allWrites(rec *recorder) int {
	n := 0
	for _, fc := range []byte{FCWriteSingleRegister, FCWriteMultipleRegisters, FCWriteSingleCoil, FCWriteMultipleCoils} {
		n += len(rec.writes(fc, -1))
	}
	return n
}

// The issue: the controller's first value (true) differs from the coil the
// device holds (false); it was recorded as sent and never written.
func TestFirstOutputDifferingFromTheDeviceIsWritten(t *testing.T) {
	d, _, u := baselineDriver(t, []TagBinding{
		{Name: "Run", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
	})
	startDriver(t, d)
	waitFor(t, "first read", func() bool { v, _ := d.ReadInputs(); _, ok := v["Run"]; return ok })
	_ = d.WriteOutputs(nio.Values{"Run": true, "SP": float64(7)}) // the first push since start
	waitFor(t, "the first values to land", func() bool { return u.Coil(10) && u.Holding(200) == 7 })
}

func firstRead(t *testing.T, d *Driver, tag string) {
	t.Helper()
	waitFor(t, "first read of "+tag, func() bool { v, _ := d.ReadInputs(); _, ok := v[tag]; return ok })
}

func settle() { time.Sleep(200 * time.Millisecond) } // several scans

// Equal to what the device holds: recorded, nothing sent. A scaled register
// compares as the words it would be written as, so 1.5 against raw 15 at
// scale 0.1 is equal. A later change is a command, once.
func TestFirstOutputEqualToTheDeviceWritesNothing(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetCoil(10, true)
	u.SetHolding(200, 7)
	u.SetHolding(300, 15)
	rec := &recorder{}
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "Run", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
		{Name: "Scaled", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Scale: 0.1, Writable: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	firstRead(t, d, "Scaled")
	_ = d.WriteOutputs(nio.Values{"Run": true, "SP": float64(7), "Scaled": 1.5})
	settle()
	if n := allWrites(rec); n != 0 {
		t.Fatalf("writes = %d, want 0", n)
	}
	_ = d.WriteOutputs(nio.Values{"Run": true, "SP": float64(8), "Scaled": 1.5})
	waitFor(t, "the change", func() bool { return u.Holding(200) == 8 })
	if n := allWrites(rec); n != 1 {
		t.Fatalf("writes = %d, want 1 (the change only)", n)
	}
}

// The other direction: the device holds another value and the program
// starts at its own. The controller owns its outputs: its value is written.
func TestFirstOutputOverwritesWhatTheDeviceHolds(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(200, 5)
	rec := &recorder{}
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	firstRead(t, d, "SP")
	_ = d.WriteOutputs(nio.Values{"SP": float64(0)})
	waitFor(t, "the program's value", func() bool { return u.Holding(200) == 0 })
	settle()
	if n := allWrites(rec); n != 1 {
		t.Fatalf("writes = %d, want 1", n)
	}
}

// An output whose register has not been read yet waits for its first read:
// it is never taken as sent. The latest value decides, once, with one write.
func TestUnreadOutputWaitsForItsFirstRead(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(200, 5)
	u.SetCoil(10, true)
	rec := &recorder{}
	var up atomic.Bool
	dial := recordingDialer(rec)
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
		{Name: "Run", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("test: source down")
		}
		return dial(ctx, addr)
	}))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	_ = d.WriteOutputs(nio.Values{"SP": float64(1), "Run": true}) // the first push, source down
	_ = d.WriteOutputs(nio.Values{"SP": float64(2), "Run": true}) // the program moved on
	settle()
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Fatalf("queued = %d before any read, want 0: an unread output waits", q)
	}
	up.Store(true)
	waitFor(t, "the latest value decided at the first read", func() bool { return u.Holding(200) == 2 })
	settle()
	if n := allWrites(rec); n != 1 {
		t.Fatalf("writes = %d, want 1: SP once; Run equals the coil", n)
	}
}

// A register the device refuses to read, and one never polled, cannot be
// compared with: each is commanded once, and not again while unchanged.
func TestUnreadableOutputIsCommandedOnce(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.InjectException(FCReadHoldingRegisters, 0x02) // reads refused; writes are not
	rec := &recorder{}
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "Refused", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
		{Name: "Never", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Writable: true, ScanClass: NoPoll},
		{Name: "Blind", Source: "DEV", Table: TableHolding, Address: 310, Format: "uint16", Writable: true, WriteOnly: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "the refused read", func() bool { return d.Health().Sources[0].Exceptions > 0 })
	vals := nio.Values{"Refused": float64(0), "Never": float64(0), "Blind": float64(0)}
	_ = d.WriteOutputs(vals) // zero equals the device, but none of it can be read
	waitFor(t, "each commanded once", func() bool { return len(rec.writes(FCWriteSingleRegister, -1)) >= 3 })
	_ = d.WriteOutputs(vals)
	settle()
	if n := len(rec.writes(FCWriteSingleRegister, -1)); n != 3 {
		t.Fatalf("single-register writes = %d, want 3 (one per output)", n)
	}
}
