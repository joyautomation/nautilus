package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// FOR loops (#1305 B1).
//
// nautilus runs a FOR on a 64-bit counter (lang/ir vm.go): the start, end
// and step are evaluated once, at 64 bits; each pass stores the counter
// into the loop variable and runs the body; the counter, not the variable,
// decides the next pass, so a body that writes the variable does not
// change the loop, and after it the variable holds the last value it ran
// with (or is unchanged when the loop never ran). A step of zero faults
// the scan.
//
// TIA's FOR steps the variable itself, in its own type: a loop whose end
// is the type's last value could wrap round and never end (a watchdog
// trip, then STOP). The writer therefore writes a plain FOR only where the
// loop provably ends on the CPU (forPlain), and every other loop as a
// counter loop over LINT temps that surely ends:
//
//	#c := <start>; #e := <end>;            (LINT, nautilus's 64 bits)
//	#r := #c <= #e;
//	WHILE #r DO
//	    <guard: #c outside the variable's type faults the scan>
//	    #i := LINT_TO_<T>(#c);
//	    #r := #c <= LINT#<max - step> AND #c + <step> <= #e;
//	    IF #r THEN #c := #c + <step>; END_IF;
//	    <body>
//	END_WHILE;
//
// The guard is written when a bound is integer arithmetic (it can leave
// the variable's type): there nautilus would store a value its type cannot
// hold into the variable, which the CPU cannot; the writer faults the scan
// at that pass instead, as an out-of-range index does (naut-lead ruling,
// #1305 B1), a difference the guide names. An integer arithmetic step
// stays refused.

// forPlain reports whether TIA's own FOR, run over a temp of the
// variable's type (forPlainLoop), surely ends and runs exactly as nautilus
// does: a constant non-zero step, and a constant start and end that keep
// the temp, stepped past the end, inside the type.
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
	pw.emit(b, at, "%s\tIF %s THEN\n%s\t\t%s := %s%s;\n%s\tEND_IF;\n", in, r, in, c, c, stepAdd, in)
	k := strings.ToLower(s.Variable)
	prev, had := pw.forRange[k]
	delete(pw.forRange, k)
	pw.stmts(b, s.Body, d+1)
	if had {
		pw.forRange[k] = prev
	}
	pw.emit(b, at, "%sEND_WHILE;\n", in)
}
