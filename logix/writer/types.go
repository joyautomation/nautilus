package writer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// User-defined types. A nautilus project declares them in ST (`TYPE X :
// STRUCT … END_STRUCT; END_TYPE`, in a library file), a program takes a
// tag of that type, and a rung names a member (`Line_Status.Alarm`). On
// Logix the type is a UDT and the tag a structure; the member path is the
// same text. The writer reads the TYPE declarations out of the library
// sources it is given, emits a <DataType> for every type a tag uses
// (nested types first), and checks every member path a rung names.
//
// v1 UDT members: BOOL, SINT, INT, DINT, REAL, LREAL, another UDT, and
// one-dimensional arrays of those starting at 0 (BOOL arrays a multiple
// of 32, as everywhere in Logix). TIME, STRING and block instances inside
// a type are refused, with the type and member named.

type udt struct {
	Name    string
	Members []udtMember
	Line    int
}

type udtMember struct {
	Name     string
	DataType string // Logix atomic, or a UDT name
	Dim      int
	Struct   *udt // set for a UDT member
	Line     int
}

// loadTypes parses every library source for TYPE declarations. Problems
// are reported only for types a tag actually uses (resolveType).
func (lw *lowered) loadTypes() {
	lw.types = map[string]*udt{}
	lw.rawTypes = map[string]*st.TypeDecl{}
	for _, lib := range lw.opts.Libs {
		prog, err := st.Parse(lib)
		if err != nil {
			continue // the compiler reports a broken library; not ours
		}
		for i := range prog.TypeDecls {
			td := &prog.TypeDecls[i]
			if _, ok := td.Type.(*st.StructType); ok {
				lw.rawTypes[strings.ToLower(td.Name)] = td
			}
		}
	}
}

// resolveType returns the UDT a name declares, building it (and the UDTs
// it nests) on first use. ok is false, with a diagnostic already raised,
// when the type is unknown or outside the v1 subset.
func (lw *lowered) resolveType(name string, line int, use string) (*udt, bool) {
	key := strings.ToLower(name)
	if u, ok := lw.types[key]; ok {
		return u, u != nil
	}
	td, ok := lw.rawTypes[key]
	if !ok {
		lw.types[key] = nil
		return nil, false
	}
	lw.types[key] = nil // guards recursion
	u := &udt{Name: td.Name, Line: td.Pos.Line}
	fields := td.Type.(*st.StructType).Fields
	good := true
	for _, f := range fields {
		m, ok := lw.udtMember(td.Name, f)
		if !ok {
			good = false
			continue
		}
		u.Members = append(u.Members, m)
	}
	if !good || len(u.Members) == 0 {
		if len(u.Members) == 0 && good {
			lw.diag(ruleType, line, "", "%s: type %s declares no members", use, td.Name)
		}
		return nil, false
	}
	lw.types[key] = u
	return u, true
}

func (lw *lowered) udtMember(typeName string, f st.VarDecl) (udtMember, bool) {
	m := udtMember{Name: f.Name, Line: f.Pos.Line}
	if !lw.checkName(f.Name, f.Pos.Line, "") {
		return m, false
	}
	te := f.Type
	if arr, ok := te.(*st.ArrayType); ok {
		if len(arr.Dims) != 1 {
			lw.diag(ruleArrayShape, f.Pos.Line, "", "type %s member %s: multi-dimensional arrays are not in the v1 subset", typeName, f.Name)
			return m, false
		}
		lo, hi, ok := literalBounds(arr.Dims[0])
		if !ok || lo != 0 {
			lw.diag(ruleArrayShape, f.Pos.Line, "", "type %s member %s: a Logix array starts at 0 with literal bounds", typeName, f.Name)
			return m, false
		}
		m.Dim = hi + 1
		te = arr.Elem
	}
	switch t := te.(type) {
	case *st.ScalarType:
		u := strings.ToUpper(t.Name)
		if scalarTypes[u] == "" {
			lw.diag(ruleType, f.Pos.Line, "", "type %s member %s: %s is not a Logix UDT member type in v1 (BOOL, SINT, INT, DINT, REAL, LREAL or another type)", typeName, f.Name, t.Name)
			return m, false
		}
		if u == "BOOL" && m.Dim > 0 && m.Dim%32 != 0 {
			lw.diag(ruleArrayShape, f.Pos.Line, "", "type %s member %s: a BOOL array's size must be a multiple of 32", typeName, f.Name)
			return m, false
		}
		m.DataType = scalarTypes[u]
	case *st.NamedType:
		if strings.EqualFold(t.Name, typeName) {
			lw.diag(ruleType, f.Pos.Line, "", "type %s member %s: a type cannot contain itself", typeName, f.Name)
			return m, false
		}
		sub, ok := lw.resolveType(t.Name, f.Pos.Line, "type "+typeName+" member "+f.Name)
		if !ok {
			if _, declared := lw.rawTypes[strings.ToLower(t.Name)]; !declared {
				lw.diag(ruleType, f.Pos.Line, "", "type %s member %s: %s is not a declared STRUCT type (block instances and unknown types cannot be UDT members)", typeName, f.Name, t.Name)
			}
			return m, false
		}
		m.DataType, m.Struct = sub.Name, sub
	default:
		lw.diag(ruleType, f.Pos.Line, "", "type %s member %s: %s is not a Logix UDT member type", typeName, f.Name, te.String())
		return m, false
	}
	return m, true
}

