package ir

// TypeKind identifies the runtime representation of a value.
type TypeKind uint8

const (
	TypeVoid TypeKind = iota
	TypeBool
	TypeInt    // canonical integer: int64. All ST integer widths collapse here in phase 1.
	TypeReal   // canonical real: float64. LREAL and REAL collapse here in phase 1.
	TypeTime   // duration in milliseconds, stored in I.
	TypeString // UTF-8 string, stored in S.
	TypeStruct // UDT instance (phase 2+).
	TypeArray  // fixed-size array (phase 2+).
	TypeFB     // function block instance (phase 4+).
)

func (k TypeKind) String() string {
	switch k {
	case TypeVoid:
		return "VOID"
	case TypeBool:
		return "BOOL"
	case TypeInt:
		return "INT"
	case TypeReal:
		return "REAL"
	case TypeTime:
		return "TIME"
	case TypeString:
		return "STRING"
	case TypeStruct:
		return "STRUCT"
	case TypeArray:
		return "ARRAY"
	case TypeFB:
		return "FB"
	}
	return "?"
}

// Type is the resolved type of an IR value. Compound types carry extra data.
type Type struct {
	Kind       TypeKind
	Struct     *StructDef // Kind == TypeStruct
	Elem       *Type      // Kind == TypeArray
	ArrLen     int        // Kind == TypeArray
	ArrLoBound int        // Kind == TypeArray — IEC arrays may start at any integer
	FB         *FBDef     // Kind == TypeFB
}

// Singleton scalar types. Use these instead of allocating new *Type for every reference.
var (
	BoolT   = &Type{Kind: TypeBool}
	IntT    = &Type{Kind: TypeInt}
	RealT   = &Type{Kind: TypeReal}
	TimeT   = &Type{Kind: TypeTime}
	StringT = &Type{Kind: TypeString}
	VoidT   = &Type{Kind: TypeVoid}
)

// StructDef describes a UDT. Populated by phase 2.
type StructDef struct {
	Name   string
	Fields []StructField
	// FieldIndex maps field name to its slot index in Value.Fld.
	FieldIndex map[string]int
}

// StructField is a single named field within a UDT.
type StructField struct {
	Name string
	Type *Type
}

// FBDef describes a function block type. Slots are laid out
// Inputs ‖ Outputs ‖ Internals so call sites can address inputs by
// SlotIndex and read outputs/internals through MemberRef. Step runs
// the FB body once per scan with the instance's slot vector and a
// host-provided context (NowMs etc.).
type FBDef struct {
	Name      string
	Inputs    []FBSlot
	Outputs   []FBSlot
	Internals []FBSlot
	SlotIndex map[string]int
	Step      FBStepFn
}

// FBSlot is a single named slot on a function block instance.
type FBSlot struct {
	Name string
	Type *Type
}

// FBStepCtx is the per-cycle context handed to an FB's Step. Built-in FBs
// only need NowMs (timers); user-defined FBs need Host so their lowered
// bodies can call other FBs that themselves need NowMs.
type FBStepCtx struct {
	NowMs int64
	Host  Host // nil for tests that don't drive any host-touching FBs
}

// FBStepFn runs one cycle of an FB. It mutates inst.Slots in place
// (outputs + internal state); inputs are written by the caller before
// invoking Step.
type FBStepFn func(inst *FBInstance, ctx FBStepCtx) error

// AllSlots returns the FB's slot layout as a single ordered slice
// matching the runtime FBInstance.Slots layout.
func (d *FBDef) AllSlots() []FBSlot {
	out := make([]FBSlot, 0, len(d.Inputs)+len(d.Outputs)+len(d.Internals))
	out = append(out, d.Inputs...)
	out = append(out, d.Outputs...)
	out = append(out, d.Internals...)
	return out
}

// FuncDef describes a stateless IEC FUNCTION: a callable POU with typed
// inputs and a single typed return value, allocated fresh per call. The
// frame layout is Inputs ‖ Locals ‖ ReturnSlot. Run executes one call:
// the caller writes argument values into the first len(Inputs) slots
// and reads the result from ReturnSlot afterward.
type FuncDef struct {
	Name       string
	Inputs     []FBSlot
	Locals     []FBSlot
	ReturnType *Type
	// ReturnSlot is the index in the per-call frame holding the return
	// value (also exposed inside the body under the function's own name).
	ReturnSlot int
	// FrameSize is len(Inputs)+len(Locals)+1, sized to allocate the
	// per-call Frame.Slots slice without reaching into Inputs/Locals.
	FrameSize int
	Run       FuncRunFn
}

// FuncRunFn executes one call of a user function. The caller supplies a
// fresh frame with input slots pre-populated; the implementation runs
// the body and leaves the result in frame.Slots[def.ReturnSlot].
type FuncRunFn func(frame *Frame, host Host) error

// String renders the type for diagnostic messages.
func (t *Type) String() string {
	if t == nil {
		return "?"
	}
	switch t.Kind {
	case TypeStruct:
		if t.Struct != nil && t.Struct.Name != "" {
			return t.Struct.Name
		}
		return "STRUCT"
	case TypeArray:
		elem := "?"
		if t.Elem != nil {
			elem = t.Elem.String()
		}
		return "ARRAY OF " + elem
	}
	return t.Kind.String()
}

// IsNumeric reports whether t permits arithmetic operators.
func (t *Type) IsNumeric() bool {
	if t == nil {
		return false
	}
	return t.Kind == TypeInt || t.Kind == TypeReal || t.Kind == TypeTime
}

// Equal reports structural equality of two types.
func (t *Type) Equal(other *Type) bool {
	if t == other {
		return true
	}
	if t == nil || other == nil {
		return false
	}
	if t.Kind != other.Kind {
		return false
	}
	switch t.Kind {
	case TypeStruct:
		return t.Struct == other.Struct
	case TypeArray:
		return t.ArrLen == other.ArrLen && t.ArrLoBound == other.ArrLoBound && t.Elem.Equal(other.Elem)
	case TypeFB:
		return t.FB == other.FB
	}
	return true
}
