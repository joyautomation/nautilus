package ir

// VarKind classifies where a slot's canonical value lives.
type VarKind uint8

const (
	VarLocal  VarKind = iota // program-internal, lives in Frame.Slots
	VarInput                 // VAR_INPUT — treated as Local for scalars; FB call sites write before invoke
	VarOutput                // VAR_OUTPUT — treated as Local; caller reads after invoke
	VarGlobal                // shared PLC variable, read/written via Host
	VarInOut                 // VAR_IN_OUT — like Local; the FB call site copies in before Step and back out after
)

// VarSlot is a compile-time description of a variable location.
// Every IR reference is an index into Program.Slots.
type VarSlot struct {
	Name     string
	Type     *Type
	Init     Value // zero-valued Value.Kind means "use Zero(Type)"
	Retained bool
	// Constant marks a VAR CONSTANT declaration: its value is always Init,
	// so an online edit (MigrateFrame) takes the new declaration's value
	// instead of carrying the old frame's.
	Constant bool
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

	// Globals maps every PLC variable this program binds — VAR_EXTERNAL and
	// VAR_GLOBAL — to its declared type. Globals have no slot (their
	// canonical value lives in the tag store, reached through Host), so
	// Slots cannot answer what tags a program touches. Introspection only:
	// the VM resolves globals by name at runtime.
	Globals map[string]*Type

	// Types maps every TYPE declared in this source file (including the
	// project libraries joined ahead of it) to its resolved type. Like
	// SlotIndex and Globals this is introspection only — the VM resolves
	// nothing through it — but it is what a manifest tag naming a UDT
	// resolves against, so the tag store and the programs cannot disagree
	// about what a Motor is.
	Types map[string]*Type

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
	// scratch is the argument stack for builtin calls made while this
	// frame executes: a call reserves a window at the top, evaluates its
	// arguments into it (nested calls stack above), hands the window to
	// the builtin, and pops. It grows to the deepest call nesting once and
	// is never reallocated after, so a scan makes no allocation per call.
	scratch []Value
}

// NewFuncFrame allocates a fresh per-call frame for a user FUNCTION,
// each slot at its declared initial value (IEC: a FUNCTION's variables are
// re-initialised on every call), or zero for its type when it has none.
// The caller writes argument values into the input slots before invoking
// def.Run.
func NewFuncFrame(def *FuncDef) *Frame {
	slots := make([]Value, def.FrameSize)
	for i, s := range def.Inputs {
		slots[i] = s.initial()
	}
	f := &Frame{Slots: slots}
	def.reinitLocals(f)
	return f
}

// reinitLocals puts a frame's locals and return slot back to their
// declared initial values (IEC: re-initialised on every call). Inputs are
// left alone — the caller overwrites every one before the body runs.
func (def *FuncDef) reinitLocals(f *Frame) {
	for i, s := range def.Locals {
		f.Slots[len(def.Inputs)+i] = s.initial()
	}
	f.Slots[def.ReturnSlot] = Zero(def.ReturnType)
}

// acquireFrame hands out a call frame for def: a recycled one re-initialised
// in place, or a fresh one. releaseFrame gives it back once the caller has
// read the return value. A scalar-only function therefore allocates
// nothing per call after its first; recursion and concurrent callers each
// get their own frame because sync.Pool never hands one out twice.
func (def *FuncDef) acquireFrame() *Frame {
	if f, ok := def.frames.Get().(*Frame); ok {
		def.reinitLocals(f)
		return f
	}
	return NewFuncFrame(def)
}

func (def *FuncDef) releaseFrame(f *Frame) { def.frames.Put(f) }

// GlobalsDeep reports every PLC variable this program binds: its own
// Globals plus, transitively, the Globals of every FUNCTION_BLOCK instance
// it declares — nested FB-in-FB included, and however deep an array or
// struct field buries the instance. A library FB compiled with its own
// VAR_EXTERNAL has no top-level declaration of its own; the tag is bound
// through whichever program (or enclosing FB) gives the FB a slot, which is
// exactly what this walk follows. An FB type that exists but is never
// instantiated by anything reachable from this program contributes
// nothing — which is the point: an unused library block shouldn't make a
// tag look bound.
func (p *Program) GlobalsDeep() map[string]*Type {
	out := make(map[string]*Type, len(p.Globals))
	for name, t := range p.Globals {
		out[name] = t
	}
	seen := map[*FBDef]bool{}
	for _, s := range p.Slots {
		collectFBGlobals(s.Type, seen, out)
	}
	return out
}

// collectFBGlobals walks t looking for FUNCTION_BLOCK instances (directly,
// or nested inside an array/struct), merges each one's own Globals into
// out, and recurses into its slots to reach FB-in-FB instances. seen guards
// against revisiting a shared *FBDef (every instance of a type points at
// the same one) and against a pathological cycle.
func collectFBGlobals(t *Type, seen map[*FBDef]bool, out map[string]*Type) {
	if t == nil {
		return
	}
	switch t.Kind {
	case TypeFB:
		if t.FB == nil || seen[t.FB] {
			return
		}
		seen[t.FB] = true
		for name, gt := range t.FB.Globals {
			out[name] = gt
		}
		for _, s := range t.FB.AllSlots() {
			collectFBGlobals(s.Type, seen, out)
		}
	case TypeArray:
		collectFBGlobals(t.Elem, seen, out)
	case TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				collectFBGlobals(f.Type, seen, out)
			}
		}
	}
}
