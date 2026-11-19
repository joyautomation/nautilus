package cip

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
)

// Design §3.4, rule 3, on EtherNet/IP: a fenced request the controller
// never answers is abandoned at its send deadline and the session's socket
// is reset (the controller side reads ECONNRESET, not a FIN), leaving the
// client unconnected for a fresh dial. fence_fin turns it red.
func TestFencedSendUnitDataResetsAtItsDeadline(t *testing.T) {
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
		_, err = io.Copy(io.Discard, pc) // takes the request, never answers
		peerErr <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Log: slog.Default(), tcp: conn}
	ctx, cancel := fence.Context(context.Background(), time.Now().Add(200*time.Millisecond))
	defer cancel()
	start := time.Now()
	if _, err := c.SendUnitData(ctx, 1, 1, 0x4D, []byte{0x91, 0x02, 'S', 'P'}, []byte{0xCA, 0, 1, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("an unanswered request succeeded")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the unanswered request took %s; want it abandoned at its 200 ms deadline", took)
	}
	select {
	case err := <-peerErr:
		if !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("the controller side read to %v; want ECONNRESET", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the socket was never closed")
	}
	if c.tcp != nil {
		t.Fatal("the client still holds the reset socket")
	}
}

// deafConn ignores deadlines, as a goroutine stalled past its read timeout
// would: only a timer can end its request.
type deafConn struct{ *net.TCPConn }

func (c deafConn) SetDeadline(time.Time) error      { return nil }
func (c deafConn) SetReadDeadline(time.Time) error  { return nil }
func (c deafConn) SetWriteDeadline(time.Time) error { return nil }
func (c deafConn) NetConn() net.Conn                { return c.TCPConn }

// #1040 review (5) on EtherNet/IP: the session's socket is reset by a timer
// AT the send deadline, not after the read's own timeout returns.
func TestFencedSendUnitDataResetIsATimer(t *testing.T) {
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
	c := &Client{Log: slog.Default(), tcp: deafConn{nc.(*net.TCPConn)}}
	ctx, cancel := fence.Context(context.Background(), time.Now().Add(200*time.Millisecond))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.SendUnitData(ctx, 1, 1, 0x4D, []byte{0x91, 0x02, 'S', 'P'}, []byte{0xCA, 0, 1, 0, 0, 0, 0, 0})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an unanswered request succeeded")
		}
	case <-time.After(3 * time.Second):
		nc.Close()
		t.Fatal("the unanswered request never ended: nothing reset the socket at its deadline")
	}
	if err := <-peerErr; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the controller side read to %v; want ECONNRESET", err)
	}
}

// 3b-3b: a request whose context carries the pair's gate (fence.WithGate:
// one fragment of a Write Tag Fragmented) reads it before it is sent; with
// the gate closed, or the lease's term moved, nothing reaches the
// controller. fragments_unchecked turns it red.
func TestSendUnitDataChecksTheGateBeforeEachRequest(t *testing.T) {
	for _, c := range []struct {
		name string
		open bool
		term uint64
	}{{"gate closed", false, 5}, {"term moved", true, 9}} {
		t.Run(c.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			got := make(chan int64, 1)
			go func() {
				pc, err := ln.Accept()
				if err != nil {
					got <- -1
					return
				}
				defer pc.Close()
				_ = pc.SetReadDeadline(time.Now().Add(time.Second))
				n, _ := io.Copy(io.Discard, pc)
				got <- n
			}()
			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			cl := &Client{Log: slog.Default(), tcp: conn}
			gate := func() (bool, time.Duration, uint64) { return c.open, time.Second, c.term }
			ctx := fence.WithGate(context.Background(), gate, 5) // the write was computed under term 5
			if _, err := cl.SendUnitData(ctx, 1, 1, 0x4D, []byte{0x91, 0x02, 'S', 'P'}, []byte{0xCA, 0, 1, 0, 0, 0, 0, 0}); !errors.Is(err, fence.ErrGateClosed) {
				t.Fatalf("SendUnitData: %v; want ErrGateClosed (not sent)", err)
			}
			conn.Close()
			if n := <-got; n != 0 {
				t.Fatalf("the controller received %d bytes; want nothing", n)
			}
		})
	}
}
