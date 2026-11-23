package runtime

import (
	"fmt"
	"sync"
	"time"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Shadow is a pair standby's copy of its leader's state (#710 S1, design
// §4.1): the newest image applied to frames of the standby's own programs,
// which never run. Applying proves the image fits this build, slot for
// slot. A hot takeover (S2) continues from these frames; until then a
// takeover is cold, and only the retain section is used, through the
// standby's own retain store (retain.Carry).
//
// Safe for concurrent use: the stream applies, the status reads.
// Experimental, like Image.
type Shadow struct {
	r *Runtime

	mu    sync.Mutex
	term  uint64
	seq   uint64
	tasks map[string]*shadowTask
	// tags is the newest image's carried tags (imagetags.go), nil when
	// that image's could not be applied (another layout, format 1).
	tags *shadowTags
	// build is the newest image's Build, arrived when it was applied (the
	// runtime's clock, monotonic in production), last what Apply reported
	// for it: Take's (#710 S2).
	build   string
	arrived time.Time
	last    ShadowReport
}

type shadowTask struct {
	prog   *ir.Program // the program the frame was built for
	layout string
	frame  *ir.Frame // the newest applied image
	spare  *ir.Frame // where the next one is applied, then swapped in
	nowMs  int64
	scans  uint64
	source string // the image's TaskImage.Source
	// applied: frame holds an image, not the program's initial values.
	applied bool
}

// NewShadow returns an empty shadow over this runtime's programs.
func (r *Runtime) NewShadow() *Shadow {
	return &Shadow{r: r, tasks: map[string]*shadowTask{}}
}

// ShadowReport says what one Apply did with each task's frame.
type ShadowReport struct {
	// Applied are the tasks whose frames now hold the image's.
	Applied []string
	// LayoutDiffers are the tasks this build runs with another layout
	// (another program, or an online edit not here yet): their frames are
	// left as they were, and a takeover would be cold for them.
	LayoutDiffers []string
	// Missing are this build's tasks the image has no frame for; Extra the
	// image's tasks this build does not run.
	Missing []string
	Extra   []string
	// TagsDiffer: the leader carries another set of tags (another
	// manifest, other types), or its tags' leaves did not fit here; the
	// shadow holds none of its tags. TagsMissing: the image carries no
	// tags (format 1, a leader of a build before #710 S2). Either way a
	// takeover would not be hot.
	TagsDiffer  bool
	TagsMissing bool
}

// InSync reports whether every task's frame and the carried tags were
// applied.
func (s ShadowReport) InSync() bool {
	return len(s.LayoutDiffers) == 0 && len(s.Missing) == 0 && len(s.Extra) == 0 && !s.TagsDiffer && !s.TagsMissing
}

// Apply applies the image's frames to the shadow, each to a frame of this
// runtime's program of the same task and layout; a task whose layout
// differs keeps its previous frame. It refuses an image of an older term
// than one it already applied, or of the same term and an older Seq: the
// stream's order is (term, seq), and an image out of it is never applied
// (design §4.1; the caller's term check is the first guard, this the
// second). A frame whose leaves do not fit (ir.ErrLeaves) leaves that
// task's frame as it was and is an error, since the layouts matched.
//
// A task this image did not apply (another layout, leaves that did not
// fit, absent from the image) has no current frame from then on: its older
// frame is not offered as this image's (frameOf, and S2's takeover).
func (s *Shadow) Apply(img *Image) (ShadowReport, error) {
	// Each program's compiled program is read before s.mu: a scan holds
	// the program's lock for its whole run, and a status read of the
	// shadow must not wait for one.
	progs := s.r.namedPrograms()
	compiled := make([]*ir.Program, len(progs))
	for i, np := range progs {
		np.prog.mu.Lock()
		compiled[i] = np.prog.prog
		np.prog.mu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !mutRuntime("shadow_older_image_applied") && (img.Term < s.term || (img.Term == s.term && img.Seq < s.seq)) {
		return ShadowReport{}, fmt.Errorf("runtime: image (term %d, seq %d) is older than the shadow's (term %d, seq %d)", img.Term, img.Seq, s.term, s.seq)
	}
	var rep ShadowReport
	inImage := make(map[string]bool, len(img.Tasks))
	byTask := make(map[string]*TaskImage, len(img.Tasks))
	for i := range img.Tasks {
		inImage[img.Tasks[i].Task] = true
		byTask[img.Tasks[i].Task] = &img.Tasks[i]
	}
	var firstErr error
	for i, np := range progs {
		ti := byTask[np.name]
		delete(inImage, np.name)
		st := s.taskFor(np.name, compiled[i])
		if ti == nil {
			s.stale(st)
			rep.Missing = append(rep.Missing, np.name)
			continue
		}
		if st.layout != ti.Layout && !mutRuntime("shadow_layout_unchecked") {
			s.stale(st)
			rep.LayoutDiffers = append(rep.LayoutDiffers, np.name)
			continue
		}
		if err := ir.ApplyLeaves(st.spare, ti.Leaves); err != nil {
			st.spare = ir.NewFrame(st.prog) // partly written: start the next one clean
			s.stale(st)
			if firstErr == nil {
				firstErr = fmt.Errorf("runtime: task %s: %w", np.name, err)
			}
			rep.LayoutDiffers = append(rep.LayoutDiffers, np.name)
			continue
		}
		st.frame, st.spare = st.spare, st.frame
		st.nowMs, st.scans, st.source, st.applied = ti.NowMs, ti.Scans, ti.Source, true
		rep.Applied = append(rep.Applied, np.name)
	}
	for _, ti := range img.Tasks {
		if inImage[ti.Task] {
			rep.Extra = append(rep.Extra, ti.Task)
		}
	}
	s.applyTagsLocked(img, &rep, &firstErr)
	// The position moves even when a task's frame did not fit: the image
	// was the newest, and its other tasks are applied.
	s.term, s.seq = img.Term, img.Seq
	s.build, s.arrived, s.last = img.Build, s.r.now(time.Now()), rep
	return rep, firstErr
}

// applyTagsLocked applies the image's carried tags when its tags' layout
// is this controller's, all or none; otherwise the shadow holds no tags
// (an older image's are not this one's). The caller holds s.mu.
func (s *Shadow) applyTagsLocked(img *Image, rep *ShadowReport, firstErr *error) {
	cs := s.r.carried
	s.tags = nil
	switch {
	case cs == nil:
		return
	case img.Format < 2:
		rep.TagsMissing = true
		return
	case img.TagsLayout != cs.layout() && !mutRuntime("shadow_tags_layout_unchecked"):
		rep.TagsDiffer = true
		return
	}
	st, err := applyTags(cs, img)
	if err != nil {
		rep.TagsDiffer = true
		if *firstErr == nil {
			*firstErr = err
		}
		return
	}
	s.tags = st
}

// tagsOf is the shadow's carried tags by ir.NameKey, nil when it holds
// none (tests).
func (s *Shadow) tagsOf() map[string]ir.Value {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tags == nil {
		return nil
	}
	out := map[string]ir.Value{}
	for i, ok := range s.tags.have {
		if ok {
			out[s.r.carried.keys[i]] = s.tags.vals[i]
		}
	}
	return out
}

// stale marks a task's frame as not this image's. The caller holds s.mu.
func (s *Shadow) stale(st *shadowTask) {
	if !mutRuntime("shadow_stale_frame_kept") {
		st.applied = false
	}
}

// taskFor is the shadow of one task, rebuilt when the program it was built
// for was replaced (an online edit, a load). The caller holds s.mu.
func (s *Shadow) taskFor(task string, prog *ir.Program) *shadowTask {
	st := s.tasks[task]
	if st != nil && st.prog == prog {
		return st
	}
	st = &shadowTask{prog: prog, layout: prog.Layout(), frame: ir.NewFrame(prog), spare: ir.NewFrame(prog)}
	s.tasks[task] = st
	return st
}

// Position is the (term, seq) of the newest image applied; zero before
// the first.
func (s *Shadow) Position() (term, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.term, s.seq
}

// Reset empties the shadow: a controller that leads has no image of its
// own leadership to keep, and one from before it must never be applied
// over what it did since (design §4.3).
func (s *Shadow) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mutRuntime("shadow_kept_after_leading") {
		return
	}
	s.term, s.seq = 0, 0
	s.tasks = map[string]*shadowTask{}
	s.tags = nil
	s.build, s.arrived, s.last = "", time.Time{}, ShadowReport{}
}

// frameOf is a task's shadow frame, nil when none was applied (tests).
func (s *Shadow) frameOf(task string) (*ir.Frame, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.tasks[task]
	if st == nil || !st.applied {
		return nil, 0
	}
	return st.frame, st.nowMs
}

// Take is the shadow's newest image as a candidate for a hot takeover
// (#710 S2), nil when it holds none: every task's frame copied out by
// value (leaves, never the shadow's frame), its carried tags, its
// position, build and age on this controller's clock. A shadow that is
// not in sync (a task of another layout, one the image lacks or this
// build lacks, tags another set or none) gives a candidate whose Cold
// says why. The pair's takeover flush takes it, after stopping the
// follower, before Reset.
func (s *Shadow) Take() *HotImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term == 0 && s.seq == 0 {
		return nil
	}
	h := &HotImage{Term: s.term, Seq: s.seq, Build: s.build, Age: s.r.now(time.Now()).Sub(s.arrived), arrived: s.arrived, tasks: map[string]hotTask{}}
	rep := s.last
	switch {
	case len(rep.LayoutDiffers) > 0:
		h.Cold = fmt.Sprintf("tasks %v run another program here than in the image", rep.LayoutDiffers)
	case len(rep.Missing) > 0:
		h.Cold = fmt.Sprintf("the image has no frame of tasks %v", rep.Missing)
	case len(rep.Extra) > 0:
		h.Cold = fmt.Sprintf("the image has tasks %v this controller does not run", rep.Extra)
	case rep.TagsMissing:
		h.Cold = "the image carries no tags (a leader of an older nautilus)"
	case rep.TagsDiffer:
		h.Cold = "the image carries another set of tags (another manifest, or other tag types)"
	}
	for name, st := range s.tasks {
		if !st.applied {
			if h.Cold == "" {
				h.Cold = "task " + name + " holds no frame of the newest image"
			}
			continue
		}
		h.tasks[name] = hotTask{layout: st.layout, source: st.source, nowMs: st.nowMs, leaves: ir.FlattenInto(nil, st.frame)}
	}
	if s.r.carried != nil {
		h.tags = s.tags // Apply makes a fresh one for every image: never written again
		if h.tags == nil && h.Cold == "" {
			h.Cold = "the shadow holds no tags of the newest image"
		}
	}
	return h
}
