package st

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// This file holds the lowering for named constants and CASE labels (#196,
// #176), enumerated types (#238) and partial access to an integer (#222).

// lowerExprHint lowers e expecting a value of type hint: an unqualified
// enumeration member that more than one enumeration declares resolves to
// the hint's. The hint never changes what an expression means otherwise.
func (l *lowerer) lowerExprHint(e Expression, hint *ir.Type) (ir.Expr, error) {
	prev := l.hint
	l.hint = hint
	defer func() { l.hint = prev }()
	return l.lowerExpr(e)
}

// isScalarKind reports an elementary (or enumerated) type: a constant of one
// folds to a literal.
func isScalarKind(t *ir.Type) bool {
	if t == nil {
		return false
	}
	switch t.Kind {
	case ir.TypeBool, ir.TypeInt, ir.TypeReal, ir.TypeTime, ir.TypeString:
		return true
	}
	return false
}

// ─── Constants ────────────────────────────────────────────────────────────

// checkNotConst rejects writing a constant: a VAR CONSTANT, a project
// constant (VAR_GLOBAL CONSTANT), or an enumeration member, whether whole
// or through a field, element or bit.
func (l *lowerer) checkNotConst(e Expression) error {
	root := e
	for {
		switch n := root.(type) {
		case *MemberExpr:
			root = n.Object
			continue
		case *IndexExpr:
			root = n.Array
			continue
		}
		break
	}
	id, ok := root.(*IdentExpr)
	if !ok {
		return nil
	}
	if sym, found := l.lookup(id.Name); found {
		if sym.constant {
			return errName(id.Pos, id.Name, fmt.Errorf("%s is a constant (VAR CONSTANT) and cannot be written", sym.name))
		}
		return nil
	}
	if c, isConst := l.consts[ir.NameKey(id.Name)]; isConst {
		return errName(id.Pos, id.Name, fmt.Errorf("%s is a constant (%s) and cannot be written", c.name, c.what))
	}
	if t, m, ok := l.uniqueEnumMember(id.Name); ok {
		return errName(id.Pos, id.Name, fmt.Errorf("%s is a value of the enumeration %s (%s#%s), not a variable", m.Name, t.Enum.Name, t.Enum.Name, m.Name))
	}
	return nil
}

// ─── CASE labels ──────────────────────────────────────────────────────────

// caseLabel is one folded CASE label (a value, or a range lo..hi) and how
// it was written, for the duplicate diagnostic.
type caseLabel struct {
	text    string
	lo, hi  ir.Value
	isRange bool
	line    int
}

// caseLabelValue folds one CASE label to a literal of the selector's type
// (#196). A label is a constant: a literal, a named constant (VAR CONSTANT
// or VAR_GLOBAL CONSTANT), an enumeration member, or arithmetic on them. It
// used to be lowered as a run-time expression, and a named-constant label
// was not even seen as a label by the parser.
func (l *lowerer) caseLabelValue(e Expression, selT *ir.Type) (*ir.Lit, caseLabel, error) {
	lab := caseLabel{text: labelText(e), line: nodePos(e).Line}
	x, err := l.lowerExprHint(e, selT)
	if err != nil {
		return nil, lab, err
	}
	if _, err := resolveBinType(ir.OpEq, selT, x.ExprType()); err != nil {
		return nil, lab, errNode(e, fmt.Errorf("CASE label %s is a %s, but the selector is a %s", lab.text, x.ExprType(), selT))
	}
	v, ok := ir.ConstValue(x)
	if !ok {
		return nil, lab, errNode(e, fmt.Errorf("CASE label %s is not a constant: use a literal, a VAR CONSTANT, or an enumeration value", lab.text))
	}
	if !isLiteralExpr(e) {
		lab.text = fmt.Sprintf("%s (= %s)", lab.text, valueText(v))
	}
	return &ir.Lit{V: v, T: x.ExprType()}, lab, nil
}