func literalBounds(d st.ArrayDim) (lo, hi int, ok bool) {
	l, ok1 := literalInt(d.Lo)
	h, ok2 := literalInt(d.Hi)
	return l, h, ok1 && ok2
}

func literalInt(e st.Expression) (int, bool) {
	switch v := e.(type) {
	case *st.NumberLit:
		n, err := strconv.Atoi(strings.ReplaceAll(v.Value, "_", ""))
		return n, err == nil
	case *st.UnaryExpr:
		if v.Op == "-" {
			if n, ok := literalInt(v.Operand); ok {
				return -n, true
			}
		}
	}
	return 0, false
}

// memberPath checks a dotted path against a type, so `P.Status.Alarm` and
// `P.Hist[3]` are known to exist before Logix is asked. It returns the
// member reached, or a message naming the segment that failed.
func (u *udt) memberPath(path string) (*udtMember, string) {
	cur := u
	var last *udtMember
	for _, seg := range strings.Split(path, ".") {
		name, idx := seg, ""
		if i := strings.Index(seg, "["); i >= 0 {
			name, idx = seg[:i], seg[i:]
		}
		if cur == nil {
			return nil, fmt.Sprintf("%s is not a structure, so it has no member %s", last.Name, name)
		}
		var m *udtMember
		for i := range cur.Members {
			if strings.EqualFold(cur.Members[i].Name, name) {
				m = &cur.Members[i]
			}
		}
		if m == nil {
			return nil, fmt.Sprintf("type %s has no member %s (members: %s)", cur.Name, name, cur.memberNames())
		}
		if idx != "" && m.Dim == 0 {
			return nil, fmt.Sprintf("%s.%s is not an array", cur.Name, name)
		}
		last = m
		cur = m.Struct
	}
	return last, ""
}

func (u *udt) memberNames() string {
	names := make([]string, 0, len(u.Members))
	for _, m := range u.Members {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

// usedTypesInOrder lists the UDTs the tags use, nested types before the
// types that contain them, each once.
func (lw *lowered) usedTypesInOrder() []*udt {
	var out []*udt
	seen := map[string]bool{}
	var visit func(u *udt)
	visit = func(u *udt) {
		if u == nil || seen[strings.ToLower(u.Name)] {
			return
		}
		seen[strings.ToLower(u.Name)] = true
		for _, m := range u.Members {
			visit(m.Struct)
		}
		out = append(out, u)
	}
	roots := map[string]*udt{}
	for _, t := range append(append([]tagDef{}, lw.ctrlTags...), lw.progTags...) {
		if t.Struct != nil {
			roots[strings.ToLower(t.Struct.Name)] = t.Struct
		}
	}
	for _, k := range sortedKeysUDT(roots) {
		visit(roots[k])
	}
	return out
}

func sortedKeysUDT(m map[string]*udt) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// structValue renders a structure tag's Decorated value: zeros, with any
// initial values a manifest map supplies (member name → value, nested
// maps for nested types). Returns the inner XML lines.
func structValue(u *udt, init any, w func(string, ...any)) {
	vals, _ := init.(map[string]any)
	for _, m := range u.Members {
		v := memberInit(vals, m.Name)
		switch {
		case m.Dim > 0 && m.Struct == nil:
			w(`<ArrayMember Name="%s" DataType="%s" Dimensions="%d" Radix="%s">`, attr(m.Name), m.DataType, m.Dim, radixOf(m.DataType))
			for i := 0; i < m.Dim; i++ {
				w(`<Element Index="[%d]" Value="%s"/>`, i, zeroOf(m.DataType))
			}
			w(`</ArrayMember>`)
		case m.Dim > 0:
			w(`<ArrayMember Name="%s" DataType="%s" Dimensions="%d">`, attr(m.Name), m.DataType, m.Dim)
			for i := 0; i < m.Dim; i++ {
				w(`<Element Index="[%d]">`, i)
				w(`<Structure DataType="%s">`, m.DataType)
				structValue(m.Struct, nil, w)
				w(`</Structure>`)
				w(`</Element>`)
			}
			w(`</ArrayMember>`)
		case m.Struct != nil:
			w(`<StructureMember Name="%s" DataType="%s">`, attr(m.Name), m.DataType)
			structValue(m.Struct, v, w)
			w(`</StructureMember>`)
		case m.DataType == "BOOL":
			w(`<DataValueMember Name="%s" DataType="BOOL" Value="%s"/>`, attr(m.Name), scalarInit(m.DataType, v))
		default:
			w(`<DataValueMember Name="%s" DataType="%s" Radix="%s" Value="%s"/>`, attr(m.Name), m.DataType, radixOf(m.DataType), scalarInit(m.DataType, v))
		}
	}
}

func memberInit(vals map[string]any, name string) any {
	if vals == nil {
		return nil
	}
	for k, v := range vals {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

// scalarInit spells a manifest value for a member, zero when absent or
// unreadable.
func scalarInit(dt string, v any) string {
	if v == nil {
		return zeroOf(dt)
	}
	if s, ok := literal(dt, fmt.Sprint(v)); ok && s != "" {
		return s
	}
	return zeroOf(dt)
}
