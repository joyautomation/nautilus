package st

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// LowerOpts carries optional compile-time context for the lowerer.
//
// UserFBs is a registry of previously compiled user FB types keyed by
// name; when non-nil it's consulted alongside the built-in FB registry
// for `VAR x : SomeFB;` resolution.
//
// UserFuncs is a registry of previously compiled user FUNCTION defs
// keyed by name; consulted at call sites for bare-name resolution.
//
// ImplicitGlobals lets the caller surface PLC-wide variables (declared
// in the PLC config) so programs can reference them directly without
// repeating VAR_GLOBAL boilerplate. Names already declared explicitly
// in the source win: a local VAR of the same name shadows the global,
// and an explicit VAR_GLOBAL/VAR_EXTERNAL binds it with its own type
// (which must agree when TagTypes states one). An implicit global is
// recorded in ir.Program.Globals only where the PROGRAM body actually
// names it — a manifest of a thousand tags must not make every program
// bind (and snapshot) all of them. A nil type marks a tag the project
// declares but cannot type (no type:, no init:); naming it is an error
// that says so, instead of "undeclared". FUNCTION_BLOCK and FUNCTION
// bodies never see implicit globals: a block reaches a tag only through
// its own VAR_EXTERNAL, so it stays self-contained.
//
// TagTypes are the tags whose type the project STATES (a manifest
// type:). An explicit VAR_GLOBAL / VAR_EXTERNAL of one must declare that
// same type; a disagreement is an error at the declaration. A type that
// was only inferred (from an init: value) is not here, so a program's
// `Count : DINT` keeps overriding an `init: 0` that would read as REAL.
//
// Types is a registry of TYPEs resolved elsewhere (a separately compiled
// library) that this source should see in addition to its own TYPE block.
// nautilus normally joins library text ahead of the program, so this is
// usually nil; a TYPE declared in the joined text needs nothing here.
type LowerOpts struct {
	UserFBs         map[string]*ir.FBDef
	UserFuncs       map[string]*ir.FuncDef
	ImplicitGlobals map[string]*ir.Type
	TagTypes        map[string]*ir.Type
	// TagTypeErrs says why a tag's stated type: did not resolve (a
	// misspelt TYPE), keyed by tag name. Such a tag reaches ImplicitGlobals
	// untyped; naming it then reports this reason rather than "no type",
	// which would send the author to add the type: the tag already has.
	TagTypeErrs map[string]error
	Types       map[string]*ir.Type
	// Warn, when set, receives what lowering accepts but a reader likely
	// misreads (LowerWarning). Lowering carries on either way: these are
	// programs that compile and run, and naut check prints them as
	// warnings.
	Warn func(LowerWarning)
	// Peers are the project's other programs (the one being lowered may be
	// among them; it is skipped). They put nothing in scope — they only let
	// an undeclared identifier that names another program's variable say
	// that programs share only tags (#327). See PeerProgram.
	Peers []PeerProgram
}

// Lower converts a parsed ST program into typed IR.
//
// It resolves UDTs, builds a slot table for locals, rejects undeclared
// identifiers (declare in VAR_* / VAR_GLOBAL / VAR_EXTERNAL), type-checks
// every expression, and rewrites array indexing to 0-based form using each
// array's declared lower bound.
//
// Top-level FUNCTION_BLOCK declarations are lowered into FBDef records
// attached to the resulting ir.Program.UserFBs slice. The engine pulls
// these out and registers them so other programs can use the FB type.
//
// userFBs is an optional registry of previously compiled user FB types
// keyed by name. Pass nil for standalone compilation (tests, single-file
// scripts). For richer context (implicit project globals) use LowerWithOpts.
func Lower(prog *Program, userFBs ...map[string]*ir.FBDef) (*ir.Program, error) {
	var resolver map[string]*ir.FBDef
	if len(userFBs) > 0 {
		resolver = userFBs[0]
	}
	return LowerWithOpts(prog, LowerOpts{UserFBs: resolver})
}

// LowerWithOpts is the option-aware lowering entry point.
func LowerWithOpts(prog *Program, opts LowerOpts) (*ir.Program, error) {
	resolver := opts.UserFBs

	// Pre-pass: build empty FBDef shells for every FUNCTION_BLOCK in this
	// file so peer FBs and the outer program can reference them by name
	// during type resolution. The defs are pointers, so populating their
	// slot tables in the next phase is observed by all earlier references.
	// A nautilus system function's name is not a POU's (#710 (b)): a call
	// would bind to the POU, and the program would read its own answer
	// for the runtime's.
	if prog.Name != "" && ir.IsSystemFunction(prog.Name) {
		return nil, systemNameErr("PROGRAM", prog.Name)
	}
	for _, fbDecl := range prog.FBDecls {
		if ir.IsSystemFunction(fbDecl.Name) {
			return nil, errAt(fbDecl.Pos, systemNameErr("FUNCTION_BLOCK", fbDecl.Name))
		}
	}
	for _, fd := range prog.FuncDecls {
		if ir.IsSystemFunction(fd.Name) {
			return nil, errAt(fd.Pos, systemNameErr("FUNCTION", fd.Name))
		}
	}
	inFile := map[string]*ir.FBDef{}
	for _, fbDecl := range prog.FBDecls {
		if _, prev, dup := ir.Lookup(inFile, fbDecl.Name); dup {
			return nil, errAt(fbDecl.Pos, dupErr("FUNCTION_BLOCK", fbDecl.Name, prev))
		}
		inFile[fbDecl.Name] = &ir.FBDef{Name: fbDecl.Name, SlotIndex: map[string]int{}}
	}
	combined := resolver
	if len(inFile) > 0 {
		combined = mergeShadowing(resolver, inFile)
	}

	// Same dance for FUNCTIONs: build empty shells so peer functions and
	// the outer program can resolve each by name regardless of order.
	inFileFuncs := map[string]*ir.FuncDef{}
	for _, fd := range prog.FuncDecls {
		if _, prev, dup := ir.Lookup(inFileFuncs, fd.Name); dup {
			return nil, errAt(fd.Pos, dupErr("FUNCTION", fd.Name, prev))
		}
		inFileFuncs[fd.Name] = &ir.FuncDef{Name: fd.Name}
	}
	combinedFuncs := opts.UserFuncs
	if len(inFileFuncs) > 0 {
		combinedFuncs = mergeShadowing(opts.UserFuncs, inFileFuncs)
	}

	l := newLowerer(prog, combined)
	l.userFuncs = combinedFuncs
	l.implicitGlobals = opts.ImplicitGlobals
	l.tagTypes = opts.TagTypes
	for name, err := range opts.TagTypeErrs {
		if l.tagTypeErrs == nil {
			l.tagTypeErrs = map[string]error{}
		}
		l.tagTypeErrs[ir.NameKey(name)] = err
	}
	l.peers = opts.Peers
	l.warn = dedupWarn(opts.Warn)
	// Seed the file's TYPE table with any the caller supplies (a separately
	// compiled library), then resolve this file's own TYPE block. Both are
	// in scope for the POUs below — a FUNCTION_BLOCK pin may name a UDT.
	for name, t := range opts.Types {
		if t != nil {
			l.types[name] = t
			if t.Enum != nil {
				l.enums = append(l.enums, t)
			}
		}
	}
	// Order: TYPE shells and enumerations first (a constant may be of an
	// enumerated type), then the file's VAR_GLOBAL CONSTANT blocks (#176),
	// then the remaining TYPEs (an array bound may name a constant).
	if err := l.collectTypeShells(); err != nil {
		return nil, err
	}
	if err := l.collectGlobalConsts(); err != nil {
		return nil, err
	}
	if err := l.collectTypes(); err != nil {
		return nil, err
	}

	// Resolve FB and FUNCTION signatures (slot lists + types) before
	// lowering any body so FB-on-FB member access type-checks regardless of
	// declaration order. They resolve against l.types, so a pin may be
	// declared with a user TYPE from this file or a project library.
	for _, fbDecl := range prog.FBDecls {
		if err := populateFBSignature(fbDecl, inFile[fbDecl.Name], l); err != nil {
			return nil, err
		}
	}
	for _, fd := range prog.FuncDecls {
		if err := populateFuncSignature(fd, inFileFuncs[fd.Name], l); err != nil {
			return nil, err
		}
	}

	// Keep the resolved TYPE table on the program. The VM never reads it —
	// this is the same introspection-only category as SlotIndex and Globals
	// — but a manifest tag naming a UDT (`type: Motor`) has nowhere else to
	// resolve against, and the ST library is where the programs already
	// agree on what a Motor is.
	l.irProg.Types = l.types
	if err := l.collectVars(); err != nil {
		return nil, err
	}
	body, err := l.lowerStmts(prog.Statements)
	if err != nil {
		return nil, err
	}
	l.irProg.Body = body

	// Lower each FB body now that all FB signatures are known. Bodies may
	// reference one another (and themselves) since `combined` exposes
	// every in-file FB plus the engine-supplied registry.
	for _, fbDecl := range prog.FBDecls {
		def := inFile[fbDecl.Name]
		if err := lowerFBBody(fbDecl, def, l); err != nil {
			return nil, err
		}
		l.irProg.UserFBs = append(l.irProg.UserFBs, def)
	}
	// Lower each FUNCTION body. Functions may call other user functions
	// (and user FBs) since both registries are now populated.
	for _, fd := range prog.FuncDecls {
		def := inFileFuncs[fd.Name]
		if err := lowerFuncBody(fd, def, l); err != nil {
			return nil, err
		}
		l.irProg.UserFuncs = append(l.irProg.UserFuncs, def)
	}
	return l.irProg, nil
}

// populateFBSignature resolves the slot types for a FUNCTION_BLOCK and
// writes them into def.Inputs/Outputs/InOuts/Internals plus the SlotIndex
// map. VAR_GLOBAL/VAR_EXTERNAL declarations don't contribute slots —
// they're only honored during body lowering.
//
// types is the enclosing file's resolved TYPE table (its own TYPE block
// plus the project libraries joined ahead of it), so a pin may be declared
// with a user TYPE: `VAR_INPUT IN : AnalogInput; END_VAR`.
func populateFBSignature(fbDecl *FunctionBlockDecl, def *ir.FBDef, env *lowerer) error {
	sig := env.child(&Program{Name: fbDecl.Name})
	for _, vb := range fbDecl.VarBlocks {
		if err := checkVarBlock(vb); err != nil {
			return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, err)
		}
		for _, vd := range vb.Variables {
			switch vb.Kind {
			case "VAR_GLOBAL", "VAR_EXTERNAL":
				continue
			}
			t, err := sig.resolveType(vd.Type)
			if err != nil {
				return errAt(vd.Pos, fmt.Errorf("FUNCTION_BLOCK %s VAR %s: %w", fbDecl.Name, vd.Name, err))
			}
			if err := checkTemp(vb, vd, t); err != nil {
				return err
			}
			slot := ir.FBSlot{Name: vd.Name, Type: t, Constant: vb.Constant, Temp: vb.Kind == "VAR_TEMP"}
			if vd.Initial != nil && vb.Kind != "VAR_IN_OUT" {
				// The instance starts here (ir.NewFBInstance); dropping it
				// left every initialised FB variable — a VAR CONSTANT most
				// visibly — reading zero.
				if slot.Init, err = sig.constValue(vd.Initial, t); err != nil {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION_BLOCK %s VAR %s initial: %w", fbDecl.Name, vd.Name, err))
				}
			}
			if vb.Constant {
				// A later declaration (an array bound, an initial value) may
				// name this constant.
				sig.consts[ir.NameKey(vd.Name)] = constSym{name: vd.Name, val: ir.SlotInitial(slot), typ: t, what: "VAR CONSTANT"}
			}
			switch vb.Kind {
			case "VAR_INPUT":
				def.Inputs = append(def.Inputs, slot)
			case "VAR_OUTPUT":
				def.Outputs = append(def.Outputs, slot)
			case "VAR_IN_OUT":
				if t.Kind == ir.TypeFB {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION_BLOCK %s VAR_IN_OUT %s: a function-block instance cannot be passed by reference", fbDecl.Name, vd.Name))
				}
				if vd.Initial != nil {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION_BLOCK %s VAR_IN_OUT %s: no initial value — the caller's variable supplies it", fbDecl.Name, vd.Name))
				}
				def.InOuts = append(def.InOuts, slot)
			default:
				def.Internals = append(def.Internals, slot)
			}
		}
	}
	for idx, s := range def.AllSlots() {
		if _, prev, dup := ir.Lookup(def.SlotIndex, s.Name); dup {
			return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, dupErr("slot", s.Name, prev))
		}
		def.SlotIndex[s.Name] = idx
	}
	return nil
}

