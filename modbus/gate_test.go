package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// A command queued while the source is down goes out on the reconnect only
// if the write gate is still open then (issue #595). A replica that lost
// leadership during the outage, or whose logic faulted, drops what it
// queued, a keep-alive re-assert included, and forgets it, so the same
// value is a command again once the gate reopens. Dropping is not a
// failure: no outage, and __Online comes up as for any reconnect.
func TestQueuedWriteRechecksTheGate(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(0, 21)
	rec := &recorder{}

	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "KA", Source: "DEV", Table: TableHolding, Address: 400, Format: "uint16", Writable: true, Rewrite: 30 * time.Millisecond},
			{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
		},
	}

	var up atomic.Bool // the slave is unreachable until up
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("test: source down")
		}
		var nd net.Dialer
		nc, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return &recordingConn{Conn: nc, rec: rec}, nil
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	startDriver(t, d)

	// Queued while the source is down and the gate open: the keep-alive's
	// baseline (a Rewrite binding asserts it) and a changed setpoint.
	if err := d.WriteOutputs(nio.Values{"KA": float64(7), "SP": float64(0)}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteOutputs(nio.Values{"SP": float64(77)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both queued", func() bool { return d.Health().Sources[0].QueuedWrites == 2 })

	open.Store(false) // leadership lost while the source is down
	up.Store(true)

	// __Online goes true only once a read is answered, and the first read
	// follows the on-connect flush: by then the queue has been dealt with.
	waitFor(t, "reconnect", func() bool {
		v, _ := d.ReadInputs()
		return v["DEV__Online"] == true
	})
	time.Sleep(100 * time.Millisecond) // a few keep-alive periods
	if got := rec.writes(FCWriteSingleRegister, 500); len(got) != 0 || u.Holding(500) != 0 {
		t.Errorf("a closed gate let a queued command through: %d frames, holding 500 = %d", len(got), u.Holding(500))
	}
	if got := rec.writes(FCWriteSingleRegister, 400); len(got) != 0 || u.Holding(400) != 0 {
		t.Errorf("a closed gate let a queued re-assert through: %d frames, holding 400 = %d", len(got), u.Holding(400))
	}
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Errorf("queued writes after the drop = %d", q)
	}
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(0) {
		t.Errorf("a dropped write is not an outage: DEV__Outages = %v", v["DEV__Outages"])
	}

	// The gate reopens: the same values are commands again, and the
	// keep-alive resumes its re-asserts.
	open.Store(true)
	if err := d.WriteOutputs(nio.Values{"KA": float64(7), "SP": float64(77)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the command once the gate reopens", func() bool { return u.Holding(500) == 77 })
	waitFor(t, "the keep-alive once the gate reopens", func() bool {
		return u.Holding(400) == 7 && len(rec.writes(FCWriteSingleRegister, 400)) >= 2
	})
	if got := rec.writes(FCWriteSingleRegister, 500); len(got) != 1 {
		t.Errorf("SP must be written once, got %d frames", len(got))
	}
}

// Writes a broken flush puts back are held to the gate too (issue #595):
// when the gate closes before the reconnect, the write that broke and the
// ones after it are dropped, not sent by a replica that is no longer
// leader. The break is an outage as before; the drop adds none.
func TestRequeuedWriteRechecksTheGate(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}

	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"},
			{Name: "A1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
			{Name: "B1", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
			{Name: "C1", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Writable: true},
		},
	}

	var open atomic.Bool
	open.Store(true)
	var dials atomic.Int32
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
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
			// The run at 100 is confirmed by then; 200 breaks, 300 is
			// unattempted: both are put back for the reconnect.
			breakOn: func(fc byte, addr uint16) bool { return fc == FCWriteSingleRegister && addr == 200 },
			onBreak: func() { open.Store(false) }, // leadership lost mid-flush
		}, nil
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(open.Load)
	startDriver(t, d)

	waitFor(t, "online", func() bool {
		v, _ := d.ReadInputs()
		return v["DEV__Online"] == true
	})
	if err := d.WriteOutputs(nio.Values{"A1": float64(0), "B1": float64(0), "C1": float64(0)}); err != nil {
		t.Fatal(err)
	}
	// One snapshot, one flush: 100, then 200 (breaks), then 300.
	if err := d.WriteOutputs(nio.Values{"A1": float64(11), "B1": float64(22), "C1": float64(33)}); err != nil {
		t.Fatal(err)
	}

	// The break is the source's one outage; online again means a read was
	// answered, and the reconnect's flush comes before its first read.
	waitFor(t, "reconnect after the break", func() bool {
		v, _ := d.ReadInputs()
		return dials.Load() >= 2 && v["DEV__Outages"] == int64(1) && v["DEV__Online"] == true
	})
	time.Sleep(60 * time.Millisecond)
	if u.Holding(100) != 11 {
		t.Errorf("holding 100 = %d: the write confirmed before the break must stand", u.Holding(100))
	}
	if got := rec.writes(FCWriteSingleRegister, 200); len(got) != 1 || u.Holding(200) != 0 {
		t.Errorf("the write that broke went out again under a closed gate: %d frames, holding 200 = %d", len(got), u.Holding(200))
	}
	if got := rec.writes(FCWriteSingleRegister, 300); len(got) != 0 || u.Holding(300) != 0 {
		t.Errorf("an unattempted write went out under a closed gate: %d frames, holding 300 = %d", len(got), u.Holding(300))
	}
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Errorf("queued writes after the drop = %d", q)
	}
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(1) {
		t.Errorf("a dropped write is not an outage: DEV__Outages = %v", v["DEV__Outages"])
	}

	// Once the gate reopens every output goes out again (#600): the
	// dropped ones, and the one confirmed before the close too, since
	// another leader may have written it in between.
	open.Store(true)
	if err := d.WriteOutputs(nio.Values{"A1": float64(11), "B1": float64(22), "C1": float64(33)}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the dropped commands once the gate reopens", func() bool {
		return u.Holding(200) == 22 && u.Holding(300) == 33
	})
	waitFor(t, "the confirmed write sent once more", func() bool {
		return len(rec.writes(FCWriteSingleRegister, 100)) == 2
	})
}

