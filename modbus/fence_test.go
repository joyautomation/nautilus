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
	nio "github.com/joyautomation/nautilus/io"
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
	fence.ResetCutsForTest()
	defer func() {
		// A write not started because the gate closed is not a cut.
		if got := fence.CutCounts(); len(got) != 0 {
			t.Errorf("a closed gate recorded cuts: %v", got)
		}
	}()
	d, s := fencedDriver(t)
	var mu sync.Mutex
	open := true
	d.SetSendGate(func() (bool, time.Duration, uint64) { mu.Lock(); defer mu.Unlock(); return open, time.Second, 1 })
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
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, 400 * time.Millisecond, 1 })
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
	d2.SetSendGate(func() (bool, time.Duration, uint64) { return true, 0, 1 })
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
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, 200 * time.Millisecond, 1 })
	tc, err := dialTCP(context.Background(), ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	fence.ResetCutsForTest()
	start := time.Now()
	d.flushWrites(context.Background(), s, tc)
	// One request was on the wire at its deadline: one write cut, by the
	// source's id (#710). The two writes behind it never started.
	if got := fence.CutCounts(); len(got) != 1 || got["DEV"] != 1 {
		t.Fatalf("writes cut = %v, want DEV 1 (once, however the failure was seen)", got)
	}
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

// WriteTimeouts names only the sources that take writes, each with its
// request timeout (naut check compares it with a pair's send budget).
func TestModbusWriteTimeouts(t *testing.T) {
	m := Manifest{
		Sources: []Source{
			{ID: "W", Host: "127.0.0.1", Port: 502, UnitID: 1, Timeout: 2 * time.Second},
			{ID: "R", Host: "127.0.0.2", Port: 502, UnitID: 1},
		},
		Tags: []TagBinding{
			{Name: "SP", Source: "W", Table: TableHolding, Address: 1, Format: "uint16", Writable: true},
			{Name: "PV", Source: "R", Table: TableHolding, Address: 1, Format: "uint16"},
		},
	}
	d, err := New(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.WriteTimeouts(); len(got) != 1 || got["W"] != 2*time.Second {
		t.Fatalf("WriteTimeouts %v; want only W at 2s", got)
	}
}

// failFirst answers like fenceConn, except that the first write fails as a
// transport error (a write past its deadline, its socket reset).
type failFirst struct {
	fenceConn
	failed bool
}

func (f *failFirst) Request(ctx context.Context, unit uint8, fc byte, pdu []byte) ([]byte, error) {
	if fc != FCReadHoldingRegisters && !f.failed {
		f.failed = true
		return nil, errors.New("modbus: read header: i/o timeout")
	}
	return f.fenceConn.Request(ctx, unit, fc, pdu)
}

// #1040 review (2): a write that failed on the wire is put back (requeue),
// and must not go out after this replica lost the lease and won it again:
// the close seen by a look or not (the lease's term moved between two
// looks), and the reopen seen by the flush itself or by another look
// before it (the rewrite loop, a push). old_term_kept turns the first
// cases red; a reopen that keeps pending turns the last red.
func TestModbusRequeuedWriteDoesNotSurviveAReLead(t *testing.T) {
	for _, c := range []struct {
		name                string
		closeSeen, lookedAt bool
	}{
		{"close seen, reopen seen by the flush", true, false},
		{"close unseen, the term moved", false, false},
		{"close seen, reopen seen by another look first", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			d, s := fencedDriver(t)
			s.pending = map[string]any{"SP1": float64(1)}
			var mu sync.Mutex
			open, term := true, uint64(5)
			d.SetSendGate(func() (bool, time.Duration, uint64) { mu.Lock(); defer mu.Unlock(); return open, time.Second, term })
			set := func(o bool, tm uint64) { mu.Lock(); open, term = o, tm; mu.Unlock() }
			d.look() // term 5 seen
			conn := &failFirst{}
			d.flushWrites(context.Background(), s, conn) // fails: SP1 is put back
			s.mu.Lock()
			_, requeued := s.pending["SP1"]
			s.mu.Unlock()
			if !requeued {
				t.Fatal("the failed write was not put back; the test needs it queued")
			}
			if c.closeSeen {
				set(false, 5)
				d.look()
			}
			set(true, 9) // the lease is won again, in term 9
			if c.lookedAt {
				d.look()
			}
			d.flushWrites(context.Background(), s, conn)
			if w := conn.writes(); len(w) != 0 {
				t.Fatalf("the term-5 write went out again in term 9 (%d request(s)); want nothing", len(w))
			}
		})
	}
}

// deafConn is a TCP conn that ignores deadlines, as a goroutine stalled
// past its read timeout would: only a timer can end its request.
type deafConn struct{ *net.TCPConn }

func (c deafConn) SetDeadline(time.Time) error      { return nil }
func (c deafConn) SetReadDeadline(time.Time) error  { return nil }
func (c deafConn) SetWriteDeadline(time.Time) error { return nil }
func (c deafConn) NetConn() net.Conn                { return c.TCPConn }

// #1040 review (5): the reset is armed with a timer AT the send deadline,
// not done after the read's timeout returns. With deadlines ignored, the
// unanswered write still ends at its deadline, by a reset the device sees.
func TestModbusResetIsATimerAtTheDeadline(t *testing.T) {
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
		_, err = io.Copy(io.Discard, pc)
		peerErr <- err
	}()
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tc := newTCPConn(deafConn{nc.(*net.TCPConn)}, 5*time.Second)
	ctx, cancel := fence.Context(context.Background(), time.Now().Add(200*time.Millisecond))
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- writeSingleRegister(ctx, tc, 1, 100, 7) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unanswered write succeeded")
		}
	case <-time.After(3 * time.Second):
		nc.Close()
		t.Fatal("the unanswered write never ended: nothing reset the socket at its deadline")
	}
	if err := <-peerErr; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the device side read to %v; want ECONNRESET", err)
	}
}

