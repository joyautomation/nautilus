//go:build !js

package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
	"github.com/joyautomation/nautilus/runtime"
)

// Forcing an output from an I/O checkout (#719):
//
//	POST /api/checkout/runs/{id}/force          {point, value, seconds?, confirm}: force the point's output
//	POST /api/checkout/runs/{id}/force/release  {}: let it go now
//
// A force bypasses the program's permissives, so a checkout force is held to
// more than a plain one. All of these must hold, or nothing is forced:
//
//   - the run is open and was begun against this sheet;
//   - the point's method is force and the caller holds the checkout
//     permission and the force permission (a fresh password, for a
//     browser sign-in) in the point's area;
//   - the caller typed the point's id (confirm);
//   - the point's equipment is out of service (the alarm engine's window),
//     so its alarms are suppressed and everyone can see it is being worked on;
//   - nothing is forced at that address, in it or around it (a force someone
//     else holds is never taken over), and no other checkout force stands.
//
// It stands for `seconds` (default 30, at most 300), on the controller's
// monotonic clock (a timer, never a wall-clock time), and then the
// controller lets it go itself. So closing the browser, signing out or
// losing the link cannot leave an output forced. It also ends, at once, when
// it is released, when its run is abandoned, when the equipment is back in
// service (or its window runs out: the interlock is checked every second
// while a force stands) and when someone else changes or clears the force.
// A force does not survive the controller; the record says "interrupted".
// While it stands, every frame carries checkoutForce (the point, who, seconds
// left).
//
// The force is identified by a generation the force table gives it
// (runtime.Tags.ForceIfFree, UnforceIf), never by its value: only the force
// this checkout set is removed, and a force set or changed since is left.
//
// Nothing here runs unless the checkout is mounted and a checkout force was
// asked for: there is no timer and no state otherwise.

const (
	checkoutForceDefault = 30 * time.Second
	checkoutForceMax     = 300 * time.Second
	// checkoutForceWatch is how often a standing force checks that its
	// interlock still holds and that the force is still its own.
	checkoutForceWatch = time.Second
)

// ckForce is a checkout force: pending (reserved, the record and the force
// table not yet written) until ready.
type ckForce struct {
	run, point, tag, by string
	pt                  checkout.Point
	gen                 uint64 // the force table's generation of it
	recSeq              int64  // the entry that recorded it (ForceRec.Seq): its end names it
	ready               bool
	ending              bool      // its end is in progress: the slot stays taken until it is done
	abandoned           bool      // its run was abandoned while it was starting
	until               time.Time // monotonic
	timer               *time.Timer
	done                chan struct{} // closed when it ends
	watched             chan struct{} // closed when its watch goroutine has returned (nil until it starts)
}

type ckState struct {
	mu    sync.Mutex
	force *ckForce
}

// forceView is the standing force for a frame, nil when none stands (or the
// one that did is no longer in the force table: someone else cleared it).
func (co *CheckoutOptions) forceView(tags *runtime.Tags) *CheckoutForceView {
	co.ck.mu.Lock()
	f := co.ck.force
	var v CheckoutForceView
	var gen uint64
	ok := f != nil && f.ready && !f.ending
	if ok {
		// copied under the lock the start writes them under
		v = CheckoutForceView{Run: f.run, Point: f.point, Tag: f.pt.Tag, By: f.by, RemainingMs: max(time.Until(f.until).Milliseconds(), 0)}
		gen = f.gen
	}
	tag := ""
	if f != nil {
		tag = f.tag
	}
	co.ck.mu.Unlock()
	_ = tag
	if !ok {
		return nil
	}
	if !tags.ForceHolds(gen) {
		return nil
	}
	return &v
}

