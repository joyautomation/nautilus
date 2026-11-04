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
//     `X := X OR a` reads X before writing it); setting a member, element
//     or bit reads the whole tag first;
//   - IF and CASE keep a write only when every branch that falls through
//     makes it; a CASE over an enumerated type that names every member
//     covers every path even with no ELSE; a branch that RETURNs or EXITs
//     carries its state to where control goes (the end of the call, or
//     past the loop) instead of falling through;
//   - REPEAT runs its body at least once, and so does a FOR whose bounds
//     are constants that give it at least one iteration;
//   - a WHILE, or a FOR whose bounds are not constant, may run zero times;
//   - a FUNCTION_BLOCK call evaluates its inputs, then walks the block's
//     body in order from the caller's state — so what the body definitely
//     writes through VAR_EXTERNAL counts after the call, and what it reads
//     first is reported — then writes its `=>` outputs. A user FUNCTION
//     call is walked the same way after its arguments.
//
// Every branch of an IF or CASE is taken as a path the scan can take. A
// read that is reached with no write on such a path is Sure. A read that is
// unwritten only when a loop of unknown count runs zero times is not Sure:
// the walk is run twice, once taking those loops as running at least once,
// and what only the second run finds is reported as possible.
//
// Introspection only — the VM never calls this.
func (p *Program) ReadBeforeWrite() map[string]ReadFirst {
	sure := rbwWalk(p.Body, true)
	maybe := rbwWalk(p.Body, false)
	out := make(map[string]ReadFirst, len(maybe.order))
	for _, r := range sure.order {
		out[r.name] = ReadFirst{Sure: true, Cause: r.cause}
	}
	for _, r := range maybe.order {
		if _, ok := sure.seen[NameKey(r.name)]; !ok {
			out[r.name] = ReadFirst{Cause: CauseLoopMayNotRun}
		}
	}
	return out
}

// ReadFirst describes the first read ReadBeforeWrite found of one global.
type ReadFirst struct {
	// Sure is set when a path the scan can take (every IF and CASE branch
	// counted as one) reaches the read with the global unwritten. When it
	// is clear, the read comes first only if a loop runs zero times.
	Sure  bool
	Cause ReadCause
}

// ReadCause says why a read comes before the program's write.
type ReadCause int

const (
	// CauseNotYet: no path before the read writes it.
	CauseNotYet ReadCause = iota
	// CauseSomePaths: some paths before the read write it, not all.
	CauseSomePaths
	// CauseSelfAssign: the assignment that writes it reads it first
	// (`X := X OR a` — a ladder seal-in or a set/reset coil).
	CauseSelfAssign
	// CauseCompound: setting a member, element or bit of it reads the
	// whole tag first.
	CauseCompound
	// CauseLoopMayNotRun: it is written first only inside a loop that may
	// run zero times (never Sure).
	CauseLoopMayNotRun
)

// rbwState is what the walk knows at one point of the scan: the globals
// written on every path to it (must) and on some path to it (may). Keys
// are NameKey: one tag, however each POU spells it. A nil *rbwState is a
// point no path reaches.
type rbwState struct{ must, may map[string]bool }

func newState() *rbwState {
	return &rbwState{must: map[string]bool{}, may: map[string]bool{}}
}

func (s *rbwState) clone() *rbwState {
	c := &rbwState{must: make(map[string]bool, len(s.must)), may: make(map[string]bool, len(s.may))}
	for k := range s.must {
		c.must[k] = true
	}
	for k := range s.may {
		c.may[k] = true
	}
	return c
}

func (s *rbwState) write(name string) {
	k := NameKey(name)
	s.must[k] = true
	s.may[k] = true
}

// mergeStates joins the states of the paths that meet at one point: must
// is what all of them wrote, may what any of them did. nil when none of
// them is live.
func mergeStates(states ...*rbwState) *rbwState {
	var out *rbwState
	for _, s := range states {
		if s == nil {
			continue
		}
		if out == nil {
			out = s.clone()
			continue
		}
		for k := range out.must {
			if !s.must[k] {
				delete(out.must, k)
			}
		}
		for k := range s.may {
			out.may[k] = true
		}
	}
	return out
}

type rbwRead struct {
	name  string
	cause ReadCause
}

type rbwWalker struct {
	// loopsRun takes every loop whose count the walk cannot prove as
	// running at least once (the Sure pass).
	loopsRun bool
	seen     map[string]bool // NameKey of every global already reported
	order    []rbwRead       // first read of each, in scan order

	loops []*rbwLoop   // enclosing loops of the body being walked, innermost last
	rets  *[]*rbwState // states at a RETURN of the callee being walked; nil in the program
	calls map[any]bool // FBDefs / FuncDefs being walked (recursion guard)
	asgn  string       // root global of the assignment whose value is being read
	bare  bool         // that assignment's target is the bare global
	inTgt bool         // reading a compound assignment target
}

// rbwLoop collects the states at the EXITs and CONTINUEs of one loop.
type rbwLoop struct{ exits, conts []*rbwState }

