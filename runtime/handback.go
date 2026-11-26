//go:build !js

package runtime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/retain"
)

// The bad-retain hand-back (#710 S2c-3): a pair's takeover that would go
// cold while this controller's own retain store cannot be read would run
// the plant on initial values. Before it does, the pair asks its peer
// whether the peer's file is good and hands the lead back to it if so
// (cmd/naut decides). The gate holds the edge meanwhile: nothing has
// loaded, saved, scanned or written to the field, so a hand-back leaves
// no trace but the election's. Only a controller's build has it; the
// playground's wasm runs no pair (handback_js.go).

// handBackState is one edge's hand-back: asked once its store read
// failed and the takeover would be cold, before the takeover runs.
type handBackState struct {
	fn     atomic.Pointer[func(term uint64, cold string) (bool, string)]
	ch     chan handBackResult
	term   uint64
	since  time.Time
	done   bool
	handed bool
	why    string // the edge's reason for no hand-back, for the report
}

// handBackResult is what the hook answered.
type handBackResult struct {
	handed bool
	why    string
}

// handBackWait bounds the hook: past it the edge goes on, cold, without
// the hand-back. The hook's own deadline is far shorter (a few RTTs).
const handBackWait = 2 * time.Second

// SetHandBack registers the pair's hand-back: f(term, cold) runs off the
// scan on a takeover in term whose store read failed and that would go
// cold for the reason cold. It answers whether it handed the lead to the
// peer (the election's gate is closed by then, so this edge never scans),
// or why it did not. Call it before Run.
func (r *Runtime) SetHandBack(f func(term uint64, cold string) (handed bool, why string)) {
	r.strm.hb.fn.Store(&f)
}

// handBackReady answers, on a pending takeover in term, whether the
// takeover may run: at once when the store reads (it then keeps that
// read for the takeover's own load, as ioReady does) or the takeover
// would go hot; otherwise only once the hand-back said no, or never on
// this edge when it handed the lead back. Under leadMu, after
// flushReady and ioReady.
func (r *Runtime) handBackReady(term uint64) bool {
	hb := &r.strm.hb
	f := hb.fn.Load()
	if f == nil || r.retainStore == nil {
		return true
	}
	if hb.done && hb.term == term {
		if hb.handed && time.Since(hb.since) > 2*handBackWait {
			// The gate should have closed at once: a lease that stays
			// open after a hand-back is not left to run nothing.
			slog.Error("runtime: handed the lead back, but still leading; taking over cold", "waited", time.Since(hb.since).Round(time.Millisecond))
			hb.handed, hb.why = false, "handed the lead back, but this controller still led after it"
		}
		return !hb.handed
	}
	if hb.ch == nil {
		bad := r.iok.loadErr || r.strm.storeBad.Load()
		if !bad && r.iok.edge == nil {
			// The edge has not read the store yet (no I/O keeper): read it
			// here, once, and the takeover loads this read.
			st, err := r.retainStore.Load()
			if err != nil {
				r.strm.storeBad.Store(true)
				slog.Error("runtime: the retain store could not be read at the takeover", "store", r.retainStore.Kind(), "error", err)
				bad = true
			} else {
				r.iok.edge = &st
			}
		}
		if bad && !mutRuntime("handback_store_bad_forgotten") {
			// Latched until a carry writes the file: the failed read moved
			// a corrupt file aside, so a later read (this edge's I/O
			// retries, or the next edge after a hand-back that fell back)
			// finds "nothing retained", and the takeover must still know
			// its store failed (Fable, S2c-3 M1).
			r.strm.storeBad.Store(true)
		}
		// Decided once per edge: no question when the store reads, or the
		// takeover goes hot (#710 S2 Q3: it needs no file).
		goOn := !bad && !mutRuntime("handback_on_readable_store")
		cold := ""
		if !goOn {
			cold = r.hotColdBefore(r.strm.hot, r.strm.hotWhy)
			goOn = cold == "" && !mutRuntime("handback_when_hot")
		}
		if goOn {
			hb.done, hb.term, hb.handed, hb.why = true, term, false, ""
			return true
		}
		ch := make(chan handBackResult, 1)
		fn := *f
		go func() {
			handed, why := fn(term, cold)
			ch <- handBackResult{handed, why}
		}()
		hb.ch, hb.term, hb.since, hb.done, hb.handed, hb.why = ch, term, time.Now(), false, false, ""
	}
	select {
	case res := <-hb.ch:
		hb.ch, hb.done, hb.handed, hb.since = nil, true, res.handed, time.Now()
		if !res.handed {
			hb.why = res.why
		}
		if hb.term != term {
			hb.done = false // asked for an earlier term: ask again for this one
			return false
		}
		return !hb.handed
	default:
	}
	if time.Since(hb.since) < handBackWait {
		return false
	}
	slog.Warn("runtime: the hand-back has not answered; taking over cold", "waited", time.Since(hb.since).Round(time.Millisecond))
	hb.ch, hb.done, hb.handed, hb.why = nil, true, false, fmt.Sprintf("the hand-back did not answer within %s", handBackWait)
	return hb.term == term
}

// MarkRetainUnreadable makes the next takeover count this controller's
// retain store as failed though it reads (until a carry writes it): a pair
// whose file was kept aside as corrupt reads back as "nothing retained"
// after a restart, and a controller that handed back over it, then
// restarted, must not take that for a good file (#710 S2c-3, Fable M1).
func (r *Runtime) MarkRetainUnreadable() {
	if r.retainStore != nil {
		r.strm.storeBad.Store(true)
	}
}

// handBackReset forgets the edge's hand-back: this replica stands by, or
// the edge has been taken. Under leadMu.
func (r *Runtime) handBackReset() {
	hb := &r.strm.hb
	hb.ch, hb.done, hb.handed, hb.term = nil, false, false, 0
}

// handBackWhy takes the edge's reason for no hand-back, for the report.
// Under leadMu.
func (r *Runtime) handBackWhy() string {
	why := r.strm.hb.why
	r.strm.hb.why = ""
	return why
}

// hotColdBefore is, before the takeover runs and with this controller's
// own store unreadable, the reason it will be cold ("" for hot): the
// same checks hotTakeover makes before its install, on the same
// candidate.
func (r *Runtime) hotColdBefore(h *HotImage, why string) string {
	if cold := hotColdOf(h, why); cold != "" {
		return cold
	}
	var lead retain.State
	if h.Retain != nil {
		if err := json.Unmarshal(h.Retain, &lead); err != nil {
			return "the image's retain section: " + err.Error()
		}
	}
	return r.hotPrecheck(h, lead)
}

// hotColdOf is the candidate's own reason to be cold, before any check
// against the programs: none, refused, or too old ("" when none of
// these).
func hotColdOf(h *HotImage, why string) string {
	switch {
	case h == nil && why != "":
		return why
	case h == nil:
		return "no state image: the standby followed no leader"
	case h.Cold != "":
		return h.Cold
	case h.MaxAge <= 0 && !mutRuntime("hot_no_age_check"):
		return "redundancy.hot-max-age is 0: every takeover is cold"
	case h.Age > h.MaxAge && !mutRuntime("hot_no_age_check"):
		return fmt.Sprintf("the image was %s old at the election, over hot-max-age %s", h.Age.Round(time.Millisecond), h.MaxAge)
	}
	return ""
}