// checkoutInterlock is nil when the point's equipment is out of service now
// (a window whose time has run out does not count, swept or not).
func (a *checkoutAPI) checkoutInterlock(pt checkout.Point) error {
	if pt.Equipment == "" {
		return fmt.Errorf("point %s names no equipment: a checkout force needs the equipment taken out of service first, so give the point the equipment its alarms are on (checkout.yaml, equipment:)", pt.ID)
	}
	if a.s.alarms == nil {
		return fmt.Errorf("this controller has no alarms, so %s cannot be taken out of service: a checkout force needs its equipment out of service first", pt.Equipment)
	}
	if !a.s.alarms.IsOutOfService(pt.Equipment) {
		return fmt.Errorf("take %s out of service first (naut alarms out-of-service %s --for 1h --reason \"I/O checkout\"): a force bypasses the program's interlocks, and its alarms would otherwise ring", pt.Equipment, pt.Equipment)
	}
	return nil
}

func (a *checkoutAPI) checkoutForceStart(w http.ResponseWriter, r *http.Request) {
	a.checkoutWrite(w, r, "point", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		if a.s.tagWriter != nil {
			return nil, http.StatusNotImplemented, fmt.Errorf("this server mirrors a controller it does not run: force on that controller")
		}
		if a.s.cluster != nil && !a.s.cluster.Status().IsLeader {
			// a force drives an output: only the controller that runs the program
			return nil, http.StatusConflict, fmt.Errorf("this controller is the standby: force an output on the controller that is running the program")
		}
		pt, ok := a.co.Sheet.Point(req.Point)
		if !ok {
			return nil, http.StatusUnprocessableEntity, fmt.Errorf("the sheet has no point %s", req.Point)
		}
		run, ok := a.co.Store.Get(r.PathValue("id"))
		if !ok {
			return nil, http.StatusNotFound, checkout.ErrNoRun
		}
		if run.SheetHash != a.co.Sheet.Hash() {
			return nil, http.StatusConflict, errSheetChanged
		}
		if pt.Method != checkout.Force {
			return nil, http.StatusUnprocessableEntity, fmt.Errorf("the sheet says %s is observed, not forced", pt.ID)
		}
		if req.Confirm != pt.ID {
			return nil, http.StatusUnprocessableEntity, fmt.Errorf("type the point's id (%s) to confirm that its output will be driven", pt.ID)
		}
		if code, msg := a.s.authorize(r, PermForce, pt.Tag); code != 0 {
			return nil, http.StatusForbidden, fmt.Errorf("%s", msg)
		}
		d := checkoutForceDefault
		if req.Seconds != 0 {
			d = time.Duration(req.Seconds) * time.Second
		}
		if d < time.Second || d > checkoutForceMax {
			return nil, http.StatusBadRequest, fmt.Errorf("%w: seconds is between 1 and %d", checkout.ErrInvalid, int(checkoutForceMax/time.Second))
		}
		if req.Value == nil {
			return nil, http.StatusBadRequest, fmt.Errorf("%w: a force needs a value", checkout.ErrInvalid)
		}
		if err := a.checkoutInterlock(pt); err != nil {
			return nil, http.StatusConflict, err
		}
		by := noteAuthor(pr.String())
		// reserve the one slot, so no store or force-table work is done under
		// the lock every frame takes
		f := &ckForce{run: run.ID, point: pt.ID, tag: pt.Tag, by: by, pt: pt, done: make(chan struct{})}
		a.co.ck.mu.Lock()
		if a.co.ck.force != nil {
			a.co.ck.mu.Unlock()
			return nil, http.StatusConflict, checkout.ErrForceActive
		}
		a.co.ck.force = f
		a.co.ck.mu.Unlock()
		ckHook("reserved")
		started := false
		defer func() { // however this ends, a start that did not finish gives the slot back
			if started {
				return
			}
			a.co.ck.mu.Lock()
			if a.co.ck.force == f {
				a.co.ck.force = nil
			}
			a.co.ck.mu.Unlock()
		}()

		// recorded first: a force that stands is always in the record
		rec, err := a.co.Store.StartForce(run.ID, pt.ID, req.Value, int(d/time.Second), by, pr.String())
		if errors.Is(err, checkout.ErrForceActive) {
			// the slot was free (no force standing, none ending, none starting),
			// so the record's standing one is stale (a failed end): close it,
			// and begin again
			_ = a.co.Store.EndForce(run.ID, checkout.ForceInterrupted, "controller")
			rec, err = a.co.Store.StartForce(run.ID, pt.ID, req.Value, int(d/time.Second), by, pr.String())
		}
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		gen, name, err := a.s.rt.Tags().ForceIfFree(pt.Tag, req.Value)
		if err != nil {
			_ = a.co.Store.EndForceOf(run.ID, rec.Seq, checkout.ForceRefused, by)
			if errors.Is(err, runtime.ErrSwitching) {
				return nil, http.StatusServiceUnavailable, err // a planned switchover: force on the new leader
			}
			if errors.Is(err, runtime.ErrAlreadyForced) {
				return nil, http.StatusConflict, fmt.Errorf("%s is already forced (by a force that is not this checkout's): clear that force first", pt.Tag)
			}
			return nil, http.StatusBadRequest, fmt.Errorf("%w: %v", checkout.ErrInvalid, err)
		}
		ckHook("applied")
		a.co.ck.mu.Lock()
		// the run was abandoned while this was starting: let the force go again
		if f.abandoned || f.ending || a.co.ck.force != f {
			a.co.ck.mu.Unlock()
			a.s.rt.Tags().UnforceIf(name, gen)
			return nil, http.StatusConflict, checkout.ErrNotOpen
		}
		f.tag, f.gen, f.until, f.ready, f.recSeq = name, gen, time.Now().Add(d), true, rec.Seq
		started = true
		// a timer's duration runs on the monotonic clock
		f.timer = time.AfterFunc(d, func() { a.guarded(func() { a.endCheckoutForce(f, checkout.ForceExpired, "controller") }) })
		watched := make(chan struct{})
		f.watched = watched // under the lock: Demoted and Stop read it there
		a.co.ck.mu.Unlock()
		go func() {
			defer close(watched)
			a.watchCheckoutForce(f)
		}()
		slog.Warn("checkout: forcing an output", "tag", pt.Tag, "point", pt.ID, "by", by, "seconds", rec.Seconds, "run", run.ID)
		return rec, http.StatusCreated, nil
	})
}