// lowerFBBody compiles the FUNCTION_BLOCK body to IR statements and
// installs a Step closure on def. The body lowers against a synthetic
// Program whose VarBlocks are reordered to
// VAR_INPUT ‖ VAR_OUTPUT ‖ VAR_IN_OUT ‖ VAR so the resulting Slots match
// FBInstance.Slots (FBDef.AllSlots) index-for-index.
func lowerFBBody(fbDecl *FunctionBlockDecl, def *ir.FBDef, env *lowerer) error {
	var inputBlocks, outputBlocks, inoutBlocks, internalBlocks, globalBlocks []VarBlock
	for _, vb := range fbDecl.VarBlocks {
		switch vb.Kind {
		case "VAR_INPUT":
			inputBlocks = append(inputBlocks, vb)
		case "VAR_OUTPUT":
			outputBlocks = append(outputBlocks, vb)
		case "VAR_IN_OUT":
			inoutBlocks = append(inoutBlocks, vb)
		case "VAR_GLOBAL", "VAR_EXTERNAL":
			globalBlocks = append(globalBlocks, vb)
		default:
			internalBlocks = append(internalBlocks, vb)
		}
	}
	var blocks []VarBlock
	blocks = append(blocks, inputBlocks...)
	blocks = append(blocks, outputBlocks...)
	blocks = append(blocks, inoutBlocks...)
	blocks = append(blocks, internalBlocks...)
	blocks = append(blocks, globalBlocks...)

	bodyProg := &Program{Name: fbDecl.Name, VarBlocks: blocks, Statements: fbDecl.Statements}
	sub := env.child(bodyProg)
	if err := sub.collectVars(); err != nil {
		return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, err)
	}
	stmts, err := sub.lowerStmts(fbDecl.Statements)
	if err != nil {
		return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, err)
	}
	sub.irProg.Body = stmts
	bodyIR := sub.irProg

	// The FB's own VAR_EXTERNAL/VAR_GLOBAL block and how its body uses it —
	// tooling's only way to see what this FB type binds, since the FB has
	// no scan path of its own to observe (Program.GlobalsDeep and
	// GlobalUses walk instances to reach these; see FBDef.Globals).
	// DirectGlobalUses, not GlobalUses: an FB body may instantiate another
	// FB declared later in this same file, whose own Uses isn't populated
	// yet at this point in the lowering loop below.
	def.Globals = bodyIR.Globals
	def.Uses = bodyIR.DirectGlobalUses()
	def.Body = bodyIR

	def.Step = func(inst *ir.FBInstance, ctx ir.FBStepCtx) error {
		return ir.Run(bodyIR, inst.StepFrame(), ctx.Host)
	}
	return nil
}

// populateFuncSignature resolves a FUNCTION's input slot list and return
// type, writing them onto def so peer call sites can type-check before
// the body itself is lowered.
func populateFuncSignature(decl *FunctionDecl, def *ir.FuncDef, env *lowerer) error {
	sig := env.child(&Program{Name: decl.Name})
	retT, err := sig.resolveType(decl.ReturnType)
	if err != nil {
		return errAt(decl.Pos, fmt.Errorf("FUNCTION %s return type: %w", decl.Name, err))
	}
	def.ReturnType = retT
	for _, vb := range decl.VarBlocks {
		switch vb.Kind {
		case "VAR_OUTPUT", "VAR_IN_OUT":
			return errAt(decl.Pos, fmt.Errorf("FUNCTION %s: %s not allowed (FUNCTIONs have a single return value)", decl.Name, vb.Kind))
		case "VAR_GLOBAL", "VAR_EXTERNAL":
			continue
		}
		if err := checkVarBlock(vb); err != nil {
			return fmt.Errorf("FUNCTION %s: %w", decl.Name, err)
		}
		for _, vd := range vb.Variables {
			t, err := sig.resolveType(vd.Type)
			if err != nil {
				return errAt(vd.Pos, fmt.Errorf("FUNCTION %s VAR %s: %w", decl.Name, vd.Name, err))
			}
			if err := checkTemp(vb, vd, t); err != nil {
				return err
			}
			slot := ir.FBSlot{Name: vd.Name, Type: t, Constant: vb.Constant, Temp: vb.Kind == "VAR_TEMP"}
			if vd.Initial != nil {
				// Each call's frame starts here (ir.NewFuncFrame).
				if slot.Init, err = sig.constValue(vd.Initial, t); err != nil {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION %s VAR %s initial: %w", decl.Name, vd.Name, err))
				}
			}
			if vb.Constant {
				sig.consts[ir.NameKey(vd.Name)] = constSym{name: vd.Name, val: ir.SlotInitial(slot), typ: t, what: "VAR CONSTANT"}
			}
			switch vb.Kind {
			case "VAR_INPUT":
				if ir.SameName(vd.Name, decl.Name) {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION %s: VAR_INPUT %q shadows the return name", decl.Name, vd.Name))
				}
				def.Inputs = append(def.Inputs, slot)
			default:
				if ir.SameName(vd.Name, decl.Name) {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION %s: local %q shadows the return name", decl.Name, vd.Name))
				}
				def.Locals = append(def.Locals, slot)
			}
		}
	}
	def.ReturnSlot = len(def.Inputs) + len(def.Locals)
	def.FrameSize = def.ReturnSlot + 1
	return nil
}

// lowerFuncBody compiles a FUNCTION body to IR and installs a Run
// closure on def. The body lowers against a synthetic Program whose
// VarBlocks are reordered VAR_INPUT ‖ VAR (locals) ‖ <return slot>, so
// the per-call Frame.Slots layout matches def's FrameSize.
func lowerFuncBody(decl *FunctionDecl, def *ir.FuncDef, env *lowerer) error {
	var inputBlocks, localBlocks, globalBlocks []VarBlock
	for _, vb := range decl.VarBlocks {
		switch vb.Kind {
		case "VAR_INPUT":
			inputBlocks = append(inputBlocks, vb)
		case "VAR_GLOBAL", "VAR_EXTERNAL":
			globalBlocks = append(globalBlocks, vb)
		default:
			localBlocks = append(localBlocks, vb)
		}
	}
	// Append a synthetic VAR block carrying the return slot named after
	// the function so assignments to `Name := ...` resolve naturally.
	retBlock := VarBlock{
		Kind: "VAR",
		Variables: []VarDecl{{
			Name:     decl.Name,
			Datatype: decl.ReturnType.String(),
			Type:     decl.ReturnType,
			Pos:      decl.Pos,
		}},
	}
	var blocks []VarBlock
	blocks = append(blocks, inputBlocks...)
	blocks = append(blocks, localBlocks...)
	blocks = append(blocks, retBlock)
	blocks = append(blocks, globalBlocks...)

	bodyProg := &Program{Name: decl.Name, VarBlocks: blocks, Statements: decl.Statements}
	sub := env.child(bodyProg)
	if err := sub.collectVars(); err != nil {
		return fmt.Errorf("FUNCTION %s: %w", decl.Name, err)
	}
	stmts, err := sub.lowerStmts(decl.Statements)
	if err != nil {
		return fmt.Errorf("FUNCTION %s: %w", decl.Name, err)
	}
	sub.irProg.Body = stmts
	bodyIR := sub.irProg
	// Every local of a FUNCTION — VAR_TEMP included — is already
	// re-initialised per call by the frame (FuncDef.reinitLocals).
	bodyIR.Temps = nil
	def.Body = bodyIR
	def.Run = func(frame *ir.Frame, host ir.Host) error {
		return ir.Run(bodyIR, frame, host)
	}
	return nil
}

type lowerer struct {
	prog   *Program
	irProg *ir.Program
	// scope is keyed by ir.NameKey — identifiers are case-insensitive — and
	// each symbol carries its declared spelling; use lookup, not an index.
	scope           map[string]symbol
	types           map[string]*ir.Type
	userFBs         map[string]*ir.FBDef   // optional; consulted for FB type resolution
	userFuncs       map[string]*ir.FuncDef // optional; consulted at call sites for bare-name lookup
	implicitGlobals map[string]*ir.Type    // optional; PLC project vars surfaced as globals
	tagTypes        map[string]*ir.Type    // optional; tags whose type the project states
	untypedTags     map[string]string      // NameKey → spelling: project tags with no knowable type
	tagTypeErrs     map[string]error       // NameKey → why the tag's stated type: did not resolve
	warn            func(LowerWarning)     // optional; see LowerOpts.Warn
	peers           []PeerProgram          // optional; the project's other programs, for diagnostics only
	// returnSlot, when >= 0, marks the slot the bare function-name
	// identifier should bind to inside a FUNCTION body so `Name := value`
	// assigns the return value rather than failing as undeclared.
	returnSlot int

	// fileConsts are the constants every POU of the composed source sees:
	// its VAR_GLOBAL CONSTANT blocks (#176). consts is this POU's view:
	// fileConsts plus its own VAR CONSTANT declarations. Both are keyed by
	// ir.NameKey.
	fileConsts map[string]constSym
	consts     map[string]constSym
	// enums are the enumerated types in scope, for resolving an unqualified
	// member name (Run for Mode#Run) — see lowerEnumMember.
	enums []*ir.Type
	// hint is the type the expression being lowered is expected to have
	// (an assignment's target, the other side of a comparison, a CASE
	// selector): it settles an unqualified enumeration member that two
	// enumerations share.
	hint *ir.Type
}

// constSym is a named compile-time constant: a VAR CONSTANT or a
// VAR_GLOBAL CONSTANT declaration, with its folded value.
type constSym struct {
	name string // as declared
	val  ir.Value
	typ  *ir.Type
	what string // "VAR CONSTANT", "VAR_GLOBAL CONSTANT" — for diagnostics
}

type symbol struct {
	name   string // as declared
	slot   int    // -1 for globals
	typ    *ir.Type
	kind   ir.VarKind
	global string
	// fileScope: declared by a file-level VAR_GLOBAL (a GVL), which a
	// POU's own VAR_EXTERNAL may name again. implicit: a project tag the
	// POU did not declare — recorded in Globals on first use.
	fileScope bool
	implicit  bool
	// constant marks a VAR CONSTANT local: reads fold to its value (see
	// lowerIdent), and writing it is a compile error.
	constant bool
}

func newLowerer(prog *Program, userFBs map[string]*ir.FBDef) *lowerer {
	return &lowerer{
		prog:       prog,
		irProg:     &ir.Program{Name: prog.Name, SlotIndex: map[string]int{}, Globals: map[string]*ir.Type{}},
		scope:      map[string]symbol{},
		types:      map[string]*ir.Type{},
		userFBs:    userFBs,
		returnSlot: -1,
		fileConsts: map[string]constSym{},
		consts:     map[string]constSym{},
	}
}

// child is a lowerer for one POU of the same source file: it shares the
// file's TYPEs, enumerations, project constants and POU registries, and
// starts its own scope and its own local constants.
func (l *lowerer) child(prog *Program) *lowerer {
	c := newLowerer(prog, l.userFBs)
	c.userFuncs = l.userFuncs
	c.types = l.types
	c.enums = l.enums
	c.fileConsts = l.fileConsts
	c.warn = l.warn
	for k, v := range l.fileConsts {
		c.consts[k] = v
	}
	return c
}

// lookup resolves an identifier in the POU's scope, case-insensitively
// (IEC 61131-3: identifiers are not case-sensitive). The symbol carries
// the declared spelling.
func (l *lowerer) lookup(name string) (symbol, bool) {
	sym, ok := l.scope[ir.NameKey(name)]
	return sym, ok
}

// userFunc resolves a call's bare name to a user FUNCTION. A function
// block instance in scope with that name wins (#336): the declaration in
// scope is what the name means, as IEC scoping has it, so `f(IN := 1)`
// with `f : Counter` runs the block even when a FUNCTION F exists (names
// are case-insensitive). Only an instance hides a function: a variable of
// any other type is not callable, so a call by its name still means the
// function. `naut check` and the editor warn about the clash
// (InstancesShadowingFunctions).
func (l *lowerer) userFunc(name string) (*ir.FuncDef, bool) {
	if _, hidden := l.hiddenFunc(name); hidden {
		return nil, false
	}
	if l.userFuncs == nil {
		return nil, false
	}
	def, _, ok := ir.Lookup(l.userFuncs, name)
	return def, ok && def != nil
}

// hiddenFunc names the user FUNCTION that an FB instance in scope called
// name hides, if there is one.
func (l *lowerer) hiddenFunc(name string) (string, bool) {
	if l.userFuncs == nil {
		return "", false
	}
	sym, defined := l.lookup(name)
	if !defined || sym.typ == nil || sym.typ.Kind != ir.TypeFB {
		return "", false
	}
	if def, _, ok := ir.Lookup(l.userFuncs, name); ok && def != nil {
		return def.Name, true
	}
	return "", false
}

