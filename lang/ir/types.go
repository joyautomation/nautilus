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
// Inputs ‖ Outputs ‖ InOuts ‖ Internals so call sites can address inputs by
// SlotIndex and read outputs/internals through MemberRef. Step runs
// the FB body once per scan with the instance's slot vector and a
// host-provided context (NowMs etc.).
//
// InOuts are VAR_IN_OUT pins: the call site must bind each to an
// assignable variable, whose value is copied in before Step and copied
// back out after it (see the FBCall lowering in lang/st).
type FBDef struct {
	Name      string
	Inputs    []FBSlot
	Outputs   []FBSlot
	InOuts    []FBSlot
	Internals []FBSlot
	SlotIndex map[string]int
	Step      FBStepFn

	// Globals are the PLC variables this FB type's OWN body binds via
	// VAR_EXTERNAL/VAR_GLOBAL, with the type each was declared as — the
	// FB-scoped counterpart of Program.Globals. A library FUNCTION_BLOCK
	// compiles and runs correctly with one of these: the VM resolves the
	// tag through whichever program's instance steps the FB, not through
	// any top-level declaration of its own — so a caller has to walk every
	// instantiated FB's Globals (Program.GlobalsDeep does this) to see the
	// tags a program actually touches. Populated once, at Lower time
	// (lang/st), for user-defined FBs; nil for built-ins, which have no
	// source body to declare one.
	Globals map[string]*Type

	// Uses records how this FB type's own body reads/writes its Globals —
	// the FB-scoped counterpart of Program.GlobalUses. Populated alongside
	// Globals.
	Uses GlobalUse
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

// Slot returns the i'th slot of the Inputs ‖ Outputs ‖ InOuts ‖ Internals
// layout without materialising the combined slice — the VM's per-scan path,
// kept allocation-free.
func (d *FBDef) Slot(i int) FBSlot {
	if i < len(d.Inputs) {
		return d.Inputs[i]
	}
	i -= len(d.Inputs)
	if i < len(d.Outputs) {
		return d.Outputs[i]
	}
	i -= len(d.Outputs)
	if i < len(d.InOuts) {
		return d.InOuts[i]
	}
	return d.Internals[i-len(d.InOuts)]
}

// AllSlots returns the FB's slot layout as a single ordered slice
// matching the runtime FBInstance.Slots layout.
func (d *FBDef) AllSlots() []FBSlot {
	out := make([]FBSlot, 0, len(d.Inputs)+len(d.Outputs)+len(d.InOuts)+len(d.Internals))
	out = append(out, d.Inputs...)
	out = append(out, d.Outputs...)
	out = append(out, d.InOuts...)
	out = append(out, d.Internals...)
	return out
}

// IsInput reports whether slot index i is a VAR_INPUT pin.
func (d *FBDef) IsInput(i int) bool { return i >= 0 && i < len(d.Inputs) }

// IsOutput reports whether slot index i is a VAR_OUTPUT pin.
func (d *FBDef) IsOutput(i int) bool {
	return i >= len(d.Inputs) && i < len(d.Inputs)+len(d.Outputs)
}

// IsInOut reports whether slot index i is a VAR_IN_OUT pin.
func (d *FBDef) IsInOut(i int) bool {
	lo := len(d.Inputs) + len(d.Outputs)
	return i >= lo && i < lo+len(d.InOuts)
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
