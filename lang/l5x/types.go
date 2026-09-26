package l5x

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/lang/stgen"
)

// Logix elementary types and the IEC type each maps to. The two families
// agree almost everywhere — both are IEC 61131-3 — so this is mostly an
// identity map, which is the point: a Logix DINT really is an IEC DINT.
var elementary = map[string]stgen.Type{
	"BOOL":  stgen.BOOL,
	"BIT":   stgen.BOOL, // a UDT's BOOL member — see Member
	"SINT":  stgen.SINT,
	"INT":   stgen.INT,
	"DINT":  stgen.DINT,
	"LINT":  stgen.LINT,
	"USINT": stgen.USINT,
	"UINT":  stgen.UINT,
	"UDINT": stgen.UDINT,
	"ULINT": stgen.ULINT,
	"REAL":  stgen.REAL,
	"LREAL": stgen.LREAL,
	// A Logix STRING is a structure (LEN + an 82-byte DATA array), but
	// what it means is a string, and nautilus has one.
	"STRING": stgen.STRING,
	// Logix's 64-bit time types. LTIME is a duration like IEC's TIME;
	// DT and LDT are absolute instants, which IEC ST has no elementary
	// for here, so they keep their 64-bit integer representation rather
	// than being dropped.
	"LTIME": stgen.TIME,
	"DT":    stgen.LINT,
	"LDT":   stgen.LINT,
}

// Logix's predefined structured types are NOT carried in an export's
// <DataTypes> — the firmware defines them, so the file just references
// them by name. A reader that ignored that would emit a dangling type
// reference for every timer in the project, so the shapes live here.
//
// Only the types a tag or UDT member can plausibly be declared as are
// listed. MESSAGE, PID and the motion types are large, mostly-opaque
// firmware structures; a tag of one is reported as unresolved rather than
// approximated, because a wrong shape is worse than a missing one.
var predefined = map[string][]stgen.FieldDef{
	"TIMER": {
		stgen.Field("PRE", stgen.DINT),
		stgen.Field("ACC", stgen.DINT),
		stgen.Field("EN", stgen.BOOL),
		stgen.Field("TT", stgen.BOOL),
		stgen.Field("DN", stgen.BOOL),
	},
	"COUNTER": {
		stgen.Field("PRE", stgen.DINT),
		stgen.Field("ACC", stgen.DINT),
		stgen.Field("CU", stgen.BOOL),
		stgen.Field("CD", stgen.BOOL),
		stgen.Field("DN", stgen.BOOL),
		stgen.Field("OV", stgen.BOOL),
		stgen.Field("UN", stgen.BOOL),
	},
	"CONTROL": {
		stgen.Field("LEN", stgen.DINT),
		stgen.Field("POS", stgen.DINT),
		stgen.Field("EN", stgen.BOOL),
		stgen.Field("EU", stgen.BOOL),
		stgen.Field("DN", stgen.BOOL),
		stgen.Field("EM", stgen.BOOL),
		stgen.Field("ER", stgen.BOOL),
		stgen.Field("UL", stgen.BOOL),
		stgen.Field("IN", stgen.BOOL),
		stgen.Field("FD", stgen.BOOL),
	},
}

// TypesOptions controls which of an export's types are rendered.
type TypesOptions struct {
	// All includes the module-defined and product-defined shapes an
	// export carries (Class "IO", "ProductDefined"). Off by default: a
	// controller export ships hundreds of them — 1.8 MB of the 1.9 MB
	// DemoLine export is exactly this — and none of them is project
	// code.
	All bool
	// Roots, when set, limits the output to these type names and
	// whatever they reference, transitively. A project needs the UDTs
	// its tags actually use, not every UDT in the controller.
	Roots []string
}

