//go:build !js

package runtime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/retain"
)

// The hot install (hot.go): only a controller's build has it. The
// playground's wasm runs no pair (hot_js.go).

// hotTakeover decides a pair's takeover and, when it goes hot, installs
// the candidate: by value into fresh frames of the programs running now,
// timers moved to this clock, host handles forgotten, FIRST_SCAN TRUE as
// on any takeover; then its tags over the retain load. It reports either
// way. why is the flush's reason when it handed over no candidate, had
// whether the standby held an image all the same (a failed or late
// flush): a refused image is warned of, a plain start is not. Called
// by takeover under leadMu, after the retain load, before any frame
// reset: false means takeover resets them (cold).
func (r *Runtime) hotTakeover(h *HotImage, why string, had bool) bool {
	rep := TakeoverReport{At: time.Now(), ImageAge: -1}
	if t, ok := r.coord.(Termed); ok {
		rep.Term = t.Term()
	}
	defer func() { r.noteTakeover(rep) }()
	cold := func(reason string) bool {
		rep.Reason = reason
		slog.Warn("runtime: cold takeover: frames reset, timers and counters start over", "reason", reason)
		return false
	}
	if h == nil {
		if why != "" {
			rep.Image = had
			return cold(why)
		}
		rep.Reason = "no state image: the standby followed no leader"
		slog.Info("runtime: cold takeover with no state image")
		return false
	}
	rep.Image, rep.ImageAge = true, h.Age
	switch {
	case h.Cold != "":
		return cold(h.Cold)
	case h.MaxAge <= 0 && !mutRuntime("hot_no_age_check"):
		return cold("redundancy.hot-max-age is 0: every takeover is cold")
	case h.Age > h.MaxAge && !mutRuntime("hot_no_age_check"):
		return cold(fmt.Sprintf("the image was %s old at the election, over hot-max-age %s", h.Age.Round(time.Millisecond), h.MaxAge))
	}
	// This controller's own store could not be read (#710 S2 Q3): a hot
	// takeover's retained values come from the image, which is the newer
	// state. Only a hot one's (naut-lead): every check is made first, on
	// the source the image's retain would run, and a takeover that fails
	// one is the plain cold one, on initial values and held, with nothing
	// of the image's.
	if r.strm.loadFailed.Load() && h.Retain != nil {
		var lead retain.State
		if err := json.Unmarshal(h.Retain, &lead); err != nil {
			return cold("the image's retain section: " + err.Error())
		}
		if !mutRuntime("hot_retain_before_checks") {
			if bad := r.hotPrecheck(h, lead); bad != "" {
				return cold(bad)
			}
		}
		if err := r.applyRetained(retain.Carry(retain.State{}, lead)); err != nil {
			r.noteRetainError(err)
		}
		r.strm.loadFailed.Store(false)
		if !mutRuntime("retain_memory_only_forgotten") {
			r.strm.memOnly.Store(true)
		}
		rep.RetainFromImage = true
		slog.Error("runtime: this controller's retain store could not be read: its retained values are the state image's, held only in memory until a save succeeds")
	}
	progs := r.namedPrograms()
	for _, np := range progs {
		np.prog.mu.Lock()
	}
	if rep.RetainFromImage {
		// Checked already, on the source the image's retain runs (above);
		// a check that fails only now (its online edit did not swap in) is
		// cold, with the image's values in memory and said so.
		defer func() {
			if !rep.Hot {
				slog.Error("runtime: a takeover that loaded the state image's retained values went cold after all: they stay, only in memory")
			}
		}()
	}
	unlock := func() {
		for _, np := range progs {
			np.prog.mu.Unlock()
		}
	}
	if len(h.tasks) != len(progs) && !mutRuntime("hot_partial_tasks") {
		unlock()
		return cold(fmt.Sprintf("the image has %d tasks, this controller runs %d", len(h.tasks), len(progs)))
	}
	frames := make([]*ir.Frame, len(progs))
	now := r.now(time.Now())
	since := now.Sub(h.arrived).Milliseconds()
	if mutRuntime("hot_age_not_elapsed") {
		since = 0
	}
	nowMs := r.tags.NowMs()
	for i, np := range progs {
		p := np.prog
		t, ok := h.tasks[np.name]
		var bad string
		switch {
		case !ok:
			bad = "the image has no frame of task " + np.name
		case p.prog.Layout() != t.layout:
			bad = "task " + np.name + " runs a program of another layout than the image's"
		case sourceHash(p.source) != t.source && !mutRuntime("hot_source_unchecked"):
			bad = "task " + np.name + " runs another source than the image's (an online edit not streamed yet?)"
		}
		if bad == "" {
			f := ir.NewFrame(p.prog)
			if err := ir.ApplyLeaves(f, t.leaves); err != nil {
				bad = fmt.Sprintf("task %s: %v", np.name, err)
			} else {
				// The image's NowMs, carried to now on this clock: the
				// time since it arrived (age at the election plus the
				// inputs wait) counts as elapsed (design §4.2 step 4).
				if !mutRuntime("hot_timer_not_rebased") {
					ir.RebaseFrame(f, nowMs-(t.nowMs+since))
				}
				if !mutRuntime("hot_ticket_carried") {
					ir.ForgetHost(f)
				}
				frames[i] = f
			}
		}
		if bad != "" && !mutRuntime("hot_partial_tasks") {
			unlock()
			return cold(bad)
		}
	}
	for i, np := range progs {
		p := np.prog
		if frames[i] == nil {
			continue // only under hot_partial_tasks: this one keeps its frame
		}
		p.frame = frames[i]
		if !mutRuntime("hot_keeps_first_scan_false") {
			p.ran = false // FIRST_SCAN() TRUE, as on every takeover, until #710 S2 (b)
		}
		// A frame copy made before the install is of the old frame: never
		// this leadership's to send (as ResetFrame).
		p.img.mu.Lock()
		p.img.ready = false
		p.img.mu.Unlock()
	}
	unlock()
	// The frames carry their VAR RETAIN locals by value.
	r.pendingLocals = nil
	// The image's tags go in after the retain load, so they win over it:
	// they are as of the image, the retain file at best as of the same.
	if h.tags != nil && !mutRuntime("hot_globals_not_carried") {
		cs := r.carried
		for i, ok := range h.tags.have {
			if ok {
				_ = r.tags.WriteGlobal(cs.keys[i], h.tags.vals[i])
			}
		}
	}
	rep.Hot = true
	slog.Info("runtime: hot takeover: the old leader's frames and tags go on from its newest image",
		"term", h.Term, "seq", h.Seq, "age", h.Age.Round(time.Millisecond), "since_arrival", time.Duration(since)*time.Millisecond)
	return true
}

// hotPrecheck is the hot checks before a takeover over an unreadable own
// store loads the image's retain section (lead): the image has every task
// this controller runs, and each runs the image's source once lead's
// online edits are applied as loadRetained would apply them (an edit
// made on this build swaps in; the shadow's layout check covers the
// layout of what ran the image). "" when all pass, else why not. Called
// before any program lock is taken.
func (r *Runtime) hotPrecheck(h *HotImage, lead retain.State) string {
	progs := r.namedPrograms()
	if len(h.tasks) != len(progs) {
		return fmt.Sprintf("the image has %d tasks, this controller runs %d", len(h.tasks), len(progs))
	}
	for _, np := range progs {
		t, ok := h.tasks[np.name]
		if !ok {
			return "the image has no frame of task " + np.name
		}
		run, build := np.prog.Hash(), np.prog.BootHash()
		if src, ok := lead.Programs[np.name]; ok && lead.ProgramBases[np.name] == build {
			run = sourceHash(src) // loadRetained swaps it in (or already runs it)
		}
		if run != t.source && !mutRuntime("hot_source_unchecked") {
			return "task " + np.name + " runs another source than the image's (an online edit not streamed yet?)"
		}
	}
	return ""
}
