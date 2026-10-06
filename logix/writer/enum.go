package writer

import (
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Enumerations (#238). Logix has no enumerated type, so an enumeration
// travels as what nautilus already runs it as — an integer — and its
// members as their values:
//
//	TYPE Mode : (Idle, Run := 10, Fault); END_TYPE   no DataType
//	State : Mode   (VAR, VAR_EXTERNAL, a manifest tag)   a DINT tag
//	init: Run / := Run / the type's default           Value="10"
//	Run, Mode#Run   (ST, a rung's operand or { := })   10
//	TO_INT(State), TO_Mode(n)                         State, n
//
// The member names are not on the controller: a reader of the routine
// sees 10, and the importer brings the tag back as a DINT. A Logix
// mapping that keeps the names (a UDT of constants, a comment) would be a
// design call; until one is made, the integer is the honest form.

// loadEnums resolves the enumerations the libraries declare, with the
// compiler's own resolution (lang/st.Types: a member without a value is
// the previous one's plus one, a value may name a constant). The libraries
// are joined, as the project compiles them; if that does not resolve,
// each one is tried alone.
func (lw *lowered) loadEnums() {
	lw.enums = map[string]*ir.EnumDef{}
	add := func(src string) bool {
		prog, err := st.Parse(src)
		if err != nil {
			return false
		}
		types, err := st.Types(prog)
		if err != nil {
			return false
		}
		for _, t := range types {
			if t != nil && t.Enum != nil {
				lw.enums[ir.NameKey(t.Enum.Name)] = t.Enum
			}
		}
		return true
	}
	if len(lw.opts.Libs) == 0 || add(strings.Join(lw.opts.Libs, "\n")) {
		return
	}
	for _, lib := range lw.opts.Libs {
		add(lib)
	}
}

// enumOf returns the enumeration a type name declares.
func (lw *lowered) enumOf(typ string) *ir.EnumDef {
	return lw.enums[ir.NameKey(strings.TrimSpace(typ))]
}

// enumMember resolves a bare member name, as the compiler does when no
// variable has the name: the one enumeration that declares it. ambiguous
// is set when several do with different values (the compiler then asks
// for Type#Member, unless the context types it).
func (lw *lowered) enumMember(name string) (v int64, ok, ambiguous bool) {
	found := false
	for _, e := range lw.enums {
		m, has := e.Member(name)
		if !has {
			continue
		}
		if found && m.Value != v {
			return 0, false, true
		}
		v, found = m.Value, true
	}
	return v, found, false
}

// enumLiteral resolves a qualified member, Mode#Run.
func (lw *lowered) enumLiteral(text string) (int64, bool) {
	typ, member, ok := strings.Cut(text, "#")
	if !ok {
		return 0, false
	}
	e := lw.enumOf(typ)
	if e == nil {
		return 0, false
	}
	m, ok := e.Member(strings.TrimSpace(member))
	return m.Value, ok
}

// enumInit spells an enumeration tag's initial value as its integer: a
// member name, Type#Member, or a number; empty is the type's default.
func (lw *lowered) enumInit(e *ir.EnumDef, init string) (string, bool) {
	init = strings.TrimSpace(init)
	if init == "" {
		return strconv.FormatInt(e.Default, 10), true
	}
	if _, member, ok := strings.Cut(init, "#"); ok && !strings.ContainsAny(init[:strings.Index(init, "#")], "0123456789") {
		init = member
	}
	if m, ok := e.Member(init); ok {
		return strconv.FormatInt(m.Value, 10), true
	}
	if n, ok := parseInt(init); ok {
		return strconv.FormatInt(n, 10), true
	}
	if f, err := strconv.ParseFloat(init, 64); err == nil && f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10), true // a YAML number
	}
	return "", false
}