// Types renders an export's data types as IEC ST type declarations.
//
// It is the lang/stgen shape: build the model in Go, render it, and let
// the ST compiler have the last word — Render validates by parsing and
// lowering what it produced, so a name Logix accepts but IEC does not is
// a returned error, never bad source on disk.
//
// unresolved names the types that were referenced but could not be
// rendered (a firmware structure with no public shape, a module type left
// out by default). Their members are omitted from the output rather than
// guessed at, so the ST that is emitted is true.
func Types(f *File, opts TypesOptions) (src string, unresolved []string, err error) {
	if f == nil || f.Controller == nil {
		return "", nil, fmt.Errorf("l5x: no controller to read types from")
	}
	byName := map[string]*DataType{}
	for _, dt := range f.Controller.DataTypes {
		byName[dt.Name] = dt
	}

	// The roots: what the caller asked for, or every type worth emitting.
	var roots []string
	if len(opts.Roots) > 0 {
		roots = append(roots, opts.Roots...)
	} else {
		for _, dt := range f.Controller.DataTypes {
			if opts.All || dt.User() {
				roots = append(roots, dt.Name)
			}
		}
	}

	// Build every reachable type first, remembering what each member
	// refers to; prune afterwards. Pruning has to reach a fixpoint —
	// dropping an unrenderable type orphans the members that reference
	// it, which can empty a type that was fine a moment ago — and doing
	// it in one pass is exactly how a dangling type reference reaches
	// the ST compiler.
	missing := map[string]bool{}
	built := map[string]*builtType{}
	var visit func(name string)
	visit = func(name string) {
		if name == "" || built[name] != nil || missing[name] {
			return
		}
		if _, ok := elementary[strings.ToUpper(name)]; ok {
			return
		}
		if fields, ok := predefined[strings.ToUpper(name)]; ok {
			built[name] = &builtType{name: ident(name), fields: fields, refs: make([]string, len(fields))}
			return
		}
		dt := byName[name]
		if dt == nil {
			missing[name] = true
			return
		}
		b := &builtType{name: ident(dt.Name)}
		built[name] = b // before recursing, so a self-reference cannot loop
		for _, m := range dt.Members {
			// Hidden members are the byte hosts Logix generates to carry
			// a UDT's BOOLs. The authored type has no such member, and
			// emitting one would both misname the type and double-count
			// the bits that overlay it.
			if m.Hidden {
				continue
			}
			t, ref := memberType(m)
			if t == nil {
				missing[m.DataType] = true
				continue
			}
			if ref != "" {
				visit(ref)
			}
			b.fields = append(b.fields, stgen.Field(ident(m.Name), t))
			b.refs = append(b.refs, ref)
		}
	}
	for _, r := range roots {
		visit(r)
	}

	// Prune to a fixpoint: a type with no renderable member is not a
	// type (an empty STRUCT is not valid ST either), and a member whose
	// type went away goes with it.
	dropped := map[string]bool{}
	for {
		changed := false
		for name, b := range built {
			if dropped[name] {
				continue
			}
			kept := b.fields[:0]
			keptRefs := b.refs[:0]
			for i, f := range b.fields {
				if ref := b.refs[i]; ref != "" && (missing[ref] || dropped[ref]) {
					missing[ref] = true
					changed = true
					continue
				}
				kept = append(kept, f)
				keptRefs = append(keptRefs, b.refs[i])
			}
			b.fields, b.refs = kept, keptRefs
			if len(b.fields) == 0 {
				dropped[name] = true
				missing[name] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	structs := make([]*stgen.StructDef, 0, len(built))
	for name, b := range built {
		if dropped[name] {
			continue
		}
		structs = append(structs, stgen.Struct(b.name, b.fields...))
	}
	sort.Slice(structs, func(i, j int) bool { return structs[i].Name < structs[j].Name })

	for name := range missing {
		if name != "" {
			unresolved = append(unresolved, name)
		}
	}
	sort.Strings(unresolved)

	src, err = stgen.Render(structs...)
	if err != nil {
		return "", unresolved, err
	}
	return src, unresolved, nil
}

// builtType is a type under construction: its rendered fields alongside,
// per field, the named type that field depends on ("" for an elementary).
type builtType struct {
	name   string
	fields []stgen.FieldDef
	refs   []string
}

// memberType maps a UDT member to its IEC type, resolving BIT to BOOL and
// Dimension to a 0-based array. It returns the named type the member
// depends on ("" for an elementary one), or a nil type when the member
// declares no type at all.
func memberType(m Member) (stgen.Type, string) {
	if m.DataType == "" {
		return nil, ""
	}
	var t stgen.Type
	ref := ""
	if e, ok := elementary[strings.ToUpper(m.DataType)]; ok {
		t = e
	} else {
		t = stgen.Ref(ident(m.DataType))
		ref = m.DataType
	}
	if m.Dimension > 0 {
		t = stgen.ArrayOf(t, 0, m.Dimension-1)
	}
	return t, ref
}

// ident sanitizes a Logix name into an IEC identifier. Two things need
// fixing, and both were found by compiling the output rather than by
// reading the spec:
//
//   - Logix allows ':' in the module- and product-defined type names an
//     export carries ("AB:5000_HART_Command_Control_Struct:I:0"); IEC
//     does not.
//   - Logix reserves nothing, so real projects have UDT members named
//     "retain" and "Constant" — both IEC variable qualifiers. A keyword
//     as a member name is a parse error, not a warning.
//
// Renaming is the only option; it is deterministic, so a regenerated
// file diffs against the last one instead of reshuffling.
func ident(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
			b.WriteByte(c)
		case c >= '0' && c <= '9':
			if b.Len() == 0 {
				b.WriteByte('_') // IEC identifiers may not start with a digit
			}
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return st.EscapeKeyword(b.String())
}
