package st

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// LowerWarning is something lowering accepts that a reader likely
// misreads: the program compiles and runs, so it is not an error, but the
// author is told what it means (LowerOpts.Warn).
type LowerWarning struct {
	Pos Pos
	Msg string
}

// dedupWarn passes each distinct warning to warn once: a FUNCTION_BLOCK's
// declarations are lowered for its signature and again for its body, and
// the author needs to read about one place once.
func dedupWarn(warn func(LowerWarning)) func(LowerWarning) {
	if warn == nil {
		return nil
	}
	seen := map[LowerWarning]bool{}
	return func(w LowerWarning) {
		if seen[w] {
			return
		}
		seen[w] = true
		warn(w)
	}
}

// warnIntAsTime warns where an integer reaches a TIME (F-16). IEC 61131-3
// has no implicit integer-to-TIME conversion, and Codesys and TIA refuse
// one; nautilus reads it as milliseconds, which happens to match a Logix
// PRE and is a timer a thousand times short for an author who meant
// seconds. It stays legal, so existing programs keep compiling, and the
// warning says what the number means and how to write it unambiguously.
// src is the source expression, v its lowered form, want the type
// expected there, and what names the place (`TON input PT`).
func (l *lowerer) warnIntAsTime(src Expression, want *ir.Type, v ir.Expr, what string) {
	if l.warn == nil || want == nil || want.Kind != ir.TypeTime || v == nil {
		return
	}
	got := v.ExprType()
	if got == nil || !got.IsInteger() || got.Enum != nil {
		return
	}
	pos := nodePos(src)
	if isIntLiteral(src) {
		if c, ok := ir.ConstValue(v); ok {
			l.warn(LowerWarning{Pos: pos, Msg: fmt.Sprintf(
				"%s: integer %d read as milliseconds where a TIME is expected; write %s",
				what, c.I, timeLiteral(c.I))})
			return
		}
	}
	l.warn(LowerWarning{Pos: pos, Msg: fmt.Sprintf(
		"%s: %s (a %s) read as milliseconds where a TIME is expected; write a T# literal, or TO_TIME(%s) if milliseconds are meant",
		what, exprLabel(src), got, exprLabel(src))})
}

// warnIntLiteralAsTime warns where a declaration initialises a TIME with
// an integer literal (`Delay : TIME := 5000;`): the same misreading as
// warnIntAsTime, in a declaration rather than a call.
func (l *lowerer) warnIntLiteralAsTime(src Expression, want *ir.Type, what string) {
	if l.warn == nil || want == nil || want.Kind != ir.TypeTime || want.Enum != nil || !isIntLiteral(src) {
		return
	}
	v, err := evalConstValue(src, want)
	if err != nil {
		return
	}
	l.warn(LowerWarning{Pos: nodePos(src), Msg: fmt.Sprintf(
		"%s: integer %d read as milliseconds where a TIME is expected; write %s",
		what, v.I, timeLiteral(v.I))})
}

// isIntLiteral reports a bare integer literal, signed or not: 30000, -5,
// 16#FF. A REAL literal is not one.
func isIntLiteral(e Expression) bool {
	if u, ok := e.(*UnaryExpr); ok && (u.Op == "-" || u.Op == "+") {
		e = u.Operand
	}
	n, ok := e.(*NumberLit)
	if !ok {
		return false
	}
	return n.Base != 10 && n.Base != 0 || !strings.ContainsAny(n.Value, ".eE")
}

// timeLiteral writes ms as the ST duration literal an author would:
// 30000 → T#30S, 90000 → T#1M30S, 250 → T#250MS.
func timeLiteral(ms int64) string {
	return strings.ToUpper(ir.FormatDuration(ms))
}
