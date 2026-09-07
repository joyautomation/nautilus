package ir

// Program.Globals answers WHICH tags a program binds. It does not answer how,
// and the difference decides whether a missing manifest entry is cosmetic or
// fatal:
//
//   - a global the program only WRITES is fine undeclared. It gets no unit and
//     no description in an HMI, which is worth a warning, but the scan runs.
//   - a global the program READS is a fault waiting to happen. An undeclared
//     tag is in no role, so it is never seeded and never driver-fed; the first
//     read of a value nothing has written faults the scan.
//
// So `nautilus check` reports the second as an error and the first as a
// warning, and needs this split to tell them apart.
//
// Introspection only — the VM never calls this.

// GlobalUse records how one program uses the PLC-wide variables it binds.
type GlobalUse struct {
	Read    map[string]bool
	Written map[string]bool
}

// DirectGlobalUses walks only this program's own body and classifies every
// GlobalRef it finds — no FB-instance recursion. lang/st uses this to
// populate an owning FBDef.Uses at Lower time, one FB body at a time; a
// deep version there would risk baking in a stale, partly-empty snapshot of
// a sibling FB not yet lowered (bodies lower in declaration order, and one
// may instantiate another declared later in the same file). GlobalUses is
// the version everything else should call.
//
// A compound assignment target (`P101.Speed := …`, `Buf[i] := …`) counts as
// BOTH: setting one field means reading the aggregate that holds it, so the
// tag must already have a value.
func (p *Program) DirectGlobalUses() GlobalUse {
	u := GlobalUse{Read: map[string]bool{}, Written: map[string]bool{}}
	u.stmts(p.Body)
	return u
}

// GlobalUses reports how this program uses the PLC-wide variables it
// binds: DirectGlobalUses, plus transitively the Uses of every
// FUNCTION_BLOCK instance it declares — nested FB-in-FB included. A
// library FB's VAR_EXTERNAL reads and writes the tag store through
// whichever program's instance steps it, so those reads/writes belong to
// this program exactly as if it had named the tag directly (see
// FBDef.Uses and Program.GlobalsDeep, which this mirrors). Safe to call
// any time after the whole project has finished lowering — unlike
// FBDef.Uses, which a sibling FB may still be populating.
func (p *Program) GlobalUses() GlobalUse {
	u := p.DirectGlobalUses()
	seen := map[*FBDef]bool{}
	for _, s := range p.Slots {
		u.collectFB(s.Type, seen)
	}
	return u
}

// collectFB is the GlobalUse counterpart of collectFBGlobals: it merges in
// the Read/Written sets of every FB instance reachable from t (directly, or
// nested inside an array/struct/FB-in-FB), guarding against revisiting a
// shared *FBDef.
func (u *GlobalUse) collectFB(t *Type, seen map[*FBDef]bool) {
	if t == nil {
		return
	}
	switch t.Kind {
	case TypeFB:
		if t.FB == nil || seen[t.FB] {
			return
		}
		seen[t.FB] = true
		for name := range t.FB.Uses.Read {
			u.Read[name] = true
		}
		for name := range t.FB.Uses.Written {
			u.Written[name] = true
		}
		for _, s := range t.FB.AllSlots() {
			u.collectFB(s.Type, seen)
		}
	case TypeArray:
		u.collectFB(t.Elem, seen)
	case TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				u.collectFB(f.Type, seen)
			}
		}
	}
}

func (u *GlobalUse) stmts(ss []Stmt) {
	for _, s := range ss {
		u.stmt(s)
	}
}

func (u *GlobalUse) stmt(s Stmt) {
	switch n := s.(type) {
	case *Assign:
		u.target(n.Target)
		u.expr(n.Value)
	case *If:
		u.expr(n.Cond)
		u.stmts(n.Then)
		u.stmts(n.Else)
	case *For:
		u.expr(n.Start)
		u.expr(n.End)
		u.expr(n.Step)
		u.stmts(n.Body)
	case *While:
		u.expr(n.Cond)
		u.stmts(n.Body)
	case *Repeat:
		u.stmts(n.Body)
		u.expr(n.Cond)
	case *Case:
		u.expr(n.Expr)
		for _, c := range n.Clauses {
			for _, v := range c.Values {
				u.expr(v)
			}
			u.stmts(c.Body)
		}
		u.stmts(n.Else)
	case *FBCall:
		for _, in := range n.Inputs {
			u.expr(in.Value)
		}
		for _, out := range n.Outputs {
			u.target(out.Target)
		}
	case *ExprStmt:
		u.expr(n.X)
	case *Return, *Exit, *Continue, nil:
		// no globals reachable
	}
}

// target classifies an assignment destination. Only a bare GlobalRef is a
// pure write; reaching into a field or element reads the aggregate first.
func (u *GlobalUse) target(l LValue) {
	switch n := l.(type) {
	case *GlobalRef:
		u.Written[n.Name] = true
	case *MemberRef:
		u.expr(n.Object)
		u.markWrite(n.Object)
	case *IndexRef:
		u.expr(n.Array)
		u.expr(n.Index)
		u.markWrite(n.Array)
	}
}

// markWrite records the tag underneath a compound target as written too, so
// a program that only ever assigns `P101.Speed` is not reported as never
// writing P101.
func (u *GlobalUse) markWrite(e Expr) {
	switch n := e.(type) {
	case *GlobalRef:
		u.Written[n.Name] = true
	case *MemberRef:
		u.markWrite(n.Object)
	case *IndexRef:
		u.markWrite(n.Array)
	}
}

func (u *GlobalUse) expr(e Expr) {
	switch n := e.(type) {
	case *GlobalRef:
		u.Read[n.Name] = true
	case *BinOp:
		u.expr(n.L)
		u.expr(n.R)
	case *UnOp:
		u.expr(n.X)
	case *IndexRef:
		u.expr(n.Array)
		u.expr(n.Index)
	case *MemberRef:
		u.expr(n.Object)
	case *Call:
		for _, a := range n.Args {
			u.expr(a)
		}
	case *UserCall:
		for _, a := range n.Args {
			u.expr(a)
		}
	case *Lit, *SlotRef, nil:
		// no globals reachable
	}
}