// systemNameErr refuses a POU named as a nautilus system function.
func systemNameErr(what, name string) error {
	return fmt.Errorf("%s %q has the name of a nautilus system function (%s()): rename it", what, name, strings.ToUpper(name))
}

// dupErr is the duplicate-declaration error. When the two declarations
// differ only in case it names both, so a project written when Nautilus
// was case-sensitive sees exactly which two names now collide.
func dupErr(what, name, prev string) error {
	if prev != "" && prev != name {
		return fmt.Errorf("duplicate %s %q: %q is already declared, and identifiers are case-insensitive", what, name, prev)
	}
	return fmt.Errorf("duplicate %s %q", what, name)
}

// mergeShadowing returns base overlaid with top, where a name in top hides
// every spelling of that name in base — the file's own POUs shadow a
// registry entry however either is cased.
func mergeShadowing[V any](base, top map[string]V) map[string]V {
	out := make(map[string]V, len(base)+len(top))
	for k, v := range base {
		if _, _, hidden := ir.Lookup(top, k); hidden {
			continue
		}
		out[k] = v
	}
	for k, v := range top {
		out[k] = v
	}
	return out
}

// ─── Type resolution ──────────────────────────────────────────────────────

// collectTypeShells registers every program-level TypeDecl by name, so
// struct fields can reference peer UDTs declared in the same TYPE block,
// and resolves the enumerations outright (they reference nothing else, and
// a project constant may be of one). collectTypes fills in the rest.
func (l *lowerer) collectTypeShells() error {
	for _, td := range l.prog.TypeDecls {
		if _, prev, dup := ir.Lookup(l.types, td.Name); dup {
			return errAt(td.Pos, dupErr("TYPE", td.Name, prev))
		}
		l.types[td.Name] = &ir.Type{
			Kind:   ir.TypeStruct,
			Struct: &ir.StructDef{Name: td.Name, FieldIndex: map[string]int{}},
		}
	}
	for _, td := range l.prog.TypeDecls {
		et, ok := td.Type.(*EnumType)
		if !ok {
			if td.Initial != nil {
				return errAt(td.Pos, fmt.Errorf("TYPE %s: an initial value on a TYPE is supported for enumerations only", td.Name))
			}
			continue
		}
		def, err := l.enumDef(td, et)
		if err != nil {
			return err
		}
		*l.types[td.Name] = ir.Type{Kind: ir.TypeInt, Enum: def}
		l.enums = append(l.enums, l.types[td.Name])
	}
	return nil
}

// enumDef resolves an enumerated TYPE (#238): each member's value (an
// explicit `:= n`, else the previous member's plus one, starting at 0), and
// the type's initial member (`:= Idle`, else the first). Member names and
// values are unique within the type.
func (l *lowerer) enumDef(td TypeDecl, et *EnumType) (*ir.EnumDef, error) {
	def := &ir.EnumDef{Name: td.Name}
	next := int64(0)
	for _, m := range et.Members {
		v := next
		if m.Value != nil {
			n, err := l.constInt(m.Value)
			if err != nil {
				return nil, errAt(m.Pos, fmt.Errorf("TYPE %s: value of %s: %w", td.Name, m.Name, err))
			}
			v = n
		}
		if prev, dup := def.Member(m.Name); dup {
			return nil, errAt(m.Pos, dupErr("enumeration member", m.Name, prev.Name))
		}
		if other := def.NameOf(v); other != "" {
			return nil, errAt(m.Pos, fmt.Errorf("TYPE %s: %s and %s both have the value %d — give each member its own value", td.Name, other, m.Name, v))
		}
		def.Members = append(def.Members, ir.EnumMember{Name: m.Name, Value: v})
		next = v + 1
	}
	if len(def.Members) > 0 {
		def.Default = def.Members[0].Value
	}
	if td.Initial != nil {
		name := ""
		switch x := td.Initial.(type) {
		case *IdentExpr:
			name = x.Name
		case *TypedLit:
			if id, ok := x.Inner.(*IdentExpr); ok && ir.SameName(x.TypeName, td.Name) {
				name = id.Name
			}
		}
		m, ok := def.Member(name)
		if !ok {
			return nil, errAt(td.Pos, fmt.Errorf("TYPE %s: the initial value must be one of its members (%s)", td.Name, memberList(def)))
		}
		def.Default = m.Value
	}
	return def, nil
}

