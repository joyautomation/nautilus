// Package fence is how a field driver lets a write leave in time on a
// redundant pair (design §3.4, #710):
//
//  1. Check before each send. The pair's gate (io.SendGate) is read
//     immediately before every send, one request, one datagram, one frame,
//     never once per batch (Begin).
//  2. A fence margin and a send deadline. The gate closes a margin before
//     the lease ends; a send gets the budget the gate reports as its
//     deadline, and a send whose deadline has passed is not started
//     (Begin, Started).
//  3. Fencing resets sockets. A send still in flight at its deadline
//     (unanswered, or with bytes still in the kernel's send buffer) is
//     closed with SO_LINGER 0, so the kernel sends a reset and discards
//     what it held (Reset). The deadline is the instant: a byte that left
//     before it arrives before the lease ends.
//
// Package fence imports only io: every driver may use it.
package fence

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Begin reads g once, immediately before a send. ok false: the send is
// not started (the gate is closed, or the send's budget is already used
// up). deadline is when the send must be done by; zero means none (no
// gate, or a gate that is not a pair's lease). term is the lease's term
// from the same read (0 without a pair), for a driver that holds queued
// commands to check they were computed under it. now is the wall clock,
// taken before the read, so the budget counts from before it.
func Begin(g nio.SendGate, now time.Time) (deadline time.Time, term uint64, ok bool) {
	if g == nil {
		return time.Time{}, 0, true
	}
	open, budget, term := g()
	pause(AfterCheck)
	if !open {
		return time.Time{}, term, false
	}
	if budget < 0 || mut("send_no_deadline") {
		return time.Time{}, term, true
	}
	if budget == 0 {
		return time.Time{}, term, false
	}
	deadline = now.Add(budget)
	pause(AfterDeadline)
	return deadline, term, true
}

// Started reports whether a send with deadline may still start now: the
// last look before the send system call. Zero deadline: always.
func Started(deadline, now time.Time) bool {
	if deadline.IsZero() || mut("send_no_deadline") {
		return true
	}
	return now.Before(deadline)
}

type fencedKey struct{}

// Context is ctx bounded by a send's deadline and marked as a fenced send,
// so the transport that carries it resets its socket when the send fails
// (Fenced). A zero deadline returns ctx unchanged and a no-op cancel.
func Context(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return ctx, func() {}
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	return context.WithValue(ctx, fencedKey{}, true), cancel
}

// Fenced reports whether ctx carries a send deadline from Context: a
// transport error on such a send (its deadline passed with the send or
// its answer still pending) must Reset the socket, never close it plainly.
func Fenced(ctx context.Context) bool {
	v, _ := ctx.Value(fencedKey{}).(bool)
	return v
}

// Reset closes c with SO_LINGER 0 when it is a TCP socket: the kernel
// sends a reset and drops what was still in its send buffer, instead of
// delivering it late after a FIN. Any other conn is closed plainly.
func Reset(c net.Conn) error {
	if c == nil {
		return nil
	}
	Linger0(c)
	return c.Close()
}

// Linger0 sets SO_LINGER 0 on c's TCP socket, so whatever closes it
// (Reset, or a transport that closes it on its own at a deadline) sends a
// reset. A failure is logged: the close is then a FIN, and what the kernel
// held may still leave.
func Linger0(c net.Conn) {
	tc := tcpOf(c)
	if tc == nil || mut("fence_fin") {
		return
	}
	if err := tc.SetLinger(0); err != nil {
		slog.Warn("fence: SO_LINGER 0 failed; this socket's close is a FIN, so bytes it holds may still leave", "remote", c.RemoteAddr(), "error", err)
	}
}

// LingerDefault lifts Linger0 from c's socket: a connection kept for more
// requests after a fenced one was answered in time closes normally again.
func LingerDefault(c net.Conn) {
	if tc := tcpOf(c); tc != nil {
		_ = tc.SetLinger(-1)
	}
}

// Arm resets c with a timer AT deadline (design §3.4, rule 3), whatever the
// sending goroutine is doing then: stalled, or waiting on a read whose own
// timeout comes later. stop disarms it once the send is done and reports
// whether the reset already fired (the send then failed, and c is closed).
// A zero deadline or a nil conn arms nothing.
func Arm(c net.Conn, deadline time.Time) (stop func() (fired bool)) {
	if c == nil || deadline.IsZero() || mut("send_no_deadline") {
		return func() bool { return false }
	}
	var mu sync.Mutex
	fired := false
	t := time.AfterFunc(time.Until(deadline), func() {
		mu.Lock()
		fired = true
		mu.Unlock()
		_ = Reset(c)
	})
	return func() bool {
		if t.Stop() {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return fired
	}
}

// tcpOf finds the *net.TCPConn under c: c itself, or what a wrapper
// exposes with NetConn (as *tls.Conn does).
func tcpOf(c net.Conn) *net.TCPConn {
	for range 4 {
		if tc, ok := c.(*net.TCPConn); ok {
			return tc
		}
		w, ok := c.(interface{ NetConn() net.Conn })
		if !ok {
			return nil
		}
		c = w.NetConn()
	}
	return nil
}
