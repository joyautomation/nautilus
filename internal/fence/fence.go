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
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
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
//
// done ends the send for Drain: the send is counted before the gate is
// read (Enter), and the caller calls done once it has ended (answered,
// failed or reset), on every path, including one that drops it after
// Begin. A send Begin refuses is already ended; its done does nothing. done
// may be called more than once. A nil gate (no pair) counts nothing; a
// driver's own look func is a gate even off a pair, so its sends are
// counted there too (one lock per send, and nothing ever drains them).
func Begin(g nio.SendGate, now time.Time) (deadline time.Time, term uint64, ok bool, done func()) {
	if g == nil {
		return time.Time{}, 0, true, noDone
	}
	early := !mut("enter_after_read")
	if early {
		enter()
	}
	open, budget, term := g()
	pause(AfterCheck)
	if !early {
		enter()
	}
	if !open {
		exit(false)
		return time.Time{}, term, false, noDone
	}
	var once atomic.Bool
	done = func() {
		if once.CompareAndSwap(false, true) {
			exit(true)
		}
	}
	if budget < 0 || mut("send_no_deadline") {
		return time.Time{}, term, true, done
	}
	if budget == 0 {
		once.Store(true)
		exit(false)
		return time.Time{}, term, false, noDone
	}
	deadline = now.Add(budget)
	pause(AfterDeadline)
	return deadline, term, true, done
}

// Started reports whether a send with deadline may still start now: the
// last look before the send system call. Zero deadline: always. During a
// Drain a send with a deadline (one on a pair) does not start.
func Started(deadline, now time.Time) bool {
	if deadline.IsZero() || mut("send_no_deadline") {
		return true
	}
	if draining.Load() > 0 && !mut("drain_no_reset") {
		return false
	}
	return now.Before(deadline)
}

type fencedKey struct{}

// Context is ctx bounded by a send's deadline and marked as a fenced send,
// so the transport that carries it resets its socket when the send fails
// (Fenced). A Drain cancels it (ResetAll). A zero deadline returns ctx
// unchanged and a no-op cancel.
func Context(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if deadline.IsZero() {
		return ctx, func() {}
	}
	ctx, cancelCause := context.WithCancelCause(ctx)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	fc := &fencedCtx{cancel: cancelCause}
	registerCtx(fc)
	return context.WithValue(ctx, fencedKey{}, true), func() {
		unregisterCtx(fc)
		cancel()
		cancelCause(nil)
	}
}

// Fenced reports whether ctx carries a send deadline from Context: a
// transport error on such a send (its deadline passed with the send or
// its answer still pending) must Reset the socket, never close it plainly.
func Fenced(ctx context.Context) bool {
	v, _ := ctx.Value(fencedKey{}).(bool)
	return v
}

type gateKey struct{}

type gateVal struct {
	g    nio.SendGate
	term uint64
}

// WithGate carries a pair's gate, and the lease's term the send was
// computed under, on ctx: a transport that splits one send into several
// requests (Write Tag Fragmented) checks it before each (Check), so a gate
// that closes, or a lease lost and won again, stops the rest.
func WithGate(ctx context.Context, g nio.SendGate, term uint64) context.Context {
	if g == nil {
		return ctx
	}
	return context.WithValue(ctx, gateKey{}, gateVal{g, term})
}

// ErrGateClosed is Check's answer when the request may not go: the gate
// is closed, or open under another term than the send's.
var ErrGateClosed = errors.New("write gate closed (or the lease's term moved) before this request: not sent")

// ErrNotStarted marks a send refused before anything went out (its gate
// closed, or its deadline passed first). Such a write is never a cut
// (CutSend). A transport that can tell wraps it (cip.ErrNotStarted is
// this value).
var ErrNotStarted = errors.New("request not started")

// Check reads the gate WithGate put on ctx, immediately before one request
// of a send: nil to send it, ErrGateClosed (or ctx's error, past its
// deadline) not to. A ctx with no gate checks only its deadline.
func Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v, ok := ctx.Value(gateKey{}).(gateVal)
	if !ok || mut("fragments_unchecked") {
		return nil
	}
	open, _, term := v.g()
	if !open || (v.term != 0 && term != v.term) {
		return ErrGateClosed
	}
	return nil
}

// Reset closes c with SO_LINGER 0 when it is a TCP socket: the kernel
// sends a reset and drops what was still in its send buffer, instead of
// delivering it late after a FIN. Any other conn is closed plainly.
//
// A conn that can abort itself in place (Aborter: one read and written
// with raw syscalls on its own descriptor) is aborted instead, never
// closed here: closing its descriptor under a thread blocked on it would
// let that thread's retry reach a reused descriptor (#1047 review); its
// owner closes it, once.
func Reset(c net.Conn) error {
	if c == nil {
		return nil
	}
	if a, ok := c.(Aborter); ok {
		if mut("fence_fin") {
			return nil
		}
		a.Abort()
		return nil
	}
	Linger0(c)
	return c.Close()
}

