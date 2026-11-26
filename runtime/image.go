package runtime

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Image is one copy of this controller's state for a redundant pair's
// standby (the state stream, #710 S1, design §4.1): what a hot takeover
// would continue from.
//
//   - Retain is the retain section: retain.State as JSON, the same state
//     this controller's retain saver writes (RetainImage). The standby
//     persists the part a pair carries (retain.Carry) to its own store,
//     by name, whatever its build.
//   - Tasks are the program frames, one per task (the main program
//     first): every variable, function-block internals (timers, counters,
//     edges), SFC steps (an SFC lowers to ST, so its steps are frame
//     slots), flattened by ir.FlattenInto. A standby applies one only to a
//     program of the same Layout.
//
// Each task's frame is copied at the end of that task's own scan, so the
// frames of two tasks are not of one instant, as their scans never are.
//
// Experimental: Image, TaskImage, Shadow and the functions that make and
// read them serve the pair's state stream while redundancy is
// experimental, and are not part of the stable API (CONTRIBUTING.md, "Go
// API") until it is not.
type Image struct {
	// Term is the leadership the image belongs to; a standby applies an
	// image only of the term its own voter knows the leader holds.
	Term uint64
	// Seq is the image's number in this process's stream: it only grows
	// (an online edit resets a program's scan count, never this), so
	// (Term, Seq) orders the images of one leadership.
	Seq uint64
	// Build names the leader's nautilus build, for the status and for S2.
	Build  string
	Retain []byte
	Tasks  []TaskImage
	// Format is the wire format the image was decoded from (DecodeImage),
	// or is to be encoded in (EncodeImageFormat): 1 carries no tags and no
	// sources, so a standby never takes over hot on it (#710 S2).
	Format byte
	// TagsLayout is the leader's carried tags' ir.TagsLayout: a standby
	// applies the tag sections only when its own is the same.
	TagsLayout string
	// Tags are the carried tags no program writes (an operator's, a
	// client's), copied from the store when the image is taken, at the
	// store's time base TagsNowMs. The tags a program writes are in its
	// task's TaskImage.Tags, copied at the end of its scan with its frame.
	TagsNowMs int64
	Tags      TagValues
	// Holding says the leader's takeover hold held when the image was
	// taken, HoldAge for how long (#710 (b2)): a hot takeover from it
	// inherits the hold, age and all. Format 3 on.
	Holding bool
	HoldAge time.Duration
	// EdgeSession is the leader's edge node session as its standby must
	// know it (#798): the bdSeq of its Sparkplug session plus one, 0 when
	// it has none. A standby that takes over starts its own session with
	// another bdSeq, so a late will of this one is never taken for its
	// death. Set by the stream, not by the runtime. Format 4 on.
	EdgeSession uint64
}

// TaskImage is one task's program frame. Experimental, like Image.
type TaskImage struct {
	Task string
	// Layout is ir.Program.Layout of the program the frame belongs to.
	Layout string
	// NowMs is the time base the program's timers counted from at the copy
	// (Tags.NowMs: the leader's wall clock in milliseconds). A timer's
	// internals are absolute on that base, so a takeover that continues
	// them rebases them by the image's age (S2).
	NowMs int64
	// Scans is how many scans the program had completed before the one the
	// copy ends: for reading only (an online edit resets it), never for
	// ordering, which is Image.Seq's.
	Scans  uint64
	Leaves []ir.Leaf
	// Source is the hash of the program source the task ran at the copy
	// (Program.Hash's form): the layout leaves bodies out, and a hot
	// takeover needs the same running source too (#710 S2).
	Source string
	// Tags are the carried tags this task's program writes, as the store
	// held them right after the scan the frame was copied at.
	Tags TagValues
}

