package ir

import "sync"

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

	// Name is the declared elementary type when it is not the canonical
	// one: every integer width collapses to TypeInt (int64), but a DINT is
	// still a DINT in a diagnostic and has 32 bits to address (#222). Empty
	// for the canonical singletons. Equal ignores it.
	Name string
	// Enum, on a TypeInt, makes this an enumerated type (#238): the value
	// is the member's integer, and the type carries the member names. Two
	// enum types are Equal only when they are the same declaration.
	Enum *EnumDef
}

// EnumDef is an IEC enumerated data type: TYPE Mode : (Idle, Run := 10,
// Fault) := Idle; END_TYPE. A value of the type is a TypeInt Value whose I
// is the member's integer and whose S is the member's name — the name rides
// with the value so the tag store, the HMI JSON and an editor's live values
// can show it with no type information of their own; the VM, Sparkplug and
// Modbus use only I.
type EnumDef struct {
	Name    string
	Members []EnumMember // declaration order
	// Default is the initial member's integer: the type's `:= Idle`, or the
	// first member when none is given.
	Default int64
}

// EnumMember is one named value of an enumerated type.
type EnumMember struct {
	Name  string
	Value int64
}

// Member finds a member by name, case-insensitively.
func (d *EnumDef) Member(name string) (EnumMember, bool) {
	if d == nil {
		return EnumMember{}, false
	}
	for _, m := range d.Members {
		if SameName(m.Name, name) {
			return m, true
		}
	}
	return EnumMember{}, false
}

// NameOf is the member name for an integer, "" when no member has it (an
// out-of-range value converted in with TO_<Enum>).
func (d *EnumDef) NameOf(v int64) string {
	if d == nil {
		return ""
	}
	for _, m := range d.Members {
		if m.Value == v {
			return m.Name
		}
	}
	return ""
}

// Val is the Value of the member with integer v, named.
func (d *EnumDef) Val(v int64) Value {
	return Value{Kind: TypeInt, I: v, S: d.NameOf(v)}
}

// intTypes are the declared integer types, one singleton each, so a
// diagnostic names DINT or WORD rather than the canonical INT.
var intTypes = map[string]*Type{}

func init() {
	for _, n := range []string{"SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD"} {
		intTypes[n] = &Type{Kind: TypeInt, Name: n}
	}
}

// IntNamed is the integer type declared as name (DINT, WORD, ...); nil
// when name is not an IEC integer type. Every one is TypeInt at run time.
func IntNamed(name string) *Type { return intTypes[name] }

// BitWidth is the number of addressable bits of an integer type: 8 for
// SINT/USINT/BYTE, 16 for INT/UINT/WORD, 32 for DINT/UDINT/DWORD, 64 for
// the L-types and for the canonical (undeclared-width) INT of an
// expression result. 0 for a type that is not an integer.
func (t *Type) BitWidth() int {
	if t == nil || t.Kind != TypeInt || t.Enum != nil {
		return 0
	}
	switch t.Name {
	case "SINT", "USINT", "BYTE":
		return 8
	case "INT", "UINT", "WORD":
		return 16
	case "DINT", "UDINT", "DWORD":
		return 32
	}
	return 64
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

// FBSlot is a single named slot on a function block instance (or a
// FUNCTION's frame).
type FBSlot struct {
	Name string
	Type *Type
	// Init is the declared initial value (`x : REAL := 2.0`); a zero
	// Value.Kind means "use Zero(Type)", as on VarSlot. NewFBInstance and
	// NewFuncFrame start the slot here.
	Init Value
	// Constant marks a VAR CONSTANT slot: its value is always Init, so an
	// online edit rebinding an instance (MigrateFrame) never carries the
	// old value over the new declaration.
	Constant bool
	// Temp marks a VAR_TEMP slot: reset at the start of every call (the
	// body's Program.Temps does it) and never carried by MigrateFrame.
	Temp bool
}

// SlotInitial is s's starting value: Init when declared, else the type's
// zero.
func SlotInitial(s FBSlot) Value { return s.initial() }

// initial is the slot's starting value: Init when declared, else the
// type's zero.
func (s FBSlot) initial() Value {
	if s.Init.Kind != TypeVoid {
		return CopyValue(s.Init)
	}
	return Zero(s.Type)
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
	// frames recycles per-call frames — see acquireFrame.
	frames sync.Pool
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
	if t.Enum != nil {
		return t.Enum.Name
	}
	if t.Name != "" {
		return t.Name
	}
	return t.Kind.String()
}

// IsNumeric reports whether t permits arithmetic operators. An enumerated
// type does not: its values are names, compared but never computed with.
func (t *Type) IsNumeric() bool {
	if t == nil || t.Enum != nil {
		return false
	}
	return t.Kind == TypeInt || t.Kind == TypeReal || t.Kind == TypeTime
}

// IsInteger reports a plain integer type (any width, not an enumeration).
func (t *Type) IsInteger() bool {
	return t != nil && t.Kind == TypeInt && t.Enum == nil
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
	case TypeInt:
		return t.Enum == other.Enum
	}
	return true
}
