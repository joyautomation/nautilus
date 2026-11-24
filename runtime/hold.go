//go:build !js

package runtime

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/ir"
)

// The takeover hold (#710 (b2), redundancy.takeover-hold). After a
// takeover the new leader scans with live inputs and TAKEOVER_HOLD() TRUE,
// but sends nothing to the field: the program's output push is skipped,
// channel WRITEs are refused, and drivers that re-send on their own forget
// what they sent. Only the safe: values go out, once, through drivers that
// can write them on their own (io.SafeWriter). The program releases the
// hold by setting its release tag TRUE; an audited operator command
// (ReleaseHold) is the only other way. At max the hold raises its alarm
// and STAYS held, unless the manifest opted into releasing (on-max:
// release). Alarms keep evaluating and paging: outputs are what the hold
// stops.
//
// A hold starts on a takeover from the peer (never on a start): on a cold
// one, or on every one with on: always. A hot takeover from a leader that
// was holding inherits its hold, with its age, so a hold is never lost by
// passing the lead across.

// TakeoverHold configures the hold (the manifest's
// redundancy.takeover-hold).
type TakeoverHold struct {
	// Release is the BOOL memory tag the program sets TRUE to release. The
	// hold sets it FALSE when it starts, and nothing but the program may
	// write it (writes and forces from outside are refused).
	Release string
	// Max is how long the hold holds before its alarm.
	Max time.Duration
	// Always holds on every takeover, hot ones too; otherwise only cold
	// ones (and a hot one that inherits a hold).
	Always bool
	// ReleaseAtMax releases at Max (on-max: release) instead of holding on.
	ReleaseAtMax bool
	// Safe are the values written to these outputs when the hold starts
	// (each output's driver must be an io.SafeWriter).
	Safe nio.Values
}

// HoldEventKind names what happened to the hold.
type HoldEventKind string

const (
	// HoldStarted: a takeover started (or inherited) the hold.
	HoldStarted HoldEventKind = "start"
	// HoldReleased: the program, an operator or on-max: release ended it.
	HoldReleased HoldEventKind = "release"
	// HoldAtMax: the hold reached Max and holds on (or releases, with
	// ReleaseAtMax: then a HoldReleased by "max" follows).
	HoldAtMax HoldEventKind = "max"
	// HoldReminder: the hold is still held, another Max after HoldAtMax.
	HoldReminder HoldEventKind = "reminder"
	// HoldSafeUndelivered: a safe value has not reached its device within
	// SafeAckWait of being sent.
	HoldSafeUndelivered HoldEventKind = "safe-undelivered"
	// HoldSafeDelivered: every safe value reached its device after a
	// HoldSafeUndelivered.
	HoldSafeDelivered HoldEventKind = "safe-delivered"
)

// Who released a hold, beside an operator's principal.
const (
	HoldByProgram = "program" // the release tag
	HoldByMax     = "max"     // on-max: release
)

// HoldEvent is one change of the hold, for the pair's journal, conditions
// and alarms.
type HoldEvent struct {
	Kind HoldEventKind
	// By is who released it (HoldReleased): HoldByProgram, HoldByMax, or
	// the operator's principal.
	By string
	// Inherited: the hold came from the previous leader (a hot takeover).
	Inherited bool
	// Age is how long the hold has held, an inherited one's time on the
	// previous leader included.
	Age time.Duration
	// Undelivered names the safe outputs not yet delivered.
	Undelivered []string
}

// HoldStatus is the hold as it stands.
type HoldStatus struct {
	Configured bool
	Holding    bool
	Inherited  bool
	AtMax      bool
	Age        time.Duration
	// Undelivered names the safe outputs not yet delivered; SafeAlarm says
	// one has not been for SafeAckWait.
	Undelivered []string
	SafeAlarm   bool
	// ReleasedAtMax: on-max: release released the hold at its max, within
	// the last max (at least 10 s): its alarm is still raised.
	ReleasedAtMax bool
}

// SafeAckWait is how long a safe value may take to reach its device before
// the hold raises an alarm.
const SafeAckWait = 2 * time.Second