// memberList renders an enumeration's member names for a diagnostic.
func memberList(def *ir.EnumDef) string {
	names := make([]string, len(def.Members))
	for i, m := range def.Members {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}

// collectGlobalConsts folds the file's VAR_GLOBAL CONSTANT blocks into
// fileConsts (#176): constants every POU sees, folded at compile time like
// a local VAR CONSTANT. A constant may name an earlier one.
func (l *lowerer) collectGlobalConsts() error {
	for _, vb := range l.prog.GlobalConsts {
		for _, vd := range vb.Variables {
			if err := l.declareConst(vb, vd, l.fileConsts); err != nil {
				return err
			}
			l.consts[ir.NameKey(vd.Name)] = l.fileConsts[ir.NameKey(vd.Name)]
		}
	}
	return nil
}

// declareConst resolves one constant declaration into table. The type must
// be elementary or an enumeration (a constant is a value, folded where it
// is used), and the value a constant expression.
func (l *lowerer) declareConst(vb VarBlock, vd VarDecl, table map[string]constSym) error {
	key := ir.NameKey(vd.Name)
	if prev, dup := table[key]; dup {
		return errAt(vd.Pos, dupErr("constant", vd.Name, prev.name))
	}
	t, err := l.resolveType(vd.Type)
	if err != nil {
		return errAt(vd.Pos, fmt.Errorf("%s %s: %w", vb.Kind, vd.Name, err))
	}
	switch t.Kind {
	case ir.TypeBool, ir.TypeInt, ir.TypeReal, ir.TypeTime, ir.TypeString:
	default:
		return errAt(vd.Pos, fmt.Errorf("%s CONSTANT %s: a constant must have an elementary or enumerated type, not %s", vb.Kind, vd.Name, t))
	}
	v := ir.Zero(t)
	if vd.Initial != nil {
		if v, err = l.constValue(vd.Initial, t); err != nil {
			return errAt(vd.Pos, fmt.Errorf("%s CONSTANT %s: %w", vb.Kind, vd.Name, err))
		}
	}
	table[key] = constSym{name: vd.Name, val: v, typ: t, what: vb.Kind + " CONSTANT"}
	return nil
}

// collectTypes resolves the program-level TypeDecls that collectTypeShells
// left as shells: structs (in a second pass, so fields can reference peer
// UDTs) and aliases.
func (l *lowerer) collectTypes() error {
	for _, td := range l.prog.TypeDecls {
		t := l.types[td.Name]
		switch body := td.Type.(type) {
		case *EnumType:
			continue // resolved by collectTypeShells
		case *StructType:
			for i, f := range body.Fields {
				ft, err := l.resolveType(f.Type)
				if err != nil {
					return errAt(f.Pos, fmt.Errorf("TYPE %s field %q: %w", td.Name, f.Name, err))
				}
				init, err := l.fieldInit(f, ft)
				if err != nil {
					return errAt(f.Pos, fmt.Errorf("TYPE %s field %q initial: %w", td.Name, f.Name, err))
				}
				t.Struct.Fields = append(t.Struct.Fields, ir.StructField{Name: f.Name, Type: ft, Init: init})
				t.Struct.FieldIndex[f.Name] = i
			}
		default:
			resolved, err := l.resolveType(td.Type)
			if err != nil {
				return errAt(td.Pos, fmt.Errorf("TYPE %s: %w", td.Name, err))
			}
			*t = *resolved
		}
	}
	return nil
}

// fieldInit folds a STRUCT member's declared initial value (`Temp : REAL :=
// 72.5;`) to a Value of the member's type, or returns the zero Value (no
// initial: the member takes the default of its type) when it has none.
// Parsed and then dropped, the initial value left every member at zero —
// a manifest tag of the type and a VAR of it alike (IEC 61131-3 has a
// variable of a structured type start at its members' initial values).
func (l *lowerer) fieldInit(f VarDecl, ft *ir.Type) (ir.Value, error) {
	if f.Initial == nil {
		return ir.Value{}, nil
	}
	if ft.Kind == ir.TypeFB {
		return ir.Value{}, fmt.Errorf("a function-block member takes no initial value")
	}
	v, err := l.constValue(f.Initial, ft)
	if err != nil {
		return ir.Value{}, err
	}
	// The literal path folds a literal as itself; a member's value is
	// published as its declared type, so a mismatch is an error here
	// rather than a STRING riding in a REAL member.
	if v.Kind != ft.Kind {
		return ir.Value{}, fmt.Errorf("cannot initialise a %s with a %s", ft, v.Kind)
	}
	return v, nil
}

func (l *lowerer) resolveType(te TypeExpr) (*ir.Type, error) {
	switch t := te.(type) {
	case *ScalarType:
		return resolveScalar(t.Name)
	case *NamedType:
		if udt, _, ok := ir.Lookup(l.types, t.Name); ok {
			return udt, nil
		}
		// Built-in FB types (TON, R_TRIG, CTU, …) live in the IR's
		// FB registry, not the program's TYPE block. Every VAR
		// declaration of an FB type gets a fresh *Type wrapping the
		// shared *FBDef so per-instance Zero allocates a private
		// FBInstance with its own slot vector.
		if fbT := ir.LookupFBType(t.Name); fbT != nil {
			return fbT, nil
		}
		// User-defined FBs supplied by the engine resolve here too. The
		// caller is responsible for compiling FB-only files first so
		// the registry is populated before any program references them.
		if l.userFBs != nil {
			if def, _, ok := ir.Lookup(l.userFBs, t.Name); ok && def != nil {
				return &ir.Type{Kind: ir.TypeFB, FB: def}, nil
			}
		}
		return nil, fmt.Errorf("unknown type %q", t.Name)
	case *ArrayType:
		elem, err := l.resolveType(t.Elem)
		if err != nil {
			return nil, err
		}
		// Build nested arrays innermost→outermost. `ARRAY[1..3, 1..4] OF INT`
		// lowers to ARRAY[1..3] OF (ARRAY[1..4] OF INT) so indexing a[i, j]
		// decomposes cleanly into a[i][j].
		current := elem
		for i := len(t.Dims) - 1; i >= 0; i-- {
			lo, err := l.constInt(t.Dims[i].Lo)
			if err != nil {
				return nil, fmt.Errorf("array dim lo: %w", err)
			}
			hi, err := l.constInt(t.Dims[i].Hi)
			if err != nil {
				return nil, fmt.Errorf("array dim hi: %w", err)
			}
			if hi < lo {
				return nil, fmt.Errorf("array dim hi (%d) < lo (%d)", hi, lo)
			}
			current = &ir.Type{
				Kind:       ir.TypeArray,
				Elem:       current,
				ArrLen:     int(hi - lo + 1),
				ArrLoBound: int(lo),
			}
		}
		return current, nil
	case *StructType:
		def := &ir.StructDef{FieldIndex: map[string]int{}}
		for i, f := range t.Fields {
			ft, err := l.resolveType(f.Type)
			if err != nil {
				return nil, err
			}
			init, err := l.fieldInit(f, ft)
			if err != nil {
				return nil, fmt.Errorf("STRUCT field %q initial: %w", f.Name, err)
			}
			def.Fields = append(def.Fields, ir.StructField{Name: f.Name, Type: ft, Init: init})
			def.FieldIndex[f.Name] = i
		}
		return &ir.Type{Kind: ir.TypeStruct, Struct: def}, nil
	case *EnumType:
		return nil, fmt.Errorf("declare the enumeration as a TYPE (TYPE Name : %s; END_TYPE) and use its name here", t)
	}
	return nil, fmt.Errorf("unsupported type %T", te)
}

func resolveScalar(name string) (*ir.Type, error) {
	switch strings.ToUpper(name) {
	case "BOOL":
		return ir.BoolT, nil
	case "BYTE", "SINT", "USINT", "INT", "UINT", "WORD", "DINT", "UDINT", "DWORD", "LINT", "ULINT", "LWORD":
		// One int64 at run time, but the declared name stays on the type so
		// a diagnostic says DINT, and w.%X31 knows a DINT has 32 bits.
		return ir.IntNamed(strings.ToUpper(name)), nil
	case "REAL":
		return ir.RealT, nil
	case "LREAL":
		// The same float64 as REAL, named so an LREAL into a REAL and an
		// integer into either follow IEC's table (#782).
		return ir.LRealT(), nil
	case "TIME", "LTIME":
		return ir.TimeT, nil
	case "STRING", "WSTRING", "CHAR", "WCHAR":
		return ir.StringT, nil
	}
	return nil, fmt.Errorf("unknown scalar type %q", name)
}

// ─── Constant folding for bounds & initial values ──────────────────────────

func evalConstInt(e Expression) (int64, error) {
	switch n := e.(type) {
	case *NumberLit:
		base := n.Base
		if base == 0 {
			base = 10
		}
		return strconv.ParseInt(n.Value, base, 64)
	case *UnaryExpr:
		v, err := evalConstInt(n.Operand)
		if err != nil {
			return 0, err
		}
		if n.Op == "-" {
			return -v, nil
		}
		return v, nil
	case *TypedLit:
		return evalConstInt(n.Inner)
	}
	return 0, fmt.Errorf("not a compile-time integer: %T", e)
}

func evalConstValue(e Expression, t *ir.Type) (ir.Value, error) {
	switch n := e.(type) {
	case *NumberLit:
		base := n.Base
		if base == 0 {
			base = 10
		}
		if t != nil && t.Kind == ir.TypeReal {
			v, err := strconv.ParseFloat(n.Value, 64)
			if err != nil {
				return ir.Value{}, err
			}
			return ir.RealVal(v), nil
		}
		v, err := strconv.ParseInt(n.Value, base, 64)
		if err != nil {
			return ir.Value{}, err
		}
		if t != nil && t.Kind == ir.TypeTime {
			return ir.TimeVal(v), nil
		}
		return ir.IntVal(v), nil
	case *BoolLit:
		return ir.BoolVal(n.Value), nil
	case *StringLit:
		return ir.StringVal(n.Value), nil
	case *TimeLit:
		return ir.TimeVal(int64(ParseTimeMs(n.Raw))), nil
	case *TypedLit:
		return evalConstValue(n.Inner, t)
	case *UnaryExpr:
		v, err := evalConstValue(n.Operand, t)
		if err != nil {
			return ir.Value{}, err
		}
		switch n.Op {
		case "-":
			if v.Kind == ir.TypeReal {
				return ir.RealVal(-v.F), nil
			}
			return ir.Value{Kind: v.Kind, I: -v.I}, nil
		case "NOT":
			if v.Kind == ir.TypeBool {
				return ir.BoolVal(!v.B), nil
			}
			if v.Kind == ir.TypeInt {
				return ir.Value{Kind: ir.TypeInt, I: ^v.I}, nil
			}
		}
	}
	return ir.Value{}, fmt.Errorf("initial value must be a literal constant: %T", e)
}

// constValue folds a declaration's initial value (or a constant's value)
// to a Value of type t. A literal takes the historic literal path; anything
// else — a named constant, an enumeration member, arithmetic on constants
// (`N_TANKS * 2`) — is lowered and must fold to a constant.
func (l *lowerer) constValue(e Expression, t *ir.Type) (ir.Value, error) {
	l.warnIntLiteralAsTime(e, t, "initial value")
	if t == nil || t.Enum == nil {
		if v, err := evalConstValue(e, t); err == nil {
			return v, initInRange(e, t, v)
		}
	}
	x, err := l.lowerExprHint(e, t)
	if err != nil {
		return ir.Value{}, err
	}
	if t != nil {
		if err := realRule(t, x); err != nil {
			return ir.Value{}, errNode(e, fmt.Errorf("initial value: %w", err))
		}
		x = coerce(x, t)
		if !assignable(t, x.ExprType()) {
			return ir.Value{}, errNode(e, fmt.Errorf("cannot initialise a %s with a %s", t, x.ExprType()))
		}
	}
	v, ok := ir.ConstValue(x)
	if !ok {
		return ir.Value{}, errNode(e, fmt.Errorf("initial value must be a constant: a literal, a named constant, or arithmetic on them"))
	}
	if t != nil {
		if _, err := storeRule(t, x, e); err != nil {
			return ir.Value{}, errNode(e, fmt.Errorf("initial value: %w", err))
		}
		v = ir.CoerceValue(v, t)
	}
	return v, initInRange(e, t, v)
}

// initInRange refuses an integer initial value its declared type cannot
// hold (VAR i : INT := 70000), as an assignment would be (#782).
func initInRange(e Expression, t *ir.Type, v ir.Value) error {
	if t == nil || t.Kind != ir.TypeInt || t.Enum != nil || v.Kind != ir.TypeInt {
		return nil
	}
	lo, hi, ok := ir.IntRange(t.Name)
	if ok && (v.I < lo || v.I > hi) {
		return errNode(e, fmt.Errorf("initial value %d does not fit %s (%d..%d)", v.I, t.Name, lo, hi))
	}
	return nil
}

// constInt folds an integer constant expression: an array bound, an
// enumeration value. A literal, a named constant, or arithmetic on them.
func (l *lowerer) constInt(e Expression) (int64, error) {
	if v, err := evalConstInt(e); err == nil {
		return v, nil
	}
	x, err := l.lowerExpr(e)
	if err != nil {
		return 0, err
	}
	if !x.ExprType().IsInteger() {
		return 0, fmt.Errorf("needs an integer constant, got %s", x.ExprType())
	}
	v, ok := ir.ConstValue(x)
	if !ok {
		return 0, fmt.Errorf("not a compile-time integer (a literal, a named constant, or arithmetic on them)")
	}
	return v.I, nil
}

// ─── Slot / symbol table ───────────────────────────────────────────────────

func (l *lowerer) collectVars() error {
	for _, vb := range l.prog.VarBlocks {
		if err := checkVarBlock(vb); err != nil {
			return err
		}
		kind := varKindFor(vb.Kind)
		for _, vd := range vb.Variables {
			prev, dup := l.lookup(vd.Name)
			if kind == ir.VarGlobal && vb.Constant {
				// VAR_GLOBAL CONSTANT inside a POU: a constant, not a tag.
				if dup {
					return errAt(vd.Pos, dupErr("declaration", vd.Name, prev.name))
				}
				if err := l.declareConst(vb, vd, l.consts); err != nil {
					return err
				}
				continue
			}
			t, err := l.resolveType(vd.Type)
			if err != nil {
				if dup {
					return errAt(vd.Pos, dupErr("declaration", vd.Name, prev.name))
				}
				return errAt(vd.Pos, fmt.Errorf("VAR %s: %w", vd.Name, err))
			}
			if dup {
				// A POU naming a GVL global in its own VAR_EXTERNAL is IEC's
				// explicit form of the same binding, not a second variable.
				if prev.fileScope && kind == ir.VarGlobal && !vb.FileScope {
					if !ir.SameShape(t, prev.typ) {
						return errAt(vd.Pos, fmt.Errorf("%s %s : %s disagrees with the global's declaration %s : %s in the project's VAR_GLOBAL",
							vb.Kind, vd.Name, t, prev.name, prev.typ))
					}
					continue
				}
				return errAt(vd.Pos, dupErr("declaration", vd.Name, prev.name))
			}
			if kind == ir.VarGlobal {
				if want, spelled, ok := ir.Lookup(l.tagTypes, vd.Name); ok && want != nil && !ir.SameShape(t, want) {
					//lint:ignore ST1005 the message ends with the manifest key it points at (type:)
					return errAt(vd.Pos, fmt.Errorf("%s %s : %s disagrees with the tag %s, whose type is %s — "+
						"a tag has one type; change this declaration or the tag's type:", vb.Kind, vd.Name, t, spelled, want))
				}
			}
			if err := checkTemp(vb, vd, t); err != nil {
				return err
			}
			var init ir.Value
			if vd.Initial != nil {
				init, err = l.constValue(vd.Initial, t)
				if err != nil {
					return errAt(vd.Pos, fmt.Errorf("VAR %s initial: %w", vd.Name, err))
				}
			}
			if kind == ir.VarGlobal {
				if vd.Initial != nil {
					// A tag has no slot here to start at this value; it
					// would be silently ignored, so say so instead.
					return errAt(vd.Pos, fmt.Errorf("%s %s: an initial value is not applied to a tag — give it an init: in the manifest instead", vb.Kind, vd.Name))
				}
				l.scope[ir.NameKey(vd.Name)] = symbol{name: vd.Name, slot: -1, typ: t, kind: ir.VarGlobal, global: vd.Name, fileScope: vb.FileScope}
				// A global has no slot, so Slots cannot record that the
				// program binds it; Globals is where tooling reads it.
				l.irProg.Globals[vd.Name] = t
				continue
			}
			slot := len(l.irProg.Slots)
			temp := vb.Kind == "VAR_TEMP"
			l.irProg.Slots = append(l.irProg.Slots, ir.VarSlot{
				Name:     vd.Name,
				Type:     t,
				Init:     init,
				Retained: vb.Retain,
				Constant: vb.Constant,
				Kind:     kind,
				Temp:     temp,
			})
			if temp {
				l.irProg.Temps = append(l.irProg.Temps, slot)
			}
			l.irProg.SlotIndex[vd.Name] = slot
			l.scope[ir.NameKey(vd.Name)] = symbol{name: vd.Name, slot: slot, typ: t, kind: kind, constant: vb.Constant}
			if vb.Constant {
				if init.Kind == ir.TypeVoid {
					init = ir.Zero(t)
				}
				l.consts[ir.NameKey(vd.Name)] = constSym{name: vd.Name, val: init, typ: t, what: "VAR CONSTANT"}
			}
		}
	}
	// Inject PLC project variables as implicit globals so unqualified
	// references resolve without a matching VAR_GLOBAL declaration.
	// Any name already declared explicitly (above) wins. They join
	// Globals on first use (lowerIdent), not here.
	for name, t := range l.implicitGlobals {
		if _, exists := l.lookup(name); exists {
			continue
		}
		if t == nil {
			if l.untypedTags == nil {
				l.untypedTags = map[string]string{}
			}
			l.untypedTags[ir.NameKey(name)] = name
			continue
		}
		l.scope[ir.NameKey(name)] = symbol{name: name, slot: -1, typ: l.canonical(t), kind: ir.VarGlobal, global: name, implicit: true}
	}
	return nil
}

// canonical maps a type resolved elsewhere (a tag's type:, resolved
// against a separate parse of the same TYPE declarations) onto this
// source's own resolution of it, so identity checks — a VAR_IN_OUT
// binding, a struct assignment — see one DoseRecipe, not two.
func (l *lowerer) canonical(t *ir.Type) *ir.Type {
	switch {
	case t == nil:
		return nil
	case t.Kind == ir.TypeStruct && t.Struct != nil:
		if own, _, ok := ir.Lookup(l.types, t.Struct.Name); ok && ir.SameShape(own, t) {
			return own
		}
	case t.Enum != nil:
		if own, _, ok := ir.Lookup(l.types, t.Enum.Name); ok && own.Enum != nil && ir.SameShape(own, t) {
			return own
		}
	case t.Kind == ir.TypeArray:
		if elem := l.canonical(t.Elem); elem != t.Elem {
			c := *t
			c.Elem = elem
			return &c
		}
	}
	return t
}

// checkVarBlock rejects qualifier combinations the block kind cannot carry.
func checkVarBlock(vb VarBlock) error {
	if vb.Kind == "VAR_TEMP" && vb.Retain {
		pos := Pos{}
		if len(vb.Variables) > 0 {
			pos = vb.Variables[0].Pos
		}
		return errAt(pos, fmt.Errorf("VAR_TEMP RETAIN: a temporary is re-initialised on every call, so it cannot be retained — declare it in VAR RETAIN"))
	}
	return nil
}

// checkTemp rejects a function-block instance in VAR_TEMP: the instance
// would be re-created on every call, losing the state that is the point of
// a block (IEC 61131-3 does not allow it either).
func checkTemp(vb VarBlock, vd VarDecl, t *ir.Type) error {
	if vb.Kind != "VAR_TEMP" || t == nil {
		return nil
	}
	if containsFB(t) {
		return errAt(vd.Pos, fmt.Errorf("VAR_TEMP %s: a function-block instance cannot be a temporary — it would lose its state on every call; declare it in VAR", vd.Name))
	}
	return nil
}

func containsFB(t *ir.Type) bool {
	switch t.Kind {
	case ir.TypeFB:
		return true
	case ir.TypeArray:
		return containsFB(t.Elem)
	case ir.TypeStruct:
		if t.Struct != nil {
			for _, f := range t.Struct.Fields {
				if containsFB(f.Type) {
					return true
				}
			}
		}
	}
	return false
}

func varKindFor(blockKind string) ir.VarKind {
	switch blockKind {
	case "VAR_INPUT":
		return ir.VarInput
	case "VAR_OUTPUT":
		return ir.VarOutput
	case "VAR_IN_OUT":
		return ir.VarInOut
	case "VAR_GLOBAL", "VAR_EXTERNAL":
		return ir.VarGlobal
	}
	return ir.VarLocal
}

// ─── Statement lowering ───────────────────────────────────────────────────

func (l *lowerer) lowerStmts(stmts []Statement) ([]ir.Stmt, error) {
	out := make([]ir.Stmt, 0, len(stmts))
	for _, s := range stmts {
		ls, err := l.lowerStmt(s)
		if err != nil {
			return nil, errAt(nodePos(s), err)
		}
		if ls != nil {
			out = append(out, ls)
		}
	}
	return out, nil
}

func (l *lowerer) lowerStmt(s Statement) (ir.Stmt, error) {
	switch n := s.(type) {
	case *AssignStmt:
		return l.lowerAssign(n)

	case *IfStmt:
		cond, err := l.lowerExpr(n.Condition)
		if err != nil {
			return nil, err
		}
		if cond.ExprType().Kind != ir.TypeBool {
			return nil, errNode(n.Condition, fmt.Errorf("IF condition must be BOOL, got %s", cond.ExprType()))
		}
		thenB, err := l.lowerStmts(n.Then)
		if err != nil {
			return nil, err
		}
		elseB, err := l.lowerStmts(n.Else)
		if err != nil {
			return nil, err
		}
		// Desugar ELSIF into nested IFs, building from the last clause up.
		for i := len(n.ElsIfs) - 1; i >= 0; i-- {
			ec, err := l.lowerExpr(n.ElsIfs[i].Condition)
			if err != nil {
				return nil, err
			}
			if ec.ExprType().Kind != ir.TypeBool {
				return nil, errNode(n.ElsIfs[i].Condition, fmt.Errorf("ELSIF condition must be BOOL, got %s", ec.ExprType()))
			}
			eb, err := l.lowerStmts(n.ElsIfs[i].Body)
			if err != nil {
				return nil, err
			}
			elseB = []ir.Stmt{&ir.If{Cond: ec, Then: eb, Else: elseB}}
		}
		return &ir.If{Cond: cond, Then: thenB, Else: elseB}, nil

	case *ForStmt:
		sym, ok := l.lookup(n.Variable)
		if !ok {
			return nil, fmt.Errorf("FOR: undeclared loop variable %q", n.Variable)
		}
		if sym.kind == ir.VarGlobal {
			return nil, fmt.Errorf("FOR: loop variable %q must be local, not global", n.Variable)
		}
		if sym.constant {
			return nil, fmt.Errorf("FOR: loop variable %q is a constant (VAR CONSTANT) and cannot be written", n.Variable)
		}
		if !sym.typ.IsInteger() {
			return nil, fmt.Errorf("FOR: loop variable %q must be integer, got %s", n.Variable, sym.typ)
		}
		start, err := l.lowerExpr(n.Start)
		if err != nil {
			return nil, err
		}
		end, err := l.lowerExpr(n.End)
		if err != nil {
			return nil, err
		}
		var step ir.Expr
		if n.Step != nil {
			step, err = l.lowerExpr(n.Step)
			if err != nil {
				return nil, err
			}
		}
		// The bounds and the step are stored into, or compared with, the
		// loop variable: they must fit its type (#782), or a SINT counting
		// to 200 would never stop.
		for _, b := range []struct {
			what string
			v    ir.Expr
			src  Expression
		}{{"start", start, n.Start}, {"end", end, n.End}, {"step", step, n.Step}} {
			if b.v == nil {
				continue
			}
			// An integer loop has integer bounds (IEC 61131-3): a REAL
			// end of 3.0 read as an integer is 0, so the loop silently
			// never ran (#893). storeRule judges integer stores only.
			if err := forOperandRule(sym.typ, b.v, b.src); err != nil {
				return nil, errNode(b.src, fmt.Errorf("FOR %s %s: %w", n.Variable, b.what, err))
			}
			if _, err := storeRule(sym.typ, b.v, b.src); err != nil {
				return nil, errNode(b.src, fmt.Errorf("FOR %s %s: %w", n.Variable, b.what, err))
			}
		}
		body, err := l.lowerStmts(n.Body)
		if err != nil {
			return nil, err
		}
		return &ir.For{Slot: sym.slot, Start: start, End: end, Step: step, Body: body}, nil

	case *WhileStmt:
		cond, err := l.lowerExpr(n.Condition)
		if err != nil {
			return nil, err
		}
		if cond.ExprType().Kind != ir.TypeBool {
			return nil, errNode(n.Condition, fmt.Errorf("WHILE condition must be BOOL, got %s", cond.ExprType()))
		}
		body, err := l.lowerStmts(n.Body)
		if err != nil {
			return nil, err
		}
		return &ir.While{Cond: cond, Body: body}, nil

	case *RepeatStmt:
		body, err := l.lowerStmts(n.Body)
		if err != nil {
			return nil, err
		}
		cond, err := l.lowerExpr(n.Condition)
		if err != nil {
			return nil, err
		}
		if cond.ExprType().Kind != ir.TypeBool {
			return nil, errNode(n.Condition, fmt.Errorf("UNTIL condition must be BOOL, got %s", cond.ExprType()))
		}
		return &ir.Repeat{Body: body, Cond: cond}, nil

	case *CaseStmt:
		expr, err := l.lowerExpr(n.Expression)
		if err != nil {
			return nil, err
		}
		selT := expr.ExprType()
		var seen []caseLabel
		var clauses []ir.CaseClause
		for _, c := range n.Cases {
			var vals []ir.Expr
			for _, v := range c.Values {
				lit, lab, err := l.caseLabelValue(v, selT)
				if err != nil {
					return nil, err
				}
				lab.lo, lab.hi = lit.V, lit.V
				if err := checkCaseOverlap(seen, lab); err != nil {
					return nil, errNode(v, err)
				}
				seen = append(seen, lab)
				vals = append(vals, lit)
			}
			var ranges []ir.CaseRange
			for _, r := range c.Ranges {
				lo, labLo, err := l.caseLabelValue(r.Lo, selT)
				if err != nil {
					return nil, err
				}
				hi, labHi, err := l.caseLabelValue(r.Hi, selT)
				if err != nil {
					return nil, err
				}
				lab := caseLabel{text: labLo.text + ".." + labHi.text, lo: lo.V, hi: hi.V, isRange: true, line: labLo.line}
				if err := checkCaseOverlap(seen, lab); err != nil {
					return nil, errNode(r.Lo, err)
				}
				seen = append(seen, lab)
				ranges = append(ranges, ir.CaseRange{Lo: lo, Hi: hi})
			}
			body, err := l.lowerStmts(c.Body)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, ir.CaseClause{Values: vals, Ranges: ranges, Body: body})
		}
		elseB, err := l.lowerStmts(n.Else)
		if err != nil {
			return nil, err
		}
		return &ir.Case{Expr: expr, Clauses: clauses, Else: elseB}, nil

	case *ReturnStmt:
		return &ir.Return{}, nil
	case *ExitStmt:
		return &ir.Exit{}, nil
	case *ContinueStmt:
		return &ir.Continue{}, nil
	case *CallStmt:
		return l.lowerCallStmt(n)
	}
	return nil, fmt.Errorf("unsupported statement %T", s)
}

