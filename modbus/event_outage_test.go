package modbus

import (
	"bytes"
	"log/slog"
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