// #1040 re-review (1): a flush reads its reopen generation, then a scan
// sees the reopen (the lease won again) and the push answering it queues
// SP1=5, recording it as written, before the flush takes the queue. The
// flush must not send it (the generation moved), and must not lose it
// either: it is forgotten as written and the outputs are asked for, so the
// next push queues SP1=5 again.
func TestModbusFlushRacingAReopenDoesNotLoseTheNewWrite(t *testing.T) {
	d, s := fencedDriver(t)
	s.pending = map[string]any{}
	var mu sync.Mutex
	term := uint64(5)
	d.SetSendGate(func() (bool, time.Duration, uint64) { mu.Lock(); defer mu.Unlock(); return true, time.Second, term })
	d.look()                                                              // term 5 seen
	if err := d.WriteOutputs(nio.Values{"SP1": float64(1)}); err != nil { // the first push: a baseline
		t.Fatal(err)
	}
	d.OutputsWanted()
	flushGenHook = func() {
		flushGenHook = nil
		mu.Lock()
		term = 9 // the lease lost and won again
		mu.Unlock()
		d.look() // a scan sees the reopen: forgetWritten, a new generation
		d.OutputsWanted()
		if err := d.WriteOutputs(nio.Values{"SP1": float64(5)}); err != nil { // the answering push
			t.Error(err)
		}
	}
	defer func() { flushGenHook = nil }()
	c := &fenceConn{}
	d.flushWrites(context.Background(), s, c)
	if w := c.writes(); len(w) != 0 {
		t.Fatalf("the racing flush sent %d write(s); want none (its generation moved)", len(w))
	}
	s.mu.Lock()
	_, kept := s.written["SP1"]
	s.mu.Unlock()
	wanted := d.OutputsWanted()
	if kept || !wanted {
		t.Fatalf("SP1=5 kept as written %v, outputs asked for %v: the next push would skip it and the device never get it", kept, wanted)
	}
	if err := d.WriteOutputs(nio.Values{"SP1": float64(5)}); err != nil {
		t.Fatal(err)
	}
	d.flushWrites(context.Background(), s, c)
	if w := c.writes(); len(w) != 1 {
		t.Fatalf("after the next push %d write(s); want SP1=5 sent once", len(w))
	}
}
