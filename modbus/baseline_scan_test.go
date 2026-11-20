package modbus

import (
	"context"
	"errors"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/runtime"
)

// The start-up baseline decides on a push computed from the device's live
// inputs, never on the first scan's push, which is computed from init values
// before anything was read (#1123 review).

func downUpDialer(rec *recorder, up *atomic.Bool) func(ctx context.Context, addr string) (net.Conn, error) {
	dial := recordingDialer(rec)
	return func(ctx context.Context, addr string) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("test: source down")
		}
		return dial(ctx, addr)
	}
}

const incProgram = "PROGRAM Main\nVAR_EXTERNAL In : DINT; Cmd : DINT; END_VAR\nCmd := In + 1;\nEND_PROGRAM"

// incRig: Cmd := In + 1, In read from the device at 0; Cmd at 10 (readable,
// NoPoll or WriteOnly by cmd).
func incRig(t *testing.T, cmd TagBinding, in uint16, held uint16, up *atomic.Bool, rec *recorder, prog ...string) (*Driver, *runtime.Runtime, func(n int)) {
	t.Helper()
	srv := newSlave(t)
	u := srv.Unit(1)
	u.SetHolding(0, in)
	u.SetHolding(10, held)
	cmd.Name, cmd.Source, cmd.Table, cmd.Address, cmd.Format, cmd.Writable = "Cmd", "DEV", TableHolding, 10, "uint16", true
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "In", Source: "DEV", Table: TableHolding, Address: 0, Format: "uint16"}, cmd}}
	program := incProgram
	if len(prog) > 0 {
		program = prog[0]
	}
	dial := recordingDialer(rec)
	if up != nil {
		dial = downUpDialer(rec, up)
	}
	d, err := New(m, WithScanRate(10*time.Millisecond), WithDialer(dial))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Options{
		Program: program, Driver: d,
		Tags: []runtime.TagDef{runtime.Input("In", runtime.Init(int64(0))), runtime.Output("Cmd", runtime.Init(int64(0))),
			runtime.Input("DEV__Online", runtime.Init(false)), runtime.Input("DEV__Outages", runtime.Init(int64(0)))},
	})
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	return d, rt, func(n int) {
		for i := 0; i < n; i++ {
			rt.Scan()
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func cmdWrites(rec *recorder) []uint16 {
	var out []uint16
	for _, f := range rec.writes(FCWriteSingleRegister, 10) {
		out = append(out, f.count)
	}
	return out
}

// The device already holds what the live input makes the program compute: the
// first scan's init-derived value (1) is never sent, and nothing is.
func TestStartupNeverDrivesTheInitDerivedValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		down bool
	}{{"device up at start", false}, {"device down 30 scans then up", true}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			var up *atomic.Bool
			if tc.down {
				up = &atomic.Bool{}
			}
			_, _, scan := incRig(t, TagBinding{}, 4, 5, up, rec)
			if tc.down {
				scan(30)
				up.Store(true)
			}
			scan(60)
			if got := cmdWrites(rec); len(got) != 0 {
				t.Fatalf("FC6 frames to Cmd = %v, want none: the device held 5 and the live input makes 5", got)
			}
		})
	}
}

// The device holds another value: exactly one write, of the live-input value.
func TestStartupWritesTheLiveValueOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		down bool
	}{{"device up at start", false}, {"device down 30 scans then up", true}} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			var up *atomic.Bool
			if tc.down {
				up = &atomic.Bool{}
			}
			_, _, scan := incRig(t, TagBinding{}, 4, 9, up, rec)
			if tc.down {
				scan(30)
				up.Store(true)
			}
			scan(60)
			if got := cmdWrites(rec); len(got) != 1 || got[0] != 5 {
				t.Fatalf("FC6 frames to Cmd = %v, want [5]: the live value once, never the init-derived 1", got)
			}
		})
	}
}

// An output that cannot be read back is commanded once at the live push, not
// at the first one.
func TestUnreadableOutputIsCommandedAtTheLivePush(t *testing.T) {
	for name, cmd := range map[string]TagBinding{
		"scan-class none": {ScanClass: NoPoll},
		"write-only":      {WriteOnly: true},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			_, _, scan := incRig(t, cmd, 4, 0, nil, rec)
			scan(60)
			if got := cmdWrites(rec); len(got) != 1 || got[0] != 5 {
				t.Fatalf("FC6 frames to Cmd = %v, want [5]", got)
			}
		})
	}
}

// Without a runtime nothing says which reads a push was computed from: a
// driver written to directly decides at the read, as before. A read that
// came before the connection dropped is not one to decide against.
func TestADroppedConnectionsReadIsNotLive(t *testing.T) {
	srv := newSlave(t)
	srv.Unit(1).SetHolding(200, 5)
	rec := &recorder{}
	var conns atomic.Pointer[net.Conn]
	var up atomic.Bool
	up.Store(true)
	dial := recordingDialer(rec)
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 200, Format: "uint16", Writable: true}}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("test: source down")
		}
		c, err := dial(ctx, addr)
		if err == nil {
			conns.Store(&c)
		}
		return c, err
	}))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	firstRead(t, d, "SP")
	up.Store(false)
	(*conns.Load()).Close() // the connection drops after a read
	waitFor(t, "offline", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"SP": float64(1)}) // differs from the stale read of 5
	settle()
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Fatalf("queued = %d: the baseline decided on a read from a dropped connection", q)
	}
	up.Store(true)
	waitFor(t, "decided on the new connection's read", func() bool { return srv.Unit(1).Holding(200) == 1 })
}

