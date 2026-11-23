package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/internal/fence"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// ChannelIO is the transport behind a Channel: one peer, reached by one
// protocol. Where a driver moves the same tags every scan, a channel moves
// one variable when a program asks. A protocol package implements it
// without importing runtime (eip's CIP channel is the first).
//
// Read and Write may block on the network. The runtime calls them from the
// channel's own goroutine, one request at a time and never from a scan, and
// cancels ctx at the channel's timeout. variable is the peer's name for the
// value, in the protocol's own syntax (a Logix tag path on a CIP channel).
// Read returns the peer's value converted to shape's type; a value that does
// not fit is an error, never a partial value. elem is the local variable's
// declared elementary type (ir.Channels): "" for a structure or an
// undeclared width.
//
// The STATUS a block reports for an error is the error's own when it has a
// CommStatus() int method (the peer's status code, or an ir.Comm* value),
// ir.CommTimeout for a cancelled ctx, and ir.CommUnreachable otherwise.
type ChannelIO interface {
	Read(ctx context.Context, variable string, shape ir.Value, elem string) (ir.Value, error)
	Write(ctx context.Context, variable string, data ir.Value, elem string) error
	Close() error
}

// commStatus is the STATUS a READ or WRITE reports for err.
func commStatus(err error) int {
	if err == nil {
		return ir.CommOK
	}
	if errors.Is(err, fence.ErrGateClosed) {
		return ir.CommLost // refused by the pair's gate mid-write, as do() refuses one
	}
	var s interface{ CommStatus() int }
	if errors.As(err, &s) && s.CommStatus() != ir.CommOK {
		return s.CommStatus()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ir.CommTimeout
	}
	return ir.CommUnreachable
}

// Channel is one communication channel the programs' READ and WRITE blocks
// run on (IEC 61131-5; docs/design/logix-authoring.md, "MSG as IEC 61131-5
// READ/WRITE"). Name is what a block's ID names.
type Channel struct {
	Name    string
	Channel ChannelIO
	// Timeout bounds one request (default DefaultChannelTimeout).
	Timeout time.Duration
	// Queue is how many requests may wait before a READ or WRITE is
	// refused with ir.CommQueueFull (default DefaultChannelQueue).
	Queue int
}

const (
	DefaultChannelTimeout = 5 * time.Second
	DefaultChannelQueue   = 64
	// finishedTTL is how long a finished request waits to be polled. A
	// block polls on its next call, so only a block that is never called
	// again (deleted by an online edit) leaves one behind.
	finishedTTL = 10 * time.Minute
)

// commHub serves ir.Channels for every task's scan. A post only enqueues
// and a poll only looks a ticket up, so the scan never waits on a peer:
// each channel's worker goroutine does the network I/O, one request at a
// time, in the order posted.
type commHub struct {
	byName map[string]*commWorker // by ir.NameKey: names match case-insensitively
	next   atomic.Int64

	mu       sync.Mutex
	inflight map[int64]struct{}
	finished map[int64]finishedReq

	// gate is the runtime's write gate (Coordinator.IsLeader), nil when
	// uncoordinated. term names the current leadership (Termed.Term), nil
	// when the coordinator has none; gen is the fallback then: takeover
	// bumps it. A write leaves only while the gate is open and under the
	// leadership that posted it, so a replica that stepped down never
	// drains its queue onto a peer the next leader is writing (see do).
	gate func() bool
	term func() uint64
	gen  atomic.Uint64
	// send, when set, is a pair's gate with its send budget and term, read
	// in one look (design §3.4): a write is checked on it, runs to its
	// budget as a deadline on a fenced context, and carries it so a
	// transport that splits one write into requests checks before each
	// (fence.Check).
	send nio.SendGate

	stop     chan struct{}
	stopped  bool            // under mu: a post after close is refused
	base     context.Context // cancelled by close, so no request outlives it
	cancel   context.CancelFunc
	stopOnce sync.Once
	started  sync.Once
	wg       sync.WaitGroup
}

type commWorker struct {
	name    string // the channel's name, for the writes-cut counter
	ch      ChannelIO
	timeout time.Duration
	q       chan commReq
}

type commReq struct {
	ticket   int64
	lead     uint64 // the leadership that posted it (commHub.leadership)
	write    bool
	variable string
	val      ir.Value
	elem     string
}