// Aborter is a conn that resets its own TCP connection in place (an RST,
// the send queue dropped) without closing its descriptor.
type Aborter interface {
	Abort() bool
}

// AbortFD resets the TCP socket fd in place (Linux: connect AF_UNSPEC),
// leaving the descriptor open for its owner to close; false elsewhere.
func AbortFD(fd uintptr) bool { return abortFD(fd) }

// Linger0 sets SO_LINGER 0 on c's TCP socket, so whatever closes it
// (Reset, or a transport that closes it on its own at a deadline) sends a
// reset. A failure is logged: the close is then a FIN, and what the kernel
// held may still leave.
func Linger0(c net.Conn) {
	tc := tcpOf(c)
	if tc == nil || mut("fence_fin") {
		return
	}
	if err := tc.SetLinger(0); err != nil && !errors.Is(err, net.ErrClosed) {
		// Already closed (a timer that fired after its owner closed it)
		// is nothing to report.
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
// whether the deadline's reset already fired (the send then failed, and c
// is closed). A drain's reset (ResetAll) reports false: a planned
// switchover's cut-short send is no write cut (CutSend).
// After stop returns the timer never resets c: a timer that had started
// finds the send done and leaves c alone, and one already resetting
// finishes before stop returns, so a later send that reuses c (after the
// caller's own lock) is never hit (#1040 re-review). A zero deadline or a
// nil conn arms nothing.
func Arm(c net.Conn, deadline time.Time) (stop func() (fired bool)) {
	if c == nil {
		return func() bool { return false }
	}
	return armFunc(deadline, func() { _ = Reset(c) })
}

// ArmAbort is Arm for a Socket a transport owns (Socket.Abort at the
// deadline).
func ArmAbort(s *Socket, deadline time.Time) (stop func() (fired bool)) {
	if s == nil {
		return func() bool { return false }
	}
	return armFunc(deadline, func() { s.Abort() })
}

// armFunc runs reset at deadline unless stop came first (see Arm). An
// armed send is also reset by ResetAll, and at once when armed during a
// Drain.
func armFunc(deadline time.Time, reset func()) (stop func() (fired bool)) {
	if deadline.IsZero() || mut("send_no_deadline") {
		return func() bool { return false }
	}
	a := &armed{reset: reset}
	t := time.AfterFunc(time.Until(deadline), func() {
		if armFireHook != nil {
			armFireHook()
		}
		a.fire(false) // a send that finished first leaves the conn alone: it may carry the next one
	})
	register(a)
	return func() bool {
		t.Stop()
		unregister(a)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.done = true
		return a.fired && !a.drained
	}
}

// Socket is a TCP socket a transport dials and owns (a library that hides
// its net.Conn, as gopcua does): Control, set as the net.Dialer's, records
// it and sets SO_LINGER 0 on it, so every close of it is a reset; Abort
// resets it in place. Safe for concurrent use. The zero value is ready.
type Socket struct {
	mu sync.Mutex
	rc syscall.RawConn
	// reset: this connection was reset (Abort), by whichever path got
	// there first.
	reset bool
}

// Control is a net.Dialer Control func: it keeps the socket and sets
// SO_LINGER 0 on it.
func (s *Socket) Control(_, _ string, c syscall.RawConn) error {
	s.mu.Lock()
	s.rc, s.reset = c, false
	s.mu.Unlock()
	if mut("fence_fin") {
		return nil
	}
	var serr error
	if err := c.Control(func(fd uintptr) { serr = linger0(fd) }); err != nil {
		return err
	}
	if serr != nil {
		slog.Warn("fence: SO_LINGER 0 failed at dial; this socket's close is a FIN, so bytes it holds may still leave", "error", serr)
	}
	return nil
}

// Abort resets the socket where it stands: the kernel sends a reset and
// drops what it still held, and the transport's next read or write fails,
// without closing the descriptor under it (Linux: connect AF_UNSPEC;
// elsewhere nothing, and Abort reports false: a pair runs on Linux only).
//
// It reports whether the connection is reset, by this call or an earlier
// one: a fenced write's send-deadline timer and its failure path both
// abort, and whichever comes second finds it done (on a loaded runner the
// timer often wins; CI on main after #1359).
func (s *Socket) Abort() bool {
	s.mu.Lock()
	rc, done := s.rc, s.reset
	s.mu.Unlock()
	if rc == nil || mut("fence_fin") {
		return false
	}
	if done && !mut("abort_twice_fails") {
		return true
	}
	ok := false
	_ = rc.Control(func(fd uintptr) { ok = abortFD(fd) })
	if ok {
		s.mu.Lock()
		if s.rc == rc {
			s.reset = true
		}
		s.mu.Unlock()
	}
	return ok
}

// armFireHook, when a test sets it, runs as an Arm timer fires, before it
// looks at whether its send is done.
var armFireHook func()

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