// imageCap is one program's frame copy: requested by the stream, taken by
// the scan at its end (captureLocked, a copy of values and nothing else),
// collected by the stream (takeImage). The two leaf buffers ping-pong
// between the program and the stream's Image, so after the first copies
// neither side allocates.
type imageCap struct {
	want atomic.Bool
	// reqStamp is the stamp the next copy carries (imageStamp), stored
	// before want, so a scan that sees want sees a stamp at least as new.
	reqStamp atomic.Uint64

	mu     sync.Mutex
	ready  bool
	stamp  uint64 // the request the copy served (imageStamp)
	order  uint64 // when the copy was made, among all copies (imageCopyOrder)
	leaves []ir.Leaf
	prog   *ir.Program
	nowMs  int64
	scans  uint64
	src    string    // the program's source at the copy (hashed by the stream)
	tags   TagValues // the carried tags the program writes, after its commit
	// layout and source cache layoutOf.Layout() and the source's hash:
	// hashed once per compile, by the stream, never by the scan.
	layoutOf *ir.Program
	layout   string
	source   string
}

// imageStamp names one request: the round (RequestImage starts one) and
// the phase within it, 0 for the first copies and 1 for the re-copy of the
// faster tasks. A copy carries the stamp of the request it served, so an
// image takes only copies made for its own request, never one left over
// from a round whose image was not taken.
func imageStamp(round uint64, phase int) uint64 { return round<<1 | uint64(phase) }

// imageCopyOrder numbers the copies in the order they are made, across
// programs, so TakeImage can tell which task's copy came last. Only a scan
// that copies touches it.
var imageCopyOrder atomic.Uint64

// stampCurrent reports whether a copy of stamp is the one wanted.
func stampCurrent(stamp, want uint64) bool {
	if mutRuntime("copy_from_older_round") {
		// An older round's copy of the same phase passes.
		return stamp&1 == want&1 && stamp>>1 <= want>>1
	}
	return stamp == want
}

// captureLocked copies the frame if the stream asked. The caller (Run)
// holds p.mu, at the end of a scan.
func (p *Program) captureLocked(tags *Tags) {
	if mutRuntime("image_copy_in_scan_encodes") {
		_ = p.prog.Layout() // the hash inside the scan, which guard (a) forbids
	}
	c := &p.img
	// Clear the request before reading its stamp: a request that comes in
	// meanwhile sets want again and the next scan serves it, and one that
	// came before is the stamp this copy carries.
	c.want.Store(false)
	c.mu.Lock()
	c.stamp, c.order = c.reqStamp.Load(), imageCopyOrder.Add(1)
	c.leaves = ir.FlattenInto(c.leaves, p.frame)
	c.prog, c.nowMs, c.scans, c.src, c.ready = p.prog, tags.NowMs(), p.scans, p.source, true
	// The tags the program writes, as its commit just left them: one
	// instant with the frame, so a latch in the frame and the state tag it
	// set never come from two different scans.
	c.tags.reset()
	if p.carry != nil && !mutRuntime("image_tags_at_take") {
		tags.mu.RLock()
		tags.copyTagsLocked(p.carry, p.carryIdx, &c.tags)
		tags.mu.RUnlock()
	}
	c.mu.Unlock()
}

// requestImage asks for a copy at the end of the next scan, stamped so.
func (p *Program) requestImage(stamp uint64) {
	p.img.reqStamp.Store(stamp)
	p.img.want.Store(true)
}

// imageReady reports whether a copy of this stamp waits to be taken, and
// when it was made (imageCopyOrder).
func (p *Program) imageReady(stamp uint64) (order uint64, ok bool) {
	p.img.mu.Lock()
	defer p.img.mu.Unlock()
	return p.img.order, p.img.ready && stampCurrent(p.img.stamp, stamp)
}

