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

// GlobalUses walks the program body and classifies every GlobalRef.
//
// A compound assignment target (`P101.Speed := …`, `Buf[i] := …`) counts as
// BOTH: setting one field means reading the aggregate that holds it, so the
// tag must already have a value.
func (p *Program) GlobalUses() GlobalUse {
	u := GlobalUse{Read: map[string]bool{}, Written: map[string]bool{}}
	u.stmts(p.Body)
	return u
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
