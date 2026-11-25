package writer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Enumerations (#1299). TIA has no enumerated type, so an enumeration is
// written as the integer nautilus carries for it (on a field bus, and as
// the Int32 an OPC UA enumeration is): a DInt, or the base type the
// declaration names (`(A, B) INT`). Each member a block names is one of
// its VAR CONSTANTs, <Enum>_<Member>, so the SCL still reads by name
// (TIA V21 takes a local constant as a CASE label, compiled 2026-11-26);
// a start value is the member's number, since SCL's declarations take a
// literal. An enumeration-typed variable, member or tag with no initial
// value starts at the type's initial member, as nautilus's does.

// enumOf is the enumeration a declared type names, or nil.
func (w *writer) enumOf(typ string) *ir.EnumDef {
	u := strings.TrimSpace(typ)
	if u == "" || elementary[strings.ToUpper(u)] || w.lib == nil || w.lib.types == nil {
		return nil
	}
	if _, ok := w.lib.conflicts[strings.ToLower(u)]; ok {
		return nil
	}
	t, _, ok := ir.Lookup(w.lib.types, u)
	if !ok {
		for k, v := range w.lib.types {
			if strings.EqualFold(k, u) {
				t, ok = v, true
				break
			}
		}
	}
	if !ok || t == nil || t.Enum == nil {
		return nil
	}
	return t.Enum
}

// enumBase is the integer type an enumeration is written as, or why it
// cannot be: the declared base when there is one, else DINT, and every
// member's value must fit it.
func (w *writer) enumBase(e *ir.EnumDef) (string, string) {
	base := "DINT"
	declared := w.lib.enumBases[strings.ToLower(e.Name)]
	if declared != "" {
		base = declared
		if !isInt(base) || base == "ULINT" {
			return "", gapMessage(gapOther, "the enumeration "+e.Name+" on the base type "+declared, "")
		}
	}
	for _, m := range e.Members {
		if !inRange(m.Value, base) {
			if declared == "" && inRange(m.Value, "LINT") {
				base = "LINT"
				continue
			}
			return "", gapMessage(gapOther, fmt.Sprintf("the enumeration %s, whose member %s (%d) does not fit its %s", e.Name, m.Name, m.Value, base), "")
		}
	}
	return base, ""
}

// enumType maps an enumeration-typed declaration to its integer type
// (upper-cased, as typ returns), or returns t unchanged.
func (w *writer) enumType(t string) string {
	if e := w.enumOf(t); e != nil {
		if base, why := w.enumBase(e); why == "" {
			return base
		}
	}
	return t
}

// enumStart is the start value an enumeration-typed declaration takes
// when it names none: the type's initial member's number.
func (w *writer) enumStart(typ string) (string, bool) {
	e := w.enumOf(typ)
	if e == nil {
		return "", false
	}
	return fmt.Sprint(e.Default), true
}

// enumConst is one member a block names: its VAR CONSTANT.
type enumConst struct {
	enum   *ir.EnumDef
	member ir.EnumMember
	line   int
}

func enumConstName(e *ir.EnumDef, m ir.EnumMember) string {
	return e.Name + "_" + m.Name
}

// enumMember resolves a member to its constant's name in the block,
// recording it for the block's VAR CONSTANT section. hint is the
// enumeration the context expects ("" when none): an unqualified member
// two enumerations declare resolves to the hint's, as in nautilus.
func (pw *progWriter) enumMember(typeName, member string, line int) string {
	var e *ir.EnumDef
	if typeName != "" {
		e = pw.w.enumOf(typeName)
	} else {
		names := pw.w.lib.enumMembers[strings.ToLower(member)]
		if len(names) == 1 {
			e = pw.w.enumOf(names[0])
		} else if h := pw.w.enumOf(pw.enumHint); h != nil {
			for _, n := range names {
				if strings.EqualFold(n, h.Name) {
					e = h
				}
			}
		}
		if e == nil && len(names) > 1 {
			sort.Strings(names)
			quals := make([]string, len(names))
			for i, n := range names {
				quals[i] = n + "#" + member
			}
			pw.gap("siemens/enum", line, gapOther, "the member "+member+", which more than one enumeration declares, where the writer cannot tell which", "write "+strings.Join(quals, " or "))
			return ""
		}
	}
	if e == nil {
		pw.diag("siemens/enum", line, fmt.Sprintf("%s is not an enumeration the writer knows", typeName))
		return ""
	}
	m, ok := e.Member(member)
	if !ok {
		pw.diag("siemens/enum", line, fmt.Sprintf("%s is not a member of %s", member, e.Name))
		return ""
	}
	if _, why := pw.w.enumBase(e); why != "" {
		pw.diag("siemens/enum", line, why)
		return ""
	}
	name := enumConstName(e, m)
	k := strings.ToLower(name)
	if l, clash := pw.locals[k]; clash {
		pw.gap("siemens/enum", line, gapOther, fmt.Sprintf("%s#%s next to the variable %s", e.Name, m.Name, l.name), "the writer names the member's constant "+name)
		return ""
	}
	if pw.enums == nil {
		pw.enums = map[string]enumConst{}
	}
	if _, ok := pw.enums[k]; !ok {
		pw.enums[k] = enumConst{enum: e, member: m, line: line}
	}
	return "#" + sclName(name)
}

// enumConsts declares the members the block named, in name order.
func (pw *progWriter) enumConsts() []string {
	keys := make([]string, 0, len(pw.enums))
	for k := range pw.enums {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		c := pw.enums[k]
		base, _ := pw.w.enumBase(c.enum)
		t, _ := sclType(base)
		out = append(out, fmt.Sprintf("      %s : %s := %d;", sclName(enumConstName(c.enum, c.member)), t, c.member.Value))
	}
	return out
}

// enumValue is a start value naming a member (Fill, WashState#Fill) as
// the member's number, for a declaration of enumeration type typ; ok is
// false when e names no member.
func (w *writer) enumValue(typ string, e st.Expression) (string, bool) {
	en := w.enumOf(typ)
	if en == nil {
		return "", false
	}
	var name string
	switch x := e.(type) {
	case *st.IdentExpr:
		name = x.Name
	case *st.TypedLit:
		id, ok := x.Inner.(*st.IdentExpr)
		if !ok || !strings.EqualFold(x.TypeName, en.Name) {
			return "", false
		}
		name = id.Name
	default:
		return "", false
	}
	m, ok := en.Member(name)
	if !ok {
		return "", false
	}
	return fmt.Sprint(m.Value), true
}

// enumInit is a manifest tag's init: (a member's name, or a number nautilus
// seeds as the member with that value) as the member's number.
func (w *writer) enumInit(typ, init string) (string, string) {
	en := w.enumOf(typ)
	s := strings.TrimSpace(init)
	if _, member, ok := strings.Cut(s, "#"); ok {
		s = member
	}
	if m, ok := en.Member(s); ok {
		return fmt.Sprint(m.Value), ""
	}
	v, err := ir.SeedFromInit(&ir.Type{Kind: ir.TypeInt, Enum: en}, init)
	if err != nil {
		return "", err.Error()
	}
	return fmt.Sprint(v.I), ""
}

// toEnum writes TO_<Enum>(x): nautilus keeps x's integer as the
// enumeration's value, named or not, so the CPU's integer must hold every
// value x can have.
func (pw *progWriter) toEnum(en *ir.EnumDef, arg st.Expression, s, name string, line int) string {
	base, why := pw.w.enumBase(en)
	if why != "" {
		pw.diag("siemens/enum", line, why)
		return ""
	}
	from := pw.typ(arg)
	switch {
	case from == base:
		return s
	case from == tAnyInt:
		if n, ok := litInt(arg); ok && inRange(n, base) {
			return s
		}
	case isInt(from) && holds(base, from):
		return from + "_TO_" + base + "(" + s + ")"
	}
	pw.gap("siemens/conversion", line, gapOther, fmt.Sprintf("%s of a %s", name, orUnknown(from)), fmt.Sprintf("the CPU holds %s as a %s, which does not hold every %s", en.Name, base, orUnknown(from)))
	return ""
}

// zeroOf is zero with an enumeration's start: its initial member.
func (w *writer) zeroOf(t string) (string, bool) {
	if s, ok := w.enumStart(t); ok {
		return s, true
	}
	return zero(strings.ToUpper(strings.TrimSpace(t)))
}

// memberInit is a struct member's initial value as one literal, a
// member of an enumeration as its number.
func (w *writer) memberInit(f st.VarDecl) (string, string) {
	if v, ok := w.enumValue(f.Datatype, f.Initial); ok {
		return v, ""
	}
	return constValue(w.lib, f.Initial)
}

// hint makes t the type an unqualified member is resolved against, as
// nautilus's lowering hints it from an assignment's target, a CASE
// selector, a comparison's other side or a call argument's pin: a type
// that is no enumeration clears the hint, as nautilus's does. The result
// restores the previous hint.
func (pw *progWriter) hint(t string) func() {
	prev := pw.enumHint
	pw.enumHint = ""
	if pw.w.enumOf(t) != nil {
		pw.enumHint = t
	}
	return func() { pw.enumHint = prev }
}

// sideHint is the enumeration one operand gives an unqualified member on
// the other side, or "".
func (pw *progWriter) sideHint(l, r st.Expression) string {
	for _, p := range [][2]st.Expression{{l, r}, {r, l}} {
		id, ok := p[0].(*st.IdentExpr)
		if !ok || len(pw.w.lib.enumMembers[strings.ToLower(id.Name)]) < 2 {
			continue
		}
		if t := pw.rawTyp(p[1]); pw.w.enumOf(t) != nil {
			return t
		}
	}
	return ""
}
