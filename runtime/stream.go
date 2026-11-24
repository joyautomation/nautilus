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
	// hadImage: the flush failed or ended late, so it hands over no
	// candidate, though the standby held an image (or may have).
	hadImage bool

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

	// hb is the edge's bad-retain hand-back (handback.go). Under leadMu
	// but for its hook.
	hb handBackState
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
//
// It reads a snapshot published under leadMu, not leadMu itself: the
// takeover flush calls it while the edge's tick holds leadMu waiting for
// that flush (#710 d1e, Fable).
func (r *Runtime) LeadingTerm() (term uint64, leading bool) {
	if mutRuntime("leading_term_locks") {
		r.leadMu.Lock()
		defer r.leadMu.Unlock()
		return r.ledTerm, r.leading
	}
	v := r.leadSnap.Load()
	return v >> 1, v&1 == 1
}

// publishLead publishes ledTerm and leading for LeadingTerm. Under leadMu,
// after every change of either.
func (r *Runtime) publishLead() {
	v := r.ledTerm << 1
	if r.leading {
		v |= 1
	}
	r.leadSnap.Store(v)
}

// sameTickWait is how long a takeover edge's tick waits for a step it
// just started (the image flush, the operator-intent catch-up) before
// leaving it to the next tick (#710 d1e). Both take well under a
// millisecond on the bench, and a tick on the edge scans nothing
// anyway: without the wait each
// step cost a whole tick of the fastest task, measured as most of a
// switchover's standby side.
var sameTickWait = 15 * time.Millisecond

// flushStartedHook, when a test sets it, runs on the edge's tick right
// after the flush started and before the tick waits for it (under leadMu).
var flushStartedHook func()

// waitStep waits at most d for a takeover step's result on ch, under
// leadMu. Meanwhile the other loops' ticks answer "do not scan" at once
// (edgeWait, gateEdge) instead of queueing on leadMu: none of them would
// scan before the edge is taken anyway, and a fast task would otherwise
// miss its slots for the wait.
func (r *Runtime) waitStep(ch <-chan flushResult, d time.Duration) (flushResult, bool) {
	r.edgeWait.Store(true)
	r.flushWait.Store(true)
	defer r.edgeWait.Store(false)
	defer r.flushWait.Store(false)
	return waitOn(ch, d)
}

// waitErr is waitStep for the intent catch-up's result.
func (r *Runtime) waitErr(ch <-chan error, d time.Duration) (error, bool) {
	r.edgeWait.Store(true)
	defer r.edgeWait.Store(false)
	return waitOn(ch, d)
}

func waitOn[T any](ch <-chan T, d time.Duration) (T, bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-t.C:
		var zero T
		return zero, false
	}
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
	started := false
	if r.strm.ch == nil {
		ch := make(chan flushResult, 1)
		fn := *f
		if !mutRuntime("flush_next_tick") && !mutRuntime("flush_wait_after_start") {
			// The flags go up BEFORE the flush runs, not when the tick
			// starts waiting: a save that comes up as the flush starts
			// (one the flush itself causes, or the saver's tick) must find
			// them, or it parks on leadMu and the flush's carry waits the
			// whole wait behind it (CI on main after #1359: the window
			// between the goroutine's start and the wait was not tiny on a
			// loaded runner). waitStep takes them down.
			r.edgeWait.Store(true)
			r.flushWait.Store(true)
		}
		go func() {
			hot, err := fn()
			ch <- flushResult{hot, err}
		}()
		r.strm.ch, r.strm.chTerm, r.strm.since = ch, term, time.Now()
		r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, "", false
		started = true
		if h := flushStartedHook; h != nil {
			h() // a test's stand-in for the tick being descheduled here
		}
	}
	var res flushResult
	got := false
	select {
	case res = <-r.strm.ch:
		got = true
	default:
		if started && !mutRuntime("flush_next_tick") {
			res, got = r.waitStep(r.strm.ch, min(sameTickWait, takeoverFlushWait))
		}
	}
	if started {
		// Done with this edge's wait (a result already in, or waited for).
		r.edgeWait.Store(false)
		r.flushWait.Store(false)
	}
	if got {
		r.strm.ch, r.strm.done, r.strm.doneTerm = nil, true, r.strm.chTerm
		r.strm.hot, r.strm.hotWhy = res.hot, ""
		if res.err != nil {
			slog.Warn("runtime: the takeover's flush of the newest state image failed; taking over on what the retain store holds", "error", res.err)
			if !errors.Is(res.err, ErrRetainUnreadable) && !mutRuntime("hot_failed_flush_installs") {
				// Only this controller's own store being unreadable keeps
				// the image hot (#710 S2 Q3): the file did not take it.
				r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, "the takeover's flush of the newest image failed: "+res.err.Error(), res.hot != nil
			}
		}
		if r.strm.doneTerm != term && !mutRuntime("stream_flush_term_unkeyed") {
			r.strm.done = false // it ran for an earlier term: the next look flushes for this one
			r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, "", false
			return false
		}
		return true
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
	r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, fmt.Sprintf("the takeover's flush of the newest image did not finish within %s", takeoverFlushWait), true
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
	r.strm.hot, r.strm.hotWhy, r.strm.hadImage = nil, "", false
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
	var leading bool
	wait := r.flushWait.Load()
	if mutRuntime("carry_snapshot_any_wait") {
		wait = r.edgeWait.Load()
	}
	if wait && !mutRuntime("leading_term_locks") {
		// A takeover edge's tick holds leadMu, waiting for its flush, which
		// is what carries here: its takeover's store load has not begun,
		// so the snapshot is as good as the lock (#710 d1e, Fable M1). Not
		// during the catch-up's wait: a carry then is a late flush's, and
		// the edge may take over on an earlier read of the store (Fable).
		_, leading = r.LeadingTerm()
	} else {
		r.leadMu.Lock()
		leading = r.leading
		r.leadMu.Unlock()
	}
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
	st.Stream = &retain.StreamPos{Term: req.term, Seq: req.seq, At: time.Now().UTC().Truncate(time.Millisecond)}
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