func rbwWalk(body []Stmt, loopsRun bool) *rbwWalker {
	w := &rbwWalker{loopsRun: loopsRun, seen: map[string]bool{}, calls: map[any]bool{}}
	w.block(body, newState())
	return w
}

// read records a read of name at state s.
func (w *rbwWalker) read(name string, s *rbwState) {
	k := NameKey(name)
	if s.must[k] || w.seen[k] {
		return
	}
	w.seen[k] = true
	cause := CauseNotYet
	switch {
	case w.inTgt && SameName(name, w.asgn):
		cause = CauseCompound
	case w.asgn != "" && SameName(name, w.asgn) && w.bare:
		cause = CauseSelfAssign
	case w.asgn != "" && SameName(name, w.asgn):
		cause = CauseCompound
	case s.may[k]:
		cause = CauseSomePaths
	}
	w.order = append(w.order, rbwRead{name: name, cause: cause})
}

// block walks ss from s and returns the state after it (nil when no path
// falls through). s may be updated in place.
func (w *rbwWalker) block(ss []Stmt, s *rbwState) *rbwState {
	for _, st := range ss {
		if s = w.stmt(st, s); s == nil {
			return nil
		}
	}
	return s
}

func (w *rbwWalker) stmt(st Stmt, s *rbwState) *rbwState {
	switch n := st.(type) {
	case *Assign:
		root := globalRoot(n.Target)
		_, bare := n.Target.(*GlobalRef)
		prevA, prevB := w.asgn, w.bare
		w.asgn, w.bare = root, bare
		w.expr(n.Value, s)
		w.target(n.Target, s)
		w.asgn, w.bare = prevA, prevB
	case *If:
		w.expr(n.Cond, s)
		return mergeStates(w.block(n.Then, s.clone()), w.block(n.Else, s.clone()))
	case *Case:
		w.expr(n.Expr, s)
		var branches []*rbwState
		for _, c := range n.Clauses {
			for _, v := range c.Values {
				w.expr(v, s)
			}
			for _, r := range c.Ranges {
				w.expr(r.Lo, s)
				w.expr(r.Hi, s)
			}
			branches = append(branches, w.block(c.Body, s.clone()))
		}
		if len(n.Else) > 0 || !enumCovered(n) {
			branches = append(branches, w.block(n.Else, s.clone()))
		}
		return mergeStates(branches...)
	case *For:
		w.expr(n.Start, s)
		w.expr(n.End, s)
		w.expr(n.Step, s)
		runs, known := forRuns(n)
		if !known {
			runs = w.loopsRun
		}
		return w.loop(n.Body, s, nil, runs)
	case *While:
		w.expr(n.Cond, s)
		return w.loop(n.Body, s, nil, w.loopsRun)
	case *Repeat:
		return w.loop(n.Body, s, n.Cond, true)
	case *FBCall:
		if n.Instance != nil {
			w.lvalueReads(n.Instance, s)
		}
		for _, in := range n.Inputs {
			w.expr(in.Value, s)
		}
		if d := n.Def; d != nil && d.Body != nil {
			if s = w.callee(d, d.Body.Body, s); s == nil {
				return nil
			}
		}
		for _, out := range n.Outputs {
			w.target(out.Target, s)
		}
	case *ExprStmt:
		w.expr(n.X, s)
	case *Return:
		if w.rets != nil {
			*w.rets = append(*w.rets, s)
		}
		return nil
	case *Exit:
		if l := len(w.loops); l > 0 {
			w.loops[l-1].exits = append(w.loops[l-1].exits, s)
		}
		return nil
	case *Continue:
		if l := len(w.loops); l > 0 {
			w.loops[l-1].conts = append(w.loops[l-1].conts, s)
		}
		return nil
	}
	return s
}

// loop walks a loop body entered with state s. cond is REPEAT's UNTIL,
// read where an iteration ends; runs says the body executes at least once.
// The first iteration starts from the least-written state any iteration
// does, so walking the body once checks every iteration's reads.
func (w *rbwWalker) loop(body []Stmt, s *rbwState, cond Expr, runs bool) *rbwState {
	l := &rbwLoop{}
	w.loops = append(w.loops, l)
	end := w.block(body, s.clone())
	w.loops = w.loops[:len(w.loops)-1]
	// The states that reach the loop's test: the body's end and every
	// CONTINUE.
	back := mergeStates(append([]*rbwState{end}, l.conts...)...)
	if cond != nil && back != nil {
		w.expr(cond, back)
	}
	after := append([]*rbwState{back}, l.exits...)
	if !runs {
		after = append(after, s)
	}
	return mergeStates(after...)
}