type finishedReq struct {
	res ir.CommResult
	at  time.Time
}

func newCommHub(chs []Channel) *commHub {
	h := &commHub{
		byName:   make(map[string]*commWorker, len(chs)),
		inflight: map[int64]struct{}{},
		finished: map[int64]finishedReq{},
		stop:     make(chan struct{}),
	}
	h.base, h.cancel = context.WithCancel(context.Background())
	// Tickets start at the wall clock in nanoseconds, so a ticket a
	// retained block instance carries over from an earlier process can
	// never name a request of this one: it polls as lost.
	h.next.Store(time.Now().UnixNano())
	for _, c := range chs {
		w := &commWorker{name: c.Name, ch: c.Channel, timeout: c.Timeout, q: make(chan commReq, c.Queue)}
		if w.timeout <= 0 {
			w.timeout = DefaultChannelTimeout
		}
		if c.Queue <= 0 {
			w.q = make(chan commReq, DefaultChannelQueue)
		}
		h.byName[ir.NameKey(c.Name)] = w
	}
	return h
}

// setGate makes every write wait on gate: false drops it as lost. term,
// when not nil, names the leadership (Termed.Term): a write posted under
// another term is dropped too. Call before the first post.
func (h *commHub) setGate(gate func() bool, term func() uint64) {
	if h != nil {
		h.gate, h.term = gate, term
	}
}

// setSendGate installs a pair's send gate (see send). Call before the
// first post.
func (h *commHub) setSendGate(g nio.SendGate) {
	if h != nil {
		h.send = g
	}
}

// leadership names the current leadership: the coordinator's term when it
// has one, else the count of takeovers (newLeadership). The term moves the
// moment a new lease is won, before the runtime's takeover runs (it waits
// for the next scan and for live inputs), so a write queued in the old
// term cannot slip out in that window.
func (h *commHub) leadership() uint64 {
	if h.term != nil {
		return h.term()
	}
	return h.gen.Load()
}

// newLeadership marks a takeover: writes posted before it are dropped as
// lost instead of sent, since the leadership that computed them is over.
// With a term (setGate) it is redundant, and harmless.
func (h *commHub) newLeadership() {
	if h != nil {
		h.gen.Add(1)
	}
}

func (h *commHub) start() {
	h.started.Do(func() {
		for _, w := range h.byName {
			h.wg.Add(1)
			go h.serve(w)
		}
	})
}

// close stops the workers, cancelling any request in flight, and closes
// every channel. A request still queued finishes as lost, and a post after
// close is refused, so no block waits on a channel that is gone.
func (h *commHub) close() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		h.stopped = true
		h.mu.Unlock()
		close(h.stop)
		h.cancel()
	})
	h.wg.Wait()
	for _, w := range h.byName {
		for drained := false; !drained; {
			select {
			case req := <-w.q:
				h.finish(req.ticket, ir.CommResult{Status: ir.CommLost})
			default:
				drained = true
			}
		}
		_ = w.ch.Close()
	}
}

func (h *commHub) serve(w *commWorker) {
	defer h.wg.Done()
	for {
		select {
		case <-h.stop:
			return
		default:
		}
		select {
		case <-h.stop:
			return
		case req := <-w.q:
			h.finish(req.ticket, h.do(w, req))
		}
	}
}

