package runtime

import (
	"fmt"
	"slices"
	"sync"

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
	keys  []string   // ir.NameKey of each tag, sorted
	types []*ir.Type // the type each is applied to
	index map[string]int
	// layoutOnce hashes the layout (ir.TagsLayout) on first use, by the
	// state stream: never in New, so the playground's wasm, which runs no
	// stream, links no layout hash (keep-wasm-tight, .github/wasm-budget.txt).
	layoutOnce sync.Once
	layoutHash string
}

// layout is the set's ir.TagsLayout.
func (cs *carrySet) layout() string {
	cs.layoutOnce.Do(func() { cs.layoutHash = ir.TagsLayout(cs.keys, cs.types) })
	return cs.layoutHash
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
	return slices.Clone(r.carried.keys), r.carried.layout()
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