type holdState struct {
	mu  sync.Mutex
	cfg *TakeoverHold
	on  func(HoldEvent)

	holding    bool
	inherited  bool
	atMax      bool
	since      time.Time // the runtime's clock, an inherited age taken off
	lastRemind time.Time

	// The safe values' delivery, touched by the main scan (under mainMu)
	// and read for status under mu.
	safeDue     bool      // the next held scan writes them
	safeSentAt  time.Time // when they were last written
	safeAlarm   bool
	undelivered []string

	// releasedAtMax is when on-max: release released the hold: its alarm
	// stays raised a while after (HoldStatus.ReleasedAtMax), so the alarm
	// engine sees it (Fable S1). Zero otherwise.
	releasedAtMax time.Time

	// The events, in order, for on: handed over by a goroutine of their
	// own, so a slow journal never stalls a takeover (under leadMu) or a
	// scan (Fable S2).
	qmu      sync.Mutex
	queue    []HoldEvent
	draining bool
}

// SetTakeoverHold configures the hold and the function its events go to
// (on may be nil). Call before Run. The release tag must be a BOOL tag of
// the store; from here on, only the program may write it.
func (r *Runtime) SetTakeoverHold(h *TakeoverHold, on func(HoldEvent)) error {
	if h == nil {
		return nil
	}
	if h.Max <= 0 {
		return fmt.Errorf("takeover hold: max %s: want more than 0", h.Max)
	}
	if err := r.tags.markRelease(h.Release); err != nil {
		return err
	}
	cfg := *h
	cfg.Safe = make(nio.Values, len(h.Safe))
	for k, v := range h.Safe {
		cfg.Safe[k] = v
	}
	r.hold.mu.Lock()
	r.hold.cfg, r.hold.on = &cfg, on
	r.hold.mu.Unlock()
	r.tags.comm.setHeld(r.tags.red.hold.Load)
	return nil
}

// markRelease makes name the hold's release tag: it must be a BOOL tag,
// and writes and forces from outside the program are refused from now on.
func (t *Tags) markRelease(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	tv, _, ok := t.lookupLocked(name)
	if !ok {
		return fmt.Errorf("takeover hold: release tag %q: no such tag", name)
	}
	if tv.v.Kind != ir.TypeBool {
		return fmt.Errorf("takeover hold: release tag %q: want a BOOL tag", name)
	}
	if !mutRuntime("hold_release_tag_writable") {
		tv.releases = true
	}
	return nil
}

// isTakeover reports whether the leadership just acquired is a takeover
// from the peer, as the coordinator knows it (TakeoverKnower).
func (r *Runtime) isTakeover() bool {
	tk, ok := r.coord.(TakeoverKnower)
	return ok && tk.IsTakeover()
}

// holdOnTakeover starts, inherits or ends the hold at a takeover (under
// leadMu, before the first scan). inherit is the image's hold age when a
// hot takeover went on from a holding leader, else negative.
func (r *Runtime) holdOnTakeover(hot bool, inherit time.Duration) {
	h := &r.hold
	h.mu.Lock()
	cfg := h.cfg
	if cfg == nil {
		h.mu.Unlock()
		return
	}
	now := r.now(time.Now())
	h.holding, h.inherited, h.atMax = false, false, false
	h.safeDue, h.safeSentAt, h.safeAlarm, h.undelivered = false, time.Time{}, false, nil
	h.releasedAtMax = time.Time{}
	takeover := r.isTakeover()
	// A hot takeover from a holding leader holds, whatever the coordinator
	// calls it: never drop a hold the program has not released (Fable N5).
	inherited := hot && inherit >= 0 && !mutRuntime("hold_not_inherited")
	start := inherited || takeover && (!hot || cfg.Always)
	if mutRuntime("hold_never_starts") {
		start = false
	}
	if !start {
		r.tags.red.hold.Store(false)
		h.mu.Unlock()
		return
	}
	h.holding, h.inherited = true, inherited
	h.since = now
	if inherited && !mutRuntime("hold_age_restarts") {
		h.since = now.Add(-inherit)
	}
	h.lastRemind = time.Time{}
	h.safeDue = len(cfg.Safe) > 0
	r.tags.red.hold.Store(true)
	// A carried TRUE must not release the hold the moment it starts.
	if !mutRuntime("hold_release_tag_kept") {
		_ = r.tags.WriteGlobal(cfg.Release, ir.BoolVal(false))
	}
	ev := HoldEvent{Kind: HoldStarted, Inherited: inherited, Age: now.Sub(h.since)}
	h.mu.Unlock()
	// Nothing this replica sent in an earlier leadership is re-sent while
	// the hold holds.
	if f, ok := r.driver.(nio.OutputsForgetter); ok && !mutRuntime("hold_drivers_remember") {
		f.ForgetOutputs()
	}
	slog.Warn("runtime: takeover hold: outputs held until the program sets its release tag",
		"release", cfg.Release, "inherited", inherited, "max", cfg.Max)
	r.holdEmit(ev)
}

