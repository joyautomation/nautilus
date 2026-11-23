package runtime

import (
	"encoding/json"
	"sync"
	"sync/atomic"

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
}

// imageCap is one program's frame copy: requested by the stream, taken by
// the scan at its end (captureLocked, a copy of values and nothing else),
// collected by the stream (takeImage). The two leaf buffers ping-pong
// between the program and the stream's Image, so after the first copies
// neither side allocates.
type imageCap struct {
	want atomic.Bool

	mu     sync.Mutex
	ready  bool
	leaves []ir.Leaf
	prog   *ir.Program
	nowMs  int64
	scans  uint64
	// layout caches layoutOf.Layout(): hashed once per compile, by the
	// stream, never by the scan.
	layoutOf *ir.Program
	layout   string
}

// captureLocked copies the frame if the stream asked. The caller (Run)
// holds p.mu, at the end of a scan.
func (p *Program) captureLocked(tags *Tags) {
	if mutRuntime("image_copy_in_scan_encodes") {
		_ = p.prog.Layout() // the hash inside the scan, which guard (a) forbids
	}
	c := &p.img
	c.mu.Lock()
	c.leaves = ir.FlattenInto(c.leaves, p.frame)
	c.prog, c.nowMs, c.scans, c.ready = p.prog, tags.NowMs(), p.scans, true
	c.want.Store(false)
	c.mu.Unlock()
}

// requestImage asks for a copy at the end of the next scan.
func (p *Program) requestImage() { p.img.want.Store(true) }

// imageReady reports whether a copy waits to be taken.
func (p *Program) imageReady() bool {
	p.img.mu.Lock()
	defer p.img.mu.Unlock()
	return p.img.ready
}

// takeImage moves the waiting copy into dst, handing dst's old leaf buffer
// back for the next copy. False when there is none.
func (p *Program) takeImage(task string, dst *TaskImage) bool {
	c := &p.img
	c.mu.Lock()
	if !c.ready {
		c.mu.Unlock()
		return false
	}
	prog := c.prog
	known := c.layoutOf == prog
	c.mu.Unlock()
	layout := ""
	if !known {
		layout = prog.Layout() // outside c.mu: a scan may copy meanwhile
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready || c.prog != prog {
		return false // a newer program's copy replaced it; take that one next
	}
	if !known {
		c.layoutOf, c.layout = prog, layout
	}
	dst.Task, dst.Layout, dst.NowMs, dst.Scans = task, c.layout, c.nowMs, c.scans
	dst.Leaves, c.leaves = c.leaves, dst.Leaves[:0]
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

// RequestImage asks every task's program to copy its frame at the end of
// its next scan. The copy is values only (ir.FlattenInto into a buffer it
// reuses); the hashing and encoding happen in TakeImage and EncodeImage,
// on the caller's goroutine.
func (r *Runtime) RequestImage() {
	r.prog.requestImage()
	for _, tr := range r.tasks {
		tr.prog.requestImage()
	}
}

// TakeImage moves the frame copies into img once every task has made one
// since RequestImage, and reports whether it did. It fills Tasks and Seq;
// Term, Build and Retain are the caller's (RetainImage). img's leaf buffers
// go back to the programs for their next copies, so a caller that keeps
// one Image allocates nothing after the first rounds.
func (r *Runtime) TakeImage(img *Image) bool {
	progs := r.namedPrograms()
	for _, np := range progs {
		if !np.prog.imageReady() {
			return false
		}
	}
	if cap(img.Tasks) < len(progs) {
		img.Tasks = append(img.Tasks[:cap(img.Tasks)], make([]TaskImage, len(progs)-cap(img.Tasks))...)
	}
	img.Tasks = img.Tasks[:len(progs)]
	for i, np := range progs {
		if !np.prog.takeImage(np.name, &img.Tasks[i]) {
			// A swap replaced a program's copy between the check and the
			// take: the tasks taken so far are spent, so ask every task
			// again rather than mix two rounds in one image.
			r.RequestImage()
			return false
		}
	}
	img.Seq = r.imageSeq.Add(1)
	if mutRuntime("image_seq_from_scans") {
		img.Seq = img.Tasks[0].Scans
	}
	return true
}

// RetainImage is the retain section of an Image: the state this
// controller's saver writes, as JSON.
func (r *Runtime) RetainImage() ([]byte, error) {
	return json.Marshal(r.retainState())
}

// ImageNow is an Image of every task's frame as it stands, copied without
// a scan, with the retain section: what naut check sizes a pair's state
// stream by. Term and Seq are zero. Not for a running runtime's stream,
// whose copies come from the end of a scan (RequestImage, TakeImage).
func (r *Runtime) ImageNow() (*Image, error) {
	img := &Image{}
	for _, np := range r.namedPrograms() {
		p := np.prog
		p.mu.Lock()
		ti := TaskImage{Task: np.name, Leaves: ir.FlattenInto(nil, p.frame), Scans: p.scans}
		prog := p.prog
		p.mu.Unlock()
		ti.Layout = prog.Layout()
		img.Tasks = append(img.Tasks, ti)
	}
	ret, err := r.RetainImage()
	if err != nil {
		return nil, err
	}
	img.Retain = ret
	return img, nil
}