// callee walks a FUNCTION_BLOCK or FUNCTION body called at state s and
// returns the state when it comes back: its end, or any RETURN. A call
// already being walked (recursion) is skipped.
func (w *rbwWalker) callee(def any, body []Stmt, s *rbwState) *rbwState {
	if w.calls[def] {
		return s
	}
	w.calls[def] = true
	loops, rets, asgn, bare, inTgt := w.loops, w.rets, w.asgn, w.bare, w.inTgt
	var returned []*rbwState
	w.loops, w.rets, w.asgn, w.bare, w.inTgt = nil, &returned, "", false, false
	end := w.block(body, s.clone())
	w.loops, w.rets, w.asgn, w.bare, w.inTgt = loops, rets, asgn, bare, inTgt
	delete(w.calls, def)
	return mergeStates(append(returned, end)...)
}

// forRuns reports whether a FOR runs its body at least once, and whether
// that is known: constant bounds and a constant non-zero step settle it
// either way (1 TO 3 runs; 3 TO 1 never does), anything else is unknown
// and left to the pass's assumption about loops.
func forRuns(n *For) (runs, known bool) {
	lo, ok1 := constInt(n.Start)
	hi, ok2 := constInt(n.End)
	step := int64(1)
	ok3 := true
	if n.Step != nil {
		step, ok3 = constInt(n.Step)
	}
	if !ok1 || !ok2 || !ok3 || step == 0 {
		return false, false
	}
	return (step > 0 && lo <= hi) || (step < 0 && lo >= hi), true
}

func constInt(e Expr) (int64, bool) {
	switch n := e.(type) {
	case *Lit:
		if n.V.Kind == TypeInt {
			return n.V.I, true
		}
	case *UnOp:
		if n.Op == OpNeg {
			if v, ok := constInt(n.X); ok {
				return -v, true
			}
		}
	}
	return 0, false
}

// enumCovered reports a CASE over an enumerated type whose labels name
// every member, so one clause always matches.
func enumCovered(n *Case) bool {
	t := n.Expr.ExprType()
	if t == nil || t.Enum == nil || len(t.Enum.Members) == 0 {
		return false
	}
	var vals []int64
	type rng struct{ lo, hi int64 }
	var rngs []rng
	for _, c := range n.Clauses {
		for _, v := range c.Values {
			i, ok := constInt(v)
			if !ok {
				return false
			}
			vals = append(vals, i)
		}
		for _, r := range c.Ranges {
			lo, ok1 := constInt(r.Lo)
			hi, ok2 := constInt(r.Hi)
			if !ok1 || !ok2 {
				return false
			}
			rngs = append(rngs, rng{lo, hi})
		}
	}
member:
	for _, m := range t.Enum.Members {
		for _, v := range vals {
			if v == m.Value {
				continue member
			}
		}
		for _, r := range rngs {
			if m.Value >= r.lo && m.Value <= r.hi {
				continue member
			}
		}
		return false
	}
	return true
}

// target records an assignment destination. A bare global is written; a
// field, element or bit of one reads the aggregate first (a tag that was
// never written has no aggregate to update), and is written after.
func (w *rbwWalker) target(l LValue, s *rbwState) {
	switch n := l.(type) {
	case *GlobalRef:
		s.write(n.Name)
	case *MemberRef, *IndexRef, *BitRef:
		root := globalRoot(l)
		prevA, prevT := w.asgn, w.inTgt
		w.asgn, w.inTgt = root, true
		w.lvalueReads(l, s)
		w.asgn, w.inTgt = prevA, prevT
		if root != "" {
			s.write(root)
		}
	}
}

// lvalueReads checks the reads a compound place makes: its aggregate and
// any index expressions.
func (w *rbwWalker) lvalueReads(l LValue, s *rbwState) {
	switch n := l.(type) {
	case *GlobalRef:
		w.expr(n, s)
	case *MemberRef:
		w.expr(n.Object, s)
	case *IndexRef:
		w.expr(n.Array, s)
		w.index(n.Index, s)
	case *BitRef:
		w.expr(n.Object, s)
	}
}

// index reads an index expression: not the aggregate, so not a compound
// target's own read.
func (w *rbwWalker) index(e Expr, s *rbwState) {
	prevA, prevT := w.asgn, w.inTgt
	w.asgn, w.inTgt = "", false
	w.expr(e, s)
	w.asgn, w.inTgt = prevA, prevT
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

func (w *rbwWalker) expr(e Expr, s *rbwState) {
	switch n := e.(type) {
	case *GlobalRef:
		w.read(n.Name, s)
	case *BinOp:
		w.expr(n.L, s)
		w.expr(n.R, s)
	case *UnOp:
		w.expr(n.X, s)
	case *IndexRef:
		w.expr(n.Array, s)
		w.index(n.Index, s)
	case *MemberRef:
		w.expr(n.Object, s)
	case *BitRef:
		w.expr(n.Object, s)
	case *Call:
		for _, a := range n.Args {
			w.expr(a, s)
		}
	case *UserCall:
		for _, a := range n.Args {
			w.expr(a, s)
		}
		if d := n.Def; d != nil && d.Body != nil {
			// The call returns, so the scan goes on in what it left.
			if after := w.callee(d, d.Body.Body, s); after != nil {
				s.must, s.may = after.must, after.may
			}
		}
	}
}
