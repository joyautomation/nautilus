package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// FOR loops (#1305 B1, #1427).
//
// nautilus runs a FOR on a 64-bit counter (lang/ir vm.go): the start, end
// and step are evaluated once, at 64 bits; the counter starts at start and
// is tested before each pass; each pass stores the counter into the loop
// variable and runs the body, then the counter steps. A pass whose counter
// the variable's type cannot hold faults the scan, as an index out of
// bounds does. The counter, not the variable, decides the next pass, so a
// body that writes the variable does not change the loop. After the loop
// the variable holds the counter's exit value, the first value that failed
// the test, wrapped to its type (end + step when the step lands on end,
// start when no pass ran); EXIT and RETURN leave it as the pass left it. A
// step of zero faults the scan.
//
// TIA's FOR steps the variable itself, in its own type. The writer writes
// a plain FOR only where the loop provably ends on the CPU and its exit
// value is known (forPlain), and every other loop as a counter loop over
// LINT temps that surely ends:
//
//	#c := <start>; #e := <end>;            (LINT, nautilus's 64 bits)
//	#r := #c <= #e;
//	WHILE #r DO
//	    <guard: #c outside the variable's type faults the scan>
//	    #i := LINT_TO_<T>(#c);
//	    #r := #c <= LINT#<max - step> AND #c + <step> <= #e;
//	    #c := #c + <step>;                 (wrapping at 64 bits for a LINT)
//	    <body>
//	END_WHILE;
//	#i := <#c wrapped to T>;               (the exit value)
//
// The guard is written when a bound is integer arithmetic (it can leave
// the variable's type), and faults exactly where nautilus does. A loop
// whose body EXITs it sets a flag first (forExitFlag), so the exit value
// is stored only when the loop ran out. An integer arithmetic step stays
// refused.

// forPlain reports whether TIA's own FOR, run over a temp of the
// variable's type, surely ends and runs exactly as nautilus does: a
// constant non-zero step, and a constant start and end that run at least
// one pass and keep the temp, stepped past the end, inside the type.
func (pw *progWriter) forPlain(s *st.ForStmt) bool {
	t := pw.typ(&st.IdentExpr{Name: s.Variable})
	if !isInt(t) {
		return false
	}
	step := int64(1)
	if s.Step != nil {
		k, ok := pw.fold(s.Step)
		if !ok || k == 0 {
			return false
		}
		step = k
	}
	// Constant bounds only: TIA's FOR is not proved to read a variable
	// end once, as nautilus does.
	start, ok := pw.fold(s.Start)
	if !ok || !inRange(start, t) {
		return false
	}
	end, ok := pw.fold(s.End)
	if !ok || addOverflows(end, step) {
		return false
	}
	// TIA refuses to compile a constant FOR that never runs ("The high
	// limit is not higher than the low limit", bench 11-27): the counter
	// loop writes it.
	if (step > 0 && start > end) || (step < 0 && start < end) {
		return false
	}
	return inRange(end, t) && inRange(end+step, t)
}

// boundRange is the values a FOR bound can take: the value of a constant,
// the range of an integer variable's type. ok is false for integer
// arithmetic and anything else.
func (pw *progWriter) boundRange(e st.Expression, t string) (lo, hi int64, ok bool) {
	if n, ok := pw.fold(e); ok {
		return n, n, true
	}
	bt := pw.typ(e)
	if !isInt(bt) || bt == "ULINT" {
		return 0, 0, false
	}
	lo, hi = typeRange(bt)
	return lo, hi, true
}

// boundInType reports whether every value bound e can take fits t: a
// constant in range, or an integer of a type t holds. Integer arithmetic
// can leave it.
func (pw *progWriter) boundInType(e st.Expression, t string) bool {
	lo, hi, ok := pw.boundRange(e, t)
	return ok && inRange(lo, t) && inRange(hi, t)
}

// forTemp declares a temp of a FOR loop: a hoisted temp with its own
// numbering, so the loops do not renumber the index and call temps.
func (pw *progWriter) forTemp(prefix, typ string) string {
	n := pw.hoistN
	pw.hoistN = pw.forN
	name := pw.hoistTemp(prefix, typ)
	pw.forN, pw.hoistN = pw.hoistN, n
	return name
}

// typeRange is the least and greatest value of integer type t (ULINT's
// greatest is clipped to LINT's).
func typeRange(t string) (int64, int64) {
	w := intWidth[t]
	if w.bits == 64 {
		if w.signed {
			return -1 << 63, 1<<63 - 1
		}
		return 0, 1<<63 - 1
	}
	if w.signed {
		h := int64(1) << (w.bits - 1)
		return -h, h - 1
	}
	return 0, int64(1)<<w.bits - 1
}

func addOverflows(a, b int64) bool {
	s := a + b
	return (b > 0 && s < a) || (b < 0 && s > a)
}