// lowerCallStmt resolves a `name(...)` statement. The name resolves to
// either an FB instance (the legacy path) or a user FUNCTION; built-in
// stateless functions remain expression-only because they have no side
// effects worth invoking as a statement.
func (l *lowerer) lowerCallStmt(n *CallStmt) (ir.Stmt, error) {
	// User FUNCTION called as a statement — discard the return value.
	if n.Call.Callee == nil {
		if def, ok := l.userFunc(n.Call.Name); ok {
			call, x, err := splitExecControl(n.Call, funcDeclaresEN(def), false)
			if err != nil {
				return nil, err
			}
			expr, err := l.lowerUserFuncCall(call, def)
			if err != nil {
				return nil, err
			}
			if x.any() {
				return l.gate(def.Name, x, []ir.Stmt{&ir.ExprStmt{X: expr}})
			}
			return &ir.ExprStmt{X: expr}, nil
		}
	}
	var def *ir.FBDef
	var instanceSlot int
	var instance ir.LValue
	if n.Call.Callee != nil {
		// Timers[2](IN := …): an element of an array of instances.
		lv, err := l.lowerLValue(n.Call.Callee)
		if err != nil {
			return nil, fmt.Errorf("call target: %w", err)
		}
		if lv.ExprType().Kind != ir.TypeFB {
			return nil, fmt.Errorf("call target is a %s, not a function-block instance", lv.ExprType())
		}
		if g, isGlobal := rootRef(lv).(*ir.GlobalRef); isGlobal {
			return nil, fmt.Errorf("FB instance %s must be a local variable, not VAR_GLOBAL", g.Name)
		}
		def = lv.ExprType().FB
		instance = lv
	} else {
		sym, ok := l.lookup(n.Call.Name)
		if !ok {
			return nil, errName(n.Call.Pos, n.Call.Name, fmt.Errorf("call to undeclared name %q", n.Call.Name))
		}
		if sym.typ == nil || sym.typ.Kind != ir.TypeFB {
			return nil, fmt.Errorf("%q is not a function-block instance (declare e.g. `t1 : TON;`)", n.Call.Name)
		}
		if sym.kind == ir.VarGlobal {
			return nil, fmt.Errorf("FB instance %q must be a local variable, not VAR_GLOBAL", n.Call.Name)
		}
		def = sym.typ.FB
		instanceSlot = sym.slot
	}
	if len(n.Call.Args) > 0 {
		return nil, fmt.Errorf("FB call %q must use named args (IN := …, PT := …)", n.Call.Name)
	}
	// EN/ENO execution control (lower_eneno.go), unless the block declares
	// pins of those names itself.
	ownEN, ownENO := fbDeclares(def)
	callNoEN, exec, err := splitExecControl(n.Call, ownEN, ownENO)
	if err != nil {
		return nil, err
	}
	n = &CallStmt{Call: callNoEN, Pos: n.Pos}
	bindings := make([]ir.FBInput, 0, len(n.Call.NamedArgs))
	// VAR_IN_OUT bindings become a pair: an input binding that copies the
	// caller's variable in before Step, and an output binding that copies
	// the (possibly rewritten) pin back to the same lvalue after it. That is
	// observably "by reference" for scan code, and it is what makes a
	// VAR_IN_OUT bound to a VAR_EXTERNAL struct round-trip to the tag store.
	inoutBack := make([]ir.FBOutput, 0, len(def.InOuts))
	boundInOut := make([]bool, len(def.InOuts))
	for _, na := range n.Call.NamedArgs {
		idx, ok := def.SlotOf(na.Name)
		if !ok {
			return nil, errName(na.Pos, na.Name, fmt.Errorf("FB %s has no input %q", def.Name, na.Name))
		}
		if def.IsInOut(idx) {
			target, err := l.lowerInOutArg(def, na)
			if err != nil {
				return nil, err
			}
			slotT := def.Slot(idx).Type
			// An ANY in-out (READ's RD_1) takes the bound variable as it is.
			// Equal compares kinds only, so the width is compared too: an
			// INT pin cannot hold a reference to a DINT (#1382).
			if !ir.IsAny(slotT) && (!slotT.Equal(target.ExprType()) || !sameWidth(slotT, target.ExprType())) {
				return nil, errNode(na.Value, fmt.Errorf("FB %s VAR_IN_OUT %q: needs a %s variable, got %s "+
					"(an IN_OUT is passed by reference, so no conversion happens)",
					def.Name, na.Name, slotT, target.ExprType()))
			}
			nth := idx - len(def.Inputs) - len(def.Outputs)
			if boundInOut[nth] {
				return nil, fmt.Errorf("FB %s: VAR_IN_OUT %q given twice", def.Name, na.Name)
			}
			boundInOut[nth] = true
			bindings = append(bindings, ir.FBInput{SlotIdx: idx, Value: target})
			inoutBack = append(inoutBack, ir.FBOutput{SlotIdx: idx, Target: target})
			bindings = appendAnyType(bindings, def, na.Name, target.ExprType())
			continue
		}
		if !def.IsInput(idx) {
			return nil, errName(na.Pos, na.Name, fmt.Errorf("FB %s field %q is not an input", def.Name, na.Name))
		}
		if ir.IsAny(def.Inputs[idx].Type) {
			// An ANY input (WRITE's SD_1) takes the value as it is: no
			// hint, no conversion.
			v, err := l.lowerExpr(na.Value)
			if err != nil {
				return nil, fmt.Errorf("FB %s arg %q: %w", def.Name, na.Name, err)
			}
			bindings = append(bindings, ir.FBInput{SlotIdx: idx, Value: v})
			bindings = appendAnyType(bindings, def, na.Name, v.ExprType())
			continue
		}
		v, err := l.lowerExprHint(na.Value, def.Inputs[idx].Type)
		if err != nil {
			return nil, fmt.Errorf("FB %s arg %q: %w", def.Name, na.Name, err)
		}
		// A standard block's numeric pins are generic (ANY_REAL, ANY_INT:
		// a PID's PV takes a REAL or an LREAL); a user block's input is a
		// declared variable, and storing into it follows the table (#782).
		if !builtinFB(def) {
			if err := realRule(def.Inputs[idx].Type, v); err != nil {
				return nil, errNode(na.Value, fmt.Errorf("FB %s input %s: %w", def.Name, na.Name, err))
			}
		}
		v = coerce(v, def.Inputs[idx].Type)
		// An input follows a FUNCTION argument's rule, in its words (#1382:
		// a REAL or a BOOL into an INT input passed). An integer into a
		// TIME input is read as milliseconds with a warning, as before.
		if !assignable(def.Inputs[idx].Type, v.ExprType()) && !intAsTime(def.Inputs[idx].Type, v.ExprType()) {
			return nil, errNode(na.Value, fmt.Errorf("FB %s input %s: cannot pass %s as %s%s", def.Name, na.Name, v.ExprType(), def.Inputs[idx].Type,
				l.inferredTagHint(na.Value, v.ExprType(), def.Inputs[idx].Type)))
		}
		l.warnIntAsTime(na.Value, def.Inputs[idx].Type, v, fmt.Sprintf("%s input %s", def.Name, def.Inputs[idx].Name))
		if v, err = storeInput(def.Inputs[idx].Type, v, na.Value); err != nil {
			return nil, errNode(na.Value, fmt.Errorf("FB %s input %s: %w", def.Name, na.Name, err))
		}
		bindings = append(bindings, ir.FBInput{SlotIdx: idx, Value: v})
	}
	// Every VAR_IN_OUT must be bound at every call site: unlike an input it
	// has no meaningful default, and leaving it unbound would let the block
	// write into whatever the previous call left behind.
	for i, ok := range boundInOut {
		if !ok {
			return nil, fmt.Errorf("FB %s: VAR_IN_OUT %q must be bound at every call site (%s := <variable>)",
				def.Name, def.InOuts[i].Name, def.InOuts[i].Name)
		}
	}
	outputs := append([]ir.FBOutput(nil), inoutBack...)
	for _, ob := range n.Call.OutputBindings {
		idx, ok := def.SlotOf(ob.Name)
		if !ok {
			return nil, errName(ob.Pos, ob.Name, fmt.Errorf("FB %s has no member %q", def.Name, ob.Name))
		}
		if def.IsInOut(idx) {
			return nil, fmt.Errorf("FB %s field %q is a VAR_IN_OUT — bind it with %q := <variable>, "+
				"which already writes back to the caller", def.Name, ob.Name, ob.Name)
		}
		if !def.IsOutput(idx) {
			return nil, errName(ob.Pos, ob.Name, fmt.Errorf("FB %s field %q is not an output (=> binds outputs only)", def.Name, ob.Name))
		}
		target, err := l.lowerLValue(ob.Target)
		if err != nil {
			return nil, fmt.Errorf("FB %s output %q target: %w", def.Name, ob.Name, err)
		}
		// out => o stores the output into o: the same rules, in the same
		// order and words, as o := fb.out (#782, #1378: a REAL output into
		// an INT is refused, not stored as a REAL). The output already
		// holds its own type's width.
		if ot := def.OutputType(idx); ot != nil {
			err := realRule(target.ExprType(), &ir.Lit{T: ot})
			if err == nil && !assignable(target.ExprType(), ot) {
				err = fmt.Errorf("cannot assign %s to %s", ot, target.ExprType())
			}
			if err == nil {
				_, err = storeRule(target.ExprType(), &ir.Lit{T: ot}, nil)
			}
			if err != nil {
				return nil, errName(ob.Pos, ob.Name, fmt.Errorf("FB %s output %s => %s: %w", def.Name, ob.Name, exprShort(ob.Target), err))
			}
		}
		outputs = append(outputs, ir.FBOutput{SlotIdx: idx, Target: target})
	}
	call := &ir.FBCall{InstanceSlot: instanceSlot, Instance: instance, Def: def, Inputs: bindings, Outputs: outputs}
	if exec.any() {
		return l.gate(n.Call.Name, exec, []ir.Stmt{call})
	}
	return call, nil
}