// takeImage moves the waiting copy into dst, handing dst's old leaf buffer
// back for the next copy. False when there is none.
func (p *Program) takeImage(task string, dst *TaskImage, stamp uint64) bool {
	c := &p.img
	c.mu.Lock()
	if !c.ready || !stampCurrent(c.stamp, stamp) {
		c.mu.Unlock()
		return false
	}
	prog, src := c.prog, c.src
	known := c.layoutOf == prog
	c.mu.Unlock()
	layout, source := "", ""
	if !known {
		layout, source = prog.Layout(), sourceHash(src) // outside c.mu: a scan may copy meanwhile
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready || c.prog != prog || !stampCurrent(c.stamp, stamp) {
		return false // a newer program's copy replaced it; take that one next
	}
	if !known {
		c.layoutOf, c.layout, c.source = prog, layout, source
	}
	dst.Task, dst.Layout, dst.NowMs, dst.Scans, dst.Source = task, c.layout, c.nowMs, c.scans, c.source
	if mutRuntime("image_source_unhashed") {
		dst.Source = ""
	}
	dst.Leaves, c.leaves = c.leaves, dst.Leaves[:0]
	dst.Tags, c.tags = c.tags, dst.Tags
	c.tags.reset()
	c.ready = false
	return true
}

// namedPrograms is every task's program, the main one first, as an Image
// lists them.
func (r *Runtime) namedPrograms() []namedProgram {
	out := make([]namedProgram, 0, 1+len(r.tasks))
	out = append(out, namedProgram{MainTaskName, r.prog})
	for _, tr := range r.tasks {
		out = append(out, namedProgram{tr.name, tr.prog})
	}
	return out
}

// imageSeqs numbers the images TakeImage makes (Image.Seq).
type imageSeqs = atomic.Uint64

type namedProgram struct {
	name string
	prog *Program
}

// RequestImage starts a round: it asks every task's program to copy its
// frame at the end of its next scan. The copy is values only
// (ir.FlattenInto into a buffer it reuses); the hashing and encoding happen
// in TakeImage and EncodeImage, on the caller's goroutine.
//
// A new round supersedes the last: a copy made for an earlier round, whose
// image was never taken, is not taken into this one.
func (r *Runtime) RequestImage() {
	r.imgMu.Lock()
	defer r.imgMu.Unlock()
	r.requestImageLocked()
}

func (r *Runtime) requestImageLocked() {
	r.imgRound++
	r.imgPhase = 0
	r.imgSlowest = -1
	st := imageStamp(r.imgRound, 0)
	r.prog.requestImage(st)
	for _, tr := range r.tasks {
		tr.prog.requestImage(st)
	}
}

// TakeImage moves the frame copies into img once every task has made one
// for the current round, and reports whether it did. It fills Tasks and
// Seq; Term, Build and Retain are the caller's (RetainImage). img's leaf
// buffers go back to the programs for their next copies, so a caller that
// keeps one Image allocates nothing after the first rounds. Callers loop on
// it until it is true.
//
// With more than one task, the first complete set of copies is not taken:
// a fast task copied right after the request and has run on until the
// slowest task's copy, up to that task's period. TakeImage asks every task
// but the one that copied last to copy again, and takes the image when they
// have. Each task's state is then at most one of its own scan periods older
// than the image, and the re-copies are all from after the slowest copy. A
// runtime with one task does no second round.
func (r *Runtime) TakeImage(img *Image) bool {
	r.imgMu.Lock()
	defer r.imgMu.Unlock()
	if r.imgRound == 0 || r.imgPhase > 1 {
		return false // not asked for, or already taken
	}
	progs := r.namedPrograms()
	// One stamp per task, in a slice kept under imgMu: the stream asks
	// every few milliseconds, and allocates nothing after the first rounds.
	r.imgStamps = append(r.imgStamps[:0], make([]uint64, len(progs))...)
	stamps := r.imgStamps
	for i := range stamps {
		stamps[i] = imageStamp(r.imgRound, 0)
		if r.imgPhase == 1 && i != r.imgSlowest {
			stamps[i] = imageStamp(r.imgRound, 1)
		}
	}
	var last uint64
	slowest := -1
	for i, np := range progs {
		order, ok := np.prog.imageReady(stamps[i])
		if !ok {
			return false
		}
		if order >= last {
			last, slowest = order, i
		}
	}
	if r.imgPhase == 0 && len(progs) > 1 && !mutRuntime("copy_first_scan_only") {
		// Every task has copied, the slowest last: ask the others again.
		r.imgPhase, r.imgSlowest = 1, slowest
		st := imageStamp(r.imgRound, 1)
		for i, np := range progs {
			if i != slowest {
				np.prog.requestImage(st)
			}
		}
		return false
	}
	if cap(img.Tasks) < len(progs) {
		img.Tasks = append(img.Tasks[:cap(img.Tasks)], make([]TaskImage, len(progs)-cap(img.Tasks))...)
	}
	img.Tasks = img.Tasks[:len(progs)]
	for i, np := range progs {
		if !np.prog.takeImage(np.name, &img.Tasks[i], stamps[i]) {
			// A swap replaced a program's copy between the check and the
			// take: the tasks taken so far are spent, so start a new round
			// rather than mix two rounds in one image.
			r.requestImageLocked()
			return false
		}
	}
	// The round is done: nothing is still asked for.
	for _, np := range progs {
		np.prog.img.want.Store(false)
	}
	r.imgPhase = 2
	img.Seq = r.imageSeq.Add(1)
	if mutRuntime("image_seq_from_scans") {
		img.Seq = img.Tasks[0].Scans
	}
	r.takeTags(img, progs)
	return true
}

// takeTags fills the image's tag sections that are not a scan's: the
// carried tags no program writes, from the store now. (Under the mutation
// image_tags_at_take, every task's too, from the store now rather than at
// its scan's end.)
func (r *Runtime) takeTags(img *Image, progs []namedProgram) {
	img.Format, img.TagsLayout, img.TagsNowMs = imageFormat, "", 0
	img.Tags.reset()
	age := r.holdAge()
	img.Holding, img.HoldAge = age >= 0, max(age, 0)
	if r.carried == nil {
		return
	}
	img.TagsLayout, img.TagsNowMs = r.carried.layout(), r.tags.NowMs()
	rest := r.restIdx()
	var idxs [][]int // read before the store's lock: a scan takes p.mu, then it
	if mutRuntime("image_tags_at_take") {
		for _, np := range progs {
			np.prog.mu.Lock()
			idxs = append(idxs, np.prog.carryIdx)
			np.prog.mu.Unlock()
		}
	}
	r.tags.mu.RLock()
	defer r.tags.mu.RUnlock()
	r.tags.copyTagsLocked(r.carried, rest, &img.Tags)
	for i, idx := range idxs {
		img.Tasks[i].Tags.reset()
		r.tags.copyTagsLocked(r.carried, idx, &img.Tasks[i].Tags)
	}
}

// RetainImage is the retain section of an Image: the state this
// controller's saver writes, as JSON.
func (r *Runtime) RetainImage() ([]byte, error) {
	return json.Marshal(r.retainState())
}

// ImageNow is an Image of every task's frame as it stands, copied without
// a scan, with the retain section: what naut check sizes a pair's state
// stream by. Term and Seq are zero. Not for a running runtime's stream,
// whose copies come from the end of a scan (RequestImage, TakeImage),
// except a switchover's final image, taken once the scans have stopped
// (FinalImage, switch.go).
func (r *Runtime) ImageNow() (*Image, error) {
	img := &Image{}
	for _, np := range r.namedPrograms() {
		p := np.prog
		p.mu.Lock()
		ti := TaskImage{Task: np.name, Leaves: ir.FlattenInto(nil, p.frame), Scans: p.scans, Source: sourceHash(p.source), NowMs: r.tags.NowMs()}
		prog, idx := p.prog, p.carryIdx
		p.mu.Unlock()
		ti.Layout = prog.Layout()
		if r.carried != nil {
			r.tags.mu.RLock()
			r.tags.copyTagsLocked(r.carried, idx, &ti.Tags)
			r.tags.mu.RUnlock()
		}
		img.Tasks = append(img.Tasks, ti)
	}
	r.takeTags(img, nil)
	ret, err := r.RetainImage()
	if err != nil {
		return nil, err
	}
	img.Retain = ret
	return img, nil
}
