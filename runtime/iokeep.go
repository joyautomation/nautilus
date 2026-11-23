package runtime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// IOKeeper is a live I/O configuration kept in the retain store (#706): the
// project's IOConfig. The runtime only carries it: retainState asks it for
// what to keep, and a leadership edge has it re-apply what the store keeps
// before the first scan runs logic or hands a driver an output.
type IOKeeper interface {
	// RetainedIO is what to keep, nil when what runs is the manifest.
	RetainedIO() *retain.IOState
	// IOCurrent reports whether what runs is already what st keeps (a
	// standby that followed the store while it stood by): no re-apply, so
	// no hold.
	IOCurrent(st *retain.IOState) bool
	// RestoreIO makes what runs what st keeps, before ctx ends. It is
	// called off the scan; the edge holds the gate until it returns.
	RestoreIO(ctx context.Context, st *retain.IOState) error
	// IOUnreadable says the edge could not read the store (after its
	// retries): nothing kept can be re-applied, and nothing applied from
	// here can be promised kept.
	IOUnreadable(err error)
	// IOSaved says the store now holds st, the IO of a save that
	// succeeded (what RetainedIO last returned).
	IOSaved(st *retain.IOState)
}

// DefaultTakeoverIOWait bounds the hold a leadership edge makes while the
// kept I/O configuration is re-applied. A standby that follows the store
// re-applies nothing at its edge; the hold is for a process start, or an
// apply the last leader made just before it failed.
const DefaultTakeoverIOWait = 10 * time.Second

// takeoverIOWait is DefaultTakeoverIOWait; a test shortens it.
var takeoverIOWait = DefaultTakeoverIOWait

// ioLoadTries and ioLoadRetry bound the edge's retries of a store read
// that failed: a store that cannot be read at an edge is reported to the
// keeper (IOUnreadable), never left to block it.
var (
	ioLoadTries = 3
	ioLoadRetry = 300 * time.Millisecond
)

// ioGrace is how long past takeoverIOWait the edge still waits for the
// keeper to return: its restore ends at the deadline and records what it
// holds on the way out. A keeper that never returns is not waited for.
var ioGrace = 2 * time.Second

// ErrNotLeading refuses SaveRetained on a replica that does not lead (or
// has not finished taking over): only the leader writes the store.
var ErrNotLeading = errors.New("this controller does not lead; only the leader saves retained state")

// ErrNoRetainStore refuses SaveRetained on a runtime with no retain store
// (a project without a retain: block).
var ErrNoRetainStore = errors.New("no retain store: the project has no retain: block")

// ioKeep is the runtime's side of an IOKeeper.
type ioKeep struct {
	mu     sync.Mutex
	keeper IOKeeper

	// The edge's restore, guarded by leadMu: passed once the edge may go on
	// (until the next edge), done while a restore runs.
	passed bool
	done   chan ioResult
	since  time.Time
	// edge is the store the edge read, for takeover's own load: read once
	// per edge, not twice. Guarded by leadMu.
	edge *retain.State
	// loadErr: this edge's read of the store failed (a corrupt file is
	// moved aside by it, so a retry then reads "nothing retained"): the
	// takeover runs on initial values (RetainLoadFailed). Under leadMu.
	loadErr bool

	saveOnce sync.Once
	saveReq  chan chan error
}

// SetIO registers the project's live I/O configuration, so the retain
// store keeps what an apply changed and a leadership edge re-applies it.
// Call it before Run, as SetAlarms.
func (r *Runtime) SetIO(k IOKeeper) {
	r.iok.mu.Lock()
	r.iok.keeper = k
	r.iok.mu.Unlock()
}

func (r *Runtime) ioKeeper() IOKeeper {
	r.iok.mu.Lock()
	defer r.iok.mu.Unlock()
	return r.iok.keeper
}

// ioReset forgets the edge's restore: this replica stands by, or the edge
// has been taken. A restore still running finishes on its own. Under leadMu.
func (r *Runtime) ioReset() {
	r.iok.passed, r.iok.done, r.iok.edge, r.iok.loadErr = false, nil, nil, false
}

// loadStore is the retain store's state: what this edge's ioReady already
// read, else a fresh load. Under leadMu (takeover).
func (r *Runtime) loadStore() (retain.State, error) {
	if st := r.iok.edge; st != nil {
		r.iok.edge = nil
		return *st, nil
	}
	return r.retainStore.Load()
}