// watchCheckoutForce ends the force the moment its interlock stops holding
// (the equipment is back in service, its window ran out) or the force is no
// longer the table's own (someone cleared or replaced it).
func (a *checkoutAPI) watchCheckoutForce(f *ckForce) {
	a.guarded(func() { a.watchLoop(f) })
}

func (a *checkoutAPI) watchLoop(f *ckForce) {
	t := time.NewTicker(checkoutForceWatch)
	defer t.Stop()
	for {
		select {
		case <-f.done:
			return
		case <-t.C:
			if err := a.checkoutInterlock(f.pt); err != nil {
				a.endCheckoutForce(f, checkout.ForceInterlock, "controller")
				return
			}
			if !a.s.rt.Tags().ForceHolds(f.gen) {
				a.endCheckoutForce(f, checkout.ForceReplaced, "controller")
				return
			}
			// the record was ended by someone else (an operator abandoning the run on
			// the other controller's screen, a reconcile): this controller drives an
			// output the log says is released, so let it go
			if rec, ok := a.co.Store.Standing(); !ok || rec.Seq != f.recSeq {
				a.dropForceLocally(f, "the log no longer says it stands (its record was ended elsewhere, or the checkout's history was replaced)", false)
				return
			}
		}
	}
}

// standingForce is the standing, ready force of the run, nil if none.
func (a *checkoutAPI) standingForce(run string) *ckForce {
	a.co.ck.mu.Lock()
	defer a.co.ck.mu.Unlock()
	if f := a.co.ck.force; f != nil && f.ready && !f.ending && f.run == run {
		return f
	}
	return nil
}