// checkCaseOverlap reports a label whose value an earlier label of the same
// CASE already covers, naming both: a second clause with the same value
// could never run.
func checkCaseOverlap(seen []caseLabel, lab caseLabel) error {
	for _, prev := range seen {
		if cmpLabel(lab.lo, prev.hi) <= 0 && cmpLabel(prev.lo, lab.hi) <= 0 {
			what := "has the same value as"
			if lab.isRange || prev.isRange {
				what = "overlaps"
			}
			return fmt.Errorf("duplicate CASE label: %s %s %s on line %d", lab.text, what, prev.text, prev.line)
		}
	}
	return nil
}

// cmpLabel orders two folded label values of one selector type.
func cmpLabel(a, b ir.Value) int {
	switch {
	case a.Kind == ir.TypeString && b.Kind == ir.TypeString:
		return strings.Compare(a.S, b.S)
	case a.Kind == ir.TypeBool && b.Kind == ir.TypeBool:
		ai, bi := 0, 0
		if a.B {
			ai = 1
		}
		if b.B {
			bi = 1
		}
		return ai - bi
	}
	return ir.CompareValues(a, b)
}

func isLiteralExpr(e Expression) bool {
	switch n := e.(type) {
	case *NumberLit, *BoolLit, *StringLit, *TimeLit:
		return true
	case *TypedLit:
		_, isEnum := n.Inner.(*IdentExpr)
		return !isEnum
	case *UnaryExpr:
		return isLiteralExpr(n.Operand)
	}
	return false
}

// labelText renders a CASE label as the user wrote it, near enough.
func labelText(e Expression) string {
	switch n := e.(type) {
	case *NumberLit:
		if n.Base != 10 && n.Base != 0 {
			return fmt.Sprintf("%d#%s", n.Base, n.Value)
		}
		return n.Value
	case *IdentExpr:
		return n.Name
	case *TypedLit:
		if id, ok := n.Inner.(*IdentExpr); ok {
			return n.TypeName + "#" + id.Name
		}
		return n.TypeName + "#" + labelText(n.Inner)
	case *UnaryExpr:
		return n.Op + labelText(n.Operand)
	case *BoolLit:
		if n.Value {
			return "TRUE"
		}
		return "FALSE"
	case *StringLit:
		return "'" + n.Value + "'"
	case *TimeLit:
		return "T#" + n.Raw
	case *MemberExpr:
		return labelText(n.Object) + "." + n.Member
	case *BinaryExpr:
		return labelText(n.Left) + " " + n.Op + " " + labelText(n.Right)
	}
	return "label"
}

func valueText(v ir.Value) string {
	switch v.Kind {
	case ir.TypeBool:
		if v.B {
			return "TRUE"
		}
		return "FALSE"
	case ir.TypeReal:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	case ir.TypeString:
		return "'" + v.S + "'"
	case ir.TypeTime:
		return fmt.Sprintf("T#%dms", v.I)
	}
	if v.S != "" {
		return fmt.Sprintf("%s, %d", v.S, v.I)
	}
	return strconv.FormatInt(v.I, 10)
}

// exprLabel names the object of a member access for a diagnostic.
func exprLabel(e Expression) string {
	switch n := e.(type) {
	case *IdentExpr:
		return n.Name
	case *MemberExpr:
		return exprLabel(n.Object) + "." + n.Member
	case *IndexExpr:
		return exprLabel(n.Array) + "[…]"
	}
	return "the expression"
}

// ─── Enumerations ─────────────────────────────────────────────────────────

// ambiguousMemberError is an unqualified member two enumerations declare,
// with nothing in context to choose between them.
type ambiguousMemberError struct {
	name  string
	types []string
}

func (e *ambiguousMemberError) Error() string {
	quals := make([]string, len(e.types))
	for i, t := range e.types {
		quals[i] = t + "#" + e.name
	}
	return fmt.Sprintf("%s is a value of more than one enumeration — write %s", e.name, strings.Join(quals, " or "))
}

// enumsWith lists the enumerations in scope that declare member name.
func (l *lowerer) enumsWith(name string) ([]*ir.Type, ir.EnumMember) {
	var out []*ir.Type
	var mem ir.EnumMember
	for _, t := range l.enums {
		if m, ok := t.Enum.Member(name); ok {
			out = append(out, t)
			mem = m
		}
	}
	return out, mem
}

