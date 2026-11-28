package ir

// The exact copy-back of a VAR_IN_OUT pin (#1458, docs/design/one-struct-model.md §2.4).
//
// A call copies an in-out pin in before Step and back out after it, so a
// host that tracks what a program assigned (runtime's #578 member merge)
// sees the whole pin as assigned on every call, even by a block that only
// read it. For most variables that is harmless. For a struct instance whose
// members are commands to a device it is not: the copy-back would count as
// the first write of every command member, and an operator's write to a
// member the block never touched, landed mid-scan, would be lost.
//
// So while a user-defined block's body runs, the VM records which parts of
// each in-out slot that body assigned, and the copy-back hands a host that
// implements InOutWriter exactly those parts. The record fails towards
// whole: any write into the slot the VM cannot describe as a path (a FOR
// counter, a path deeper than MaxMemberDepth, more than maxInOutPaths
// assignments in one call, a pin that holds a function block, a built-in
// block's pins, which it writes in Go) marks the pin whole, which is the
// old behaviour. A record too small would drop a real command; too big is
// only what every host already handles.

// InOutWriter is an optional Host extension: the copy-back of a VAR_IN_OUT
// pin bound to a global, or to part of one. whole is the global's new whole
// value; base is the part the pin is bound to (no steps: the whole global);
// assigned lists, relative to base, the parts the block's body assigned on
// this call, and is empty when it assigned none. all is true when the block
// is to be taken as having assigned the whole pin.
//
// A host decides what to make of it. One that keeps the old rule writes
// base whole whatever assigned says; runtime does that for every global but
// a struct instance. A host without InOutWriter gets the old rule from the
// VM: WriteGlobal, or WriteGlobalMember at base.
type InOutWriter interface {
	WriteGlobalInOut(name string, whole Value, base MemberPath, assigned []MemberPath, all bool) error
}

// maxInOutPaths bounds one call's record of one pin: past it the pin is
// taken as assigned whole, so a loop over a large array costs bounded
// memory and the host a bounded walk.
const maxInOutPaths = 64

// inOutRec is what one call's body assigned into each of its in-out slots.
// It lives on the instance and is reused call to call, so recording does
// not allocate once it has grown.
type inOutRec struct {
	slots []inOutSlot
}

type inOutSlot struct {
	slot  int
	all   bool
	paths []MemberPath
}

// slotRec returns the record for frame slot i, or nil when i is no in-out
// slot being recorded.
func (r *inOutRec) slotRec(i int) *inOutSlot {
	if r == nil {
		return nil
	}
	for k := range r.slots {
		if r.slots[k].slot == i {
			return &r.slots[k]
		}
	}
	return nil
}

// mark records an assignment at p (relative to the slot).
func (s *inOutSlot) mark(p MemberPath, ok bool) {
	if s.all {
		return
	}
	if !ok || (p.n == 0 && p.bits == 0) || len(s.paths) >= maxInOutPaths {
		s.all, s.paths = true, s.paths[:0]
		return
	}
	s.paths = append(s.paths, p)
}

// markUnder records sub (a record from an inner call) beneath base.
func (s *inOutSlot) markUnder(base MemberPath, sub *inOutSlot) {
	if sub == nil || sub.all {
		s.mark(base, true) // base with no steps marks the slot whole
		return
	}
	for _, p := range sub.paths {
		j, ok := base.Join(p)
		s.mark(j, ok)
	}
}

// startInOut prepares the instance's record for one call of a user block,
// one entry per in-out pin whose type is plain data. A pin that holds a
// function block starts whole: a block is identity, mutated without a
// store the VM sees.
func (inst *FBInstance) startInOut(def *FBDef) *inOutRec {
	if len(def.InOuts) == 0 || def.Body == nil {
		return nil
	}
	if inst.inout == nil {
		inst.inout = &inOutRec{slots: make([]inOutSlot, len(def.InOuts))}
	}
	r := inst.inout
	if len(r.slots) != len(def.InOuts) {
		r.slots = make([]inOutSlot, len(def.InOuts))
	}
	lo := len(def.Inputs) + len(def.Outputs)
	for i := range def.InOuts {
		s := &r.slots[i]
		s.slot, s.paths = lo+i, s.paths[:0]
		s.all = holdsFB(def.InOuts[i].Type)
	}
	return r
}

