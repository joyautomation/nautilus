// Package stgen builds IEC 61131-3 Structured Text type declarations
// functionally in Go and renders them to ST source. It is codegen, not a
// parallel type system: the output is ordinary ST text you commit and
// compile, so the compiler stays the single source of truth and generated
// types work with the LSP, online edits, and pull like any hand-written
// type. Use it to turn a schema — device UDTs, a data dictionary, a config —
// into ST programmatically.
//
//	motor := stgen.Struct("Motor",
//	    stgen.Field("Running", stgen.BOOL),
//	    stgen.Field("Speed", stgen.REAL),
//	    stgen.Field("Faults", stgen.ArrayOf(stgen.INT, 0, 9)),
//	    stgen.Field("Cmd", stgen.Ref("MotorCmd")),
//	)
//	src, err := stgen.Render(motorCmd, motor) // dependency-ordered TYPE block
//
// Render validates its output by compiling it, so a malformed name or a
// dangling type reference is a returned error, never bad ST on disk.
package stgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Type is an ST type expression: an elementary type, an array, or a
// reference to a named type (another struct).
type Type interface{ render() string }

// elementary is a built-in scalar type name.
type elementary string

func (e elementary) render() string { return string(e) }

// Elementary IEC types.
const (
	BOOL   = elementary("BOOL")
	SINT   = elementary("SINT")
	INT    = elementary("INT")
	DINT   = elementary("DINT")
	LINT   = elementary("LINT")
	USINT  = elementary("USINT")
	UINT   = elementary("UINT")
	UDINT  = elementary("UDINT")
	ULINT  = elementary("ULINT")
	BYTE   = elementary("BYTE")
	WORD   = elementary("WORD")
	DWORD  = elementary("DWORD")
	LWORD  = elementary("LWORD")
	REAL   = elementary("REAL")
	LREAL  = elementary("LREAL")
	TIME   = elementary("TIME")
	STRING = elementary("STRING")
)

// arrayType is a fixed IEC array with explicit bounds.
type arrayType struct {
	elem   Type
	lo, hi int
}

func (a arrayType) render() string {
	return fmt.Sprintf("ARRAY [%d..%d] OF %s", a.lo, a.hi, a.elem.render())
}

// ArrayOf builds an array type over [lo..hi] (IEC arrays may start at any
// bound). For a plain 0-based array of n elements use ArrayOf(elem, 0, n-1).
func ArrayOf(elem Type, lo, hi int) Type { return arrayType{elem: elem, lo: lo, hi: hi} }

// namedRef references another declared type by name (a nested UDT).
type namedRef string

func (r namedRef) render() string { return string(r) }

// Ref references a named type — typically another struct in the same Render
// call. The reference resolves at Render time; a dangling one is an error.
func Ref(name string) Type { return namedRef(name) }

// FieldDef is one member of a struct: a name, a type, and an optional
// initial-value literal rendered verbatim (e.g. "5", "2.0", "TRUE").
type FieldDef struct {
	Name string
	Type Type
	Init string
}

// Field builds a struct member.
func Field(name string, t Type) FieldDef { return FieldDef{Name: name, Type: t} }

// FieldInit builds a struct member with an initial value (raw ST literal).
func FieldInit(name string, t Type, init string) FieldDef {
	return FieldDef{Name: name, Type: t, Init: init}
}

// StructDef is a named UDT under construction.
type StructDef struct {
	Name   string
	Fields []FieldDef
}

// Struct builds a UDT from ordered fields.
func Struct(name string, fields ...FieldDef) *StructDef {
	return &StructDef{Name: name, Fields: fields}
}

// AddField appends a field, returning the struct for chaining — handy when
// building a struct in a loop.
func (s *StructDef) AddField(f FieldDef) *StructDef {
	s.Fields = append(s.Fields, f)
	return s
}