// holdEmit hands events to on, in order, from a goroutine of its own.
func (r *Runtime) holdEmit(evs ...HoldEvent) {
	h := &r.hold
	if h.on == nil || len(evs) == 0 {
		return
	}
	h.qmu.Lock()
	h.queue = append(h.queue, evs...)
	if h.draining {
		h.qmu.Unlock()
		return
	}
	h.draining = true
	h.qmu.Unlock()
	go func() {
		for {
			h.qmu.Lock()
			if len(h.queue) == 0 {
				h.draining = false
				h.qmu.Unlock()
				return
			}
			ev := h.queue[0]
			h.queue = h.queue[1:]
			h.qmu.Unlock()
			h.on(ev)
		}
	}()
}

// holdScan is the main scan's look at the hold, after the logic and
// before the output push: it releases the hold when the program set its
// release tag, raises the alarm at max, writes the safe values, and
// reports whether the push is held. Under mainMu.
func (r *Runtime) holdScan() bool {
	h := &r.hold
	if h.cfg == nil {
		return false
	}
	h.mu.Lock()
	if !h.holding {
		h.mu.Unlock()
		return false
	}
	cfg := h.cfg
	now := r.now(time.Now())
	var evs []HoldEvent
	rel := false
	if v, err := r.tags.ReadGlobal(cfg.Release); err == nil && v.Kind == ir.TypeBool && v.B {
		rel = true
		evs = append(evs, r.releaseLocked(HoldByProgram, now))
	} else if age := now.Sub(h.since); age >= cfg.Max && !mutRuntime("hold_no_max") {
		switch {
		case !h.atMax:
			h.atMax, h.lastRemind = true, now
			evs = append(evs, HoldEvent{Kind: HoldAtMax, Inherited: h.inherited, Age: age})
		case now.Sub(h.lastRemind) >= cfg.Max:
			h.lastRemind = now
			evs = append(evs, HoldEvent{Kind: HoldReminder, Inherited: h.inherited, Age: age})
		}
		if cfg.ReleaseAtMax && !mutRuntime("hold_release_at_max_ignored") {
			rel = true
			evs = append(evs, r.releaseLocked(HoldByMax, now))
			h.releasedAtMax = now
		}
	}
	safeDue := h.safeDue && !rel
	h.safeDue = false
	h.mu.Unlock()

	if !rel {
		evs = append(evs, r.holdSafe(now, safeDue)...)
	}
	for _, ev := range evs {
		switch ev.Kind {
		case HoldReleased:
			slog.Info("runtime: takeover hold released; every output goes out on this scan", "by", ev.By, "age", ev.Age)
		case HoldAtMax, HoldReminder:
			slog.Error("runtime: takeover hold at its max: outputs still held; the program's release tag or `naut redundancy release-hold` releases them", "age", ev.Age, "release", cfg.Release)
		case HoldSafeUndelivered:
			slog.Error("runtime: takeover hold: safe values not delivered", "outputs", ev.Undelivered)
		}
	}
	r.holdEmit(evs...)
	return !rel
}

// holdSafe writes the safe values when due (the hold's first scan, or a
// driver that asked for its outputs again) and follows their delivery.
// Under mainMu, not hold.mu: a driver call may take a while.
func (r *Runtime) holdSafe(now time.Time, due bool) []HoldEvent {
	h := &r.hold
	if len(h.cfg.Safe) == 0 || r.driver == nil || mutRuntime("hold_safe_unsent") {
		return nil
	}
	sw, ok := r.driver.(nio.SafeWriter)
	if !ok {
		h.mu.Lock()
		h.undelivered = sortedKeys(h.cfg.Safe)
		h.mu.Unlock()
		if due {
			return []HoldEvent{{Kind: HoldSafeUndelivered, Undelivered: sortedKeys(h.cfg.Safe)}}
		}
		return nil
	}
	// A driver that forgot what it was sent (its gate closed and reopened)
	// asks for its outputs: under the hold, that is the safe values again.
	if r.asker != nil && r.asker.OutputsWanted() {
		due = true
	}
	// A write starts the 2 s wait afresh only when nothing was waiting
	// before it (the first, or a re-send after all had landed: Fable S3).
	// A re-send while a value is still undelivered keeps the running
	// clock: a driver that asks every second (a stuck write's retry) must
	// not keep the alarm from ever raising (Fable re-check).
	h.mu.Lock()
	fresh := h.safeSentAt.IsZero() || len(h.undelivered) == 0 || mutRuntime("hold_safe_wait_restarts")
	h.mu.Unlock()
	if due {
		if err := sw.WriteSafe(h.cfg.Safe); err != nil {
			slog.Error("runtime: takeover hold: writing safe values", "error", err)
		}
	}
	und := sw.SafeUndelivered()
	slices.Sort(und)
	var evs []HoldEvent
	h.mu.Lock()
	if due && fresh {
		h.safeSentAt = now
	}
	h.undelivered = und
	switch {
	case len(und) > 0 && !h.safeAlarm && !h.safeSentAt.IsZero() && now.Sub(h.safeSentAt) >= SafeAckWait:
		h.safeAlarm = true
		evs = append(evs, HoldEvent{Kind: HoldSafeUndelivered, Undelivered: und})
	case len(und) == 0 && h.safeAlarm:
		h.safeAlarm = false
		evs = append(evs, HoldEvent{Kind: HoldSafeDelivered})
	}
	h.mu.Unlock()
	return evs
}