// holdsFB reports whether a value of t is, or holds, a function block.
func holdsFB(t *Type) bool {
	switch {
	case t == nil:
		return true // unknown: take it whole
	case t.Kind == TypeFB:
		return true
	case t.Kind == TypeArray:
		return holdsFB(t.Elem)
	case t.Kind == TypeStruct && t.Struct != nil:
		for _, f := range t.Struct.Fields {
			if holdsFB(f.Type) {
				return true
			}
		}
	}
	return false
}

// Join returns p followed by sub's steps (and sub's bits); ok is false when
// the result would be deeper than MaxMemberDepth.
func (p MemberPath) Join(sub MemberPath) (MemberPath, bool) {
	if p.n+sub.n > MaxMemberDepth {
		return MemberPath{}, false
	}
	if p.bits != 0 && sub.n > 0 {
		return MemberPath{}, false // a step below a bit: not a path
	}
	out := p
	for i := 0; i < sub.n; i++ {
		out.idx[p.n+i] = sub.idx[i]
		if sub.field&(1<<uint(i)) != 0 {
			out.field |= 1 << uint(p.n+i)
		}
	}
	out.n = p.n + sub.n
	switch {
	case sub.bits != 0 && p.bits != 0:
		out.bits = p.bits & sub.bits
	case sub.bits != 0:
		out.bits = sub.bits
	}
	return out, true
}

// lvalueRoot walks lv down to its root, evaluating any index on the way:
// the root (a *SlotRef or a *GlobalRef) and the chain, leaf first, as
// writeLValue builds it. ok is false for a target this walk does not
// model (a bit), which takes the old copy-back.
func lvalueRoot(ctx *EvalCtx, lv LValue) (LValue, []accessor, bool, error) {
	var chain []accessor
	cur := lv
	for {
		switch n := cur.(type) {
		case *SlotRef, *GlobalRef:
			return n, chain, true, nil
		case *IndexRef:
			iv, err := evalExpr(ctx, n.Index)
			if err != nil {
				return nil, nil, false, err
			}
			chain = append(chain, accessor{arrIdx: int(iv.I)})
			inner, ok := n.Array.(LValue)
			if !ok {
				return nil, nil, false, nil
			}
			cur = inner
		case *MemberRef:
			chain = append(chain, accessor{fieldIdx: n.FieldIdx, isField: true})
			inner, ok := n.Object.(LValue)
			if !ok {
				return nil, nil, false, nil
			}
			cur = inner
		default:
			return nil, nil, false, nil
		}
	}
}

// copyBack writes an in-out pin's value back to its target, telling a
// recording caller and an InOutWriter host only what the block assigned.
func copyBack(ctx *EvalCtx, target LValue, v Value, rec *inOutSlot) error {
	if rec == nil {
		return writeLValue(ctx, target, v)
	}
	root, chain, ok, err := lvalueRoot(ctx, target)
	if err != nil {
		return err
	}
	if !ok {
		return writeLValue(ctx, target, v)
	}
	base, pathOK := memberPath(chain)
	switch r := root.(type) {
	case *SlotRef:
		outer := ctx.Frame.inout.slotRec(r.Slot)
		if outer == nil {
			// Not a pin of a block being recorded: store it as before.
			return writeLValue(ctx, target, v)
		}
		if !rec.all && len(rec.paths) == 0 {
			return nil // the block assigned nothing: the slot already holds it
		}
		if err := storeAtRoot(&ctx.Frame.Slots[r.Slot], chain, v); err != nil {
			return err
		}
		if !pathOK {
			outer.mark(MemberPath{}, false)
			return nil
		}
		outer.markUnder(base, rec)
		return nil
	case *GlobalRef:
		w, isW := ctx.Host.(InOutWriter)
		if !isW || !pathOK {
			return writeLValue(ctx, target, v)
		}
		whole := CopyValue(v)
		if len(chain) > 0 {
			cur, err := ctx.Host.ReadGlobal(r.Name)
			if err != nil {
				return err
			}
			cur = CopyValue(coerceValue(cur, r.T))
			if err := storeAtRoot(&cur, chain, v); err != nil {
				return err
			}
			whole = cur
		}
		return w.WriteGlobalInOut(r.Name, whole, base, rec.paths, rec.all)
	}
	return writeLValue(ctx, target, v)
}
