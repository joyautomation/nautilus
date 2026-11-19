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
	if d, term, ok := Begin(nil, now); !ok || !d.IsZero() || term != 0 {
		t.Errorf("no gate: %v %v; want started, no deadline", d, ok)
	}
	if _, _, ok := Begin(gate(false, time.Second), now); ok {
		t.Error("a closed gate started a send")
	}
	if _, _, ok := Begin(gate(true, 0), now); ok {
		t.Error("a send with no budget left started")
	}
	if d, _, ok := Begin(gate(true, -1), now); !ok || !d.IsZero() {
		t.Errorf("a plain gate: %v %v; want started with no deadline", d, ok)
	}
	if d, term, ok := Begin(gate(true, 300*time.Millisecond), now); !ok || !d.Equal(now.Add(300*time.Millisecond)) || term != 4 {
		t.Errorf("300 ms of budget: %v %v; want a deadline 300 ms out", d, ok)
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