// appendAnyType binds an ANY pin's hidden type slot (ir.AnyTypeSlot), when
// the block declares one, to the declared elementary type of what the call
// binds to the pin: a Value does not carry an integer's width, and a READ
// or WRITE channel must tell an INT from a DINT.
func appendAnyType(bindings []ir.FBInput, def *ir.FBDef, pin string, t *ir.Type) []ir.FBInput {
	idx, ok := def.SlotOf(ir.AnyTypeSlot(strings.ToUpper(pin)))
	if !ok {
		return bindings
	}
	return append(bindings, ir.FBInput{SlotIdx: idx, Value: &ir.Lit{V: ir.StringVal(ir.ElemTypeName(t)), T: ir.StringT}})
}

// rootRef follows an lvalue to the slot or global it addresses.
func rootRef(lv ir.LValue) ir.LValue {
	for {
		switch n := lv.(type) {
		case *ir.IndexRef:
			inner, ok := n.Array.(ir.LValue)
			if !ok {
				return lv
			}
			lv = inner
		case *ir.MemberRef:
			inner, ok := n.Object.(ir.LValue)
			if !ok {
				return lv
			}
			lv = inner
		case *ir.BitRef:
			lv = n.Object
		default:
			return lv
		}
	}
}

// lowerInOutArg lowers the argument bound to a VAR_IN_OUT pin. The pin is
// written back to the caller after Step, so the argument has to name
// storage the caller can see: a variable, a struct field, an array element,
// or a VAR_EXTERNAL tag. An expression has nowhere to write back to, and
// that is a compile error rather than a silently discarded result.
func (l *lowerer) lowerInOutArg(def *ir.FBDef, na NamedArg) (ir.LValue, error) {
	lowered, err := l.lowerExpr(na.Value)
	if err != nil {
		return nil, fmt.Errorf("FB %s VAR_IN_OUT %q: %w", def.Name, na.Name, err)
	}
	lv, ok := lowered.(ir.LValue)
	if !ok {
		return nil, fmt.Errorf("FB %s VAR_IN_OUT %q needs a variable to write back to, "+
			"got an expression — pass a variable, struct field, array element, or VAR_EXTERNAL tag",
			def.Name, na.Name)
	}
	return lv, nil
}

func (l *lowerer) lowerAssign(a *AssignStmt) (ir.Stmt, error) {
	if s, gated, err := l.lowerGatedAssign(a); gated {
		return s, err
	}
	if a.TargetExpr == nil {
		return nil, fmt.Errorf("assignment has no structured target (parser bug)")
	}
	target, err := l.lowerLValue(a.TargetExpr)
	if err != nil {
		return nil, err
	}
	value, err := l.lowerExprHint(a.Value, target.ExprType())
	if err != nil {
		return nil, err
	}
	if err := realRule(target.ExprType(), value); err != nil {
		return nil, err
	}
	value = coerce(value, target.ExprType())
	if !assignable(target.ExprType(), value.ExprType()) {
		return nil, fmt.Errorf("cannot assign %s to %s", value.ExprType(), target.ExprType())
	}
	wrap, err := storeRule(target.ExprType(), value, a.Value)
	if err != nil {
		return nil, err
	}
	if _, isLit := value.(*ir.Lit); wrap == nil && !isLit {
		// A same-type copy is cut too: a value from outside the program's
		// own stores (a force, a driver) may hold more than its width, and
		// the next assignment of it keeps the width (#782). Inline, a mask.
		wrap = ir.NewWrap(target.ExprType())
	}
	return &ir.Assign{Target: target, Value: value, Wrap: wrap}, nil
}

// storeRule applies the integer store rules (#782) to a value stored into
// a variable of declared type t, at every place a program stores one (an
// assignment, an FB or function input, an output binding, a FOR variable,
// a declaration's initial value). IEC 61131-3 converts implicitly only as
// its Figure 11 allows (ir.ImplicitConversion): within a family and
// wider, or unsigned into a wider signed type. Anything else is refused
// with the conversion to write, which wraps. A constant must fit t's range.
// What is left is stored at t's width: arithmetic never wraps on its own
// (#326), so INT + INT = 60000 stores -5536 in an INT, as on a PLC. The
// returned Wrap is that width, nil when the value needs no cut.
func storeRule(t *ir.Type, value ir.Expr, src Expression) (*ir.Wrap, error) {
	vt := value.ExprType()
	if t == nil || vt == nil || t.Kind != ir.TypeInt || t.Enum != nil || !vt.IsInteger() || vt.Enum != nil || t.Name == "" {
		return nil, nil
	}
	if vt.Name != "" && vt.Name != t.Name && !ir.ImplicitConversion(vt.Name, t.Name) {
		return nil, fmt.Errorf("cannot assign %s to %s implicitly; write %s_TO_%s(…)", vt.Name, t.Name, vt.Name, t.Name)
	}
	lo, hi, ranged := ir.IntRange(t.Name)
	if !ranged {
		return nil, nil // 64 bits: whatever the VM holds
	}
	// A constant, literal or folded (30000 + 30000, a named constant),
	// must fit: it is refused, never wrapped.
	c, isConst := int64(0), false
	if lit, ok := value.(*ir.Lit); ok {
		c, isConst = lit.V.I, true
	} else if v, ok := ir.ConstValue(value); ok && v.Kind == ir.TypeInt {
		c, isConst = v.I, true
	} else if src != nil {
		if v, err := evalConstInt(src); err == nil {
			c, isConst = v, true
		}
	}
	if isConst {
		if c < lo || c > hi {
			return nil, fmt.Errorf("cannot assign %d to %s: it holds %d..%d; write a value in range, or %s(%d) to keep its low bits", c, t.Name, lo, hi, convName(vt, t), c)
		}
		return nil, nil
	}
	if vt.Name == t.Name && !vt.Computed {
		return nil, nil // a declared value of the target's own type
	}
	if vt.Name != "" && !vt.Computed {
		return nil, nil // an implicit widening: it fits
	}
	return ir.NewWrap(t), nil
}

// forOperandRule refuses a FOR start, end or step that is not an integer,
// naming the conversion to write into the loop variable's type t. A value
// of no known type is left to the VM, as before.
func forOperandRule(t *ir.Type, v ir.Expr, src Expression) error {
	vt := v.ExprType()
	if vt == nil || vt.IsInteger() {
		return nil
	}
	from := vt.Name
	switch {
	case vt.Enum != nil:
		from = vt.Enum.Name
	case vt.Kind == ir.TypeReal && from != "LREAL":
		from = "REAL"
	case from == "" && vt.Struct != nil && vt.Struct.Name != "":
		from = vt.Struct.Name
	case from == "":
		from = vt.Kind.String()
	}
	if from == "" || from == "VOID" {
		return nil
	}
	to := t.Name
	if to == "" {
		to = "DINT"
	}
	what, arg := "the value", "…"
	if id, ok := src.(*IdentExpr); ok {
		what, arg = id.Name, id.Name
	}
	msg := fmt.Sprintf("%s is %s, and a FOR's bounds and step are integers like its variable", what, from)
	// Name the conversion only when there is one (there is no
	// STRUCT_TO_DINT).
	if conv := from + "_TO_" + to; vt.Enum != nil {
		// An enumeration converts with the generic TO_ form only.
		msg += fmt.Sprintf("; write TO_%s(%s)", to, arg)
	} else if ir.IsConversion(conv) {
		msg += fmt.Sprintf("; write %s(%s)", conv, arg)
	}
	return errors.New(msg)
}

// realRule is IEC 61131-3's table for a store into a REAL or an LREAL
// (#782, held for James's call): an integer goes into a REAL only when
// REAL's 24-bit mantissa holds it (SINT, INT, USINT, UINT), into an LREAL
// up to the 32-bit types, and an LREAL never into a REAL; anything else
// names the conversion to write. A value with no declared width (a
// literal, an untyped result) converts as before.
func realRule(t *ir.Type, v ir.Expr) error {
	vt := v.ExprType()
	if t == nil || vt == nil || t.Kind != ir.TypeReal {
		return nil
	}
	to := "REAL"
	if t.Name == "LREAL" {
		to = "LREAL"
	}
	from := vt.Name
	if vt.Kind == ir.TypeReal {
		if from != "LREAL" {
			from = "REAL"
		}
	} else if !vt.IsInteger() || vt.Enum != nil || from == "" {
		return nil
	}
	if from == to || ir.ImplicitRealConversion(from, to) {
		return nil
	}
	return fmt.Errorf("cannot assign %s to %s implicitly; write %s_TO_%s(…)", from, to, from, to)
}

// builtinFB reports a standard function block (TON, CTU, PID, ...), whose
// definition is the one registered in ir.FBs.
func builtinFB(def *ir.FBDef) bool {
	d, _, ok := ir.Lookup(ir.FBs, def.Name)
	return ok && d == def
}