func (a *checkoutAPI) checkoutForceRelease(w http.ResponseWriter, r *http.Request) {
	a.checkoutWrite(w, r, "standing", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		f := a.standingForce(r.PathValue("id"))
		if f == nil {
			return nil, http.StatusConflict, checkout.ErrNoForce
		}
		// asked again for the force actually standing now (it may have been
		// replaced since the area was checked)
		if code, msg := a.s.authorize(r, PermCheckout, f.tag); code != 0 {
			return nil, http.StatusForbidden, fmt.Errorf("%s", msg)
		}
		how := a.endCheckoutForce(f, checkout.ForceReleased, noteAuthor(pr.String()))
		if how == "" {
			return nil, http.StatusConflict, checkout.ErrNoForce
		}
		return map[string]any{"released": f.point, "how": how}, http.StatusOK, nil
	})
}

// endCheckoutForce lets the force f go, if it is still the standing one, and
// records how; it returns how it ended. Only the force this checkout set, by
// its generation, is removed from the force table: one set or changed since
// is not touched, and the record says it was replaced.
func (a *checkoutAPI) endCheckoutForce(f *ckForce, how, by string) string {
	a.co.ck.mu.Lock()
	if a.co.ck.force != f || f.ending {
		a.co.ck.mu.Unlock()
		return "" // it ended another way first
	}
	// the slot stays taken until the end is done: no force starts in the gap
	// between letting go and recording it
	f.ending = true
	if f.timer != nil {
		f.timer.Stop()
	}
	close(f.done)
	a.co.ck.mu.Unlock()
	defer func() {
		a.co.ck.mu.Lock()
		if a.co.ck.force == f {
			a.co.ck.force = nil
		}
		a.co.ck.mu.Unlock()
	}()
	ckHook("ending")

	if !a.s.rt.Tags().UnforceIf(f.tag, f.gen) {
		how = checkout.ForceReplaced // not ours any more: left as it is
	}
	if err := a.co.Store.EndForceOf(f.run, f.recSeq, how, by); err != nil && !errors.Is(err, checkout.ErrNoForce) {
		slog.Error("checkout: could not record the end of a force", "err", err, "point", f.point)
	}
	slog.Warn("checkout: force ended", "tag", f.tag, "point", f.point, "how", how)
	// a person's release or abandon is already an entry of its own; the
	// controller's own end of it (expiry, the interlock, a replaced force) has
	// no request, so it is journalled here
	if a.s.audit != nil && by == "controller" {
		a.s.audit.record(audit.Entry{Action: "checkout.force-end", Target: f.pt.Tag, User: "system", Source: audit.SourceSystem, Result: audit.ResultOK,
			Detail: map[string]any{"run": f.run, "point": f.point, "how": how, "by": by}})
	}
	return how
}

// ckHook is a test's seam: called at named points of a start and an end, so a
// test can hold one there and run another request against it.
var ckHook = func(string) {}

// guarded runs fn, and if it panics logs it and keeps the controller up: a
// timer or watch goroutine that dies must not take the controller (which would
// have to be restarted by hand while its output was forced) with it. The force
// itself is let go before anything that can fail (endCheckoutForce unforces
// first).
func (a *checkoutAPI) guarded(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("checkout: a force's timer or watch panicked", "panic", r)
		}
	}()
	fn()
}

