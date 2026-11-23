package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// The runtime's side of a redundant pair's state stream (#710 S1b, design
// §4.1): what the leader's stream reads (LeadingTerm), the standby's
// persist of each image's retain section (CarryRetained, through the
// retain saver, the store's one writer), and the takeover's flush of the
// newest image before the store is read (SetTakeoverFlush).
//
// Experimental, like Image.

// ErrStaleImage refuses CarryRetained for an image that is not after the
// stream position the retain store records (retain.State.Stream): a file
// never goes back to an older leadership's, or an older image's, values.
var ErrStaleImage = errors.New("runtime: the image is older than what the retain store holds")

// ErrLeadingNoCarry refuses CarryRetained on a runtime that has taken over:
// a leader's store holds its own state, never an image's.
var ErrLeadingNoCarry = errors.New("runtime: this controller leads; it carries no image into its retain store")

// ErrRetainUnreadable is CarryRetained's error when this controller's own
// retain store cannot be read: the image is not in the file, but a hot
// takeover may still go on from it, loading its retain section in memory
// (#710 S2 Q3).
var ErrRetainUnreadable = errors.New("runtime: this controller's retain store could not be read")

// takeoverFlushWait bounds how long a takeover waits for the flush: a
// store that hangs must not hold the new leadership's first scan past it.
// A variable for tests.
var takeoverFlushWait = 2 * time.Second

// streamSide is the runtime's state for the stream.
type streamSide struct {
	flush atomic.Pointer[func() (*HotImage, error)]
	// The edge's flush, guarded by leadMu: ch while it runs, done once
	// this edge's flush ended, for the term it ran for (until the edge is
	// taken or abandoned).
	ch       chan flushResult
	chTerm   uint64 // the term the running flush is for
	done     bool
	doneTerm uint64
	since    time.Time
	// hot is the candidate this edge's flush handed over in time, nil
	// when none; hotWhy says why there is none (the flush failed or was
	// late). Taken by takeover, cleared by flushReset (hot.go).
	hot    *HotImage
	hotWhy string

	// report is the last takeover's (hot.go), memOnly that its retained
	// values are held only in memory until a save succeeds, onTakeover
	// the pair's hook for each report.
	reportMu   sync.Mutex
	report     TakeoverReport
	memOnly    atomic.Bool
	onTakeover atomic.Pointer[func(TakeoverReport)]

	// loadFailed: this leadership's takeover could not read the retain
	// store, so it runs on initial values (RetainLoadFailed). storeBad: a
	// read of the store failed since (a corrupt file is moved aside by
	// it, so the next read finds "nothing retained"), until a carry
	// writes it again; a takeover takes it as its own failed load.
	loadFailed, storeBad atomic.Bool

	carryOnce atomic.Pointer[chan *carryReq]
}

// flushResult is what a takeover flush returned.
type flushResult struct {
	hot *HotImage
	err error
}

// carryReq is one CarryRetained, handed to the retain saver.
type carryReq struct {
	lead      []byte
	term, seq uint64
	reply     chan error
	took      time.Duration // the store's save, set before reply
}

// SetTakeoverFlush registers the pair's takeover flush: f runs at the
// start of every takeover (a leadership edge that is not the same term
// resumed), off the scan, and the takeover reads the retain store only
// once it has returned (or takeoverFlushWait has passed, with a warning).
// The pair's f stops its stream follower, carries the newest image's
// retain section into the store (CarryRetained) and returns that image as
// its shadow held it (Shadow.Take), the candidate for a hot takeover
// (#710 S2): nil when it has none. A flush that fails (other than on an
// unreadable own store, ErrRetainUnreadable) or ends late gives no hot
// takeover. Call it before Run. A runtime with a flush registered also
// records, in every save it makes as leader, the term it leads in
// (retain.State.Stream).
func (r *Runtime) SetTakeoverFlush(f func() (*HotImage, error)) {
	if f == nil {
		r.strm.flush.Store(nil)
		return
	}
	r.strm.flush.Store(&f)
}

// LeadingTerm is the term of this runtime's last takeover (0 before the
// first, or without a Termed coordinator) and whether it leads in it now:
// its takeover ran and its gate has not closed since. A leader's stream
// sends an image only while leading is true at the term it stamps.
func (r *Runtime) LeadingTerm() (term uint64, leading bool) {
	r.leadMu.Lock()
	defer r.leadMu.Unlock()
	return r.ledTerm, r.leading
}

