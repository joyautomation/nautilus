//go:build !js

package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/auth"
	"github.com/joyautomation/nautilus/checkout"
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
//   - the point's equipment is out of service (the alarm engine's window), so
//     its alarms are suppressed and everyone can see it is being worked on;
//   - nothing else is forced at that address (a force someone else holds is
//     left alone) and no other checkout force stands (one at a time).
//
// It stands for `seconds` (default 30, at most 300), on the controller's
// monotonic clock (a timer, never a wall-clock time), and then the
// controller lets it go itself. So closing the browser, signing out or
// losing the link cannot leave an output forced. It also ends when released,
// when its run is abandoned, and when the controller restarts (a force does
// not survive one; the record says "interrupted"). While it stands, every
// frame carries checkoutForce (the point, who, seconds left) so every screen
// can say an output is being driven.
//
// Nothing here runs unless the project has a checkout: section and a
// checkout force was asked for: there is no timer and no state otherwise.

const (
	checkoutForceDefault = 30 * time.Second
	checkoutForceMax     = 300 * time.Second
)

// ckForce is the standing checkout force.
type ckForce struct {
	run, point, tag, by string
	value               any
	sinceMs             int64 // the force table's stamp on it: how to know it is still ours
	until               time.Time
	timer               *time.Timer
}

type ckState struct {
	mu    sync.Mutex
	force *ckForce
}

// checkoutForceView is the standing force for a frame, nil when none stands.
func (co *CheckoutOptions) forceView() *CheckoutForceView {
	co.ck.mu.Lock()
	defer co.ck.mu.Unlock()
	f := co.ck.force
	if f == nil {
		return nil
	}
	return &CheckoutForceView{Run: f.run, Point: f.point, Tag: f.tag, By: f.by, RemainingMs: max(time.Until(f.until).Milliseconds(), 0)}
}

// checkoutInterlock is nil when the point's equipment is out of service.
func (a *checkoutAPI) checkoutInterlock(pt checkout.Point) error {
	if pt.Equipment == "" {
		return fmt.Errorf("point %s names no equipment: a checkout force needs the equipment taken out of service first, so give the point the equipment its alarms are on (checkout.yaml, equipment:)", pt.ID)
	}
	if a.s.alarms == nil {
		return fmt.Errorf("this controller has no alarms, so %s cannot be taken out of service: a checkout force needs its equipment out of service first", pt.Equipment)
	}
	for _, o := range a.s.alarms.OutOfServiceList() {
		if strings.EqualFold(o.Equipment, pt.Equipment) {
			return nil
		}
	}
	return fmt.Errorf("take %s out of service first (naut alarms out-of-service %s --for 1h --reason \"I/O checkout\"): a force bypasses the program's interlocks, and its alarms would otherwise ring", pt.Equipment, pt.Equipment)
}

func sameTag(a, b string) bool { return strings.EqualFold(a, b) }

func (a *checkoutAPI) checkoutForceStart(w http.ResponseWriter, r *http.Request) {
	a.checkoutWrite(w, r, "point", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		if a.s.tagWriter != nil {
			return nil, http.StatusNotImplemented, fmt.Errorf("this server mirrors a controller it does not run: force on that controller")
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
		tags := a.s.rt.Tags()
		for _, f := range tags.Forces() {
			if sameTag(f.Name, pt.Tag) {
				return nil, http.StatusConflict, fmt.Errorf("%s is already forced (by someone's force, not this checkout's): clear that force first", pt.Tag)
			}
		}
		a.co.ck.mu.Lock()
		defer a.co.ck.mu.Unlock()
		if a.co.ck.force != nil {
			return nil, http.StatusConflict, checkout.ErrForceActive
		}
		by := noteAuthor(pr.String())
		// recorded first: a force that stands is always in the record
		rec, err := a.co.Store.StartForce(run.ID, pt.ID, req.Value, int(d/time.Second), by, pr.String())
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if err := tags.Force(pt.Tag, req.Value); err != nil {
			_ = a.co.Store.EndForce(run.ID, checkout.ForceRefused, by)
			return nil, http.StatusBadRequest, fmt.Errorf("%w: %v", checkout.ErrInvalid, err)
		}
		since := int64(0)
		for _, f := range tags.Forces() {
			if sameTag(f.Name, pt.Tag) {
				since = f.SinceMs
			}
		}
		f := &ckForce{run: run.ID, point: pt.ID, tag: pt.Tag, by: by, value: req.Value, sinceMs: since, until: time.Now().Add(d)}
		// a timer's duration runs on the monotonic clock
		f.timer = time.AfterFunc(d, func() { a.endCheckoutForce(checkout.ForceExpired, "controller") })
		a.co.ck.force = f
		slog.Warn("checkout: forcing an output", "tag", pt.Tag, "point", pt.ID, "by", by, "seconds", rec.Seconds, "run", run.ID)
		return rec, http.StatusCreated, nil
	})
}

func (a *checkoutAPI) checkoutForceRelease(w http.ResponseWriter, r *http.Request) {
	a.checkoutWrite(w, r, "standing", func(pr auth.Principal, req checkoutRequest) (any, int, error) {
		a.co.ck.mu.Lock()
		f := a.co.ck.force
		a.co.ck.mu.Unlock()
		if f == nil || f.run != r.PathValue("id") {
			return nil, http.StatusConflict, checkout.ErrNoForce
		}
		a.endCheckoutForce(checkout.ForceReleased, noteAuthor(pr.String()))
		return map[string]any{"released": f.point}, http.StatusOK, nil
	})
}

// endCheckoutForce lets the standing force go (if there is one), and records
// how. A force someone else has since changed or cleared at that address is
// not touched: only the one this checkout put there, by its stamp, is removed.
func (a *checkoutAPI) endCheckoutForce(how, by string) {
	a.co.ck.mu.Lock()
	f := a.co.ck.force
	if f == nil {
		a.co.ck.mu.Unlock()
		return
	}
	a.co.ck.force = nil
	f.timer.Stop()
	a.co.ck.mu.Unlock()

	tags := a.s.rt.Tags()
	ours := false
	for _, e := range tags.Forces() {
		if sameTag(e.Name, f.tag) && e.SinceMs == f.sinceMs && reflect.DeepEqual(e.Value, plainValue(f.value, e.Value)) {
			ours = true
		}
	}
	if ours {
		tags.Unforce(f.tag)
	} else if how == checkout.ForceExpired || how == checkout.ForceReleased || how == checkout.ForceAbandoned {
		how = checkout.ForceReplaced // not ours any more: left as it is
	}
	if err := a.co.Store.EndForce(f.run, how, by); err != nil {
		slog.Error("checkout: could not record the end of a force", "err", err, "point", f.point)
	}
	slog.Warn("checkout: force ended", "tag", f.tag, "point", f.point, "how", how)
	// a person's release or abandon is already an entry of its own; the
	// controller's own expiry has no request, so it is journalled here
	if a.s.audit != nil && by == "controller" {
		a.s.audit.record(audit.Entry{Action: "checkout.force-end", Target: f.tag, User: "system", Source: audit.SourceSystem, Result: audit.ResultOK,
			Detail: map[string]any{"run": f.run, "point": f.point, "how": how, "by": by}})
	}
}

// plainValue is v as the force table reports it (a number sent as an
// integer comes back as the tag's type): compared as the table has it.
func plainValue(v, as any) any {
	if reflect.TypeOf(v) == reflect.TypeOf(as) {
		return v
	}
	if f, ok := toFloat(v); ok {
		if g, ok := toFloat(as); ok && f == g {
			return as
		}
	}
	return v
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}