// renderBody renders "  Name : STRUCT ... END_STRUCT;" at two-space indent.
func (s *StructDef) renderBody() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  %s : STRUCT\n", s.Name)
	for _, f := range s.Fields {
		if f.Init != "" {
			fmt.Fprintf(&b, "    %s : %s := %s;\n", f.Name, f.Type.render(), f.Init)
		} else {
			fmt.Fprintf(&b, "    %s : %s;\n", f.Name, f.Type.render())
		}
	}
	b.WriteString("  END_STRUCT;\n")
	return b.String()
}

// Render emits a TYPE … END_TYPE block for the given structs, ordered so a
// struct is declared before any struct that references it, then validates the
// result by compiling it. An empty input yields "". Errors on a dependency
// cycle (IEC UDTs must be acyclic) or if the generated ST fails to compile
// (bad identifier, dangling Ref).
func Render(structs ...*StructDef) (string, error) {
	if len(structs) == 0 {
		return "", nil
	}
	ordered, err := topoSort(structs)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("TYPE\n")
	for _, s := range ordered {
		b.WriteString(s.renderBody())
	}
	b.WriteString("END_TYPE\n")
	src := b.String()
	if err := validate(src); err != nil {
		return "", fmt.Errorf("stgen: generated ST is invalid: %w", err)
	}
	return src, nil
}

// VarBlock renders a variable block of the given kind (e.g. "VAR_EXTERNAL",
// "VAR") from fields. It is not validated on its own — it has meaning only
// inside a POU — but pairs with Render for a scaffold's declarations.
func VarBlock(kind string, fields ...FieldDef) string {
	var b strings.Builder
	b.WriteString(kind + "\n")
	for _, f := range fields {
		if f.Init != "" {
			fmt.Fprintf(&b, "  %s : %s := %s;\n", f.Name, f.Type.render(), f.Init)
		} else {
			fmt.Fprintf(&b, "  %s : %s;\n", f.Name, f.Type.render())
		}
	}
	b.WriteString("END_VAR\n")
	return b.String()
}

// topoSort orders structs dependencies-first by their Ref edges, alphabetical
// among independent peers for stable output. Refs to names not in the set
// (elementary-only or externally-declared) impose no ordering.
func topoSort(structs []*StructDef) ([]*StructDef, error) {
	byName := make(map[string]*StructDef, len(structs))
	for _, s := range structs {
		if _, dup := byName[s.Name]; dup {
			return nil, fmt.Errorf("stgen: duplicate struct %q", s.Name)
		}
		byName[s.Name] = s
	}
	const (
		unseen = iota
		active
		done
	)
	state := make(map[string]int, len(structs))
	var order []*StructDef
	var visit func(s *StructDef, path []string) error
	visit = func(s *StructDef, path []string) error {
		switch state[s.Name] {
		case done:
			return nil
		case active:
			return fmt.Errorf("stgen: type cycle: %s -> %s", strings.Join(path, " -> "), s.Name)
		}
		state[s.Name] = active
		var deps []string
		for _, f := range s.Fields {
			for _, dep := range refNames(f.Type) {
				if _, ok := byName[dep]; ok {
					deps = append(deps, dep)
				}
			}
		}
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(byName[dep], append(path, s.Name)); err != nil {
				return err
			}
		}
		state[s.Name] = done
		order = append(order, s)
		return nil
	}
	names := make([]string, 0, len(structs))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := visit(byName[n], nil); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// refNames returns the named-type references a type carries, descending
// through arrays.
func refNames(t Type) []string {
	switch v := t.(type) {
	case namedRef:
		return []string{string(v)}
	case arrayType:
		return refNames(v.elem)
	}
	return nil
}

// validate compiles the generated ST so a bad identifier or dangling Ref
// surfaces as an error instead of invalid source on disk.
func validate(src string) error {
	prog, err := st.Parse(src)
	if err != nil {
		return err
	}
	_, err = st.Lower(prog)
	return err
}