// ioResult is an edge's restore, as it ended: the store it read (for
// takeover's own load), and its error.
type ioResult struct {
	st  *retain.State
	err error
}

// ioReady answers, on a leadership edge that takes over, whether the kept
// I/O configuration runs: when the keeper already runs what the store
// keeps (the steady case for a standby that followed the store), at once;
// otherwise it starts the keeper's restore and answers false, so the scan
// gates out (no logic, no outputs) and the loop keeps ticking, until the
// restore is done or takeoverIOWait (and ioGrace) has passed. Past the
// wait it goes on with a warning: the keeper holds the outputs of what it
// has not re-applied (its contract), and the rest scans. A store read that
// fails is retried off the scan (ioLoadTries); one that keeps failing is
// reported to the keeper (IOUnreadable). Under leadMu.
func (r *Runtime) ioReady() bool {
	if r.iok.passed {
		return true
	}
	k := r.ioKeeper()
	if k == nil || r.retainStore == nil {
		r.iok.passed = true
		return true
	}
	if r.iok.done == nil {
		done := make(chan ioResult, 1)
		ctx, cancel := context.WithTimeout(context.Background(), takeoverIOWait)
		restore := func(st retain.State) {
			defer cancel()
			done <- ioResult{&st, k.RestoreIO(ctx, st.IO)}
		}
		st, err := r.retainStore.Load()
		switch {
		case err == nil && k.IOCurrent(st.IO):
			cancel()
			r.iok.edge = &st // takeover loads it from here
			r.iok.passed = true
			return true
		case err == nil:
			r.iok.edge = &st
			go restore(st)
		default:
			r.iok.loadErr = true
			go func() {
				for i := 0; i < ioLoadTries; i++ {
					time.Sleep(ioLoadRetry)
					if st, err = r.retainStore.Load(); err == nil {
						break
					}
				}
				if err != nil {
					cancel()
					k.IOUnreadable(err)
					done <- ioResult{nil, fmt.Errorf("the retain store could not be read: %w", err)}
					return
				}
				if k.IOCurrent(st.IO) {
					cancel()
					done <- ioResult{&st, nil}
					return
				}
				restore(st)
			}()
		}
		r.iok.done, r.iok.since = done, time.Now()
		slog.Info("runtime: leading; re-applying the kept I/O configuration before the first scan", "up_to", takeoverIOWait)
		return false
	}
	select {
	case res := <-r.iok.done:
		r.iok.done, r.iok.passed = nil, true
		if res.st != nil && r.iok.edge == nil {
			r.iok.edge = res.st
		}
		if res.err != nil {
			slog.Error("runtime: the kept I/O configuration was not re-applied; what it changes holds its outputs", "error", res.err)
			r.noteRetainError(res.err)
		} else {
			slog.Info("runtime: kept I/O configuration re-applied", "took", time.Since(r.iok.since).Round(time.Millisecond))
		}
		return true
	default:
	}
	if time.Since(r.iok.since) < takeoverIOWait+ioGrace {
		return false
	}
	slog.Warn("runtime: the kept I/O configuration is still being re-applied; scanning, with what it changes holding its outputs", "waited", time.Since(r.iok.since).Round(time.Millisecond))
	r.iok.done, r.iok.passed = nil, true
	return true
}

func (r *Runtime) saveRequests() chan chan error {
	r.iok.saveOnce.Do(func() { r.iok.saveReq = make(chan chan error) })
	return r.iok.saveReq
}

// SaveRetained saves the retained state now, through the saver, and
// returns once the store holds it: nil also when nothing changed since the
// last save. It is for a caller that must not answer before its change is
// durable (an API handler: a live I/O apply, #706; a substitute, #707).
//
// The result is definite. ctx bounds only the wait for the saver to take
// the request (ctx's error: nothing was written by this call); once taken,
// SaveRetained waits for the store's own answer, which the store bounds
// (a ConfigMap request times out, and a write that may have landed is
// read back). It returns ErrNotLeading on a replica that does not lead (or
// has not finished taking over), ErrNoRetainStore without a store, and the
// store's error when the save fails. A store that another writer changed
// since this replica read it (retain.ErrConflict, a deposed leader's late
// write) is written over once: this replica leads.
func (r *Runtime) SaveRetained(ctx context.Context) error {
	if r.retainStore == nil {
		return ErrNoRetainStore
	}
	req := make(chan error, 1)
	select {
	case r.saveRequests() <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	return <-req
}
