//go:build !js

package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// A planned switchover's switching state (#710 d1c, redundancy design
// §4.7). Before the leader hands its lead to a current standby it stops
// where the standby can carry on without a seam:
//
//  1. BeginSwitch: from here on nothing from outside the programs lands in
//     the store (Tags.Write*, forces, substitutes, online edits:
//     ErrSwitching), and each task finishes the scan it is in (its logic
//     and its output push) and then scans no more. The loops keep
//     ticking, so liveness sees them alive.
//  2. FinalImage: one off-scan image of that state, the last completed
//     scans, for the standby to acknowledge.
//  3. The caller resigns the lead (leader.Quorum.Resign): this controller
//     stands down and follows its peer as any standby does. Its next
//     leadership's takeover edge ends the switching state (gateEdge). A
//     gate that closes for a tick and reopens in the same term does not:
//     the switchover resumes with the scans still stopped.
//
// EndSwitch ends it without a stand-down (an abort before the resign):
// the scans go on in the same leadership, nothing lost. Images otherwise
// come only from the end of a scan while the gate is open (S1b); the
// final image is the one exception, and it is still taken before the gate
// shuts.

// switchState is the runtime's side of it.
type switchState struct {
	mu sync.Mutex // BeginSwitch and EndSwitch, one at a time
	// term is the leadership switching over, 0 when none.
	term atomic.Uint64
	// inScan counts the scans past their gate and not yet done. A scan
	// adds itself BEFORE it reads term, and BeginSwitch sets term before it
	// reads inScan, so one of the two sees the other.
	inScan atomic.Int64
}

// ErrNotSwitching: FinalImage outside a switchover of that leadership.
var ErrNotSwitching = errors.New("runtime: no switchover of this leadership is under way")

// BeginSwitch starts the switching state for term, which this runtime
// leads in, and returns once every scan under way has ended (each task's
// last scan has pushed its outputs). It refuses forces (a takeover drops
// them, so a standby would run without them) and a takeover hold (the
// outputs it holds are not the program's to pass on). ctx bounds the wait
// for the scans: when it ends first the switching state ends too, and the
// scans go on.
func (r *Runtime) BeginSwitch(ctx context.Context, term uint64) error {
	r.sw.mu.Lock()
	defer r.sw.mu.Unlock()
	if t, lead := r.LeadingTerm(); !lead || t != term || term == 0 {
		return fmt.Errorf("switchover: this controller does not lead in term %d", term)
	}
	if r.sw.term.Load() != 0 {
		return errors.New("switchover: one is already under way")
	}
	if r.holdAge() >= 0 && !mutRuntime("switch_ignores_hold") {
		return errors.New("switchover: a takeover hold is holding the outputs: release it first (the program's release tag, or naut redundancy release-hold)")
	}
	if err := r.forcesRefuse(); err != nil {
		return err
	}
	// The leadership is read again, and the flag set, under leadMu: a new
	// leadership's takeover edge, which clears the flag (switchEnded) and
	// then publishes its term, cannot land between the two (Fable, d1e).
	if !mutRuntime("beginswitch_reads_snapshot") {
		r.leadMu.Lock()
		if !r.leading || r.ledTerm != term {
			r.leadMu.Unlock()
			return fmt.Errorf("switchover: this controller does not lead in term %d", term)
		}
	}
	r.tags.mu.Lock() // a write that holds the lock now lands before the flag
	r.tags.switching.Store(true)
	r.sw.term.Store(term)
	r.tags.mu.Unlock()
	if !mutRuntime("beginswitch_reads_snapshot") {
		r.leadMu.Unlock()
	}
	// An online edit or SFC command looks at the flag under its program's
	// lock, and the final image copies each frame under that lock: one
	// under way when the flag was set is in the image, a later one is
	// refused. No barrier here: a scan holds that lock for its whole run,
	// and the wait below is the bounded one (Fable S4).
	// A force that took the store's lock between the look above and the
	// flag: refused all the same.
	if err := r.forcesRefuse(); err != nil {
		r.switchEnded()
		return err
	}
	if mutRuntime("switch_no_wait") {
		return nil
	}
	for r.sw.inScan.Load() != 0 {
		if ctx.Err() != nil {
			r.switchEnded()
			return fmt.Errorf("switchover: a scan did not finish in time; the scans go on: %w", ctx.Err())
		}
		time.Sleep(200 * time.Microsecond)
	}
	return nil
}

func (r *Runtime) forcesRefuse() error {
	if n := r.tags.ForceCount(); n > 0 && !mutRuntime("switch_ignores_forces") {
		return fmt.Errorf("switchover: %d force(s) held: a takeover drops forces, so remove them first", n)
	}
	return nil
}

// EndSwitch ends the switching state without a stand-down (an abort
// before the resign): writes land again and the scans go on, in the same
// leadership.
func (r *Runtime) EndSwitch() {
	r.sw.mu.Lock()
	defer r.sw.mu.Unlock()
	if !mutRuntime("abort_keeps_switching") {
		r.switchEnded()
	}
}

// Switching reports the leadership a switchover is under way in, 0 when
// none.
func (r *Runtime) Switching() uint64 { return r.sw.term.Load() }

// switchEnded clears the switching state. Called by EndSwitch, a refused
// BeginSwitch, and a new leadership's takeover edge (under leadMu).
func (r *Runtime) switchEnded() {
	r.sw.term.Store(0)
	r.tags.switching.Store(false)
}

// scanEnter is a scan's step past its gate: false while switching (the
// scan does nothing). A true answer is paired with scanExit.
func (r *Runtime) scanEnter() bool {
	r.sw.inScan.Add(1)
	if r.sw.term.Load() != 0 && !mutRuntime("switch_scans_on") {
		r.sw.inScan.Add(-1)
		return false
	}
	return true
}

func (r *Runtime) scanExit() { r.sw.inScan.Add(-1) }

// FinalImage is the switchover's final image of term (§4.7): every
// task's frame and the tags as the last completed scans left them, the
// retain section, numbered after every image this runtime streamed. Only
// while switching in term, after BeginSwitch returned.
func (r *Runtime) FinalImage(term uint64) (*Image, error) {
	if term == 0 || r.sw.term.Load() != term {
		return nil, ErrNotSwitching
	}
	img, err := r.ImageNow()
	if err != nil {
		return nil, err
	}
	// Close the stream's open round first: copies the last scans made for
	// it must not become an image numbered after this one, which the
	// standby would take and then refuse this one as older (Fable S1).
	r.imgMu.Lock()
	if !mutRuntime("final_keeps_round") {
		r.imgPhase = 2
		for _, np := range r.namedPrograms() {
			np.prog.img.want.Store(false)
		}
	}
	img.Term, img.Seq = term, r.imageSeq.Add(1)
	r.imgMu.Unlock()
	return img, nil
}

// InputsLive reports whether this runtime's drivers say their inputs are
// live (io.InputsReadier; true for a driver that does not say): a
// standby's I/O health, which a switchover to it requires (#710 d1c).
func (r *Runtime) InputsLive() bool {
	return r.ready == nil || r.ready.InputsReady()
}
