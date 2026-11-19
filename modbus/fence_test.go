package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// fenceReq is one request the fenceConn saw: its function code, the
// deadline it ran under and whether it was fenced.
type fenceReq struct {
	fc       byte
	deadline time.Time
	fenced   bool
}

// fenceConn answers every request as a healthy device would and records
// it; after(n) runs once the n-th request is answered.
type fenceConn struct {
	mu    sync.Mutex
	reqs  []fenceReq
	after func(n int)
}

func (f *fenceConn) Request(ctx context.Context, _ uint8, fc byte, pdu []byte) ([]byte, error) {
	dl, _ := ctx.Deadline()
	f.mu.Lock()
	f.reqs = append(f.reqs, fenceReq{fc, dl, fence.Fenced(ctx)})
	n := len(f.reqs)
	f.mu.Unlock()
	if f.after != nil {
		defer f.after(n)
	}
	switch fc {
	case FCReadHoldingRegisters:
		return []byte{2, 0, 0}, nil
	case FCWriteMultipleRegisters:
		return append([]byte(nil), pdu[:4]...), nil
	default:
		return append([]byte(nil), pdu...), nil // FC 5, 6: the echo
	}
}

func (f *fenceConn) writes() []fenceReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fenceReq
	for _, r := range f.reqs {
		if r.fc != FCReadHoldingRegisters {
			out = append(out, r)
		}
	}
	return out
}

// fencedDriver is a driver with one source and three writes queued: a bit
// of a holding register (a read-modify-write), and two registers far
// apart (two requests).
func fencedDriver(t *testing.T) (*Driver, *source) {
	t.Helper()
	m := Manifest{
		Sources: []Source{{ID: "DEV", Host: "127.0.0.1", Port: 502, UnitID: 1, Timeout: time.Second}},
		Tags: []TagBinding{
			{Name: "Run", Source: "DEV", Table: TableHolding, Address: 10, Format: "bit:3", Writable: true},
			{Name: "SP1", Source: "DEV", Table: TableHolding, Address: 100, Format: "uint16", Writable: true},
			{Name: "SP2", Source: "DEV", Table: TableHolding, Address: 900, Format: "uint16", Writable: true},
		},
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	s := d.sources[0]
	s.pending = map[string]any{"Run": true, "SP1": float64(1), "SP2": float64(2)}
	return d, s
}

// Design §3.4, rule 1, on Modbus: the gate is looked at immediately before
// every request. A bit's write is its own send: a gate that closes while
// its register is read stops the write, and the register runs after it.
func TestModbusGateClosedAfterTheBitReadStopsItsWrite(t *testing.T) {
	d, s := fencedDriver(t)
	var mu sync.Mutex
	open := true
	d.SetSendGate(func() (bool, time.Duration) { mu.Lock(); defer mu.Unlock(); return open, time.Second })
	c := &fenceConn{after: func(n int) {
		if n == 1 { // the bit's register has been read
			mu.Lock()
			open = false
			mu.Unlock()
		}
	}}
	d.flushWrites(context.Background(), s, c)
	if w := c.writes(); len(w) != 0 {
		t.Fatalf("sent %d write(s) after the gate closed: %+v", len(w), w)
	}
}

// Rule 2: every write request runs fenced, with the gate's budget as its
// deadline from its own look; with no budget left nothing is sent.
// send_no_deadline turns it red.
func TestModbusEachWriteRunsToItsSendDeadline(t *testing.T) {
	d, s := fencedDriver(t)
	d.SetSendGate(func() (bool, time.Duration) { return true, 400 * time.Millisecond })
	c := &fenceConn{}
	start := time.Now()
	d.flushWrites(context.Background(), s, c)
	w := c.writes()
	if len(w) != 3 {
		t.Fatalf("sent %d writes; want 3 (the bit and two registers)", len(w))
	}
	for i, r := range w {
		if !r.fenced || r.deadline.Before(start.Add(400*time.Millisecond)) || r.deadline.After(time.Now().Add(400*time.Millisecond)) {
			t.Errorf("write %d (fc %d): deadline %v fenced %v; want fenced, 400 ms from its own look", i, r.fc, r.deadline, r.fenced)
		}
	}

	d2, s2 := fencedDriver(t)
	d2.SetSendGate(func() (bool, time.Duration) { return true, 0 })
	c2 := &fenceConn{}
	d2.flushWrites(context.Background(), s2, c2)
	if len(c2.reqs) != 0 {
		t.Fatalf("%d request(s) started with no budget left", len(c2.reqs))
	}
}

// Rule 3, on a real socket: a fenced write the device never answers is
// abandoned at its deadline, well before the source's timeout, and the
// socket is reset: the device side reads ECONNRESET, not a FIN.
// fence_fin turns it red (and send_no_deadline: the write waits out the
// timeout).
func TestModbusFencedWriteResetsTheSocketAtItsDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peerErr := make(chan error, 1)
	go func() {
		pc, err := ln.Accept()
		if err != nil {
			peerErr <- err
			return
		}
		defer pc.Close()
		_ = pc.SetReadDeadline(time.Now().Add(10 * time.Second))
		_, err = io.Copy(io.Discard, pc) // reads the request, never answers
		peerErr <- err
	}()
	d, s := fencedDriver(t)
	d.SetSendGate(func() (bool, time.Duration) { return true, 200 * time.Millisecond })
	tc, err := dialTCP(context.Background(), ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	start := time.Now()
	d.flushWrites(context.Background(), s, tc)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the unanswered write held the flush %s; want it abandoned at its 200 ms deadline", took)
	}
	select {
	case err := <-peerErr:
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("the device side read to %v; want ECONNRESET (the socket reset at the deadline)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the socket was never closed")
	}
}
