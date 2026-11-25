package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// An S7-1500 IEC timer (TON_TIME, TOF_TIME, TP_TIME) is no plain data:
// every read of its Q or ET updates it from the clock, not only its call
// (measured on the bench 1510SP, #1385). In nautilus a read returns what
// the last call left. So the writer latches them: each timer instance t
// has a static beside it, "<prefix>t_last" (lastNames), that its call
// fills through Q => and ET =>, and every read of t.Q or t.ET in the
// program, from outside a block too, reads that static instead. The
// program then touches the timer only through its call, so the
// instance's own Q and ET, which the Web API reads (Output.Symbol,
// naut siemens serve), hold what the last call left too. Before the first call it holds FALSE and T#0s, an
// uncalled nautilus timer's outputs; a call that is skipped leaves the
// last call's values. A write to Q or ET is refused (refusedTimerWrite).

// timers are the IEC timers the writer latches.
var timers = map[string]bool{"TON": true, "TOF": true, "TP": true}

// timerOut reports whether member is a timer output the latch holds.
func timerOut(member string) bool {
	u := strings.ToUpper(member)
	return u == "Q" || u == "ET"
}

// lastDecl declares a timer's latch, a static as the instance is.
func lastDecl(name string) string {
	return fmt.Sprintf("      %s : Struct\n         Q : Bool;\n         ET : Time;\n      END_STRUCT;", sclName(name))
}

// lastNames names the latch of each static timer instance that vbs
// declare (lower-cased instance name to latch name): the block prefix,
// the instance's name and "_last", with a number after it when the block
// already declares that name in any case (a user's own variable keeps its
// name).
func (w *writer) lastNames(vbs []st.VarBlock) map[string]string {
	// A library's global constant is copied into each block that names
	// it under its own name (pous.go), so its name is taken too.
	taken := map[string]bool{}
	for k := range w.lib.constDecls {
		taken[k] = true
	}
	var insts []string
	for _, vb := range vbs {
		for _, v := range vb.Variables {
			taken[strings.ToLower(v.Name)] = true
			if vb.Kind == "VAR" && !vb.Constant && timers[strings.ToUpper(strings.TrimSpace(v.Datatype))] {
				insts = append(insts, v.Name)
			}
		}
	}
	out := map[string]string{}
	for _, n := range insts {
		base := w.prefix + n + "_last"
		name := base
		for i := 2; taken[strings.ToLower(name)]; i++ {
			name = base + strconv.Itoa(i)
		}
		taken[strings.ToLower(name)] = true
		out[strings.ToLower(n)] = name
	}
	return out
}

// lastPath prints the latch member that holds m, a timer's Q or ET: the
// program's or block's own timer (t.Q), or one inside an instance of the
// project's block (m.t.Q, at any depth). Any other path is refused, never
// written as a read of the timer.
func (pw *progWriter) lastPath(m *st.MemberExpr) string {
	pin := strings.ToUpper(m.Member)
	switch o := m.Object.(type) {
	case *st.IdentExpr:
		if n, ok := pw.last[strings.ToLower(o.Name)]; ok {
			return "#" + sclName(n) + "." + pin
		}
	case *st.MemberExpr:
		if p := pw.w.userFB(pw.typ(o.Object)); p != nil {
			if n, ok := pw.w.lastNames(p.vbs)[strings.ToLower(o.Member)]; ok {
				if obj := pw.expr(o.Object, unaryPrec+1); obj != "" {
					return obj + "." + sclName(n) + "." + pin
				}
				return ""
			}
		}
	}
	pw.gap("siemens/timer", m.Pos.Line, gapOther, "reading "+exprShort(m)+" here", "the writer latches a timer's Q and ET where it can name the instance")
	return ""
}

// refusedTimerWrite refuses a write to a timer's Q or ET (an assignment,
// an output binding of any call, an in-out binding). The writer keeps a
// timer's outputs as its call leaves them, in the latch; nautilus's TP
// reads its own Q back as its pulse at the next call, which the CPU's
// TP_TIME never sees.
func (pw *progWriter) refusedTimerWrite(e st.Expression, line int) bool {
	m, ok := e.(*st.MemberExpr)
	if !ok || !timers[pw.typ(m.Object)] || !timerOut(m.Member) {
		return false
	}
	pw.gap("siemens/timer", line, gapOther, "a write to "+exprShort(m)+", a timer's output", "the writer keeps a timer's Q and ET as its call leaves them")
	return true
}

// refusedTimerCopy refuses assigning a whole timer (u := t): the copy
// would leave u's latch as it was.
func (pw *progWriter) refusedTimerCopy(s *st.AssignStmt) bool {
	te := s.TargetExpr
	if te == nil {
		te = &st.IdentExpr{Name: s.Target, Pos: s.Pos}
	}
	if !timers[pw.typ(te)] && !timers[pw.typ(s.Value)] {
		return false
	}
	pw.gap("siemens/timer", s.Pos.Line, gapOther, "assigning a whole timer instance", "call each timer and read its outputs")
	return true
}
