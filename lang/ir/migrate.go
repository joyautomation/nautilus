package ir

import "strings"

// MigrateFrame builds the frame for a newly-compiled program, carrying
// retained state over from the previous program's frame — the heart of a
// PLC-style online edit: a PID integral, a timer's elapsed time, a counter's
// count all survive the swap.
//
// A slot carries when the previous program has a slot with the same name
// (case-insensitive, like IEC identifiers) and a structurally-compatible
// type. Everything else — new variables, renamed variables, variables whose
// type changed — starts at its declared initial value, and their names are
// returned so the editor can tell the user exactly what reset.
func MigrateFrame(next *Program, prev *Program, prevFrame *Frame) (*Frame, []string) {
	frame := NewFrame(next)
	if prev == nil || prevFrame == nil {
		return frame, nil
	}
	prevByName := make(map[string]int, len(prev.Slots))
	for i, s := range prev.Slots {
		if s.Kind != VarGlobal {
			prevByName[strings.ToLower(s.Name)] = i
		}
	}
	var resets []string
	for i, s := range next.Slots {
		if s.Kind == VarGlobal {
			continue // canonical value lives in the tag store, nothing to carry
		}
		j, ok := prevByName[strings.ToLower(s.Name)]
		if !ok {
			resets = append(resets, s.Name)
			continue
		}
		if !typesCompatible(s.Type, prev.Slots[j].Type) {
			resets = append(resets, s.Name)
			continue
		}
		frame.Slots[i] = carryValue(s.Type, prevFrame.Slots[j])
	}
	return frame, resets
}

// typesCompatible is a structural comparison: two independently-compiled
// programs never share Type pointers, so Type.Equal (pointer identity for
// structs/FBs) can't be used across a swap. Compatibility is strict — any
// change to a UDT's shape resets variables of that type, which is the
// predictable behavior for an online edit.
func typesCompatible(a, b *Type) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case TypeArray:
		return a.ArrLen == b.ArrLen && a.ArrLoBound == b.ArrLoBound && typesCompatible(a.Elem, b.Elem)
	case TypeStruct:
		if a.Struct == nil || b.Struct == nil || len(a.Struct.Fields) != len(b.Struct.Fields) {
			return false
		}
		for i := range a.Struct.Fields {
			fa, fb := a.Struct.Fields[i], b.Struct.Fields[i]
			if !strings.EqualFold(fa.Name, fb.Name) || !typesCompatible(fa.Type, fb.Type) {
				return false
			}
		}
		return true
	case TypeFB:
		return fbDefsCompatible(a.FB, b.FB)
	}
	return true
}

func fbDefsCompatible(a, b *FBDef) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a == b { // builtin FBs are shared singletons
		return true
	}
	if !strings.EqualFold(a.Name, b.Name) {
		return false
	}
	as, bs := a.AllSlots(), b.AllSlots()
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if !strings.EqualFold(as[i].Name, bs[i].Name) || !typesCompatible(as[i].Type, bs[i].Type) {
			return false
		}
	}
	return true
}

// carryValue deep-copies a value out of the old frame, rebinding FB
// instances to the new program's defs so their Step functions are the
// newly-compiled ones while their retained slots (a TON's start time, a
// CTU's count) carry over.
func carryValue(t *Type, v Value) Value {
	switch v.Kind {
	case TypeArray:
		out := Value{Kind: TypeArray, Arr: make([]Value, len(v.Arr))}
		var elem *Type
		if t != nil {
			elem = t.Elem
		}
		for i, e := range v.Arr {
			out.Arr[i] = carryValue(elem, e)
		}
		return out
	case TypeStruct:
		out := Value{Kind: TypeStruct, Fld: make([]Value, len(v.Fld))}
		if t != nil {
			out.Struct = t.Struct // adopt the new program's def for field naming
		} else {
			out.Struct = v.Struct
		}
		for i, f := range v.Fld {
			var ft *Type
			if out.Struct != nil && i < len(out.Struct.Fields) {
				ft = out.Struct.Fields[i].Type
			}
			out.Fld[i] = carryValue(ft, f)
		}
		return out
	case TypeFB:
		if v.FB == nil {
			return v
		}
		def := v.FB.Def
		if t != nil && t.FB != nil {
			def = t.FB // rebind to the new compile's FBDef
		}
		inst := &FBInstance{Def: def, Slots: make([]Value, len(v.FB.Slots))}
		all := def.AllSlots()
		for i, s := range v.FB.Slots {
			var st *Type
			if i < len(all) {
				st = all[i].Type
			}
			inst.Slots[i] = carryValue(st, s)
		}
		return Value{Kind: TypeFB, FB: inst}
	default:
		return v // scalars are self-contained
	}
}