// A readable output waiting for its first read while the gate closes sends
// nothing; after the gate reopens the takeover commands it, equal or not.
func TestGateClosedWhileAReadableOutputWaits(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	rec := &recorder{}
	var up atomic.Bool
	m := Manifest{Sources: []Source{fastSource("DEV", srv.Addr())}, Tags: []TagBinding{
		{Name: "SP", Source: "DEV", Table: TableHolding, Address: 500, Format: "uint16", Writable: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(downUpDialer(rec, &up)))
	if err != nil {
		t.Fatal(err)
	}
	var open atomic.Bool
	open.Store(true)
	d.SetWriteGate(open.Load)
	startDriver(t, d)
	_ = d.WriteOutputs(nio.Values{"SP": float64(77)})
	open.Store(false)
	up.Store(true)
	waitFor(t, "online", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == true })
	settle()
	if n := allWrites(rec); n != 0 || u.Holding(500) != 0 {
		t.Fatalf("closed gate: writes=%d holding=%d", n, u.Holding(500))
	}
	u.SetHolding(500, 77)
	settle()
	open.Store(true)
	waitFor(t, "the reopen asks for the outputs", func() bool { return d.OutputsWanted() })
	_ = d.WriteOutputs(nio.Values{"SP": float64(77)})
	waitFor(t, "commanded after reopen, though equal", func() bool { return len(rec.writes(FCWriteSingleRegister, 500)) == 1 })
}

// Equal means the words the program's value would be written as: floats,
// 32-bit, negative, bits in a shared register and scale plus offset.
func TestBaselineEqualityMatrix(t *testing.T) {
	srv := newSlave(t)
	u := srv.Unit(1)
	f := math.Float32bits(12.5)
	u.SetHolding(100, uint16(f)) // little word order: low word first
	u.SetHolding(101, uint16(f>>16))
	n := uint32(0xFFFE1234)
	u.SetHolding(110, uint16(n))
	u.SetHolding(111, uint16(n>>16))
	u.SetHolding(120, 0xFFF9) // int16 -7
	u.SetHolding(130, 0x0004) // bit 2 set
	u.SetHolding(140, 250)    // scale 0.1 offset -5: 20
	rec := &recorder{}
	src := fastSource("DEV", srv.Addr())
	src.WordOrder = OrderLittle
	m := Manifest{Sources: []Source{src}, Tags: []TagBinding{
		{Name: "F", Source: "DEV", Table: TableHolding, Address: 100, Format: "float32", Writable: true},
		{Name: "D", Source: "DEV", Table: TableHolding, Address: 110, Format: "int32", Writable: true},
		{Name: "I", Source: "DEV", Table: TableHolding, Address: 120, Format: "int16", Writable: true},
		{Name: "B2", Source: "DEV", Table: TableHolding, Address: 130, Format: "bit:2", Writable: true},
		{Name: "B3", Source: "DEV", Table: TableHolding, Address: 130, Format: "bit:3", Writable: true},
		{Name: "S", Source: "DEV", Table: TableHolding, Address: 140, Format: "uint16", Scale: 0.1, Offset: -5, Writable: true},
	}}
	d, err := New(m, WithScanRate(20*time.Millisecond), WithDialer(recordingDialer(rec)))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	firstRead(t, d, "S")
	firstRead(t, d, "F")
	_ = d.WriteOutputs(nio.Values{"F": 12.5, "D": int64(-126412), "I": int64(-7), "B2": true, "B3": false, "S": 20.0})
	settle()
	if n := allWrites(rec); n != 0 {
		t.Fatalf("equal values wrote %d frames: %+v", n, rec.frames)
	}
	// and a differing bit in the shared register is written, the other kept
	_ = d.WriteOutputs(nio.Values{"B3": true})
	waitFor(t, "bit 3 set, bit 2 kept", func() bool { return u.Holding(130) == 0x000C })
}

// An output the program never changes after its first scan is still decided
// once its live read has reached a scan: the driver asks the runtime for the
// whole output set (OutputsWanted), as no output change would push it.
func TestAnUnchangingOutputIsStillDecidedAtTheLivePush(t *testing.T) {
	rec := &recorder{}
	_, _, scan := incRig(t, TagBinding{}, 4, 3, nil, rec,
		"PROGRAM Main\nVAR_EXTERNAL In : DINT; Cmd : DINT; END_VAR\nCmd := 7;\nEND_PROGRAM")
	scan(60)
	if got := cmdWrites(rec); len(got) != 1 || got[0] != 7 {
		t.Fatalf("FC6 frames to Cmd = %v, want [7]", got)
	}
}
