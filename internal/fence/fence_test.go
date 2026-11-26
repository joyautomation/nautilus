package fence

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func gate(ok bool, budget time.Duration) func() (bool, time.Duration, uint64) {
	return func() (bool, time.Duration, uint64) { return ok, budget, 4 }
}

// Begin reads the gate once: closed or out of budget, the send is not
// started; a negative budget (a gate that is not a pair's lease) has no
// deadline; otherwise the deadline is now plus the budget.
func TestBegin(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if d, term, ok, done := Begin(nil, now); !ok || !d.IsZero() || term != 0 {
		t.Errorf("no gate: %v %v; want started, no deadline", d, ok)
	} else if done(); InFlight() != 0 {
		t.Error("a send without a gate was counted")
	}
	if _, _, ok, done := Begin(gate(false, time.Second), now); ok {
		t.Error("a closed gate started a send")
	} else if done(); InFlight() != 0 {
		t.Errorf("a send the closed gate refused is still counted (%d)", InFlight())
	}
	if _, _, ok, _ := Begin(gate(true, 0), now); ok {
		t.Error("a send with no budget left started")
	} else if InFlight() != 0 {
		t.Errorf("a send with no budget left is still counted (%d)", InFlight())
	}
	if d, _, ok, done := Begin(gate(true, -1), now); !ok || !d.IsZero() {
		t.Errorf("a plain gate: %v %v; want started with no deadline", d, ok)
	} else {
		done()
	}
	d, term, ok, done := Begin(gate(true, 300*time.Millisecond), now)
	if !ok || !d.Equal(now.Add(300*time.Millisecond)) || term != 4 {
		t.Errorf("300 ms of budget: %v %v; want a deadline 300 ms out", d, ok)
	}
	if InFlight() != 1 {
		t.Errorf("a started send is not counted (%d in flight)", InFlight())
	}
	done()
	done() // a second done does not count again
	if InFlight() != 0 {
		t.Errorf("an ended send is still counted (%d in flight)", InFlight())
	}
	if Started(now, now) || !Started(now.Add(time.Millisecond), now) || !Started(time.Time{}, now) {
		t.Error("Started: a send at or past its deadline must not start; one before it, or with none, may")
	}
}

// Context carries the deadline and the fenced mark; a zero deadline leaves
// ctx unchanged.
func TestContext(t *testing.T) {
	base := context.Background()
	if ctx, cancel := Context(base, time.Time{}); ctx != base || Fenced(ctx) {
		t.Error("a zero deadline changed the context")
	} else {
		cancel()
	}
	dl := time.Now().Add(time.Hour)
	ctx, cancel := Context(base, dl)
	defer cancel()
	if d, ok := ctx.Deadline(); !ok || !d.Equal(dl) || !Fenced(ctx) {
		t.Errorf("deadline %v %v, fenced %v; want the send's deadline, fenced", d, ok, Fenced(ctx))
	}
}

// Reset closes a TCP socket with a reset (SO_LINGER 0): the peer reads
// ECONNRESET, not the clean EOF a FIN gives, and the bytes the kernel
// still held for it are dropped (fence_fin turns this red).
func TestResetSendsAnRSTAndDropsQueuedBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			acc <- c
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer := <-acc
	defer peer.Close()
	// The peer does not read yet: fill its receive window and our send
	// buffer, so bytes sit in our kernel when the fence comes.
	tc := c.(*net.TCPConn)
	_ = tc.SetWriteBuffer(4096)
	_ = peer.(*net.TCPConn).SetReadBuffer(4096)
	chunk := make([]byte, 64<<10)
	_ = c.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	for {
		if _, err := c.Write(chunk); err != nil {
			break // the window is full: the rest sits in the send buffer
		}
	}
	if err := Reset(c); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.Copy(io.Discard, peer)
	if !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the peer read to %v; want ECONNRESET (a reset, not a FIN that delivers the queued bytes late)", err)
	}
}