// storeInput is storeRule for a value bound to an FB or FUNCTION input:
// refused as an assignment would be, and wrapped (ir.StoreWrap) when it
// needs the input's width.
func storeInput(t *ir.Type, v ir.Expr, src Expression) (ir.Expr, error) {
	w, err := storeRule(t, v, src)
	if err != nil {
		return nil, err
	}
	if w != nil {
		return ir.StoreWrap(v, t), nil
	}
	return v, nil
}

// convName is the conversion a refusal suggests: DINT_TO_INT, or the
// LINT_TO_ form for an untyped literal.
func convName(vt, t *ir.Type) string {
	if vt.Name == "" {
		return "LINT_TO_" + t.Name
	}
	return vt.Name + "_TO_" + t.Name
}

// ─── Expression lowering ──────────────────────────────────────────────────

func (l *lowerer) lowerExpr(e Expression) (ir.Expr, error) {
	switch n := e.(type) {
	case *NumberLit:
		return lowerNumberLit(n)
	case *BoolLit:
		return &ir.Lit{V: ir.BoolVal(n.Value), T: ir.BoolT}, nil
	case *StringLit:
		return &ir.Lit{V: ir.StringVal(n.Value), T: ir.StringT}, nil
	case *TimeLit:
		return &ir.Lit{V: ir.TimeVal(int64(ParseTimeMs(n.Raw))), T: ir.TimeT}, nil
	case *TypedLit:
		if id, ok := n.Inner.(*IdentExpr); ok {
			return l.lowerEnumLiteral(n, id)
		}
		x, err := l.lowerExpr(n.Inner)
		if err != nil {
			return nil, err
		}
		// BYTE#1 is a BYTE: an integer literal takes its prefix's type
		// (#782), so a shift of it acts on 8 bits and its store follows
		// the IEC table.
		if lit, ok := x.(*ir.Lit); ok && lit.V.Kind == ir.TypeInt {
			if t := ir.IntNamed(strings.ToUpper(n.TypeName)); t != nil {
				if lo, hi, ranged := ir.IntRange(t.Name); ranged && (lit.V.I < lo || lit.V.I > hi) {
					return nil, errNode(n, fmt.Errorf("%s#%d does not fit %s (%d..%d)", t.Name, lit.V.I, t.Name, lo, hi))
				}
				return &ir.Lit{V: lit.V, T: t}, nil
			}
		}
		return x, nil
	case *IdentExpr:
		return l.lowerIdent(n)
	case *MemberExpr:
		return l.lowerMember(n)
	case *IndexExpr:
		return l.lowerIndex(n)
	case *BinaryExpr:
		return l.lowerBinary(n)
	case *UnaryExpr:
		return l.lowerUnary(n)
	case *CallExpr:
		return l.lowerCallExpr(n)
	}
	return nil, fmt.Errorf("unsupported expression %T", e)
}

func (l *lowerer) lowerCallExpr(n *CallExpr) (ir.Expr, error) {
	// User-defined FUNCTION dispatch — preferred over the (case-insensitive)
	// builtin table so a user can shadow nothing accidentally with mixed
	// case while still binding by bare name.
	if def, ok := l.userFunc(n.Name); ok {
		if !funcDeclaresEN(def) && hasExecControl(n) {
			return nil, errExecInExpr(n)
		}
		return l.lowerUserFuncCall(n, def)
	}
	if conv, handled, err := l.lowerEnumConversion(n); handled {
		return conv, err
	}
	sig, ok := ir.Builtins[strings.ToUpper(n.Name)]
	if !ok {
		// FB instance "calls" inside expressions are illegal — outputs
		// are read via member access (t1.Q), and bare `t1(...)` produces
		// no value. Surface a clearer message when this is the case.
		if sym, defined := l.lookup(n.Name); defined && sym.typ != nil && sym.typ.Kind == ir.TypeFB {
			hides := ""
			if fn, ok := l.hiddenFunc(n.Name); ok {
				hides = fmt.Sprintf(" (it hides FUNCTION %s of the same name — rename the instance to call the function)", fn)
			}
			return nil, errName(n.Pos, n.Name, fmt.Errorf("FB instance %q can't be used as an expression — invoke it as a statement and read outputs (e.g. %s.Q)%s", n.Name, n.Name, hides))
		}
		return nil, errName(n.Pos, n.Name, fmt.Errorf("unknown function %q%s", n.Name, ir.ConversionHint(n.Name)))
	}
	if hasExecControl(n) {
		return nil, errExecInExpr(n)
	}
	for _, ob := range n.OutputBindings {
		return nil, errName(ob.Pos, ob.Name, fmt.Errorf("function %s has no output %q to bind", sig.Name, ob.Name))
	}
	srcArgs, err := lowerFormalBuiltinArgs(n, sig)
	if err != nil {
		return nil, err
	}
	args := make([]ir.Expr, 0, len(srcArgs))
	argTypes := make([]*ir.Type, 0, len(srcArgs))
	for _, a := range srcArgs {
		la, err := l.lowerExpr(a)
		if err != nil {
			return nil, fmt.Errorf("function %s arg: %w", sig.Name, err)
		}
		args = append(args, la)
		argTypes = append(argTypes, la.ExprType())
	}
	if !sig.Variadic && len(args) != len(sig.Params) {
		return nil, fmt.Errorf("function %s expects %d argument(s), got %d", sig.Name, len(sig.Params), len(args))
	}
	if sig.Variadic && len(args) < len(sig.Params) {
		return nil, fmt.Errorf("function %s expects at least %d argument(s), got %d", sig.Name, len(sig.Params), len(args))
	}
	resultT, fn := sig.Result, sig.Fn
	switch {
	case sig.Resolve != nil:
		t, f, err := sig.Resolve(argTypes)
		if err != nil {
			return nil, errNode(srcArgs[0], err)
		}
		resultT, fn = t, f
	case sig.Coerce != nil:
		t, err := sig.Coerce(argTypes)
		if err != nil {
			return nil, err
		}
		resultT = t
	case resultT != nil && resultT.Kind == ir.TypeReal && resultT.Name == "" && !ir.IsConversion(sig.Name):
		// SQRT, SIN, EXPT, ...: an LREAL argument (or a DINT one) gives an
		// LREAL, so the result keeps its width into a store (#782).
		t, err := ir.RealResult(argTypes)
		if err != nil {
			return nil, errNode(srcArgs[0], fmt.Errorf("function %s: %w", sig.Name, err))
		}
		resultT = t
	}
	for i, p := range sig.Params {
		if p == nil || i >= len(args) {
			continue
		}
		args[i] = coerce(args[i], p)
		if !assignable(p, args[i].ExprType()) {
			return nil, errNode(srcArgs[i], fmt.Errorf("function %s arg %d: cannot pass %s as %s", sig.Name, i+1, args[i].ExprType(), p))
		}
	}
	return &ir.Call{Name: sig.Name, Args: args, Fn: fn, HostFn: sig.HostFn, T: resultT}, nil
}

// lowerUserFuncCall resolves a CallExpr against a user-defined FUNCTION.
// IEC 61131-3 allows both positional and named-argument forms for
// FUNCTION calls; named args resolve by input slot name.
func (l *lowerer) lowerUserFuncCall(n *CallExpr, def *ir.FuncDef) (ir.Expr, error) {
	if len(n.Args) > 0 && len(n.NamedArgs) > 0 {
		return nil, fmt.Errorf("FUNCTION %s: cannot mix positional and named arguments", def.Name)
	}
	expected := len(def.Inputs)
	bound := make([]ir.Expr, expected)
	have := make([]bool, expected)
	if len(n.NamedArgs) > 0 {
		for _, na := range n.NamedArgs {
			idx := -1
			for i, in := range def.Inputs {
				if ir.SameName(in.Name, na.Name) {
					idx = i
					break
				}
			}
			if idx < 0 {
				return nil, errName(na.Pos, na.Name, fmt.Errorf("FUNCTION %s has no input %q", def.Name, na.Name))
			}
			if have[idx] {
				return nil, fmt.Errorf("FUNCTION %s: input %q given twice", def.Name, na.Name)
			}
			v, err := l.lowerExprHint(na.Value, def.Inputs[idx].Type)
			if err != nil {
				return nil, fmt.Errorf("FUNCTION %s arg %s: %w", def.Name, na.Name, err)
			}
			if err := realRule(def.Inputs[idx].Type, v); err != nil {
				return nil, errNode(na.Value, fmt.Errorf("FUNCTION %s arg %s: %w", def.Name, na.Name, err))
			}
			v = coerce(v, def.Inputs[idx].Type)
			if !assignable(def.Inputs[idx].Type, v.ExprType()) {
				return nil, errNode(na.Value, fmt.Errorf("FUNCTION %s arg %s: cannot pass %s as %s", def.Name, na.Name, v.ExprType(), def.Inputs[idx].Type))
			}
			if v, err = storeInput(def.Inputs[idx].Type, v, na.Value); err != nil {
				return nil, errNode(na.Value, fmt.Errorf("FUNCTION %s arg %s: %w", def.Name, na.Name, err))
			}
			bound[idx] = v
			have[idx] = true
		}
		for i, ok := range have {
			if !ok {
				return nil, fmt.Errorf("FUNCTION %s: missing input %q", def.Name, def.Inputs[i].Name)
			}
		}
	} else {
		if len(n.Args) != expected {
			return nil, fmt.Errorf("FUNCTION %s expects %d argument(s), got %d", def.Name, expected, len(n.Args))
		}
		for i, a := range n.Args {
			v, err := l.lowerExprHint(a, def.Inputs[i].Type)
			if err != nil {
				return nil, fmt.Errorf("FUNCTION %s arg %d: %w", def.Name, i+1, err)
			}
			if err := realRule(def.Inputs[i].Type, v); err != nil {
				return nil, errNode(a, fmt.Errorf("FUNCTION %s arg %d: %w", def.Name, i+1, err))
			}
			v = coerce(v, def.Inputs[i].Type)
			if !assignable(def.Inputs[i].Type, v.ExprType()) {
				return nil, errNode(a, fmt.Errorf("FUNCTION %s arg %d: cannot pass %s as %s", def.Name, i+1, v.ExprType(), def.Inputs[i].Type))
			}
			if v, err = storeInput(def.Inputs[i].Type, v, a); err != nil {
				return nil, errNode(a, fmt.Errorf("FUNCTION %s arg %d: %w", def.Name, i+1, err))
			}
			bound[i] = v
		}
	}
	return &ir.UserCall{Def: def, Args: bound, T: def.ReturnType}, nil
}

func lowerNumberLit(n *NumberLit) (ir.Expr, error) {
	base := n.Base
	if base == 0 {
		base = 10
	}
	if base == 10 && strings.ContainsAny(n.Value, ".eE") {
		v, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return nil, err
		}
		return &ir.Lit{V: ir.RealVal(v), T: ir.RealT}, nil
	}
	v, err := strconv.ParseInt(n.Value, base, 64)
	if err != nil {
		return nil, err
	}
	return &ir.Lit{V: ir.IntVal(v), T: ir.IntT}, nil
}

func (l *lowerer) lowerIdent(n *IdentExpr) (ir.Expr, error) {
	name := n.Name
	sym, ok := l.lookup(name)
	if ok && sym.constant {
		// A VAR CONSTANT folds to its value; an aggregate one (no literal
		// form) is still read from its slot.
		if c, isConst := l.consts[ir.NameKey(name)]; isConst && isScalarKind(c.typ) {
			return &ir.Lit{V: c.val, T: c.typ}, nil
		}
	}
	if !ok {
		if c, isConst := l.consts[ir.NameKey(name)]; isConst {
			return &ir.Lit{V: c.val, T: c.typ}, nil
		}
		if lit, found, err := l.lowerEnumMember(n); found || err != nil {
			return lit, err
		}
		if name == "_" {
			// The diagram editors' placeholder: an open FBD pin, a ladder
			// coil or contact not yet named ("+ rung" writes `( _ )`).
			return nil, errName(n.Pos, name, fmt.Errorf("unfilled placeholder `_`: connect this pin or name its variable"))
		}
		if tag, isTag := l.untypedTags[ir.NameKey(name)]; isTag {
			if why := l.tagTypeErrs[ir.NameKey(name)]; why != nil {
				return nil, errName(n.Pos, name, fmt.Errorf("tag %s: %w", tag, why))
			}
			return nil, errName(n.Pos, name, fmt.Errorf("%q is a project tag with no type — give %s a type: (or an init:) in the manifest, or declare it in VAR_EXTERNAL", name, tag))
		}
		if hint := l.peerHint(name); hint != "" {
			return nil, errName(n.Pos, name, fmt.Errorf("undeclared identifier %q: %s", name, hint))
		}
		return nil, errName(n.Pos, name, fmt.Errorf("undeclared identifier %q (declare in VAR_* or VAR_GLOBAL block)", name))
	}
	if sym.kind == ir.VarGlobal {
		if sym.implicit {
			l.irProg.Globals[sym.global] = sym.typ
		}
		return &ir.GlobalRef{Name: sym.global, T: sym.typ}, nil
	}
	return &ir.SlotRef{Slot: sym.slot, T: sym.typ}, nil
}

