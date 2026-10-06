package ir

// SameShape compares two resolved types structurally: two separately
// compiled sources resolve the same TYPE declaration to distinct pointers,
// which Equal (pointer identity for a struct) would call different. A
// struct matches by name and member list, recursively; an array by bounds
// and element; an FB by its definition's name.
func SameShape(a, b *Type) bool {
	switch {
	case a == b:
		return true
	case a == nil || b == nil || a.Kind != b.Kind:
		return false
	}
	switch a.Kind {
	case TypeStruct:
		if a.Struct == nil || b.Struct == nil {
			return a.Struct == b.Struct
		}
		if !SameName(a.Struct.Name, b.Struct.Name) || len(a.Struct.Fields) != len(b.Struct.Fields) {
			return false
		}
		for i := range a.Struct.Fields {
			if !SameName(a.Struct.Fields[i].Name, b.Struct.Fields[i].Name) ||
				!SameShape(a.Struct.Fields[i].Type, b.Struct.Fields[i].Type) {
				return false
			}
		}
		return true
	case TypeArray:
		return a.ArrLen == b.ArrLen && a.ArrLoBound == b.ArrLoBound && SameShape(a.Elem, b.Elem)
	case TypeFB:
		if a.FB == nil || b.FB == nil {
			return a.FB == b.FB
		}
		return SameName(a.FB.Name, b.FB.Name)
	}
	return a.Equal(b)
}