// Arm resets the socket from a timer AT the deadline, with nothing else
// touching it (the sending goroutine stalled, or waiting on a read with a
// later timeout): the peer reads ECONNRESET about then. A send answered in
// time disarms it.
func TestArmResetsAtTheDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			acc <- c
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peer := <-acc
	defer peer.Close()
	start := time.Now()
	stop := Arm(c, start.Add(150*time.Millisecond))
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.Copy(io.Discard, peer)
	took := time.Since(start)
	if !errors.Is(err, syscall.ECONNRESET) || took < 100*time.Millisecond || took > 2*time.Second {
		t.Fatalf("the peer read to %v after %s; want ECONNRESET at about 150 ms", err, took)
	}
	if !stop() {
		t.Error("stop after the reset says it never fired")
	}
	if Arm(c, time.Now().Add(time.Hour))() {
		t.Error("a send done before its deadline reports a reset")
	}
}

// #1040 re-review (3): a timer that has already started when its send
// finishes must not reset the connection, which the next send (after the
// caller's lock) may already be using. The hook holds the timer just
// after it fired; stop runs then; released, the timer leaves c alone.
func TestArmTimerThatLostTheRaceLeavesTheConnAlone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			acc <- c
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	peer := <-acc
	defer peer.Close()
	fired, release := make(chan struct{}), make(chan struct{})
	armFireHook = func() { close(fired); <-release }
	defer func() { armFireHook = nil }()
	stop := Arm(c, time.Now().Add(20*time.Millisecond))
	<-fired // the timer has started
	if stop() {
		t.Fatal("stop says the reset fired; it has not yet")
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
	if _, err := c.Write([]byte("next send")); err != nil {
		t.Fatalf("the next send on the connection failed (%v): the late timer reset it", err)
	}
	buf := make([]byte, 9)
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(peer, buf); err != nil {
		t.Fatalf("the peer did not get the next send: %v", err)
	}
}

// peerRead accepts one connection on ln and reports how its read ended.
func peerRead(t *testing.T, ln net.Listener) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		pc, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer pc.Close()
		_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.Copy(io.Discard, pc)
		done <- err
	}()
	return done
}

// A Socket a transport dials through (gopcua hides its net.Conn) is reset
// two ways: Abort, in place, while the transport still holds it (its next
// write fails), and any plain close, since SO_LINGER 0 was set at dial.
// fence_fin turns both red.
func TestSocketAbortAndCloseReset(t *testing.T) {
	for _, viaAbort := range []bool{true, false} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := peerRead(t, ln)
		var s Socket
		c, err := (&net.Dialer{Control: s.Control}).Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte("a request")); err != nil {
			t.Fatal(err)
		}
		if viaAbort {
			if !s.Abort() {
				t.Fatal("Abort reports it did nothing")
			}
			time.Sleep(50 * time.Millisecond)
			if _, err := c.Write([]byte("after")); err == nil {
				if _, err := c.Write([]byte("after")); err == nil {
					t.Error("the transport could still write on an aborted socket")
				}
			}
		} else {
			c.Close()
		}
		if err := <-done; !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("abort %v: the peer read to %v; want ECONNRESET", viaAbort, err)
		}
		c.Close()
		ln.Close()
	}
}

// A second Abort of a connection already reset reports it reset: a fenced
// write's send-deadline timer and its failure path both abort, and the one
// that comes second must not read "nothing reset" (CI on main after #1359:
// TestOPCUAFencedWriteResetsItsSession waited for a reset the timer had
// already made). abort_twice_fails turns it red.
func TestASecondAbortReportsTheReset(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := peerRead(t, ln)
	var s Socket
	c, err := (&net.Dialer{Control: s.Control}).Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Abort() {
		t.Fatal("the first Abort reports it did nothing")
	}
	<-done
	c.Close() // the transport tears the failed connection down
	if !s.Abort() {
		t.Fatal("a second Abort of the reset (and closed) connection reports nothing reset")
	}
}
