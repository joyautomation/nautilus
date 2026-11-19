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
