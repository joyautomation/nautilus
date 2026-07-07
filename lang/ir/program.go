
package ir

// VarKind classifies where a slot's canonical value lives.
type VarKind uint8

const (
	VarLocal  VarKind = iota // program-internal, lives in Frame.Slots
	VarInput                 // VAR_INPUT — treated as Local for scalars; FB call sites write before invoke
	VarOutput                // VAR_OUTPUT — treated as Local; caller reads after invoke
	VarGlobal                // shared PLC variable, read/written via Host
)

// VarSlot is a compile-time description of a variable location.
// Every IR reference is an index into Program.Slots.
type VarSlot struct {
	Name     string
	Type     *Type
	Init     Value // zero-valued Value.Kind means "use Zero(Type)"
	Retained bool
	Kind     VarKind
	Global   string // Kind == VarGlobal: PLC variable name passed to Host
}

// Program is a compiled ST program (or function-block body in phase 4).
type Program struct {
	Name  string
	Slots []VarSlot
	Body  []Stmt

	// SlotIndex maps name → index. Populated by the lowering pass.
	// The VM does not consult this; it exists for introspection (LSP, debug, tests).
	SlotIndex map[string]int

	// UserFBs are FBDefs declared at the top of this source file via
	// FUNCTION_BLOCK ... END_FUNCTION_BLOCK. The engine pulls these out
	// after Lower returns and registers them so other programs can use
	// them by name.
	UserFBs []*FBDef

	// UserFuncs are FuncDefs declared at the top of this source file via
	// FUNCTION Name : Return ... END_FUNCTION. The engine registers them
	// so other programs can call them by bare name like a built-in.
	UserFuncs []*FuncDef
}

// NewFrame allocates a Frame sized to the program's slot table and populates initial values.
// Retained state survives across scans — callers keep the frame pointer stable between Run calls.
func NewFrame(prog *Program) *Frame {
	slots := make([]Value, len(prog.Slots))
	for i, sl := range prog.Slots {
		if sl.Init.Kind != TypeVoid {
			slots[i] = sl.Init
		} else {
			slots[i] = Zero(sl.Type)
		}
	}
	return &Frame{Slots: slots}
}

// Frame is a mutable runtime slot vector. One per program instance.
type Frame struct {
	Slots []Value
}

// NewFuncFrame allocates a fresh per-call frame for a user FUNCTION,
// zero-initialised against each slot's declared type. The caller writes
// argument values into the input slots before invoking def.Run.
func NewFuncFrame(def *FuncDef) *Frame {
	slots := make([]Value, def.FrameSize)
	for i, s := range def.Inputs {
		slots[i] = Zero(s.Type)
	}
	for i, s := range def.Locals {
		slots[len(def.Inputs)+i] = Zero(s.Type)
	}
	slots[def.ReturnSlot] = Zero(def.ReturnType)
	return &Frame{Slots: slots}
}