// flushReady answers, on a pending takeover in term, whether the
// takeover flush has run for it. The first call of an edge starts it;
// until it returns the gate says "do not scan", as inputsReady does. A
// term that moved while the edge was pending flushes again. Under leadMu.
func (r *Runtime) flushReady(term uint64) bool {
	f := r.strm.flush.Load()
	if f == nil || r.strm.done && (r.strm.doneTerm == term || mutRuntime("stream_flush_term_unkeyed")) || mutRuntime("stream_takeover_no_flush") {
		return true
	}
	if r.strm.done {
		r.strm.done = false // a new term since: its own flush
	}
	if r.strm.ch == nil {
		ch := make(chan flushResult, 1)
		fn := *f
		go func() {
			hot, err := fn()
			ch <- flushResult{hot, err}
		}()
		r.strm.ch, r.strm.chTerm, r.strm.since = ch, term, time.Now()
		r.strm.hot, r.strm.hotWhy = nil, ""
	}
	select {
	case res := <-r.strm.ch:
		r.strm.ch, r.strm.done, r.strm.doneTerm = nil, true, r.strm.chTerm
		r.strm.hot, r.strm.hotWhy = res.hot, ""
		if res.err != nil {
			slog.Warn("runtime: the takeover's flush of the newest state image failed; taking over on what the retain store holds", "error", res.err)
			if !errors.Is(res.err, ErrRetainUnreadable) && !mutRuntime("hot_failed_flush_installs") {
				// Only this controller's own store being unreadable keeps
				// the image hot (#710 S2 Q3): the file did not take it.
				r.strm.hot, r.strm.hotWhy = nil, "the takeover's flush of the newest image failed: "+res.err.Error()
			}
		}
		if r.strm.doneTerm != term && !mutRuntime("stream_flush_term_unkeyed") {
			r.strm.done = false // it ran for an earlier term: the next look flushes for this one
			r.strm.hot, r.strm.hotWhy = nil, ""
			return false
		}
		return true
	default:
	}
	if time.Since(r.strm.since) < takeoverFlushWait {
		return false
	}
	if mutRuntime("stream_flush_unbounded") {
		return false
	}
	slog.Warn("runtime: the takeover's flush of the newest state image has not finished; taking over on what the retain store holds", "waited", time.Since(r.strm.since).Round(time.Millisecond))
	late := r.strm.ch
	r.strm.ch, r.strm.done, r.strm.doneTerm = nil, true, r.strm.chTerm
	// Late: whatever it hands over afterwards is never installed (the
	// channel is dropped), and this takeover is cold.
	r.strm.hot, r.strm.hotWhy = nil, fmt.Sprintf("the takeover's flush of the newest image did not finish within %s", takeoverFlushWait)
	if mutRuntime("hot_late_flush_installs") {
		res := <-late // waits it out, and takes what it hands over
		r.strm.hot, r.strm.hotWhy = res.hot, ""
	}
	return r.strm.doneTerm == term
}

// flushReset forgets the edge's flush: this replica stands by, or the
// edge has been taken. A flush still running finishes on its own, and
// its candidate is never installed. Under leadMu.
func (r *Runtime) flushReset() {
	r.strm.ch, r.strm.done = nil, false
	r.strm.hot, r.strm.hotWhy = nil, ""
}

// RetainLoadFailed reports whether this runtime's last takeover could not
// read its retain store and runs on initial values. A pair's leader then
// streams nothing, so its standby's file keeps the last good values for
// an operator to fail over to (#710 S1b review F5).
func (r *Runtime) RetainLoadFailed() bool { return r.strm.loadFailed.Load() }

// StreamPosition is the state-stream position the retain store records
// (retain.State.Stream), nil when none or without a store. A standby takes
// no image below its term.
func (r *Runtime) StreamPosition() (*retain.StreamPos, error) {
	if r.retainStore == nil {
		return nil, nil
	}
	st, err := r.retainStore.Load()
	if err != nil {
		r.strm.storeBad.Store(true)
		slog.Error("runtime: the retain store could not be read; it is kept aside, and a takeover before the next image is kept runs on initial values", "store", r.retainStore.Kind(), "error", err)
		r.noteRetainError(err)
		return nil, err
	}
	return st.Stream, nil
}

func (r *Runtime) carryRequests() chan *carryReq {
	if p := r.strm.carryOnce.Load(); p != nil {
		return *p
	}
	ch := make(chan *carryReq)
	if r.strm.carryOnce.CompareAndSwap(nil, &ch) {
		return ch
	}
	return *r.strm.carryOnce.Load()
}

// CarryRetained persists an image's retain section (lead, retain.State as
// JSON, from the leader's RetainImage) into this standby's retain store:
// retain.Carry of what the store holds and lead, recorded at (term, seq).
// It runs in the retain saver, the store's one writer, and waits for the
// store's answer (with how long its save took, fsync included): nil once durable, or when the store already stands at
// exactly (term, seq). ErrStaleImage when the store stands after it,
// ErrLeadingNoCarry once this runtime has taken over, ErrNoRetainStore
// without a store. ctx bounds only the wait for the saver to take it.
// Call it while Run runs.
func (r *Runtime) CarryRetained(ctx context.Context, lead []byte, term, seq uint64) (time.Duration, error) {
	if r.retainStore == nil {
		return 0, ErrNoRetainStore
	}
	req := &carryReq{lead: lead, term: term, seq: seq, reply: make(chan error, 1)}
	select {
	case r.carryRequests() <- req:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	err := <-req.reply
	return req.took, err
}

// carry is CarryRetained in the saver goroutine: its answer, and how long
// the store's save took in req.took.
func (r *Runtime) carry(req *carryReq) error {
	r.leadMu.Lock()
	leading := r.leading
	r.leadMu.Unlock()
	if leading && !mutRuntime("stream_leader_carries") {
		return ErrLeadingNoCarry
	}
	own, err := r.retainStore.Load()
	if err != nil {
		r.strm.storeBad.Store(true)
		return fmt.Errorf("%w: %w", ErrRetainUnreadable, err)
	}
	if !own.Stream.Before(req.term, req.seq) && !mutRuntime("stream_old_image_carried") {
		if own.Stream.Term == req.term && own.Stream.Seq == req.seq {
			return nil
		}
		return ErrStaleImage
	}
	var lead retain.State
	if err := json.Unmarshal(req.lead, &lead); err != nil {
		return fmt.Errorf("the image's retain section: %w", err)
	}
	st := retain.Carry(own, lead)
	st.Stream = &retain.StreamPos{Term: req.term, Seq: req.seq}
	start := time.Now()
	err = r.retainStore.Save(st)
	req.took = time.Since(start)
	if err != nil {
		r.noteRetainError(err)
	} else {
		r.strm.storeBad.Store(false) // the file holds an image now
	}
	return err
}