// uniqueEnumMember resolves an unqualified member name that exactly one
// enumeration declares.
func (l *lowerer) uniqueEnumMember(name string) (*ir.Type, ir.EnumMember, bool) {
	ts, m := l.enumsWith(name)
	if len(ts) != 1 {
		return nil, ir.EnumMember{}, false
	}
	return ts[0], m, true
}

// lowerEnumMember resolves an identifier that names no variable or constant
// as an unqualified enumeration member (Run for Mode#Run). found is false
// when no enumeration declares it. Two enumerations declaring it is an
// error unless the expected type (l.hint) is one of them.
func (l *lowerer) lowerEnumMember(n *IdentExpr) (ir.Expr, bool, error) {
	ts, _ := l.enumsWith(n.Name)
	switch len(ts) {
	case 0:
		return nil, false, nil
	case 1:
		m, _ := ts[0].Enum.Member(n.Name)
		return &ir.Lit{V: ts[0].Enum.Val(m.Value), T: ts[0]}, true, nil
	}
	if l.hint != nil && l.hint.Enum != nil {
		for _, t := range ts {
			if t.Enum == l.hint.Enum {
				m, _ := t.Enum.Member(n.Name)
				return &ir.Lit{V: t.Enum.Val(m.Value), T: t}, true, nil
			}
		}
	}
	amb := &ambiguousMemberError{name: n.Name}
	for _, t := range ts {
		amb.types = append(amb.types, t.Enum.Name)
	}
	return nil, true, errName(n.Pos, n.Name, amb)
}

// lowerEnumLiteral lowers a qualified enumeration value, Mode#Run.
func (l *lowerer) lowerEnumLiteral(n *TypedLit, id *IdentExpr) (ir.Expr, error) {
	t, _, ok := ir.Lookup(l.types, n.TypeName)
	if !ok || t == nil {
		return nil, errName(n.Pos, n.TypeName, fmt.Errorf("unknown enumeration type %s (in %s#%s)", n.TypeName, n.TypeName, id.Name))
	}
	if t.Enum == nil {
		return nil, errName(n.Pos, n.TypeName, fmt.Errorf("%s is not an enumeration, so %s#%s is not a value", t, n.TypeName, id.Name))
	}
	m, ok := t.Enum.Member(id.Name)
	if !ok {
		return nil, errName(n.Pos, n.TypeName, fmt.Errorf("enumeration %s has no member %s (members: %s)", t.Enum.Name, id.Name, memberList(t.Enum)))
	}
	return &ir.Lit{V: t.Enum.Val(m.Value), T: t}, nil
}

// resolveEnumBinType types an operator with an enumeration on either side
// (#238). Two values of the same enumeration compare (=, <>, and the
// ordering operators, by member value); nothing computes with one, and an
// enumeration never meets a plain integer without TO_INT / TO_<Enum>.
func resolveEnumBinType(op ir.BinKind, lt, rt *ir.Type) (*ir.Type, error) {
	switch op {
	case ir.OpEq, ir.OpNeq, ir.OpLt, ir.OpLte, ir.OpGt, ir.OpGte:
		if lt.Equal(rt) {
			return ir.BoolT, nil
		}
		e, other := lt, rt
		if e.Enum == nil {
			e, other = rt, lt
		}
		return nil, fmt.Errorf("an enumeration (%s) compares only with a value of the same type (%s#…); convert with TO_INT or TO_%s to compare with %s",
			e.Enum.Name, e.Enum.Name, e.Enum.Name, other)
	}
	e := lt
	if e.Enum == nil {
		e = rt
	}
	return nil, fmt.Errorf("%s is an enumeration: its values are names, not numbers — convert with TO_INT to compute with one", e.Enum.Name)
}

