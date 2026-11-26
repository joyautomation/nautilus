package fence

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// A planned switchover's drain (#710 d1b, design §4.7): before a leader
// tells the voters it resigns, every send that passed its gate must have
// ended, so RESIGN (which removes the lease-and-fence gap) can never let a
// send of the old leadership land under the new one.
//
// A send is counted from BEFORE the gate read it is judged on (Enter,
// Begin) until it ends (done). After the gate closes, every send that can
// still leave read the gate open earlier, so it was counted earlier; Drain
// waits until the count is zero, so it waits for each of them. Counted
// after the read, a send that read the gate open just before it closed
// would not be counted yet when Drain looked, and would leave after it.

var inflight struct {
	mu      sync.Mutex
	n       int
	lastEnd time.Time     // when the last counted send that went out ended
	idle    chan struct{} // closed when n falls to zero; nil while it is zero
}

// draining is above zero while a Drain runs: a send armed then is reset at
// once, one not yet started does not start, and a fenced context made then
// is already cancelled.
var draining atomic.Int32

// Enter counts a send in flight. Call it BEFORE reading the gate the send
// is judged on, and call done when the send has ended (answered, failed,
// or reset), or at once when it is not sent. done may be called more than
// once; only the first counts.
func Enter() (done func()) {
	enter()
	var once atomic.Bool
	return func() {
		if once.CompareAndSwap(false, true) {
			exit(true)
		}
	}
}

func enter() {
	inflight.mu.Lock()
	if inflight.n == 0 {
		inflight.idle = make(chan struct{})
	}
	inflight.n++
	inflight.mu.Unlock()
}

// exit ends one counted send; sent marks one that may have gone out, whose
// end moves the instant Drain settles from.
func exit(sent bool) {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	if inflight.n == 0 {
		panic("fence: a send ended that was never counted")
	}
	if sent {
		inflight.lastEnd = time.Now()
	}
	inflight.n--
	if inflight.n == 0 {
		close(inflight.idle)
		inflight.idle = nil
	}
}

// InFlight is how many counted sends have not ended.
func InFlight() int {
	inflight.mu.Lock()
	defer inflight.mu.Unlock()
	return inflight.n
}

// noDone is done for a send that was never counted.
func noDone() {}

// ErrDraining is the cause of a fenced context cancelled by a drain.
var ErrDraining = errors.New("fence: the outputs are being drained for a switchover; not sent")

// Drain waits until every counted send has ended, then settle more after
// the last of them that went out (m_send + d_net: its bytes are then
// delivered). Call it only once the gate is closed for good: it proves
// nothing about sends that may still read it open.
//
// While it runs, sends are cut short so a hung device cannot stretch it to
// a lease: every armed send is reset now (ResetAll), and one armed later
// is reset as it is armed; every fenced context is cancelled; and a send
// not yet started does not start (Started). It returns ctx's error if ctx
// ends first: the drain is then unproven, and its caller must not act as
// if it were.
func Drain(ctx context.Context, settle time.Duration) error {
	draining.Add(1)
	defer draining.Add(-1)
	if !mut("drain_no_reset") {
		ResetAll()
	}
	for !mut("drain_no_wait") {
		inflight.mu.Lock()
		ch := inflight.idle
		inflight.mu.Unlock()
		if ch == nil {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	inflight.mu.Lock()
	last := inflight.lastEnd
	inflight.mu.Unlock()
	if wait := time.Until(last.Add(settle)); wait > 0 && !mut("drain_no_settle") {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

// The armed sends and live fenced contexts, which ResetAll cuts short.
var registry struct {
	mu    sync.Mutex
	armed map[*armed]struct{}
	ctxs  map[*fencedCtx]struct{}
}

type armed struct {
	mu          sync.Mutex
	fired, done bool
	// drained: a drain reset it (ResetAll), not its deadline. Its stop
	// then reports false: a planned switchover's reset is no write cut.
	drained bool
	reset   func()
}

// fire resets the send unless it has ended or was reset already; byDrain
// marks a drain's reset.
func (a *armed) fire(byDrain bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done || a.fired {
		return
	}
	a.fired, a.drained = true, byDrain
	a.reset()
}

type fencedCtx struct{ cancel context.CancelCauseFunc }

// ResetAll resets every armed send now (Arm, ArmAbort), as its deadline
// would, and cancels every fenced context (Context): the sends in flight
// fail at once instead of running to their deadlines.
func ResetAll() {
	registry.mu.Lock()
	as := make([]*armed, 0, len(registry.armed))
	for a := range registry.armed {
		as = append(as, a)
	}
	cs := make([]*fencedCtx, 0, len(registry.ctxs))
	for c := range registry.ctxs {
		cs = append(cs, c)
	}
	registry.mu.Unlock()
	for _, a := range as {
		a.fire(true)
	}
	for _, c := range cs {
		c.cancel(ErrDraining)
	}
}

// register adds a to the armed sends; during a drain it is reset at once.
// The registry is taken before draining is read, as Drain raises draining
// before ResetAll takes it, so one of the two always resets a.
func register(a *armed) {
	registry.mu.Lock()
	if registry.armed == nil {
		registry.armed = map[*armed]struct{}{}
	}
	registry.armed[a] = struct{}{}
	registry.mu.Unlock()
	if draining.Load() > 0 && !mut("drain_no_reset") {
		a.fire(true)
	}
}

func unregister(a *armed) {
	registry.mu.Lock()
	delete(registry.armed, a)
	registry.mu.Unlock()
}

func registerCtx(c *fencedCtx) {
	registry.mu.Lock()
	if registry.ctxs == nil {
		registry.ctxs = map[*fencedCtx]struct{}{}
	}
	registry.ctxs[c] = struct{}{}
	registry.mu.Unlock()
	if draining.Load() > 0 && !mut("drain_no_reset") {
		c.cancel(ErrDraining)
	}
}

func unregisterCtx(c *fencedCtx) {
	registry.mu.Lock()
	delete(registry.ctxs, c)
	registry.mu.Unlock()
}