// do runs one request on w's channel. A write is checked against the gate
// and the leadership right before it is sent, not when it was posted: a
// queued write may wait seconds behind another, and by then this replica
// may have stepped down. Reads are not gated; they change nothing on the
// peer.
//
// The GATE is read before the TERM, and the order matters. Read the other
// way, a freeze longer than a lease between the two reads (the term still
// the one that posted, then the lease lost and won back in a new term)
// finds an open gate that belongs to the new term, and an old term's write
// goes out under it. In this order an open gate followed by an unchanged
// term pins the gate to the posting term: the term only grows, the write
// was stamped before the gate was read, and it is still the same after.
func (h *commHub) do(w *commWorker, req commReq) ir.CommResult {
	var deadline time.Time
	if req.write {
		if h.send != nil {
			// A pair (design §3.4): gate, budget and term in ONE read, so
			// they cannot straddle a renewal or a lease lost and won back.
			dl, term, ok := fence.Begin(h.send, time.Now())
			if !ok || !fence.Started(dl, time.Now()) {
				return ir.CommResult{Status: ir.CommLost}
			}
			if h.term != nil && term != req.lead {
				return ir.CommResult{Status: ir.CommLost}
			}
			deadline = dl
		} else {
			if h.gate != nil && !h.gate() {
				return ir.CommResult{Status: ir.CommLost}
			}
		}
		if req.lead != h.leadership() {
			return ir.CommResult{Status: ir.CommLost}
		}
	}
	ctx, cancel := context.WithTimeout(h.base, w.timeout)
	defer cancel()
	if req.write {
		if h.send != nil {
			var fcancel context.CancelFunc
			ctx, fcancel = fence.Context(ctx, deadline)
			defer fcancel()
			ctx = fence.WithGate(ctx, h.send, req.lead)
		}
		werr := w.ch.Write(ctx, req.variable, req.val, req.elem)
		if werr != nil && !deadline.IsZero() && !time.Now().Before(deadline) &&
			!errors.Is(werr, fence.ErrGateClosed) && !errors.Is(werr, fence.ErrNotStarted) {
			fence.Cut(w.name) // sent, and its deadline passed: one write cut
		}
		return ir.CommResult{Status: commStatus(werr)}
	}
	v, err := w.ch.Read(ctx, req.variable, req.val, req.elem)
	if err != nil {
		return ir.CommResult{Status: commStatus(err)}
	}
	return ir.CommResult{Value: v}
}

func (h *commHub) finish(ticket int64, res ir.CommResult) {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.inflight, ticket)
	h.finished[ticket] = finishedReq{res: res, at: now}
	for t, f := range h.finished {
		if now.Sub(f.at) > finishedTTL {
			delete(h.finished, t)
		}
	}
}

func (h *commHub) post(channel string, write bool, variable string, v ir.Value, elem string) (int64, int) {
	if h == nil {
		return 0, ir.CommNoChannel
	}
	w, ok := h.byName[ir.NameKey(channel)]
	if !ok {
		return 0, ir.CommNoChannel
	}
	h.start()
	// Under mu, so close either sees this request in the queue (and
	// finishes it) or this post sees close.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return 0, ir.CommUnreachable
	}
	ticket := h.next.Add(1)
	select {
	case w.q <- commReq{ticket: ticket, lead: h.leadership(), write: write, variable: variable, val: v, elem: elem}:
		h.inflight[ticket] = struct{}{}
		return ticket, ir.CommOK
	default:
		return 0, ir.CommQueueFull
	}
}

// PostRead, PostWrite and Poll are ir.Channels.
func (h *commHub) PostRead(channel, variable string, shape ir.Value, elem string) (int64, int) {
	return h.post(channel, false, variable, shape, elem)
}

func (h *commHub) PostWrite(channel, variable string, data ir.Value, elem string) (int64, int) {
	return h.post(channel, true, variable, data, elem)
}

func (h *commHub) Poll(ticket int64) (ir.CommResult, bool) {
	if h == nil {
		return ir.CommResult{Status: ir.CommLost}, true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if f, ok := h.finished[ticket]; ok {
		delete(h.finished, ticket)
		return f.res, true
	}
	if _, ok := h.inflight[ticket]; ok {
		return ir.CommResult{}, false
	}
	return ir.CommResult{Status: ir.CommLost}, true
}

// UnknownChannels lists the channel names READ and WRITE calls name as a
// constant ID that no Channel declares, each once, in program order. A
// program that calls one would fail every request with ir.CommNoChannel;
// `naut check` reports them instead.
func (r *Runtime) UnknownChannels() []string {
	progs := []*Program{r.prog}
	for _, tr := range r.tasks {
		progs = append(progs, tr.prog)
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range progs {
		p.mu.Lock()
		var body []ir.Stmt
		if p.prog != nil {
			body = p.prog.Body
			// A READ or WRITE inside a user FUNCTION_BLOCK counts too.
			for _, fb := range p.prog.UserFBs {
				if fb.Body != nil {
					body = append(append([]ir.Stmt(nil), body...), fb.Body.Body...)
				}
			}
		}
		p.mu.Unlock()
		for _, c := range ir.CommCalls(body) {
			k := ir.NameKey(c.ID)
			if seen[k] {
				continue
			}
			seen[k] = true
			if r.tags.comm == nil || r.tags.comm.byName[k] == nil {
				out = append(out, c.ID)
			}
		}
	}
	return out
}
