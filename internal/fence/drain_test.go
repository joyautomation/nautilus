package fence

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The drain counts a send from BEFORE its gate read (#710 d1b): a send
// that read the gate open just before it closed is still in flight when
// Drain looks, so Drain waits for it. The gate here closes for good the
// moment it is read open, and the read itself stalls: counted after the
// read (enter_after_read), Drain finds nothing in flight and returns while
// the send is still to go out.
func TestDrainWaitsForASendThatReadTheGateOpenJustBeforeItClosed(t *testing.T) {
	var closed atomic.Bool
	reading, release := make(chan struct{}), make(chan struct{})
	g := func() (bool, time.Duration, uint64) {
		open := !closed.Swap(true)
		if open {
			close(reading)
			<-release // the read stalls after the gate has closed behind it
		}
		return open, time.Second, 1
	}
	var ended atomic.Int64
	go func() {
		_, _, ok, done := Begin(g, time.Now())
		if ok {
			time.Sleep(20 * time.Millisecond) // the send
		}
		ended.Store(time.Now().UnixNano())
		done()
	}()
	<-reading
	drained := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		drained <- Drain(ctx, 0)
	}()
	select {
	case err := <-drained:
		t.Fatalf("Drain returned (%v) while a send that read the gate open had not ended", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if ended.Load() == 0 || time.Now().UnixNano() < ended.Load() {
		t.Fatal("Drain returned before the send ended")
	}
}

// Drain waits for every counted send, and settle after the last one that
// went out (its bytes then delivered), not after the drain began.
func TestDrainSettlesAfterTheLastSend(t *testing.T) {
	_, _, ok, done := Begin(gate(true, time.Second), time.Now())
	if !ok {
		t.Fatal("not started")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		done()
	}()
	const settle = 150 * time.Millisecond
	start := time.Now()
	if err := Drain(context.Background(), settle); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < 100*time.Millisecond+settle-10*time.Millisecond {
		t.Fatalf("Drain took %v; want the send's 100 ms plus %v of settle", took, settle)
	}
	if InFlight() != 0 {
		t.Fatalf("%d sends still counted", InFlight())
	}
}

// A drain whose send never ends is unproven: Drain returns ctx's error.
func TestDrainUnprovenWhenASendNeverEnds(t *testing.T) {
	done := Enter()
	defer done()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Drain(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v with a send still in flight; want the context's deadline", err)
	}
}

// A hung device cannot stretch a drain to the send's deadline: Drain
// resets every armed send at once (ResetAll), with SO_LINGER 0 (the device
// sees a reset). drain_no_reset waits the 5 s deadline out.
func TestDrainResetsAHungSend(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	devErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			devErr <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err = io.Copy(io.Discard, c) // reads the request, never answers
		devErr <- err
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	deadline, _, ok, done := Begin(gate(true, 5*time.Second), time.Now())
	if !ok {
		t.Fatal("not started")
	}
	sent := make(chan struct{})
	firedC := make(chan bool, 1)
	go func() {
		defer done()
		stop := Arm(c, deadline)
		_, _ = c.Write([]byte("request"))
		close(sent)
		_, _ = c.Read(make([]byte, 1)) // the answer never comes
		firedC <- stop()
	}()
	<-sent
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := Drain(ctx, 0); err != nil {
		t.Fatalf("Drain = %v after %v; the hung send was not reset", err, time.Since(start))
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Drain took %v with a hung device; want it reset at once", took)
	}
	if err := <-devErr; !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the device saw %v; want a reset", err)
	}
	if <-firedC {
		t.Fatal("the drain's reset reported as the deadline's: it would count as a write cut")
	}
}

// During a drain a send armed late (it had passed the gate before it
// closed) is reset as it is armed, one not yet started does not start, and
// a fenced context is born cancelled; after it, sends start again.
func TestDuringADrainLateSendsAreCutShort(t *testing.T) {
	hold := Enter() // keeps the drain running
	drained := make(chan error, 1)
	go func() { drained <- Drain(context.Background(), 0) }()
	waitDraining(t)

	future := time.Now().Add(time.Minute)
	if Started(future, time.Now()) {
		t.Error("a send started during a drain")
	}
	reset := false
	stop := armFunc(future, func() { reset = true })
	fired := stop()
	if !reset {
		t.Error("a send armed during a drain was not reset at once")
	}
	if fired {
		t.Error("a drain's reset reported as the deadline's: it would count as a write cut")
	}
	ctx, cancel := Context(context.Background(), future)
	if ctx.Err() == nil || !errors.Is(context.Cause(ctx), ErrDraining) {
		t.Errorf("a fenced context made during a drain: err %v cause %v; want cancelled by the drain", ctx.Err(), context.Cause(ctx))
	}
	cancel()

	hold()
	if err := <-drained; err != nil {
		t.Fatal(err)
	}
	if !Started(future, time.Now()) {
		t.Error("a send cannot start after the drain ended")
	}
	ctx, cancel = Context(context.Background(), future)
	defer cancel()
	if ctx.Err() != nil {
		t.Error("a fenced context made after the drain is cancelled")
	}
}

// ResetAll cancels the fenced contexts live when it runs.
func TestResetAllCancelsFencedContexts(t *testing.T) {
	ctx, cancel := Context(context.Background(), time.Now().Add(time.Minute))
	defer cancel()
	ResetAll()
	if !errors.Is(context.Cause(ctx), ErrDraining) {
		t.Fatalf("cause %v; want the drain's", context.Cause(ctx))
	}
}

func waitDraining(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); draining.Load() == 0; {
		if time.Now().After(end) {
			t.Fatal("the drain never began")
		}
		time.Sleep(time.Millisecond)
	}
}
