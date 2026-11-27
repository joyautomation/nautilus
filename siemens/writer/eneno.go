package writer

import (
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// EN/ENO (#1305 B2). nautilus (lang/st lower_eneno.go) gates a call that
// binds EN or ENO:
//
//	IF <EN> THEN <the call, or the assignment of its result>; ENO := TRUE
//	ELSE ENO := FALSE END_IF
//
// EN is evaluated once, before the call; while it is FALSE the call's
// arguments are not evaluated, an FB's body does not run, its outputs and
// every `=>` binding keep their values, and a function's result is not
// assigned. A call that errors faults the scan, so ENO is EN. The writer
// writes exactly that structure. ENO is never TIA's own (which turns FALSE
// on errors nautilus does not have): it is the branch the gate took.
// A block that declares its own EN input or ENO output keeps them as
// ordinary pins (lang/st splitExecControl).

// execControl splits EN := … and ENO => … off call, unless the callee
// declares pins of those names. rest is a copy; the parsed call is never
// changed.
func (pw *progWriter) execControl(call *st.CallExpr) (rest *st.CallExpr, en, eno st.Expression, ok bool) {
	ownEN, ownENO := pw.declaresENENO(call)
	out := *call
	out.NamedArgs = nil
	out.OutputBindings = nil
	for _, na := range call.NamedArgs {
		if !ownEN && strings.EqualFold(na.Name, "EN") {
			en = na.Value
			continue
		}
		out.NamedArgs = append(out.NamedArgs, na)
	}
	for _, ob := range call.OutputBindings {
		if !ownENO && strings.EqualFold(ob.Name, "ENO") {
			eno = ob.Target
			continue
		}
		out.OutputBindings = append(out.OutputBindings, ob)
	}
	return &out, en, eno, en != nil || eno != nil
}

// declaresENENO reports whether the called block declares its own EN
// input and ENO output (a ladder-style user block).
func (pw *progWriter) declaresENENO(call *st.CallExpr) (en, eno bool) {
	var p *pou
	fc := false
	if l, ok := pw.locals[strings.ToLower(call.Name)]; ok && call.Callee == nil {
		p = pw.w.userFB(l.typ)
	} else {
		p, fc = pw.w.userFC(call.Name), true
	}
	if p == nil {
		return false, false
	}
	for _, vb := range p.vbs {
		for _, v := range vb.Variables {
			switch {
			case vb.Kind == "VAR_INPUT" && strings.EqualFold(v.Name, "EN"):
				en = true
			case vb.Kind == "VAR_OUTPUT" && strings.EqualFold(v.Name, "ENO") && !fc:
				eno = true // a FUNCTION's ENO is always nautilus's (lower_eneno.go)
			}
		}
	}
	return en, eno
}

// gated writes s, a statement whose call has execution control split off
// into en and eno, under the gate. It reports false when s has none.
func (pw *progWriter) gated(b *strings.Builder, s st.Statement, d int) bool {
	var call *st.CallExpr
	var with func(*st.CallExpr) st.Statement
	switch s := s.(type) {
	case *st.AssignStmt:
		c, ok := s.Value.(*st.CallExpr)
		if !ok {
			return false
		}
		call = c
		with = func(r *st.CallExpr) st.Statement { a := *s; a.Value = r; return &a }
	case *st.CallStmt:
		call = s.Call
		with = func(r *st.CallExpr) st.Statement { c := *s; c.Call = r; return &c }
	default:
		return false
	}
	rest, en, eno, ok := pw.execControl(call)
	if !ok {
		return false
	}
	setENO := func(v bool) st.Statement {
		return &st.AssignStmt{TargetExpr: eno, Value: &st.BoolLit{Value: v}, Pos: call.Pos}
	}
	if en == nil {
		pw.stmt(b, with(rest), d)
		if eno != nil {
			pw.stmt(b, setENO(true), d)
		}
		return true
	}
	// The gate is an IF, written as any IF is: what EN computes first (an
	// index check, a FUNCTION that can fault) comes before it.
	gate := &st.IfStmt{Condition: en, Then: []st.Statement{with(rest)}, Pos: call.Pos}
	if eno != nil {
		gate.Then = append(gate.Then, setENO(true))
		gate.Else = []st.Statement{setENO(false)}
	}
	pw.stmt(b, gate, d)
	return true
}
