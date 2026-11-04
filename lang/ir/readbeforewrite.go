package ir

// ReadBeforeWrite answers the question GlobalUses cannot: which globals can
// one scan of this program READ before the same scan has WRITTEN them?
//
// A tag the manifest never seeds (an output with no init:) holds nothing
// until a program writes it, and reading nothing faults the scan with
// "undefined tag". A program that writes such a tag on every path before it
// reads it is safe; a seal-in (`Horn := (Alarm OR Horn) AND NOT Ack`) reads
// its own coil first, so its very first scan faults and, since the faulting
// statement is the one that would have written the tag, so does every scan
// after it. `naut check` uses this to report that before it runs.
//
// The walk is a definite-assignment analysis over the statements in scan
// order:
//
//   - an assignment reads its value first, then writes its target (so
//     `X := X OR a` reads X before writing it);
//   - IF and CASE keep a write only when every branch that falls through
//     makes it; a branch that RETURNs (or EXITs a loop) does not fall
//     through, so it neither adds nor removes anything;
//   - a loop's body may run zero times, so its writes do not count after
//     the loop (reads inside it are still checked);
//   - a FUNCTION_BLOCK call reads every global its body (and the blocks it
//     instantiates) reads, and is credited only with the outputs it copies
//     out (`Q => X`): what the block writes inside may be conditional.
//
// Conservative where it cannot know: a read it reports may sit on a path
// that never runs on the first scan. Introspection only — the VM never
// calls this.
func (p *Program) ReadBeforeWrite() map[string]bool {
	w := &rbwWalker{out: map[string]bool{}}
	w.block(p.Body, map[string]bool{})
	return w.out
}

type rbwWalker struct{ out map[string]bool }

// block walks ss with written (the globals definitely written so far, owned
// by the caller and updated in place). dead reports that no path through ss
// falls through to what follows it.
func (w *rbwWalker) block(ss []Stmt, written map[string]bool) (dead bool) {
	for _, s := range ss {
		if w.stmt(s, written) {
			return true
		}
	}
	return false
}

func copySet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// merge sets written to what every live branch wrote. Returns true when no
// branch is live.
func merge(written map[string]bool, branches []map[string]bool) bool {
	if len(branches) == 0 {
		return true
	}
	for k := range written {
		delete(written, k)
	}
	for k := range branches[0] {
		all := true
		for _, b := range branches[1:] {
			if !b[k] {
				all = false
				break
			}
		}
		if all {
			written[k] = true
		}
	}
	return false
}

func (w *rbwWalker) stmt(s Stmt, written map[string]bool) (dead bool) {
	switch n := s.(type) {
	case *Assign:
		w.expr(n.Value, written)
		w.target(n.Target, written)
	case *If:
		w.expr(n.Cond, written)
		var live []map[string]bool
		for _, body := range [][]Stmt{n.Then, n.Else} {
			b := copySet(written)
			if !w.block(body, b) {
				live = append(live, b)
			}
		}
		return merge(written, live)
	case *Case:
		w.expr(n.Expr, written)
		var live []map[string]bool
		for _, c := range n.Clauses {
			for _, v := range c.Values {
				w.expr(v, written)
			}
			for _, r := range c.Ranges {
				w.expr(r.Lo, written)
				w.expr(r.Hi, written)
			}
			b := copySet(written)
			if !w.block(c.Body, b) {
				live = append(live, b)
			}
		}
		b := copySet(written)
		if !w.block(n.Else, b) {
			live = append(live, b)
		}
		return merge(written, live)
	case *For:
		w.expr(n.Start, written)
		w.expr(n.End, written)
		w.expr(n.Step, written)
		w.block(n.Body, copySet(written))
	case *While:
		w.expr(n.Cond, written)
		w.block(n.Body, copySet(written))
	case *Repeat:
		// The body runs at least once, but an EXIT can leave it part way;
		// counting none of its writes is the safe side.
		b := copySet(written)
		w.block(n.Body, b)
		w.expr(n.Cond, b)
	case *FBCall:
		if n.Instance != nil {
			w.lvalueReads(n.Instance, written)
		}
		for _, in := range n.Inputs {
			w.expr(in.Value, written)
		}
		if n.Def != nil {
			u := GlobalUse{Read: map[string]bool{}, Written: map[string]bool{}}
			u.collectFB(&Type{Kind: TypeFB, FB: n.Def}, map[*FBDef]bool{})
			for name := range u.Read {
				if !written[name] {
					w.out[name] = true
				}
			}
		}
		for _, out := range n.Outputs {
			w.target(out.Target, written)
		}
	case *ExprStmt:
		w.expr(n.X, written)
	case *Return, *Exit, *Continue:
		return true
	}
	return false
}

// target records an assignment destination. A bare global is written; a
// field, element or bit of one reads the aggregate first (a tag that was
// never written has no aggregate to update), and is written after.
func (w *rbwWalker) target(l LValue, written map[string]bool) {
	switch n := l.(type) {
	case *GlobalRef:
		written[n.Name] = true
	case *MemberRef, *IndexRef, *BitRef:
		w.lvalueReads(l, written)
		if root := globalRoot(l); root != "" {
			written[root] = true
		}
	}
}

// lvalueReads checks the reads a compound place makes: its aggregate and
// any index expressions.
func (w *rbwWalker) lvalueReads(l LValue, written map[string]bool) {
	switch n := l.(type) {
	case *GlobalRef:
		w.expr(n, written)
	case *MemberRef:
		w.expr(n.Object, written)
	case *IndexRef:
		w.expr(n.Array, written)
		w.expr(n.Index, written)
	case *BitRef:
		w.expr(n.Object, written)
	}
}

// globalRoot is the global under a compound place ("" when it is a local).
func globalRoot(e any) string {
	switch n := e.(type) {
	case *GlobalRef:
		return n.Name
	case *MemberRef:
		return globalRoot(n.Object)
	case *IndexRef:
		return globalRoot(n.Array)
	case *BitRef:
		return globalRoot(n.Object)
	}
	return ""
}

func (w *rbwWalker) expr(e Expr, written map[string]bool) {
	switch n := e.(type) {
	case *GlobalRef:
		if !written[n.Name] {
			w.out[n.Name] = true
		}
	case *BinOp:
		w.expr(n.L, written)
		w.expr(n.R, written)
	case *UnOp:
		w.expr(n.X, written)
	case *IndexRef:
		w.expr(n.Array, written)
		w.expr(n.Index, written)
	case *MemberRef:
		w.expr(n.Object, written)
	case *BitRef:
		w.expr(n.Object, written)
	case *Call:
		for _, a := range n.Args {
			w.expr(a, written)
		}
	case *UserCall:
		for _, a := range n.Args {
			w.expr(a, written)
		}
	}
}
