package runtime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The tags a pair's state image carries (#710 S2, naut-lead's ruling
// 2026-11-23): every tag the field does not provide, whoever writes it, a
// program or an operator. So a hot takeover keeps a state tag a program
// latched and a mode an operator switched on the HMI, retained or not. Not
// carried:
//
//   - inputs (RoleInput, Options.Inputs, Comms inputs): they come back from
//     the field;
//   - outputs (RoleOutput, Options.Outputs): the first scan recomputes them;
//   - the substitute companions the runtime owns (#707): the log owns
//     substitutes.
//
// What must survive a COLD takeover still belongs in retain: the guide
// says so.

// carrySet is the runtime's carried tags, fixed in New: keys sorted, each
// with the type a standby applies its leaves to, and the set's layout.
type carrySet struct {
	keys   []string   // ir.NameKey of each tag, sorted
	types  []*ir.Type // the type each is applied to
	index  map[string]int
	layout string // ir.TagsLayout(keys, types)
}

// TagValues is a set of carried tags' values in an image: Idx[i] is the
// tag's place in the carried set (both controllers order it alike when
// their TagsLayouts match), its leaves Leaves[Ends[i-1]:Ends[i]].
// Experimental, like Image.
type TagValues struct {
	Idx    []int
	Ends   []int
	Leaves []ir.Leaf
}

// Len is the number of tags.
func (tv *TagValues) Len() int { return len(tv.Idx) }

// leavesOf is tag i's leaves.
func (tv *TagValues) leavesOf(i int) []ir.Leaf {
	start := 0
	if i > 0 {
		start = tv.Ends[i-1]
	}
	return tv.Leaves[start:tv.Ends[i]]
}

func (tv *TagValues) reset() {
	tv.Idx, tv.Ends, tv.Leaves = tv.Idx[:0], tv.Ends[:0], tv.Leaves[:0]
}

// buildCarrySet is New's: every tag the store holds or a program binds,
// less the inputs, the outputs and the runtime's own companions. A tag
// with no declared type is carried by the type of the value it was seeded
// with, when that is elementary; one with neither is not carried.
//
// The dt-tags (the runtime writes each scan's dt there) are not carried
// either: the first scan writes them again.
func buildCarrySet(t *Tags, inputs, outputs, dtTags []string) *carrySet {
	notCarried := map[string]bool{}
	for _, n := range dtTags {
		if n != "" {
			notCarried[ir.NameKey(n)] = true
		}
	}
	if !mutRuntime("image_tags_carry_io") {
		for _, n := range append(slices.Clone(inputs), outputs...) {
			notCarried[ir.NameKey(rootOf(n))] = true
		}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	types := map[string]*ir.Type{}
	for key, ty := range t.types {
		types[key] = ty
	}
	for name, tv := range t.vals {
		key := ir.NameKey(name)
		if tv.owned || tv.out && !mutRuntime("image_tags_carry_io") {
			notCarried[key] = true
			continue
		}
		if _, ok := types[key]; !ok {
			types[key] = elementaryType(tv.v.Kind)
		}
	}
	cs := &carrySet{index: map[string]int{}}
	for key, ty := range types {
		if notCarried[key] || ty == nil || holdsFB(ty, 0) {
			continue
		}
		cs.keys = append(cs.keys, key)
	}
	slices.Sort(cs.keys)
	for i, key := range cs.keys {
		cs.types = append(cs.types, types[key])
		cs.index[key] = i
	}
	cs.layout = ir.TagsLayout(cs.keys, cs.types)
	return cs
}

// holdsFB reports whether a value of t holds a function-block instance at
// any depth. Such a tag is not carried: an instance's clock slots are
// rebased only in frames (ir.RebaseFrame), and an FB bound as a global is
// shared state New already warns about.
func holdsFB(t *ir.Type, depth int) bool {
	if t == nil || depth > 64 {
		return false
	}
	switch t.Kind {
	case ir.TypeFB:
		return true
	case ir.TypeArray:
		return holdsFB(t.Elem, depth+1)
	case ir.TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				if holdsFB(f.Type, depth+1) {
					return true
				}
			}
		}
	}
	return false
}

// rootOf is the tag a name or member path ("P101.Speed", "Buf[2]") is in.
func rootOf(n string) string {
	if i := strings.IndexAny(n, ".["); i > 0 {
		return n[:i]
	}
	return n
}

func elementaryType(k ir.TypeKind) *ir.Type {
	switch k {
	case ir.TypeBool:
		return ir.BoolT
	case ir.TypeInt:
		return ir.IntT
	case ir.TypeReal:
		return ir.RealT
	case ir.TypeTime:
		return ir.TimeT
	case ir.TypeString:
		return ir.StringT
	}
	return nil
}

// idxOf is the carried places of the written tags, sorted: what one
// program's scan copies at its end. nil without a carry set.
func (cs *carrySet) idxOf(written map[string]bool) []int {
	if cs == nil {
		return nil
	}
	var out []int
	for key := range written {
		if i, ok := cs.index[key]; ok {
			out = append(out, i)
		}
	}
	slices.Sort(out)
	return out
}

