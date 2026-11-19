package optommp

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"
)

// silentUnit is a TCP peer that reads what it is sent and never answers;
// done reports how the read ended and how many bytes came.
func silentUnit(t *testing.T) (*Client, <-chan error, *int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	done := make(chan error, 1)
	var n int64
	go func() {
		pc, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer pc.Close()
		_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err = io.Copy(io.Discard, pc)
		done <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return NewClient(blockingConn(conn), 5*time.Second), done, &n
}

// fencedOpto is a driver for the bench chassis (not started) with one
// digital output queued.
func fencedOpto(t *testing.T) *Driver {
	t.Helper()
	d, err := New(benchManifest(t, newBenchDevice(), ManifestOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	d.pending = map[string]any{"EPIC_M1_DO00": true}
	return d
}

// Design §3.4 on OptoMMP: an output write runs to its send deadline, and
// one the unit has not answered by then is reset (the unit's side reads
// ECONNRESET), well before the client's own 5 s timeout. send_no_deadline
// and fence_fin turn it red.
func TestOptoMMPFencedWriteResetsAtItsDeadline(t *testing.T) {
	c, done, _ := silentUnit(t)
	d := fencedOpto(t)
	d.SetSendGate(func() (bool, time.Duration, uint64) { return true, 200 * time.Millisecond, 1 })
	start := time.Now()
	if _, err := d.flushCount(c); err == nil {
		t.Fatal("an unanswered write succeeded")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the unanswered write held the flush %s; want it ended at its 200 ms deadline", took)
	}
	if err := <-done; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the unit's side read to %v; want ECONNRESET", err)
	}
}

// A command queued under one term of the lease never goes out under the
// next, even when no look saw the gate closed in between (the lease lost
// and won again between two looks). old_term_kept turns it red.
func TestOptoMMPTermMoveDropsTheQueue(t *testing.T) {
	c, done, n := silentUnit(t)
	d := fencedOpto(t)
	var mu sync.Mutex
	term := uint64(5)
	d.SetSendGate(func() (bool, time.Duration, uint64) {
		mu.Lock()
		defer mu.Unlock()
		return true, 200 * time.Millisecond, term
	})
	d.look() // term 5 seen; the command was queued under it
	mu.Lock()
	term = 9
	mu.Unlock()
	if sent, _ := d.flushCount(c); sent != 0 {
		t.Fatalf("sent %d write(s) in term 9; want none computed in term 5", sent)
	}
	c.conn.Close()
	<-done
	if *n != 0 {
		t.Fatalf("the unit received %d bytes; want nothing", *n)
	}
}