// ReconcileForces ends a checkout force the record says stands that this
// controller is not driving: on a pair, one the other controller started and
// lost the lead with, or one whose start was proposed, not confirmed, and
// committed later under a new leader. Nothing drives it and nothing would end
// it, and it would block abandon and seal for good. It is for the controller
// that runs the program, and returns how many it ended.
func (co *CheckoutOptions) ReconcileForces() (int, error) {
	if co.Store == nil || co.Store.Halted() != "" {
		return 0, nil // a halted store's standing force is stale: nothing is ended from it
	}
	rec, ok := co.Store.Standing()
	if !ok {
		return 0, nil
	}
	co.ck.mu.Lock()
	f := co.ck.force
	drives := f != nil && f.run == rec.Run && f.point == rec.Point && (f.recSeq == 0 || f.recSeq == rec.Seq)
	co.ck.mu.Unlock()
	if drives {
		return 0, nil
	}
	err := co.Store.EndForceOf(rec.Run, rec.Seq, checkout.ForceInterrupted, "controller")
	if errors.Is(err, checkout.ErrNoForce) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// the controller's own end of a force nothing here drove is journalled like
	// its expiry is
	co.ck.mu.Lock()
	a := co.api
	co.ck.mu.Unlock()
	if a != nil && a.s.audit != nil {
		a.s.audit.record(audit.Entry{Action: "checkout.force-end", Target: rec.Point, User: "system", Source: audit.SourceSystem, Result: audit.ResultOK,
			Detail: map[string]any{"run": rec.Run, "point": rec.Point, "how": checkout.ForceInterrupted, "by": "controller"}})
	}
	return 1, nil
}

// Stop ends a standing checkout force and waits for its goroutines: the
// controller's shutdown (the force is let go and recorded as interrupted, as a
// restart would have found it), and a test's cleanup, so no timer or watch
// outlives the server it belongs to. It is safe with nothing standing, and to
// call more than once.
func (co *CheckoutOptions) Stop() {
	co.ck.mu.Lock()
	f, a := co.ck.force, co.api
	co.ck.mu.Unlock()
	if f == nil || a == nil || co.Store == nil {
		return
	}
	a.endCheckoutForce(f, checkout.ForceInterrupted, "controller")
	co.ck.mu.Lock()
	w := f.watched
	co.ck.mu.Unlock()
	if w != nil {
		select {
		case <-w:
		case <-time.After(2 * time.Second):
		}
	}
}

// Demoted is what a controller does the moment it stops leading without exiting:
// the force it was driving is let go here at once (unforced in its runtime, its
// timer stopped, its watch closed) and nothing is proposed, since it no longer
// leads. Its outputs are fenced anyway, but a force must not linger in this
// controller's table to come back when it leads again. The record still says it
// stands; the controller that runs the program now ends it (ReconcileForces).
func (co *CheckoutOptions) Demoted() {
	co.ck.mu.Lock()
	f, a := co.ck.force, co.api
	co.ck.mu.Unlock()
	if f == nil || a == nil {
		return
	}
	a.dropForceLocally(f, "this controller no longer leads", true)
}

// dropForceLocally is endCheckoutForce without the record and the journal: the
// local half only.
func (a *checkoutAPI) dropForceLocally(f *ckForce, why string, wait bool) {
	a.co.ck.mu.Lock()
	if a.co.ck.force != f || f.ending {
		a.co.ck.mu.Unlock()
		return
	}
	f.ending = true
	if f.timer != nil {
		f.timer.Stop()
	}
	close(f.done)
	// what the force had applied, read under the lock the start sets it under: a
	// start still between ForceIfFree and its lock sees ending and lets go itself
	ready, tag, gen, w := f.ready, f.tag, f.gen, f.watched
	a.co.ck.mu.Unlock()
	ckHook("dropping")
	if ready {
		a.s.rt.Tags().UnforceIf(tag, gen)
	}
	a.co.ck.mu.Lock()
	if a.co.ck.force == f {
		a.co.ck.force = nil
	}
	a.co.ck.mu.Unlock()
	// the watch goroutine itself calls with wait false: it is the one it would wait for
	if w != nil && wait {
		select {
		case <-w:
		case <-time.After(2 * time.Second):
		}
	}
	slog.Warn("checkout: force let go here", "why", why, "tag", f.tag, "point", f.point)
}