func (l *lowerer) lowerMember(m *MemberExpr) (ir.Expr, error) {
	obj, err := l.lowerExpr(m.Object)
	if err != nil {
		return nil, err
	}
	ot := obj.ExprType()
	if isBitMember(m.Member) || isPartialMember(m.Member) {
		return l.lowerPartial(m, obj)
	}
	switch ot.Kind {
	case ir.TypeStruct:
		idx, ok := ot.Struct.FieldOf(m.Member)
		if !ok {
			label := ot.Struct.Name
			if label == "" {
				label = "STRUCT"
			}
			return nil, errName(m.MemberPos, m.Member, fmt.Errorf("field %q not found on %s", m.Member, label))
		}
		return &ir.MemberRef{Object: obj, FieldIdx: idx, T: ot.Struct.Fields[idx].Type}, nil
	case ir.TypeFB:
		idx, ok := ot.FB.SlotOf(m.Member)
		if !ok {
			return nil, errName(m.MemberPos, m.Member, fmt.Errorf("FB %s has no field %q", ot.FB.Name, m.Member))
		}
		all := ot.FB.AllSlots()
		return &ir.MemberRef{Object: obj, FieldIdx: idx, T: all[idx].Type}, nil
	}
	return nil, errName(m.MemberPos, m.Member, fmt.Errorf("member access on non-struct type %s (%s.%s)", ot, exprLabel(m.Object), m.Member))
}

func (l *lowerer) lowerIndex(n *IndexExpr) (ir.Expr, error) {
	arr, err := l.lowerExpr(n.Array)
	if err != nil {
		return nil, err
	}
	cur := arr
	curT := arr.ExprType()
	for _, idxExpr := range n.Indices {
		if curT.Kind != ir.TypeArray {
			return nil, fmt.Errorf("indexing non-array type %s", curT)
		}
		idx, err := l.lowerExpr(idxExpr)
		if err != nil {
			return nil, err
		}
		if !idx.ExprType().IsInteger() {
			return nil, errNode(idxExpr, fmt.Errorf("array index must be integer, got %s", idx.ExprType()))
		}
		zero := ir.Expr(idx)
		if curT.ArrLoBound != 0 {
			zero = &ir.BinOp{
				Op: ir.OpSub,
				L:  idx,
				R:  &ir.Lit{V: ir.IntVal(int64(curT.ArrLoBound)), T: ir.IntT},
				T:  ir.IntT,
			}
		}
		cur = &ir.IndexRef{Array: cur, Index: zero, T: curT.Elem}
		curT = curT.Elem
	}
	return cur, nil
}

func (l *lowerer) lowerLValue(e Expression) (ir.LValue, error) {
	if err := l.checkNotConst(e); err != nil {
		return nil, err
	}
	lowered, err := l.lowerExpr(e)
	if err != nil {
		return nil, err
	}
	lv, ok := lowered.(ir.LValue)
	if !ok {
		return nil, fmt.Errorf("expression is not assignable: %T", e)
	}
	return lv, nil
}

func (l *lowerer) lowerBinary(b *BinaryExpr) (ir.Expr, error) {
	// Each side is the other's hint, so `m = Idle` settles an unqualified
	// member two enumerations share. Lower the left first; if it is the
	// ambiguous one, lower the right first and come back.
	left, err := l.lowerExpr(b.Left)
	var right ir.Expr
	if err != nil {
		var amb *ambiguousMemberError
		if !errors.As(err, &amb) {
			return nil, err
		}
		if right, err = l.lowerExpr(b.Right); err != nil {
			return nil, err
		}
		if left, err = l.lowerExprHint(b.Left, right.ExprType()); err != nil {
			return nil, err
		}
	} else if right, err = l.lowerExprHint(b.Right, left.ExprType()); err != nil {
		return nil, err
	}
	op, err := mapBinOp(b.Op)
	if err != nil {
		return nil, err
	}
	resultT, err := resolveBinType(op, left.ExprType(), right.ExprType())
	if err != nil {
		return nil, errNode(b, fmt.Errorf("operator %s on %s and %s: %w", b.Op, left.ExprType(), right.ExprType(), err))
	}
	if resultT.Kind == ir.TypeReal {
		if left.ExprType().Kind == ir.TypeInt {
			left = intToReal(left)
		}
		if right.ExprType().Kind == ir.TypeInt {
			right = intToReal(right)
		}
	}
	return &ir.BinOp{Op: op, L: left, R: right, T: resultT}, nil
}

func (l *lowerer) lowerUnary(u *UnaryExpr) (ir.Expr, error) {
	x, err := l.lowerExpr(u.Operand)
	if err != nil {
		return nil, err
	}
	switch u.Op {
	case "-":
		if !x.ExprType().IsNumeric() {
			return nil, errNode(u.Operand, fmt.Errorf("unary - on non-numeric %s", x.ExprType()))
		}
		// Computed: -x of an INT at -32768 is 32768, which its store cuts
		// back to the INT's width (#782).
		return &ir.UnOp{Op: ir.OpNeg, X: x, T: ir.Computed(x.ExprType())}, nil
	case "NOT":
		// Logical on BOOL, bitwise complement (of the 64-bit value) on INT.
		switch {
		case x.ExprType().Kind == ir.TypeBool:
			return &ir.UnOp{Op: ir.OpNot, X: x, T: ir.BoolT}, nil
		case x.ExprType().IsInteger():
			return &ir.UnOp{Op: ir.OpNot, X: x, T: ir.IntT}, nil
		}
		return nil, errNode(u.Operand, fmt.Errorf("NOT requires a BOOL or INT operand, got %s", x.ExprType()))
	}
	return nil, fmt.Errorf("unknown unary operator %q", u.Op)
}

// ─── Type checking helpers ────────────────────────────────────────────────

func mapBinOp(op string) (ir.BinKind, error) {
	switch op {
	case "+":
		return ir.OpAdd, nil
	case "-":
		return ir.OpSub, nil
	case "*":
		return ir.OpMul, nil
	case "/":
		return ir.OpDiv, nil
	case "MOD":
		return ir.OpMod, nil
	case "=":
		return ir.OpEq, nil
	case "<>":
		return ir.OpNeq, nil
	case "<":
		return ir.OpLt, nil
	case "<=":
		return ir.OpLte, nil
	case ">":
		return ir.OpGt, nil
	case ">=":
		return ir.OpGte, nil
	case "AND":
		return ir.OpAnd, nil
	case "OR":
		return ir.OpOr, nil
	case "XOR":
		return ir.OpXor, nil
	}
	return 0, fmt.Errorf("unknown operator %q", op)
}

// bitwiseType is the type of AND, OR or XOR on two integers: the
// arithmetic common type within one family or with an untyped operand
// (WORD AND 16#FF is a WORD), so a shift or a store of it keeps the
// width (#782); a bit string with a number is untyped, as before.
func bitwiseType(lt, rt *ir.Type) *ir.Type {
	if lt.Name != "" && rt.Name != "" && ir.IsBitString(lt.Name) != ir.IsBitString(rt.Name) {
		return ir.IntT
	}
	return ir.CommonInt(lt, rt)
}

func resolveBinType(op ir.BinKind, lt, rt *ir.Type) (*ir.Type, error) {
	if lt.Enum != nil || rt.Enum != nil {
		return resolveEnumBinType(op, lt, rt)
	}
	switch op {
	case ir.OpAdd, ir.OpSub, ir.OpMul, ir.OpDiv, ir.OpMod:
		if !lt.IsNumeric() || !rt.IsNumeric() {
			return nil, fmt.Errorf("arithmetic requires numeric operands")
		}
		if lt.Kind == ir.TypeReal || rt.Kind == ir.TypeReal {
			return ir.RealResult([]*ir.Type{lt, rt})
		}
		if lt.Kind == ir.TypeTime && rt.Kind == ir.TypeTime {
			return ir.TimeT, nil
		}
		if lt.Kind == ir.TypeTime || rt.Kind == ir.TypeTime {
			return nil, fmt.Errorf("TIME may only be combined with TIME")
		}
		// The operands' common type: DINT + DINT is DINT (#326).
		return ir.CommonInt(lt, rt), nil
	case ir.OpEq, ir.OpNeq, ir.OpLt, ir.OpLte, ir.OpGt, ir.OpGte:
		if lt.Kind == ir.TypeBool && rt.Kind == ir.TypeBool && (op == ir.OpEq || op == ir.OpNeq) {
			return ir.BoolT, nil
		}
		if lt.Kind == ir.TypeString && rt.Kind == ir.TypeString && (op == ir.OpEq || op == ir.OpNeq) {
			return ir.BoolT, nil
		}
		if !lt.IsNumeric() || !rt.IsNumeric() {
			return nil, fmt.Errorf("comparison requires numeric operands (or matching BOOL/STRING for =/<>)")
		}
		return ir.BoolT, nil
	case ir.OpAnd, ir.OpOr:
		if lt.Kind == ir.TypeBool && rt.Kind == ir.TypeBool {
			return ir.BoolT, nil
		}
		if lt.IsInteger() && rt.IsInteger() {
			return bitwiseType(lt, rt), nil
		}
		return nil, fmt.Errorf("logical op requires BOOL or INT operands")
	case ir.OpXor:
		if lt.Kind == ir.TypeBool && rt.Kind == ir.TypeBool {
			return ir.BoolT, nil
		}
		if lt.IsInteger() && rt.IsInteger() {
			return bitwiseType(lt, rt), nil
		}
		return nil, fmt.Errorf("XOR requires BOOL or INT operands")
	}
	return nil, fmt.Errorf("internal: unhandled operator")
}

// intToReal promotes an INT expression to REAL. Literals fold directly;
// non-literal ints ride through the VM's asFloat() helper because BinOp
// already coerces on mixed kinds. A dedicated convert node will arrive
// with the conversion-builtins work in Phase 5.
func intToReal(e ir.Expr) ir.Expr {
	if lit, ok := e.(*ir.Lit); ok && lit.V.Kind == ir.TypeInt {
		return &ir.Lit{V: ir.RealVal(float64(lit.V.I)), T: ir.RealT}
	}
	return &ir.BinOp{Op: ir.OpAdd, L: e, R: &ir.Lit{V: ir.RealVal(0), T: ir.RealT}, T: ir.RealT}
}

func coerce(e ir.Expr, want *ir.Type) ir.Expr {
	if want == nil || e.ExprType().Equal(want) {
		return e
	}
	if want.Kind == ir.TypeReal && e.ExprType().IsInteger() {
		return intToReal(e)
	}
	return e
}

// inferredTagHint names the fix when a refused value is a project tag
// whose type was only inferred from its init: (a number is REAL): the
// tag needs a type:, which the author may not know it lacks (#1382).
func (l *lowerer) inferredTagHint(src Expression, got, want *ir.Type) string {
	id, ok := src.(*IdentExpr)
	if !ok || got == nil || want == nil {
		return ""
	}
	sym, ok := l.lookup(id.Name)
	if !ok || !sym.implicit {
		return ""
	}
	if _, _, stated := ir.Lookup(l.tagTypes, sym.global); stated {
		return ""
	}
	return fmt.Sprintf("; tag %s has no type:, so it is %s; give it type: %s", sym.global, got, want)
}

// intAsTime reports an integer bound to a TIME input: read as milliseconds,
// with a warning (warnIntAsTime), not refused.
func intAsTime(want, got *ir.Type) bool {
	return want != nil && got != nil && want.Kind == ir.TypeTime && got.IsInteger() && got.Enum == nil
}

// sameWidth reports whether two types of the same kind (Type.Equal) also
// have the same width, element by element in an array: an INT and a DINT,
// or a REAL and an LREAL, are Equal but cannot stand in for each other by
// reference. An integer of no declared width matches any.
func sameWidth(a, b *ir.Type) bool {
	for a != nil && b != nil && a.Kind == ir.TypeArray && b.Kind == ir.TypeArray {
		a, b = a.Elem, b.Elem
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return true
	}
	switch a.Kind {
	case ir.TypeInt:
		return a.Enum != nil || a.Name == "" || b.Name == "" || a.Name == b.Name
	case ir.TypeReal:
		return (a.Name == "LREAL") == (b.Name == "LREAL")
	}
	return true
}

func assignable(lhs, rhs *ir.Type) bool {
	if lhs.Equal(rhs) {
		return true
	}
	if lhs.Kind == ir.TypeReal && rhs.IsInteger() {
		return true
	}
	return false
}

// isBitMember reports a member name that is a bit index: Word.3.
func isBitMember(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}
