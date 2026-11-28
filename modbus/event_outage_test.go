package modbus

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// syncBuf is a log sink the driver's goroutines may write while the test
// reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// An event command (TagBinding.Event: a struct-wiring `event: true`
// member, #1458) written while its source is offline is dropped when the
// source reconnects, never sent late: a Start pressed while the device was
// unreachable must not fire minutes later. The drop is logged once,
// naming the binding, and counted in the source's health. A level command
// queued meanwhile still goes out on the reconnect (it is re-asserted, as
// ever), and the event binding's next change goes out as usual.
func TestEventCommandQueuedOfflineIsDroppedAtReconnect(t *testing.T) {
	srv := newSlave(t)
	ln := &link{up: true}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "P1.Cmd.Start", Source: "DEV", Table: TableCoil, Address: 1, Writable: true, WriteOnly: true, Event: true},
			{Name: "P1.Cmd.Run", Source: "DEV", Table: TableCoil, Address: 2, Writable: true, WriteOnly: true},
		},
	}
	logs := &syncBuf{}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial),
		WithLogger(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	read := func() nio.Values { v, _ := d.ReadInputs(); return v }
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": false, "P1.Cmd.Run": false}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first writes confirmed", func() bool { return read()["DEV__Online"] == true && d.Health().Sources[0].QueuedWrites == 0 })

	ln.cut()
	waitFor(t, "source offline", func() bool { return read()["DEV__Online"] == false })
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": true, "P1.Cmd.Run": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both queued", func() bool { return d.Health().Sources[0].QueuedWrites == 2 })

	ln.restore()
	waitFor(t, "level command re-asserted on the reconnect", func() bool { return srv.Unit(1).Coil(2) })
	time.Sleep(60 * time.Millisecond) // a few more flush rounds
	if srv.Unit(1).Coil(1) {
		t.Fatal("the event command written while the device was offline went out late on the reconnect")
	}
	if got := d.Health().Sources[0].DroppedEvents; got != 1 {
		t.Errorf("DroppedEvents = %d, want 1", got)
	}
	if q := d.Health().Sources[0].QueuedWrites; q != 0 {
		t.Errorf("QueuedWrites = %d after the reconnect, want 0", q)
	}
	if n := strings.Count(logs.String(), "event command dropped"); n != 1 {
		t.Errorf("logged the drop %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "P1.Cmd.Start") {
		t.Errorf("the drop's log line does not name the binding:\n%s", logs.String())
	}

	// A live replace of the source keeps the count (since the driver
	// started; a replace is not a start).
	if err := d.ReplaceSource(fastSource("DEV", srv.Addr()), m.Tags, ReplaceOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := d.Health().Sources[0].DroppedEvents; got != 1 {
		t.Errorf("DroppedEvents = %d after a replace, want 1 carried over", got)
	}
	waitFor(t, "the replaced source connects", func() bool { return d.Health().Sources[0].State == "connected" })

	// The device never got TRUE; the binding's next change goes out.
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": false, "P1.Cmd.Run": true}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": true, "P1.Cmd.Run": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the next change of the event command", func() bool { return srv.Unit(1).Coil(1) })
}

// An event command written while connected is sent as any command is: the
// drop is for what waited out an outage only.
func TestEventCommandWhileConnectedIsSent(t *testing.T) {
	srv := newSlave(t)
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags:    []TagBinding{{Name: "P1.Cmd.Start", Source: "DEV", Table: TableCoil, Address: 1, Writable: true, WriteOnly: true, Event: true}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": false}); err != nil {
		t.Fatal(err)
	}
	if err := d.WriteOutputs(nio.Values{"P1.Cmd.Start": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "event command sent", func() bool { return srv.Unit(1).Coil(1) })
	if got := d.Health().Sources[0].DroppedEvents; got != 0 {
		t.Errorf("DroppedEvents = %d, want 0", got)
	}
}

// The release of a trigger is not dropped: an event command that went out
// (Start TRUE), then was released (FALSE) while the device could not be
// reached, sends the release on the reconnect. Dropping it would leave the
// device's coil asserted while the program holds FALSE, and the next press
// would write TRUE onto TRUE with no edge.
func TestEventCommandReleaseQueuedOfflineStillGoesOut(t *testing.T) {
	srv := newSlave(t)
	ln := &link{up: true}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags: []TagBinding{
			{Name: "P1.Cmd.Start", Source: "DEV", Table: TableCoil, Address: 1, Writable: true, WriteOnly: true, Event: true},
			{Name: "P1.Cmd.Reset", Source: "DEV", Table: TableHolding, Address: 5, Format: "bit:3", Writable: true, WriteOnly: true, Event: true},
		},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(ln.dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": false, "P1.Cmd.Reset": false})
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": true, "P1.Cmd.Reset": true})
	waitFor(t, "the press reaches the device", func() bool { return srv.Unit(1).Coil(1) && srv.Unit(1).Holding(5) == 8 })

	ln.cut()
	waitFor(t, "source offline", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": false, "P1.Cmd.Reset": false})
	waitFor(t, "both releases queued", func() bool { return d.Health().Sources[0].QueuedWrites == 2 })
	ln.restore()
	waitFor(t, "the releases go out on the reconnect", func() bool { return !srv.Unit(1).Coil(1) && srv.Unit(1).Holding(5) == 0 })
	if got := d.Health().Sources[0].DroppedEvents; got != 0 {
		t.Errorf("DroppedEvents = %d, want 0: a release is never dropped", got)
	}
}

// event marks a command, and an event command is never re-sent every
// period: Validate refuses event without writable and event with rewrite.
func TestEventValidate(t *testing.T) {
	src := []Source{{ID: "A", Host: "127.0.0.1"}}
	for want, b := range map[string]TagBinding{
		"event without writable": {Name: "T", Source: "A", Table: TableCoil, Event: true},
		"event and rewrite":      {Name: "T", Source: "A", Table: TableCoil, Writable: true, Event: true, Rewrite: time.Second},
		"event on a INT binding": {Name: "T", Source: "A", Table: TableHolding, Format: "int16", Writable: true, Event: true},
	} {
		err := Manifest{Sources: src, Tags: []TagBinding{b}}.Validate()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want %q", err, want)
		}
	}
	if err := (Manifest{Sources: src, Tags: []TagBinding{{Name: "T", Source: "A", Table: TableCoil, Writable: true, Event: true}}}).Validate(); err != nil {
		t.Errorf("a writable event command: %v", err)
	}
}

// event is no manifest key: struct wiring sets it, and the runtime's event
// rules in front of the driver are what close its other late paths (the
// start-up baseline, a gateway's retry, a gate reopen). A hand-written
// manifest naming it is refused, not half-protected.
func TestEventIsNoManifestKey(t *testing.T) {
	_, err := ParseManifest([]byte("sources:\n  - {id: A, host: 127.0.0.1}\ntags:\n  - {name: T, source: A, table: coil, address: 0, writable: true, event: true}\n"))
	if err == nil || !strings.Contains(err.Error(), `unknown key "event"`) {
		t.Fatalf("ParseManifest = %v, want the event key refused", err)
	}
}

// A press and its release both raised during one outage leave the device
// at rest with no late edge: the queue holds the latest value (the
// release), which goes out, and no TRUE is ever written to the coil after
// the reconnect.
func TestEventPressAndReleaseInOneOutageSendNoLateEdge(t *testing.T) {
	srv := newSlave(t)
	ln := &link{up: true}
	rec := &recorder{}
	dial := func(ctx context.Context, addr string) (net.Conn, error) {
		nc, err := ln.dial(ctx, addr)
		if err != nil {
			return nil, err
		}
		return &recordingConn{Conn: nc, rec: rec}, nil
	}
	m := Manifest{
		Sources: []Source{fastSource("DEV", srv.Addr())},
		Tags:    []TagBinding{{Name: "P1.Cmd.Start", Source: "DEV", Table: TableCoil, Address: 1, Writable: true, WriteOnly: true, Event: true}},
	}
	d, err := New(m, WithScanRate(15*time.Millisecond), WithDialer(dial))
	if err != nil {
		t.Fatal(err)
	}
	startDriver(t, d)
	waitFor(t, "connect", func() bool { return d.Health().Sources[0].State == "connected" })
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": false})
	waitFor(t, "the device holds FALSE", func() bool { return d.Health().Sources[0].QueuedWrites == 0 })
	pressed := func() int {
		n := 0
		for _, f := range rec.writes(FCWriteSingleCoil, 1) {
			if f.count == 0xFF00 {
				n++
			}
		}
		return n
	}

	ln.cut()
	waitFor(t, "source offline", func() bool { v, _ := d.ReadInputs(); return v["DEV__Online"] == false })
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": true})  // press
	_ = d.WriteOutputs(nio.Values{"P1.Cmd.Start": false}) // and release, still offline
	ln.restore()
	waitFor(t, "back online with nothing queued", func() bool {
		v, _ := d.ReadInputs()
		return v["DEV__Online"] == true && d.Health().Sources[0].QueuedWrites == 0
	})
	time.Sleep(60 * time.Millisecond) // a few more flush rounds
	if srv.Unit(1).Coil(1) {
		t.Fatal("the device is left asserted after a press and release raised during the outage")
	}
	if n := pressed(); n != 0 {
		t.Fatalf("%d TRUE writes reached the coil: a press raised during the outage went out late", n)
	}
}