// copyTagsLocked appends the carried tags at idx, as the store holds them,
// to dst: a forced tag's actual value (forces are not carried, design
// §4.2), never the forced one. A tag the store does not hold yet (nothing
// wrote it) is left out. The caller holds t.mu (either mode). No
// allocation once dst has grown.
func (t *Tags) copyTagsLocked(cs *carrySet, idx []int, dst *TagValues) {
	for _, i := range idx {
		tv, root, ok := t.lookupLocked(cs.keys[i])
		if !ok {
			continue
		}
		v := t.liveLocked(root, tv)
		if mutRuntime("image_tag_forced_value") {
			v = tv.v
		}
		dst.Leaves = ir.FlattenValueInto(dst.Leaves, &v)
		dst.Idx = append(dst.Idx, i)
		dst.Ends = append(dst.Ends, len(dst.Leaves))
	}
}

// restIdx is the carried places no program writes now: the stream copies
// them from the store when it takes an image (an operator's tags). A
// retained one is left out: the image's retain section, taken at the same
// moment, carries it already, and a retained setpoint table would
// otherwise go twice.
func (r *Runtime) restIdx() []int {
	cs := r.carried
	if cs == nil {
		return nil
	}
	written := map[string]bool{}
	if r.retainStore != nil && !mutRuntime("image_tags_retained_twice") {
		for _, n := range r.retainTags {
			written[ir.NameKey(rootOf(n))] = true
		}
	}
	if !mutRuntime("image_tags_written_only") {
		for _, np := range r.namedPrograms() {
			for k := range np.prog.writtenKeys() {
				written[k] = true
			}
		}
	}
	var out []int
	for i, key := range cs.keys {
		if mutRuntime("image_tags_written_only") {
			break
		}
		if !written[key] {
			out = append(out, i)
		}
	}
	return out
}

// CarriedTags lists the tags an image carries, by ir.NameKey, sorted, and
// their layout (ir.TagsLayout). For status and tests.
func (r *Runtime) CarriedTags() (keys []string, layout string) {
	if r.carried == nil {
		return nil, ""
	}
	return slices.Clone(r.carried.keys), r.carried.layout
}

// shadowTags is the standby's copy of the leader's carried tags, as the
// newest image holds them: one value per carried place, applied to a fresh
// value of the tag's type here (so leaves that do not fit it are refused).
type shadowTags struct {
	vals []ir.Value
	have []bool
	at   []int64 // the NowMs of the copy each value came from
}

// applyTags applies an image's tag sections to a fresh shadowTags: the
// rest first, then each task's in the order of its copy's NowMs, so a tag
// two tasks write takes the later copy. It is all or nothing: an error
// leaves the caller's previous tags.
func applyTags(cs *carrySet, img *Image) (*shadowTags, error) {
	st := &shadowTags{vals: make([]ir.Value, len(cs.keys)), have: make([]bool, len(cs.keys)), at: make([]int64, len(cs.keys))}
	type section struct {
		nowMs int64
		tv    *TagValues
		what  string
	}
	secs := []section{{img.TagsNowMs, &img.Tags, "the store's"}}
	for i := range img.Tasks {
		secs = append(secs, section{img.Tasks[i].NowMs, &img.Tasks[i].Tags, "task " + img.Tasks[i].Task + "'s"})
	}
	slices.SortStableFunc(secs[1:], func(a, b section) int {
		switch {
		case a.nowMs < b.nowMs:
			return -1
		case a.nowMs > b.nowMs:
			return 1
		}
		return 0
	})
	for _, sec := range secs {
		tv := sec.tv
		if len(tv.Ends) != len(tv.Idx) {
			return nil, fmt.Errorf("runtime: %s tags: %d places, %d ends", sec.what, len(tv.Idx), len(tv.Ends))
		}
		prev := 0
		for j, i := range tv.Idx {
			if i < 0 || i >= len(cs.keys) {
				return nil, fmt.Errorf("runtime: %s tags: place %d, this controller carries %d", sec.what, i, len(cs.keys))
			}
			end := tv.Ends[j]
			if end < prev || end > len(tv.Leaves) {
				return nil, fmt.Errorf("runtime: %s tags: tag %s ends at leaf %d of %d", sec.what, cs.keys[i], end, len(tv.Leaves))
			}
			prev = end
			if st.have[i] && st.at[i] > sec.nowMs {
				continue
			}
			v := ir.Zero(cs.types[i])
			if err := ir.ApplyValueLeaves(&v, tv.leavesOf(j)); err != nil {
				return nil, fmt.Errorf("runtime: %s tags: tag %s: %w", sec.what, cs.keys[i], err)
			}
			st.vals[i], st.have[i], st.at[i] = v, true, sec.nowMs
		}
	}
	return st, nil
}
