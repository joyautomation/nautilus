package writer

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// LReal literals (#1429). TIA keeps 15 significant digits of an LReal
// literal (measured on the bench: 3.141592653589793 compiled to
// 400921FB54442D11, float64's π is 400921FB54442D18). nautilus reads a
// literal as the float64 it names, which can need 17. Where a literal is
// an LREAL (pw.lreal: an LREAL store or argument, arithmetic or a
// comparison with an LREAL, a call that returns one) and 15 digits do not
// give back its float64, the writer writes its bit pattern instead,
// LWORD_TO_LREAL(16#…), with the decimal beside it. A REAL needs 9
// digits, so a REAL literal is written as it is. A start value is one
// literal in SCL: one that 15 digits cannot reproduce is refused.

// longReal reports a real literal (or its negation) whose float64 value
// a 15-digit decimal does not reproduce, with that value.
func longReal(e st.Expression) (float64, bool) {
	neg := false
	if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
		e, neg = u.Operand, true
	}
	n, ok := e.(*st.NumberLit)
	if !ok || (n.Base != 10 && n.Base != 0) || !strings.ContainsAny(n.Value, ".eE") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(n.Value, "_", ""), 64)
	if err != nil || math.IsInf(f, 0) {
		return 0, false
	}
	back, err := strconv.ParseFloat(strconv.FormatFloat(f, 'g', 15, 64), 64)
	if err == nil && back == f {
		return 0, false
	}
	if neg {
		f = -f
	}
	return f, true
}

// lrealBits writes the float64 f as its bit pattern, its decimal beside.
func lrealBits(f float64) string {
	return fmt.Sprintf("LWORD_TO_LREAL(16#%016X) (* %s *)", math.Float64bits(f), strconv.FormatFloat(f, 'g', -1, 64))
}

// inLREAL runs print with pw.lreal set to on, and restores it.
func (pw *progWriter) inLREAL(on bool, print func() string) string {
	was := pw.lreal
	pw.lreal = on
	defer func() { pw.lreal = was }()
	return print()
}

// realContext is whether the operands of an operation on types a and b
// are LREALs: yes beside an LREAL, no beside a REAL, and as the caller's
// context when both are literals.
func (pw *progWriter) realContext(a, b string) bool {
	switch {
	case a == "LREAL" || b == "LREAL":
		return true
	case a == "REAL" || b == "REAL":
		return false
	}
	return pw.lreal
}

// refusedStart refuses a start value 15 digits cannot reproduce: a real
// limit of TIA's literals, not a writer gap.
func (pw *progWriter) refusedStart(e st.Expression, name string, line int) bool {
	f, long := longReal(e)
	if !long {
		return false
	}
	pw.diag("siemens/literal", line, fmt.Sprintf("%s: the start value %s has more than the 15 significant digits TIA keeps of a literal (it would start at %s); write at most 15 (#1429)",
		name, strconv.FormatFloat(f, 'g', -1, 64), strconv.FormatFloat(f, 'g', 15, 64)))
	return true
}
