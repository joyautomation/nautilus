package logixserver

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/eip/cip"
)

// TagSurfaceSpec is the declarative tag surface a Logix server advertises and
// serves. It compiles into the wire-level Schema — instance ids, symbol-type
// bit fields, and template member offsets are all assigned by the loader,
// keeping the spec readable.
type TagSurfaceSpec struct {
	ControllerName string         `json:"controllerName,omitempty"`
	Templates      []TemplateSpec `json:"templates"`
	Symbols        []SymbolSpec   `json:"symbols"`
	Tags           []TagSpec      `json:"tags"`
}

// TemplateSpec is a UDT definition: a named struct with ordered members.
type TemplateSpec struct {
	Name    string       `json:"name"`
	Members []MemberSpec `json:"members"`
}

// MemberSpec is one struct member. Datatype is an elementary name (BOOL, DINT,
// REAL, ...) or the name of another template (nested struct).
type MemberSpec struct {
	Name     string `json:"name"`
	Datatype string `json:"datatype"`
}

// SymbolSpec is one tag-list symbol. Scope "" is controller; "Program:<prog>"
// is program-scoped. Datatype is elementary or a template name. Program marks
// the controller-scope marker symbol for a program (name "Program:<prog>",
// no datatype).
type SymbolSpec struct {
	Name     string   `json:"name"`
	Scope    string   `json:"scope,omitempty"`
	Datatype string   `json:"datatype,omitempty"`
	Dims     []uint32 `json:"dims,omitempty"`
	Program  bool     `json:"program,omitempty"`
}

// TagSpec is one resolvable read leaf: the canonical path a client reads and
// the elementary type its value is encoded as.
type TagSpec struct {
	Path     string `json:"path"`
	Datatype string `json:"datatype"`
}

// TagConfig is a compiled leaf tag: the canonical store path, its CIP
// elementary type code, and the value served before any update.
type TagConfig struct {
	Path     string
	LeafType uint16
	Default  any
}

// cipTypeForName maps an IEC/Logix elementary type name to its CIP type code.
func cipTypeForName(name string) (uint16, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BOOL":
		return cip.TypeBOOL, true
	case "SINT":
		return cip.TypeSINT, true
	case "USINT", "BYTE":
		return cip.TypeUSINT, true
	case "INT":
		return cip.TypeINT, true
	case "UINT", "WORD":
		return cip.TypeUINT, true
	case "DINT":
		return cip.TypeDINT, true
	case "UDINT", "DWORD":
		return cip.TypeUDINT, true
	case "LINT":
		return cip.TypeLINT, true
	case "ULINT", "LWORD":
		return cip.TypeULINT, true
	case "REAL":
		return cip.TypeREAL, true
	case "LREAL":
		return cip.TypeLREAL, true
	default:
		return 0, false
	}
}

// sizeForCIPType returns the wire size in bytes of an elementary CIP type.
func sizeForCIPType(code uint16) uint32 {
	switch code {
	case cip.TypeBOOL, cip.TypeSINT, cip.TypeUSINT:
		return 1
	case cip.TypeINT, cip.TypeUINT, cip.TypeWORD:
		return 2
	case cip.TypeDINT, cip.TypeUDINT, cip.TypeDWORD, cip.TypeREAL:
		return 4
	case cip.TypeLINT, cip.TypeULINT, cip.TypeLWORD, cip.TypeLREAL:
		return 8
	default:
		return 4
	}
}

// zeroForCIPType returns the served-before-first-update default for a type.
func zeroForCIPType(code uint16) any {
	if code == cip.TypeBOOL {
		return false
	}
	return float64(0)
}

// LoadTagSurface reads a tag-surface JSON file and compiles it into a Schema
// and the resolvable tag list. Returns the controller name from the spec (may
// be empty).
func LoadTagSurface(path string) (*Schema, []TagConfig, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read tag surface: %w", err)
	}
	var spec TagSurfaceSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, nil, "", fmt.Errorf("parse tag surface: %w", err)
	}
	return CompileSurface(&spec)
}