// lintOf prints e as a LINT: integer arithmetic at nautilus's 64 bits, a
// constant as is, an integer or bit string widened.
func (pw *progWriter) lintOf(e st.Expression) string {
	if n, ok := pw.fold(e); ok {
		return strconv.FormatInt(n, 10)
	}
	t := pw.typ(e)
	if t == tWide {
		wide := pw.wide
		pw.wide = true
		s := pw.expr(e, 0)
		pw.wide = wide
		return s
	}
	return toLINT(t, pw.expr(e, 0))
}

// forCounter writes the counter loop (see above), or refuses it.
func (pw *progWriter) forCounter(b *strings.Builder, s *st.ForStmt, in string, at, d int) {
	v := pw.ident(s.Variable, s.Pos.Line)
	t := pw.typ(&st.IdentExpr{Name: s.Variable})
	if !isInt(t) || t == "ULINT" {
		pw.gap("siemens/int-width", at, gapOther, "a FOR over a "+orUnknown(t), "")
		return
	}
	if s.Step != nil && pw.typ(s.Step) == tWide {
		if _, ok := pw.fold(s.Step); !ok {
			pw.gap("siemens/int-width", at, gapOther, "a FOR step that is integer arithmetic", "compute the step into a variable first")
			return
		}
	}
	c := "#" + pw.forTemp("naut_for_c", "LINT")
	e := "#" + pw.forTemp("naut_for_e", "LINT")
	r := "#" + pw.forTemp("naut_for_r", "BOOL")
	pw.emit(b, at, "%s%s := %s;\n", in, c, pw.lintOf(s.Start))
	pw.emit(b, at, "%s%s := %s;\n", in, e, pw.lintOf(s.End))
	var first, next string
	step, constStep := int64(1), true
	if s.Step != nil {
		step, constStep = pw.fold(s.Step)
	}
	stepAdd := ""
	switch {
	case constStep && step == 0:
		pw.faultIf(b, in, at, "TRUE") // nautilus: "FOR step is zero"
		return
	// The next pass runs when c + step stays on the near side of the end;
	// c + step is only compared once it cannot overflow, so the test is
	// exact for every LINT, and the counter never wraps round.
	case constStep && step > 0:
		first = fmt.Sprintf("%s <= %s", c, e)
		next = fmt.Sprintf("%s <= LINT#%d AND %s + %d <= %s", c, int64(1<<63-1)-step, c, step, e)
		stepAdd = " + " + strconv.FormatInt(step, 10)
	case constStep:
		first = fmt.Sprintf("%s >= %s", c, e)
		next = fmt.Sprintf("%s >= LINT#%d AND %s - %d >= %s", c, int64(-1<<63)-step, c, -step, e)
		stepAdd = " - " + strconv.FormatInt(-step, 10)
	default:
		k := "#" + pw.forTemp("naut_for_s", "LINT")
		pw.emit(b, at, "%s%s := %s;\n", in, k, pw.lintOf(s.Step))
		pw.faultIf(b, in, at, k+" = 0") // nautilus: "FOR step is zero"
		first = fmt.Sprintf("(%s > 0 AND %s <= %s) OR (%s < 0 AND %s >= %s)", k, c, e, k, c, e)
		next = fmt.Sprintf("(%s > 0 AND %s <= LINT#9223372036854775807 - %s AND %s + %s <= %s) OR (%s < 0 AND %s >= LINT#-9223372036854775807 - 1 - %s AND %s + %s >= %s)",
			k, c, k, c, k, e, k, c, k, c, k, e)
		stepAdd = " + " + k
	}
	x := pw.forExitFlag(b, s, in, at)
	pw.emit(b, at, "%s%s := %s;\n", in, r, first)
	pw.emit(b, at, "%sWHILE %s DO\n", in, r)
	if t != "LINT" && (!pw.boundInType(s.Start, t) || !pw.boundInType(s.End, t)) {
		lo, hi := typeRange(t)
		pw.faultIf(b, in+"\t", at, fmt.Sprintf("%s < LINT#%d OR %s > LINT#%d", c, lo, c, hi))
	}
	if t == "LINT" {
		pw.emit(b, at, "%s\t%s := %s;\n", in, v, c)
	} else {
		pw.emit(b, at, "%s\t%s := LINT_TO_%s(%s);\n", in, v, t, c)
	}
	pw.emit(b, at, "%s\t%s := %s;\n", in, r, next)
	pw.forStep(b, in+"\t", at, t, c, stepAdd, step, constStep)
	k := strings.ToLower(s.Variable)
	prev, had := pw.forRange[k]
	delete(pw.forRange, k)
	pw.loopExit = append(pw.loopExit, x)
	pw.stmts(b, s.Body, d+1)
	pw.loopExit = pw.loopExit[:len(pw.loopExit)-1]
	if had {
		pw.forRange[k] = prev
	}
	pw.emit(b, at, "%sEND_WHILE;\n", in)
	pw.forExit(b, in, at, x, fmt.Sprintf("%s := %s;", v, wrap(c, t)))
}