// gatingConn records like recordingConn and runs onFrame as a matching
// request goes on the wire, before the device answers it: a gate closed
// there is closed before the driver can look at it again.
type gatingConn struct {
	recordingConn
	match   func(fc byte, addr uint16) bool
	onFrame func()
}

func (c *gatingConn) Write(b []byte) (int, error) {
	if len(b) >= 12 && c.match(b[7], binary.BigEndian.Uint16(b[8:10])) {
		c.onFrame()
	}
	return c.recordingConn.Write(b)
}

// A flush the gate closes partway through stops there (issue #595): the
// write in flight completes, nothing after it goes out, and what was
// dropped is forgotten, so pushing the same values once the gate reopens
// sends them. A long flush through a slow gateway would otherwise keep
// commanding for seconds after this replica stopped being leader.
func TestGateClosingMidFlushStopsTheFlush(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}

	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "PV", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"},
			// Send order in one flush: the bit RMW, then register runs by
			// address (100, 200, 300), then the coil.
			{Name: "BIT", Source: "DEV", Table: TableHolding, Address: 50, Format: "bit:0", Writable: true},
			{Name: "A1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
			{Name: "B1", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true},
			{Name: "C1", Source: "DEV", Table: TableHolding, Address: 300, Format: "uint16", Writable: true},
			{Name: "K1", Source: "DEV", Table: TableCoil, Address: 10, Writable: true},
		},
	}

	var open, armed atomic.Bool
	open.Store(true)
	dialer := func(ctx context.Context, addr string) (net.Conn, error) {
		var nd net.Dialer
		nc, err := nd.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return &gatingConn{
			recordingConn: recordingConn{Conn: nc, rec: rec},
			// The first register run: the bit RMW is confirmed by then.
			match: func(fc byte, addr uint16) bool { return fc == FCWriteSingleRegister && addr == 100 },
			onFrame: func() {
				if armed.CompareAndSwap(true, false) {
					open.Store(false) // leadership lost mid-flush
				}
			},
		}, nil
	}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(dialer))
	if err != nil {
		t.Fatal(err)
	}
	d.SetWriteGate(open.Load)
	startDriver(t, d)
	waitFor(t, "online", func() bool {
		v, _ := d.ReadInputs()
		return v["DEV__Online"] == true
	})

	base := nio.Values{"BIT": false, "A1": float64(0), "B1": float64(0), "C1": float64(0), "K1": false}
	if err := d.WriteOutputs(base); err != nil {
		t.Fatal(err)
	}
	cmd := nio.Values{"BIT": true, "A1": float64(11), "B1": float64(22), "C1": float64(33), "K1": true}
	armed.Store(true)
	if err := d.WriteOutputs(cmd); err != nil { // one snapshot, one flush
		t.Fatal(err)
	}
	waitFor(t, "the run in flight lands", func() bool { return u.Holding(100) == 11 })
	waitFor(t, "the queue drains", func() bool { return d.Health().Sources[0].QueuedWrites == 0 })
	time.Sleep(60 * time.Millisecond) // several poll cycles: a flush that carried on is done

	if u.Holding(50) != 1 {
		t.Errorf("holding 50 = %#x: the bit RMW before the close must land", u.Holding(50))
	}
	if got := rec.writes(FCWriteSingleRegister, 200); len(got) != 0 || u.Holding(200) != 0 {
		t.Errorf("run at 200 went out after the gate closed: %d frames, holding 200 = %d", len(got), u.Holding(200))
	}
	if got := rec.writes(FCWriteSingleRegister, 300); len(got) != 0 || u.Holding(300) != 0 {
		t.Errorf("run at 300 went out after the gate closed: %d frames, holding 300 = %d", len(got), u.Holding(300))
	}
	if got := rec.writes(FCWriteSingleCoil, 10); len(got) != 0 || u.Coil(10) {
		t.Errorf("coil 10 went out after the gate closed: %d frames, coil = %v", len(got), u.Coil(10))
	}
	if v, _ := d.ReadInputs(); v["DEV__Outages"] != int64(0) || v["DEV__Online"] != true {
		t.Errorf("a dropped write is not a failure: DEV__Online = %v, DEV__Outages = %v", v["DEV__Online"], v["DEV__Outages"])
	}

	// Forgotten: the same snapshot pushed after the gate reopens sends the
	// dropped writes, and the ones that landed before the close once more
	// (#600: another leader may have written them since).
	open.Store(true)
	if err := d.WriteOutputs(cmd); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the dropped writes once the gate reopens", func() bool {
		return u.Holding(200) == 22 && u.Holding(300) == 33 && u.Coil(10)
	})
	waitFor(t, "the writes that landed before the close sent once more", func() bool {
		return len(rec.writes(FCWriteSingleRegister, 100)) == 2 && len(rec.writes(FCWriteSingleRegister, 50)) == 2
	})
	time.Sleep(60 * time.Millisecond) // several poll cycles: nothing more goes out
	if got := rec.writes(FCWriteSingleRegister, 100); len(got) != 2 {
		t.Errorf("the run at 100 must go out once after the reopen, got %d frames in all", len(got))
	}
	if got := rec.writes(FCWriteSingleRegister, 50); len(got) != 2 {
		t.Errorf("the bit RMW must go out once after the reopen, got %d frames in all", len(got))
	}
}
