package ir

// Value is the tagged-union runtime representation of an IR value.
// A concrete tag-union avoids interface allocation on every arithmetic op.
type Value struct {
	Kind TypeKind
	I    int64   // TypeInt, TypeTime, TypeBool(fallback) encoded bits if needed
	F    float64 // TypeReal
	B    bool    // TypeBool
	S    string  // TypeString; on a TypeInt, an enumeration member's name (see EnumDef)
	Arr  []Value // TypeArray
	Fld  []Value // TypeStruct — parallel to StructDef.Fields
	// Struct names the fields of a TypeStruct value. The VM addresses fields
	// by index and never needs it; it exists so consumers outside the VM
	// (HMI JSON, field-bus drivers) can render or bind fields by name.
	Struct *StructDef
	FB     *FBInstance
}

// FBInstance holds the retained slot frame of a function block. Placeholder; populated in phase 4.
type FBInstance struct {
	Def   *FBDef
	Slots []Value
	// frame is the body's execution frame over Slots, kept across steps so
	// its scratch stack (see Frame) warms up once. See StepFrame.
	frame *Frame
}

// StepFrame returns the frame a user-defined FB's body runs in: a view over
// the instance's own Slots, created once and reused on every step. If the
// slots were replaced (an online edit migrated the instance) a fresh one
// is built, so the frame can never outlive the storage it indexes.
func (inst *FBInstance) StepFrame() *Frame {
	if inst.frame == nil || len(inst.Slots) == 0 || len(inst.frame.Slots) != len(inst.Slots) || &inst.frame.Slots[0] != &inst.Slots[0] {
		inst.frame = &Frame{Slots: inst.Slots}
	}
	return inst.frame
}

// Constructors (keep the call sites concise).

func IntVal(v int64) Value     { return Value{Kind: TypeInt, I: v} }
func RealVal(v float64) Value  { return Value{Kind: TypeReal, F: v} }
func BoolVal(v bool) Value     { return Value{Kind: TypeBool, B: v} }
func TimeVal(ms int64) Value   { return Value{Kind: TypeTime, I: ms} }
func StringVal(v string) Value { return Value{Kind: TypeString, S: v} }

// CopyValue returns a value with independent storage for composite kinds.
//
// Value is a tagged union whose ARRAY and STRUCT payloads are slices, so a
// plain Go copy aliases the source's backing array — two variables would
// then share fields, and a later `a.F := 1` would silently mutate `b`. Every
// point that *stores* a value (assignment, FB pin copy-in/copy-back) runs it
// through here so ST assignment has value semantics, which is what the
// standard means by assigning a structured variable.
//
// FB instances are deliberately NOT copied: an instance is identity (its
// retained state), never a value, and nothing in the language assigns one.
func CopyValue(v Value) Value {
	switch v.Kind {
	case TypeArray:
		if v.Arr == nil {
			return v
		}
		out := v
		out.Arr = make([]Value, len(v.Arr))
		for i, e := range v.Arr {
			out.Arr[i] = CopyValue(e)
		}
		return out
	case TypeStruct:
		if v.Fld == nil {
			return v
		}
		out := v
		out.Fld = make([]Value, len(v.Fld))
		for i, f := range v.Fld {
			out.Fld[i] = CopyValue(f)
		}
		return out
	}
	return v
}

// Zero returns the IEC 61131-3 default value for t.
func Zero(t *Type) Value {
	if t == nil {
		return Value{}
	}
	switch t.Kind {
	case TypeBool:
		return Value{Kind: TypeBool}
	case TypeInt:
		if t.Enum != nil {
			return t.Enum.Val(t.Enum.Default)
		}
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
		return Value{Kind: TypeStruct, Fld: f, Struct: t.Struct}
	case TypeFB:
		return Value{Kind: TypeFB, FB: NewFBInstance(t.FB)}
	}
	return Value{}
}

// NewFBInstance allocates a fresh FB instance with every slot at its
// declared initial value (`VAR Gain : REAL := 2.0;`, VAR CONSTANT
// included), or zero for its type when it has none. Inputs start there
// too until the first call binds them.
func NewFBInstance(def *FBDef) *FBInstance {
	all := def.AllSlots()
	slots := make([]Value, len(all))
	for i, s := range all {
		slots[i] = s.initial()
	}
	return &FBInstance{Def: def, Slots: slots}
}