const (
	lintMax = "LINT#9223372036854775807"
	lintMin = "(LINT#-9223372036854775807 - 1)"
)

// forStep steps the counter c to the next value nautilus's counter takes.
// Narrower than LINT, c holds a value of the variable's type (the guard
// saw to it) and so does the step, so c + step cannot leave LINT. A LINT
// counter can: past LINT's range the step wraps at 64 bits, as nautilus's
// does, computed in an order no partial sum of which leaves LINT, so the
// CPU's overflow handling never matters.
func (pw *progWriter) forStep(b *strings.Builder, in string, at int, t, c, stepAdd string, step int64, constStep bool) {
	if t != "LINT" {
		pw.emit(b, at, "%s%s := %s%s;\n", in, c, c, stepAdd)
		return
	}
	up := func(k string) string { // c + k - 2^64, for c > max - k
		return fmt.Sprintf("%s - %s - 1 + %s - %s - 1", c, lintMax, k, lintMax)
	}
	down := func(k string) string { // c + k + 2^64, for c < min - k
		return fmt.Sprintf("%s + %s + 1 + %s + %s + 1", c, lintMax, k, lintMax)
	}
	switch {
	case constStep && step > 0:
		k := strconv.FormatInt(step, 10)
		pw.emit(b, at, "%sIF %s <= LINT#%d THEN\n%s\t%s := %s + %s;\n%sELSE\n%s\t%s := %s;\n%sEND_IF;\n",
			in, c, int64(1<<63-1)-step, in, c, c, k, in, in, c, up(k), in)
	case constStep:
		k := "(" + strconv.FormatInt(step, 10) + ")"
		pw.emit(b, at, "%sIF %s >= LINT#%d THEN\n%s\t%s := %s%s;\n%sELSE\n%s\t%s := %s;\n%sEND_IF;\n",
			in, c, int64(-1<<63)-step, in, c, c, stepAdd, in, in, c, down(k), in)
	default:
		k := strings.TrimPrefix(stepAdd, " + ")
		pw.emit(b, at, "%sIF %s > 0 THEN\n", in, k)
		pw.emit(b, at, "%s\tIF %s <= %s - %s THEN\n%s\t\t%s := %s + %s;\n%s\tELSE\n%s\t\t%s := %s;\n%s\tEND_IF;\n",
			in, c, lintMax, k, in, c, c, k, in, in, c, up(k), in)
		pw.emit(b, at, "%sELSIF %s >= %s - %s THEN\n%s\t%s := %s + %s;\n", in, c, lintMin, k, in, c, c, k)
		pw.emit(b, at, "%sELSE\n%s\t%s := %s;\n%sEND_IF;\n", in, in, c, down(k), in)
	}
}

// forExitFlag declares and clears the flag a FOR's EXIT sets, when its
// body EXITs it; "" when it does not.
func (pw *progWriter) forExitFlag(b *strings.Builder, s *st.ForStmt, in string, at int) string {
	if !exitsLoop(s.Body) {
		return ""
	}
	x := "#" + pw.forTemp("naut_for_x", "BOOL")
	pw.emit(b, at, "%s%s := FALSE;\n", in, x)
	return x
}

// forExit stores the exit value (set, a statement) after the loop, unless
// its EXIT left it (flag x set).
func (pw *progWriter) forExit(b *strings.Builder, in string, at int, x, set string) {
	if x == "" {
		pw.emit(b, at, "%s%s\n", in, set)
		return
	}
	pw.emit(b, at, "%sIF NOT %s THEN\n%s\t%s\n%sEND_IF;\n", in, x, in, set, in)
}

// exitsLoop reports whether body has an EXIT of the loop it is the body
// of: one outside any loop nested in it.
func exitsLoop(body []st.Statement) bool {
	for _, s := range body {
		switch s := s.(type) {
		case *st.ExitStmt:
			return true
		case *st.IfStmt:
			if exitsLoop(s.Then) || exitsLoop(s.Else) {
				return true
			}
			for _, e := range s.ElsIfs {
				if exitsLoop(e.Body) {
					return true
				}
			}
		case *st.CaseStmt:
			if exitsLoop(s.Else) {
				return true
			}
			for _, c := range s.Cases {
				if exitsLoop(c.Body) {
					return true
				}
			}
		}
	}
	return false
}

// forPlainExit is the exit value of a loop forPlain writes: the last
// pass's value plus the step (at least one pass runs, and the value is
// inside the type: forPlain saw to both).
func (pw *progWriter) forPlainExit(s *st.ForStmt) int64 {
	start, _ := pw.fold(s.Start)
	end, _ := pw.fold(s.End)
	step := int64(1)
	if s.Step != nil {
		step, _ = pw.fold(s.Step)
	}
	return start + ((end-start)/step+1)*step
}