// releaseLocked ends the hold: TAKEOVER_HOLD() FALSE from the next scan,
// and the next push hands every driver every output. Under hold.mu.
func (r *Runtime) releaseLocked(by string, now time.Time) HoldEvent {
	h := &r.hold
	ev := HoldEvent{Kind: HoldReleased, By: by, Inherited: h.inherited, Age: now.Sub(h.since)}
	h.holding, h.atMax, h.safeAlarm, h.undelivered = false, false, false, nil
	r.tags.red.hold.Store(false)
	// Every held scan left it false already; said again so the release's
	// push is the whole set whatever ran in between.
	r.outSent.Store(false)
	return ev
}

// ReleaseHold releases the hold for an operator (the audited
// `naut redundancy release-hold`); by names them. The next scan pushes
// every output.
func (r *Runtime) ReleaseHold(by string) error {
	h := &r.hold
	h.mu.Lock()
	if !h.holding {
		h.mu.Unlock()
		return ErrNotHolding
	}
	ev := r.releaseLocked(by, r.now(time.Now()))
	h.mu.Unlock()
	slog.Warn("runtime: takeover hold released by an operator; every output goes out on the next scan", "by", by, "age", ev.Age)
	r.holdEmit(ev)
	return nil
}

// HoldStatus is the hold as it stands. It takes no lock but the hold's
// own, never leadMu: the pair's system alarms read it from the alarm
// engine's evaluation, under the engine's lock, which a takeover (under
// leadMu) takes too (Fable M1).
func (r *Runtime) HoldStatus() HoldStatus {
	h := &r.hold
	h.mu.Lock()
	defer h.mu.Unlock()
	st := HoldStatus{Configured: h.cfg != nil, Holding: h.holding, Inherited: h.inherited, AtMax: h.atMax}
	if !h.releasedAtMax.IsZero() && h.cfg != nil {
		st.ReleasedAtMax = r.now(time.Now()).Sub(h.releasedAtMax) < max(h.cfg.Max, 10*time.Second)
	}
	if h.holding {
		st.Age = r.now(time.Now()).Sub(h.since)
		st.Undelivered = slices.Clone(h.undelivered)
		st.SafeAlarm = h.safeAlarm
	}
	return st
}

// holdAge is the hold's age for the state image, negative when none
// holds: a hot takeover from this image inherits it.
func (r *Runtime) holdAge() time.Duration {
	h := &r.hold
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.holding || mutRuntime("hold_not_imaged") {
		return -1
	}
	return r.now(time.Now()).Sub(h.since)
}

// holdDriverReplaced: a driver swapped in while the hold holds gets the
// safe values on the next scan (it holds none of them; Fable S4).
func (r *Runtime) holdDriverReplaced() {
	h := &r.hold
	h.mu.Lock()
	if h.holding && h.cfg != nil && len(h.cfg.Safe) > 0 && !mutRuntime("hold_replace_unsafe") {
		h.safeDue = true
	}
	h.mu.Unlock()
}

// holdAfterPush closes the window between the scan's look at the hold and
// its push: a takeover (another task's gate) that started a hold in
// between must not leave the push it raced in place, where a driver's
// keep-alive would re-assert it for the whole hold (Fable N1).
func (r *Runtime) holdAfterPush() {
	if !r.tags.red.hold.Load() {
		return
	}
	if f, ok := r.driver.(nio.OutputsForgetter); ok {
		f.ForgetOutputs()
	}
	r.outSent.Store(false)
}

func sortedKeys(v nio.Values) []string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