// lowerEnumConversion handles the conversion INTO an enumeration (#238):
//
//	TO_Mode(n) — the Mode with integer n (a number no member has stays
//	             that number, shown unnamed)
//
// The other direction, TO_INT(m) / TO_DINT(m) / …, is the registry's
// overloaded TO_<type> (lang/ir/conversions.go), which takes an
// enumeration as its integer. handled is false for any call that is not
// TO_<an enumeration in scope>, which then resolves as before.
func (l *lowerer) lowerEnumConversion(n *CallExpr) (ir.Expr, bool, error) {
	up := strings.ToUpper(n.Name)
	if !strings.HasPrefix(up, "TO_") || len(n.Args) != 1 || len(n.NamedArgs) > 0 {
		return nil, false, nil
	}
	target := n.Name[3:]
	et, _, ok := ir.Lookup(l.types, target)
	if !ok || et == nil || et.Enum == nil {
		return nil, false, nil
	}
	arg, err := l.lowerExpr(n.Args[0])
	if err != nil {
		return nil, true, err
	}
	if !arg.ExprType().IsInteger() && !arg.ExprType().Equal(et) {
		return nil, true, errNode(n.Args[0], fmt.Errorf("TO_%s converts an integer, got %s", et.Enum.Name, arg.ExprType()))
	}
	def := et.Enum
	return &ir.Call{Name: "TO_" + def.Name, Args: []ir.Expr{arg}, Fn: func(a []ir.Value) (ir.Value, error) {
		return def.Val(a[0].I), nil
	}, T: et}, true, nil
}

// ─── Partial access: w.3, w.%X3, w.%B1, w.%W0, w.%D0 ─────────────────────

// isPartialMember reports the IEC partial-access member the lexer produced
// (%X3, %B1, %W0, %D0, %L0).
func isPartialMember(name string) bool {
	return len(name) >= 3 && name[0] == '%' && strings.ContainsRune("XBWDL", rune(name[1])) && isBitMember(name[2:])
}

// lowerPartial lowers a bit (w.3, w.%X3) or a wider part (w.%B1 the second
// byte, w.%W0 the low word, w.%D1 the high double word) of an integer
// variable (#222). The part must fit the declared type: a DINT has bits
// 0..31, bytes 0..3 and words 0..1.
func (l *lowerer) lowerPartial(m *MemberExpr, obj ir.Expr) (ir.Expr, error) {
	ot := obj.ExprType()
	form := "." + m.Member
	if !ot.IsInteger() {
		return nil, errName(m.MemberPos, m.Member, fmt.Errorf("%s%s: partial access needs an integer variable (ANY_BIT/ANY_INT), and %s is a %s", exprLabel(m.Object), form, exprLabel(m.Object), ot))
	}
	width, index := 1, 0
	var partT *ir.Type
	if isBitMember(m.Member) {
		index, _ = strconv.Atoi(m.Member)
	} else {
		index, _ = strconv.Atoi(m.Member[2:])
		switch m.Member[1] {
		case 'B':
			width, partT = 8, ir.IntNamed("BYTE")
		case 'W':
			width, partT = 16, ir.IntNamed("WORD")
		case 'D':
			width, partT = 32, ir.IntNamed("DWORD")
		case 'L':
			width, partT = 64, ir.IntNamed("LWORD")
		}
	}
	total := ot.BitWidth()
	if width > total {
		return nil, errName(m.MemberPos, m.Member, fmt.Errorf("%s%s: a %s has %d bits, fewer than one %d-bit part", exprLabel(m.Object), form, ot, total, width))
	}
	parts := total / width
	if index < 0 || index >= parts {
		unit := "bit"
		if width > 1 {
			unit = map[int]string{8: "byte", 16: "word", 32: "double word", 64: "long word"}[width]
		}
		return nil, errName(m.MemberPos, m.Member, fmt.Errorf("%s%s: %s %d is out of range for %s (%ss 0..%d)", exprLabel(m.Object), form, unit, index, ot, unit, parts-1))
	}
	lv, ok := obj.(ir.LValue)
	if !ok {
		return nil, errName(m.MemberPos, m.Member, fmt.Errorf("partial access %s needs a variable, not an expression", form))
	}
	if width == 1 {
		return &ir.BitRef{Object: lv, Bit: index}, nil
	}
	return &ir.BitRef{Object: lv, Bit: index * width, Width: width, T: partT}, nil
}
