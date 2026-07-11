package eip

import (
	"context"
	"errors"
	"fmt"

	"github.com/joyautomation/nautilus/eip/logix"
	"github.com/joyautomation/nautilus/lang/ir"
)

// Leaf-mode struct polling.
//
// Logix refuses whole-struct reads (CIP 0x0F privilege violation, ext 0x2100)
// on tags whose type carries access-restricted members — AOI backing tags in
// particular. When a root read is refused, the driver drops that binding into
// leaf mode: every elementary member is read individually (batched through
// Multiple Service Packets) and the struct value is assembled client-side —
// the same strategy tentacle used for all struct tags.

// leafDesc is one readable member beneath a struct binding.
type leafDesc struct {
	rel      string // dotted path relative to the device tag ("Header.Displacement")
	typ      string // manifest type: elementary name or "STRING"
	arrayLen int
	size     int // expected wire size in bytes, for batch packing
}

const maxLeafDepth = 8

// expandLeaves flattens a manifest type into its leaf reads.
func (d *Driver) expandLeaves(typeName, prefix string, depth int) ([]leafDesc, error) {
	if depth >= maxLeafDepth {
		return nil, fmt.Errorf("eip: type %q nests deeper than %d", typeName, maxLeafDepth)
	}
	td, ok := d.typeDef(typeName)
	if !ok {
		return nil, fmt.Errorf("eip: unknown type %q", typeName)
	}
	var out []leafDesc
	for _, f := range td.Fields {
		rel := f.Name
		if prefix != "" {
			rel = prefix + "." + f.Name
		}
		if code, isElem := elementaryCode(f.Type); isElem {
			ti, _ := logix.TypeByCode(code)
			size := ti.Size
			if f.ArrayLen > 0 {
				size *= f.ArrayLen
			}
			out = append(out, leafDesc{rel: rel, typ: f.Type, arrayLen: f.ArrayLen, size: size})
			continue
		}
		if f.Type == "STRING" {
			out = append(out, leafDesc{rel: rel, typ: "STRING", size: 92})
			continue
		}
		if f.ArrayLen > 0 {
			for i := 0; i < f.ArrayLen; i++ {
				sub, err := d.expandLeaves(f.Type, fmt.Sprintf("%s[%d]", rel, i), depth+1)
				if err != nil {
					return nil, err
				}
				out = append(out, sub...)
			}
			continue
		}
		sub, err := d.expandLeaves(f.Type, rel, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// leavesFor returns (and caches) the leaf expansion of a binding.
func (d *Driver) leavesFor(b TagBinding) ([]leafDesc, error) {
	if l, ok := d.leafCache[b.Name]; ok {
		return l, nil
	}
	l, err := d.expandLeaves(b.Type, "", 0)
	if err != nil {
		return nil, err
	}
	d.leafCache[b.Name] = l
	return l, nil
}

// pollLeaves reads every leaf-mode binding member-wise and assembles the
// struct values. Returns false when the connection broke.
func (d *Driver) pollLeaves(ctx context.Context, sess *session, bindings []TagBinding) bool {
	if len(bindings) == 0 {
		return true
	}
	// Gather every leaf of every binding into one batch.
	type slot struct {
		binding string
		rel     string
	}
	var tags []string
	sizes := map[string]int{}
	bySlot := map[string]slot{}
	perBinding := map[string][]leafDesc{}
	for _, b := range bindings {
		leaves, err := d.leavesFor(b)
		if err != nil {
			d.tagError(b, err)
			continue
		}
		perBinding[b.Name] = leaves
		for _, l := range leaves {
			if d.deadLeaves[b.Name][l.rel] {
				continue // permanently unreadable — assembled as zero
			}
			dev := b.Device + "." + l.rel
			tags = append(tags, dev)
			sizes[dev] = l.size
			bySlot[dev] = slot{binding: b.Name, rel: l.rel}
		}
	}
	if len(tags) == 0 {
		return true
	}

	results := sess.ctrl.ReadTags(ctx, tags, func(t string) int { return sizes[t] })
	values := map[string]map[string]ir.Value{} // binding -> rel -> value
	failed := map[string]error{}
	for _, r := range results {
		s := bySlot[r.Tag]
		if r.Err != nil {
			if sess.ctrl.Broken() {
				return false
			}
			// Members the controller will never serve (internal words like
			// TIMER.Control, access-restricted AOI members) drop out of the
			// poll set and assemble as zero; anything else holds the last
			// good struct value for this cycle.
			var ce *logix.CIPError
			if errors.As(r.Err, &ce) && ce.Permanent() {
				if d.deadLeaves[s.binding] == nil {
					d.deadLeaves[s.binding] = map[string]bool{}
				}
				d.deadLeaves[s.binding][s.rel] = true
				d.log.Info("eip: member unreadable, will assemble as zero",
					"tag", s.binding, "member", r.Tag, "status", fmt.Sprintf("0x%02x", ce.Status))
				continue
			}
			if _, seen := failed[s.binding]; !seen {
				failed[s.binding] = fmt.Errorf("member %s: %w", r.Tag, r.Err)
			}
			continue
		}
		lv, err := sess.registry.Decode(r.RawTag)
		if err != nil {
			if _, seen := failed[s.binding]; !seen {
				failed[s.binding] = fmt.Errorf("member %s: %w", r.Tag, err)
			}
			continue
		}
		if values[s.binding] == nil {
			values[s.binding] = map[string]ir.Value{}
		}
		values[s.binding][s.rel] = leafValue(lv)
	}

	for _, b := range bindings {
		if _, ok := perBinding[b.Name]; !ok {
			continue
		}
		if err, bad := failed[b.Name]; bad {
			// Hold the last-known value rather than publishing a partial struct.
			d.tagError(b, err)
			continue
		}
		td, _ := d.typeDef(b.Type)
		v, err := d.buildFromLeaves(td, "", values[b.Name])
		if err != nil {
			d.tagError(b, err)
			continue
		}
		d.mu.Lock()
		d.snapshot[b.Name] = v
		d.mu.Unlock()
	}
	return true
}

// leafValue converts a decoded member read into an ir.Value (scalar, string,
// or array of scalars).
func leafValue(lv logix.Value) ir.Value {
	if lv.Elems != nil {
		arr := make([]ir.Value, len(lv.Elems))
		for i, e := range lv.Elems {
			arr[i] = toValue(e.Scalar)
		}
		return ir.Value{Kind: ir.TypeArray, Arr: arr}
	}
	return toValue(lv.Scalar)
}

// buildFromLeaves assembles a struct ir.Value from per-member reads, in
// manifest field order.
func (d *Driver) buildFromLeaves(td TypeDef, prefix string, vals map[string]ir.Value) (ir.Value, error) {
	out := ir.Value{Kind: ir.TypeStruct, Struct: d.defs[td.Name], Fld: make([]ir.Value, len(td.Fields))}
	for i, f := range td.Fields {
		rel := f.Name
		if prefix != "" {
			rel = prefix + "." + f.Name
		}
		if _, isElem := elementaryCode(f.Type); isElem || f.Type == "STRING" {
			v, ok := vals[rel]
			if !ok {
				// Dead leaf (or first cycle after one was marked): IEC zero.
				out.Fld[i] = ir.Zero(d.defs[td.Name].Fields[i].Type)
				continue
			}
			out.Fld[i] = v
			continue
		}
		sub, ok := d.typeDef(f.Type)
		if !ok {
			return ir.Value{}, fmt.Errorf("unknown type %q at %s", f.Type, rel)
		}
		if f.ArrayLen > 0 {
			arr := ir.Value{Kind: ir.TypeArray, Arr: make([]ir.Value, f.ArrayLen)}
			for j := 0; j < f.ArrayLen; j++ {
				el, err := d.buildFromLeaves(sub, fmt.Sprintf("%s[%d]", rel, j), vals)
				if err != nil {
					return ir.Value{}, err
				}
				arr.Arr[j] = el
			}
			out.Fld[i] = arr
			continue
		}
		el, err := d.buildFromLeaves(sub, rel, vals)
		if err != nil {
			return ir.Value{}, err
		}
		out.Fld[i] = el
	}
	return out, nil
}
