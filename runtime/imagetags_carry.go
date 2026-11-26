//go:build !js

package runtime

import (
	"slices"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The carried tags' set and the scan's copy of them (imagetags.go): only a
// controller's build has them. The playground's wasm runs no pair, so it
// carries nothing and links none of this (imagetags_js.go; the wasm budget
// in .github/wasm-budget.txt).

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
