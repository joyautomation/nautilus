package runtime

import (
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// A pair's hot takeover (#710 S2b, design §4.2): the new leader goes on
// from its standby's copy of the old leader's state (Shadow) instead of
// resetting every frame. The pair's takeover flush hands the runtime one
// candidate (Shadow.Take, SetTakeoverFlush); the takeover installs it, or
// goes cold exactly as before and says why (LastTakeover).
//
// Hot only when ALL of these hold:
//   - an image exists, its every task's frame and its tags applied;
//   - the image is of this build (the pair checks, HotImage.Cold);
//   - it was at most MaxAge old at the election (hot-max-age; 0 = never);
//   - the flush ended in time, and kept the image in the retain file or
//     found this controller's own store unreadable (ErrRetainUnreadable);
//   - after the retain load (which may swap in a carried online edit),
//     every task this controller runs has the image's layout and source.
//
// All tasks hot or none: tags the tasks share make a mix unsafe.
//
// Experimental, like Image.

// HotImage is a candidate for a hot takeover: one image as a standby's
// shadow held it (Shadow.Take). Experimental, like Image.
type HotImage struct {
	Term, Seq uint64
	// Build is the image's Image.Build.
	Build string
	// Age is how old the image was when it was taken (at the election
	// edge), on this controller's clock: the time since it arrived.
	Age time.Duration
	// MaxAge is the oldest image that goes hot (redundancy.hot-max-age);
	// 0 means never hot. The pair sets it.
	MaxAge time.Duration
	// Retain is the image's retain section (retain.State as JSON): the
	// takeover loads it in memory when this controller's own retain store
	// cannot be read (#710 S2 Q3). The pair sets it.
	Retain []byte
	// Cold, when set, says why this image cannot go hot.
	Cold string

	arrived time.Time // on the runtime's clock, monotonic in production
	refMs   int64     // the latest leader time in the image (Shadow.refMs)
	tasks   map[string]hotTask
	tags    *shadowTags
}

// hotTask is one task's frame in a candidate, by value.
type hotTask struct {
	layout string
	source string
	nowMs  int64
	leaves []ir.Leaf
}

// TakeoverReport is what a pair's takeover did (LastTakeover).
// Experimental, like Image.
type TakeoverReport struct {
	Term uint64
	At   time.Time
	// Hot: the takeover went on from the image. Otherwise Reason says
	// why it was cold, and Image whether there was an image at all (a
	// plain first start has none, and is no fault).
	Hot    bool
	Reason string
	Image  bool
	// ImageAge is the image's age at the election, -1 without one: what
	// hot-max-age judged. OldestFrameAge is, on a hot takeover, how old
	// the oldest task's frame was at the install, on the leader's clock
	// carried over (a fast task copies its frame before a slow one has
	// scanned): hot-max-age is judged on the image, and a fast task's
	// state can be older by up to the slowest task's period.
	ImageAge       time.Duration
	OldestFrameAge time.Duration
	// RetainFromImage: this controller's own retain store could not be
	// read, so its retained values are the image's, held only in memory
	// until a save succeeds (RetainOnlyInMemory).
	RetainFromImage bool
	// NoHandBack is, on a cold takeover whose own retain store could not
	// be read, why the lead was not handed to the peer (#710 S2c-3), ""
	// when the hand-back was not asked.
	NoHandBack string
}

// LastTakeover is the report of this runtime's last takeover with a
// takeover flush registered (a pair's); the zero report before one.
func (r *Runtime) LastTakeover() TakeoverReport {
	r.strm.reportMu.Lock()
	defer r.strm.reportMu.Unlock()
	return r.strm.report
}

// SetTakeoverReport registers f, called (on its own goroutine) with each
// takeover's report once the takeover is done: the pair journals it.
// Call it before Run.
func (r *Runtime) SetTakeoverReport(f func(TakeoverReport)) {
	if f == nil {
		r.strm.onTakeover.Store(nil)
		return
	}
	r.strm.onTakeover.Store(&f)
}

// RetainOnlyInMemory reports whether this controller's retained values
// are held only in memory: its takeover went hot over a retain store it
// could not read, and no save has succeeded since. A power cycle now
// loses them.
func (r *Runtime) RetainOnlyInMemory() bool { return r.strm.memOnly.Load() }

// noteTakeover records a pair's takeover report and hands it to the hook.
func (r *Runtime) noteTakeover(rep TakeoverReport) {
	r.strm.reportMu.Lock()
	r.strm.report = rep
	r.strm.reportMu.Unlock()
	if f := r.strm.onTakeover.Load(); f != nil {
		go (*f)(rep)
	}
}
