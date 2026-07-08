package ir

// Value is the tagged-union runtime representation of an IR value.
// A concrete tag-union avoids interface allocation on every arithmetic op.
type Value struct {
	Kind TypeKind
	I    int64   // TypeInt, TypeTime, TypeBool(fallback) encoded bits if needed
	F    float64 // TypeReal
	B    bool    // TypeBool
	S    string  // TypeString
	Arr  []Value // TypeArray
	Fld  []Value // TypeStruct — parallel to StructDef.Fields
	FB   *FBInstance
}

// FBInstance holds the retained slot frame of a function block. Placeholder; populated in phase 4.
type FBInstance struct {
	Def   *FBDef
	Slots []Value
}

// Constructors (keep the call sites concise).

func IntVal(v int64) Value     { return Value{Kind: TypeInt, I: v} }
func RealVal(v float64) Value  { return Value{Kind: TypeReal, F: v} }
func BoolVal(v bool) Value     { return Value{Kind: TypeBool, B: v} }
func TimeVal(ms int64) Value   { return Value{Kind: TypeTime, I: ms} }
func StringVal(v string) Value { return Value{Kind: TypeString, S: v} }

// Zero returns the IEC 61131-3 default value for t.
func Zero(t *Type) Value {
	if t == nil {
		return Value{}
	}
	switch t.Kind {
	case TypeBool:
		return Value{Kind: TypeBool}
	case TypeInt:
		return Value{Kind: TypeInt}
	case TypeReal:
		return Value{Kind: TypeReal}
	case TypeTime:
		return Value{Kind: TypeTime}
	case TypeString:
		return Value{Kind: TypeString}
	case TypeArray:
		a := make([]Value, t.ArrLen)
		for i := range a {
			a[i] = Zero(t.Elem)
		}
		return Value{Kind: TypeArray, Arr: a}
	case TypeStruct:
		f := make([]Value, len(t.Struct.Fields))
		for i, fld := range t.Struct.Fields {
			f[i] = Zero(fld.Type)
		}
		return Value{Kind: TypeStruct, Fld: f}
	case TypeFB:
		return Value{Kind: TypeFB, FB: NewFBInstance(t.FB)}
	}
	return Value{}
}

// NewFBInstance allocates a fresh FB instance with all slots zero-valued
// per their declared types. Outputs and internals start at IEC default;
// inputs are also zero until the first call binds them.
func NewFBInstance(def *FBDef) *FBInstance {
	all := def.AllSlots()
	slots := make([]Value, len(all))
	for i, s := range all {
		slots[i] = Zero(s.Type)
	}
	return &FBInstance{Def: def, Slots: slots}
}
