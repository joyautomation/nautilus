package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

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
// not fit is an error, never a partial value.
//
// The STATUS a block reports for an error is the error's own when it has a
// CommStatus() int method (the peer's status code, or an ir.Comm* value),
// ir.CommTimeout for a cancelled ctx, and ir.CommUnreachable otherwise.
type ChannelIO interface {
	Read(ctx context.Context, variable string, shape ir.Value) (ir.Value, error)
	Write(ctx context.Context, variable string, data ir.Value) error
	Close() error
}

// commStatus is the STATUS a READ or WRITE reports for err.
func commStatus(err error) int {
	if err == nil {
		return ir.CommOK
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
	byName map[string]*commWorker
	next   atomic.Int64

	mu       sync.Mutex
	inflight map[int64]struct{}
	finished map[int64]finishedReq

	stop     chan struct{}
	base     context.Context // cancelled by close, so no request outlives it
	cancel   context.CancelFunc
	stopOnce sync.Once
	started  sync.Once
	wg       sync.WaitGroup
}

type commWorker struct {
	ch      ChannelIO
	timeout time.Duration
	q       chan commReq
}

type commReq struct {
	ticket   int64
	write    bool
	variable string
	val      ir.Value
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
		w := &commWorker{ch: c.Channel, timeout: c.Timeout, q: make(chan commReq, c.Queue)}
		if w.timeout <= 0 {
			w.timeout = DefaultChannelTimeout
		}
		if c.Queue <= 0 {
			w.q = make(chan commReq, DefaultChannelQueue)
		}
		h.byName[c.Name] = w
	}
	return h
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
// every channel.
func (h *commHub) close() {
	h.stopOnce.Do(func() { close(h.stop); h.cancel() })
	h.wg.Wait()
	for _, w := range h.byName {
		_ = w.ch.Close()
	}
}

func (h *commHub) serve(w *commWorker) {
	defer h.wg.Done()
	for {
		select {
		case <-h.stop:
			return
		case req := <-w.q:
			h.finish(req.ticket, w.do(h.base, req))
		}
	}
}

func (w *commWorker) do(base context.Context, req commReq) ir.CommResult {
	ctx, cancel := context.WithTimeout(base, w.timeout)
	defer cancel()
	if req.write {
		return ir.CommResult{Status: commStatus(w.ch.Write(ctx, req.variable, req.val))}
	}
	v, err := w.ch.Read(ctx, req.variable, req.val)
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

func (h *commHub) post(channel string, write bool, variable string, v ir.Value) (int64, int) {
	if h == nil {
		return 0, ir.CommNoChannel
	}
	w, ok := h.byName[channel]
	if !ok {
		return 0, ir.CommNoChannel
	}
	h.start()
	ticket := h.next.Add(1)
	h.mu.Lock()
	h.inflight[ticket] = struct{}{}
	h.mu.Unlock()
	select {
	case w.q <- commReq{ticket: ticket, write: write, variable: variable, val: v}:
		return ticket, ir.CommOK
	default:
		h.mu.Lock()
		delete(h.inflight, ticket)
		h.mu.Unlock()
		return 0, ir.CommQueueFull
	}
}

// PostRead, PostWrite and Poll are ir.Channels.
func (h *commHub) PostRead(channel, variable string, shape ir.Value) (int64, int) {
	return h.post(channel, false, variable, shape)
}

func (h *commHub) PostWrite(channel, variable string, data ir.Value) (int64, int) {
	return h.post(channel, true, variable, data)
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
		}
		p.mu.Unlock()
		for _, c := range ir.CommCalls(body) {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			if r.tags.comm == nil || r.tags.comm.byName[c.ID] == nil {
				out = append(out, c.ID)
			}
		}
	}
	return out
}
