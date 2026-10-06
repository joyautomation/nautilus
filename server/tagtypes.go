package server

import (
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
)

// tagMetaJSON is one tag's entry in /api/meta: its HMI documentation
// (desc, unit) and, when it has one, its declared type (#246).
type tagMetaJSON struct {
	runtime.TagMeta
	typeInfo
}

// typeInfo is a declared type as a client needs it to show a value. Type
// is the type's name (`REAL`, `Motor`, `Mode`). Enum, on an enumerated
// type, lists its members in declaration order: an enumerated value streams
// as its member's name ("Run"), and without this a client cannot tell it
// from a STRING — nor offer the members when an operator sets one.
//
// Members (a struct's fields, a function block's pins and internals) and
// Elem (an array's element) are present only where an enumeration sits
// somewhere beneath, so a 173-member AOI with no enum costs one name, and
// the stream itself carries no type information at all.
type typeInfo struct {
	Type    string               `json:"type,omitempty"`
	Enum    []enumMember         `json:"enum,omitempty"`
	Members map[string]*typeInfo `json:"members,omitempty"`
	Elem    *typeInfo            `json:"elem,omitempty"`
}

// enumMember is one named value of an enumerated type.
type enumMember struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// describeType renders t for /api/meta (see typeInfo).
func describeType(t *ir.Type) typeInfo {
	if t == nil {
		return typeInfo{}
	}
	ti := typeInfo{Type: t.String()}
	if t.Kind == ir.TypeFB && t.FB != nil && t.FB.Name != "" {
		ti.Type = t.FB.Name // String says FB
	}
	if t.Enum != nil {
		ti.Enum = make([]enumMember, len(t.Enum.Members))
		for i, m := range t.Enum.Members {
			ti.Enum[i] = enumMember{Name: m.Name, Value: m.Value}
		}
		return ti
	}
	member := func(name string, mt *ir.Type) {
		if !holdsEnum(mt, 0) {
			return
		}
		if ti.Members == nil {
			ti.Members = map[string]*typeInfo{}
		}
		d := describeType(mt)
		ti.Members[name] = &d
	}
	switch t.Kind {
	case ir.TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				member(f.Name, f.Type)
			}
		}
	case ir.TypeFB:
		if t.FB != nil {
			for _, group := range [][]ir.FBSlot{t.FB.Inputs, t.FB.Outputs, t.FB.InOuts, t.FB.Internals} {
				for _, s := range group {
					member(s.Name, s.Type)
				}
			}
		}
	case ir.TypeArray:
		if holdsEnum(t.Elem, 0) {
			d := describeType(t.Elem)
			ti.Elem = &d
		}
	}
	return ti
}

// holdsEnum reports whether an enumeration sits in t or anywhere beneath
// it. depth bounds the walk; a declared type cannot nest itself, but a
// malformed one must not hang /api/meta.
func holdsEnum(t *ir.Type, depth int) bool {
	if t == nil || depth > 32 {
		return false
	}
	if t.Enum != nil {
		return true
	}
	switch t.Kind {
	case ir.TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				if holdsEnum(f.Type, depth+1) {
					return true
				}
			}
		}
	case ir.TypeFB:
		if t.FB != nil {
			for _, group := range [][]ir.FBSlot{t.FB.Inputs, t.FB.Outputs, t.FB.InOuts, t.FB.Internals} {
				for _, s := range group {
					if holdsEnum(s.Type, depth+1) {
						return true
					}
				}
			}
		}
	case ir.TypeArray:
		return holdsEnum(t.Elem, depth+1)
	}
	return false
}

// metaTags merges the runtime's tag documentation with every tag's
// declared type, keyed by the spelling the stream uses.
func metaTags(rt *runtime.Runtime) map[string]tagMetaJSON {
	out := map[string]tagMetaJSON{}
	for name, m := range rt.Meta() {
		out[name] = tagMetaJSON{TagMeta: m}
	}
	for name, t := range rt.TagTypes() {
		e := out[name]
		e.typeInfo = describeType(t)
		out[name] = e
	}
	return out
}

// metaLocals is every streamed program local's declared type.
func metaLocals(rt *runtime.Runtime) map[string]typeInfo {
	out := map[string]typeInfo{}
	for name, t := range rt.LocalTypes() {
		out[name] = describeType(t)
	}
	return out
}