// CompileSurface turns a declarative spec into the wire-level Schema plus the
// store-seed tag list. Template ids start at 0x101; instance ids are assigned
// per scope starting at 1 (matching Logix conventions pycomm3 expects).
func CompileSurface(spec *TagSurfaceSpec) (*Schema, []TagConfig, string, error) {
	// Assign a template id to each template name, in declared order.
	tmplID := make(map[string]uint32, len(spec.Templates))
	nextTmpl := uint32(0x101)
	for _, t := range spec.Templates {
		if t.Name == "" {
			return nil, nil, "", fmt.Errorf("template with empty name")
		}
		if _, dup := tmplID[t.Name]; dup {
			return nil, nil, "", fmt.Errorf("duplicate template %q", t.Name)
		}
		tmplID[t.Name] = nextTmpl
		nextTmpl++
	}

	templates := make(map[uint32]*templateDef, len(spec.Templates))
	handle := uint16(0x1001)
	for _, t := range spec.Templates {
		def := &templateDef{
			instanceID: tmplID[t.Name],
			name:       t.Name,
			handle:     handle,
		}
		handle++
		var offset uint32
		for _, m := range t.Members {
			tm := templateMember{name: m.Name, offset: offset}
			if code, ok := cipTypeForName(m.Datatype); ok {
				tm.typeCode = code
				offset += sizeForCIPType(code)
			} else if childID, ok := tmplID[m.Datatype]; ok {
				child, built := templates[childID]
				if !built {
					return nil, nil, "", fmt.Errorf("template %q member %q: nested template %q must be declared first", t.Name, m.Name, m.Datatype)
				}
				tm.typeCode = symbolTypeStructBit | uint16(childID)
				offset += child.structSize
			} else {
				return nil, nil, "", fmt.Errorf("template %q member %q: unknown datatype %q", t.Name, m.Name, m.Datatype)
			}
			def.members = append(def.members, tm)
		}
		// Round the struct size up to a 4-byte boundary, as Logix does.
		def.structSize = (offset + 3) / 4 * 4
		if def.structSize == 0 {
			def.structSize = 4
		}
		templates[def.instanceID] = def
	}

	// Per-scope instance-id counters. Controller scope is "".
	nextInst := map[string]uint32{}
	allocInst := func(scope string) uint32 {
		n := nextInst[scope]
		if n == 0 {
			n = 1
		}
		nextInst[scope] = n + 1
		return n
	}

	var symbols []symbolDef
	instanceBase := map[uint32]string{}
	symbolByPath := map[string]symbolDef{}
	for _, s := range spec.Symbols {
		sym := symbolDef{name: s.Name, scope: s.Scope, instanceID: allocInst(s.Scope)}
		switch {
		case s.Program || (s.Datatype == "" && strings.HasPrefix(s.Name, "Program:")):
			sym.symbolType = 0 // program marker — clients consume the name only
		default:
			if code, ok := cipTypeForName(s.Datatype); ok {
				sym.symbolType = code
			} else if id, ok := tmplID[s.Datatype]; ok {
				sym.symbolType = symbolTypeStructBit | uint16(id)
			} else {
				return nil, nil, "", fmt.Errorf("symbol %q: unknown datatype %q", s.Name, s.Datatype)
			}
			if n := len(s.Dims); n > 0 {
				if n > 3 {
					return nil, nil, "", fmt.Errorf("symbol %q: %d dimensions (max 3)", s.Name, n)
				}
				sym.symbolType |= uint16(n) << symbolTypeDimShift
				for i := 0; i < n; i++ {
					sym.dims[i] = s.Dims[i]
				}
			}
		}
		symbols = append(symbols, sym)
		if sym.scope == "" && !strings.HasPrefix(sym.name, "Program:") {
			instanceBase[sym.instanceID] = sym.name
		}
		canonical := sym.name
		if sym.scope != "" {
			canonical = sym.scope + "." + sym.name
		}
		symbolByPath[strings.ToUpper(canonical)] = sym
	}

	tags := make([]TagConfig, 0, len(spec.Tags))
	for _, t := range spec.Tags {
		code, ok := cipTypeForName(t.Datatype)
		if !ok {
			return nil, nil, "", fmt.Errorf("tag %q: unknown leaf datatype %q", t.Path, t.Datatype)
		}
		tags = append(tags, TagConfig{
			Path:     t.Path,
			LeafType: code,
			Default:  zeroForCIPType(code),
		})
	}

	// Keep symbols deterministic for stable upload ordering.
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].scope != symbols[j].scope {
			return symbols[i].scope < symbols[j].scope
		}
		return symbols[i].instanceID < symbols[j].instanceID
	})

	return &Schema{
		symbols:      symbols,
		templates:    templates,
		instanceBase: instanceBase,
		symbolByPath: symbolByPath,
	}, tags, spec.ControllerName, nil
}
