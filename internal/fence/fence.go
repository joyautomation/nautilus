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
	"net"
	"time"

	nio "github.com/joyautomation/nautilus/io"
)

// Begin reads g once, immediately before a send. ok false: the send is
// not started (the gate is closed, or the send's budget is already used
// up). deadline is when the send must be done by; zero means none (no
// gate, or a gate that is not a pair's lease). now is the wall clock.
func Begin(g nio.SendGate, now time.Time) (deadline time.Time, ok bool) {
	if g == nil {
		return time.Time{}, true
	}
	open, budget := g()
	pause(AfterCheck)
	if !open {
		return time.Time{}, false
	}
	if budget < 0 || mut("send_no_deadline") {
		return time.Time{}, true
	}
	if budget == 0 {
		return time.Time{}, false
	}
	deadline = now.Add(budget)
	pause(AfterDeadline)
	return deadline, true
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
	if tc := tcpOf(c); tc != nil && !mut("fence_fin") {
		_ = tc.SetLinger(0)
	}
	return c.Close()
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
