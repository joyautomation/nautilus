
package st

import (
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
// in the source win; the implicit entry is then silently ignored.
type LowerOpts struct {
	UserFBs         map[string]*ir.FBDef
	UserFuncs       map[string]*ir.FuncDef
	ImplicitGlobals map[string]*ir.Type
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
	inFile := map[string]*ir.FBDef{}
	for _, fbDecl := range prog.FBDecls {
		if _, dup := inFile[fbDecl.Name]; dup {
			return nil, fmt.Errorf("duplicate FUNCTION_BLOCK %q", fbDecl.Name)
		}
		inFile[fbDecl.Name] = &ir.FBDef{Name: fbDecl.Name, SlotIndex: map[string]int{}}
	}
	combined := resolver
	if len(inFile) > 0 {
		combined = make(map[string]*ir.FBDef, len(resolver)+len(inFile))
		for k, v := range resolver {
			combined[k] = v
		}
		for k, v := range inFile {
			combined[k] = v
		}
	}

	// Resolve FB signatures (slot lists + types) before lowering any body
	// so FB-on-FB member access type-checks regardless of declaration order.
	for _, fbDecl := range prog.FBDecls {
		if err := populateFBSignature(fbDecl, inFile[fbDecl.Name], combined); err != nil {
			return nil, err
		}
	}

	// Same dance for FUNCTIONs: build empty shells so peer functions and
	// the outer program can resolve each by name regardless of order.
	inFileFuncs := map[string]*ir.FuncDef{}
	for _, fd := range prog.FuncDecls {
		if _, dup := inFileFuncs[fd.Name]; dup {
			return nil, fmt.Errorf("duplicate FUNCTION %q", fd.Name)
		}
		inFileFuncs[fd.Name] = &ir.FuncDef{Name: fd.Name}
	}
	combinedFuncs := opts.UserFuncs
	if len(inFileFuncs) > 0 {
		combinedFuncs = make(map[string]*ir.FuncDef, len(opts.UserFuncs)+len(inFileFuncs))
		for k, v := range opts.UserFuncs {
			combinedFuncs[k] = v
		}
		for k, v := range inFileFuncs {
			combinedFuncs[k] = v
		}
	}

	// Resolve FUNCTION signatures (return type + input list) before any
	// body so peer calls type-check regardless of declaration order.
	for _, fd := range prog.FuncDecls {
		if err := populateFuncSignature(fd, inFileFuncs[fd.Name], combined); err != nil {
			return nil, err
		}
	}

	l := newLowerer(prog, combined)
	l.userFuncs = combinedFuncs
	l.implicitGlobals = opts.ImplicitGlobals
	if err := l.collectTypes(); err != nil {
		return nil, err
	}
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
		if err := lowerFBBody(fbDecl, def, combined, combinedFuncs); err != nil {
			return nil, err
		}
		l.irProg.UserFBs = append(l.irProg.UserFBs, def)
	}
	// Lower each FUNCTION body. Functions may call other user functions
	// (and user FBs) since both registries are now populated.
	for _, fd := range prog.FuncDecls {
		def := inFileFuncs[fd.Name]
		if err := lowerFuncBody(fd, def, combined, combinedFuncs); err != nil {
			return nil, err
		}
		l.irProg.UserFuncs = append(l.irProg.UserFuncs, def)
	}
	return l.irProg, nil
}

// populateFBSignature resolves the slot types for a FUNCTION_BLOCK and
// writes them into def.Inputs/Outputs/Internals plus the SlotIndex map.
// VAR_GLOBAL/VAR_EXTERNAL declarations don't contribute slots — they're
// only honored during body lowering.
func populateFBSignature(fbDecl *FunctionBlockDecl, def *ir.FBDef, userFBs map[string]*ir.FBDef) error {
	sig := newLowerer(&Program{Name: fbDecl.Name}, userFBs)
	for _, vb := range fbDecl.VarBlocks {
		for _, vd := range vb.Variables {
			switch vb.Kind {
			case "VAR_GLOBAL", "VAR_EXTERNAL":
				continue
			}
			t, err := sig.resolveType(vd.Type)
			if err != nil {
				return errAt(vd.Pos, fmt.Errorf("FUNCTION_BLOCK %s VAR %s: %w", fbDecl.Name, vd.Name, err))
			}
			slot := ir.FBSlot{Name: vd.Name, Type: t}
			switch vb.Kind {
			case "VAR_INPUT":
				def.Inputs = append(def.Inputs, slot)
			case "VAR_OUTPUT":
				def.Outputs = append(def.Outputs, slot)
			default:
				def.Internals = append(def.Internals, slot)
			}
		}
	}
	idx := 0
	for _, s := range def.Inputs {
		if _, dup := def.SlotIndex[s.Name]; dup {
			return fmt.Errorf("FUNCTION_BLOCK %s: duplicate slot %q", fbDecl.Name, s.Name)
		}
		def.SlotIndex[s.Name] = idx
		idx++
	}
	for _, s := range def.Outputs {
		if _, dup := def.SlotIndex[s.Name]; dup {
			return fmt.Errorf("FUNCTION_BLOCK %s: duplicate slot %q", fbDecl.Name, s.Name)
		}
		def.SlotIndex[s.Name] = idx
		idx++
	}
	for _, s := range def.Internals {
		if _, dup := def.SlotIndex[s.Name]; dup {
			return fmt.Errorf("FUNCTION_BLOCK %s: duplicate slot %q", fbDecl.Name, s.Name)
		}
		def.SlotIndex[s.Name] = idx
		idx++
	}
	return nil
}

// lowerFBBody compiles the FUNCTION_BLOCK body to IR statements and
// installs a Step closure on def. The body lowers against a synthetic
// Program whose VarBlocks are reordered to VAR_INPUT ‖ VAR_OUTPUT ‖ VAR
// so the resulting Slots match FBInstance.Slots index-for-index.
func lowerFBBody(fbDecl *FunctionBlockDecl, def *ir.FBDef, userFBs map[string]*ir.FBDef, userFuncs map[string]*ir.FuncDef) error {
	var inputBlocks, outputBlocks, internalBlocks, globalBlocks []VarBlock
	for _, vb := range fbDecl.VarBlocks {
		switch vb.Kind {
		case "VAR_INPUT":
			inputBlocks = append(inputBlocks, vb)
		case "VAR_OUTPUT":
			outputBlocks = append(outputBlocks, vb)
		case "VAR_GLOBAL", "VAR_EXTERNAL":
			globalBlocks = append(globalBlocks, vb)
		default:
			internalBlocks = append(internalBlocks, vb)
		}
	}
	var blocks []VarBlock
	blocks = append(blocks, inputBlocks...)
	blocks = append(blocks, outputBlocks...)
	blocks = append(blocks, internalBlocks...)
	blocks = append(blocks, globalBlocks...)

	bodyProg := &Program{Name: fbDecl.Name, VarBlocks: blocks, Statements: fbDecl.Statements}
	sub := newLowerer(bodyProg, userFBs)
	sub.userFuncs = userFuncs
	if err := sub.collectVars(); err != nil {
		return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, err)
	}
	stmts, err := sub.lowerStmts(fbDecl.Statements)
	if err != nil {
		return fmt.Errorf("FUNCTION_BLOCK %s: %w", fbDecl.Name, err)
	}
	sub.irProg.Body = stmts
	bodyIR := sub.irProg

	def.Step = func(inst *ir.FBInstance, ctx ir.FBStepCtx) error {
		frame := &ir.Frame{Slots: inst.Slots}
		return ir.Run(bodyIR, frame, ctx.Host)
	}
	return nil
}

// populateFuncSignature resolves a FUNCTION's input slot list and return
// type, writing them onto def so peer call sites can type-check before
// the body itself is lowered.
func populateFuncSignature(decl *FunctionDecl, def *ir.FuncDef, userFBs map[string]*ir.FBDef) error {
	sig := newLowerer(&Program{Name: decl.Name}, userFBs)
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
		for _, vd := range vb.Variables {
			t, err := sig.resolveType(vd.Type)
			if err != nil {
				return errAt(vd.Pos, fmt.Errorf("FUNCTION %s VAR %s: %w", decl.Name, vd.Name, err))
			}
			slot := ir.FBSlot{Name: vd.Name, Type: t}
			switch vb.Kind {
			case "VAR_INPUT":
				if vd.Name == decl.Name {
					return errAt(vd.Pos, fmt.Errorf("FUNCTION %s: VAR_INPUT %q shadows the return name", decl.Name, vd.Name))
				}
				def.Inputs = append(def.Inputs, slot)
			default:
				if vd.Name == decl.Name {
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
func lowerFuncBody(decl *FunctionDecl, def *ir.FuncDef, userFBs map[string]*ir.FBDef, userFuncs map[string]*ir.FuncDef) error {
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
	sub := newLowerer(bodyProg, userFBs)
	sub.userFuncs = userFuncs
	if err := sub.collectVars(); err != nil {
		return fmt.Errorf("FUNCTION %s: %w", decl.Name, err)
	}
	stmts, err := sub.lowerStmts(decl.Statements)
	if err != nil {
		return fmt.Errorf("FUNCTION %s: %w", decl.Name, err)
	}
	sub.irProg.Body = stmts
	bodyIR := sub.irProg
	def.Run = func(frame *ir.Frame, host ir.Host) error {
		return ir.Run(bodyIR, frame, host)
	}
	return nil
}

type lowerer struct {
	prog            *Program
	irProg          *ir.Program
	scope           map[string]symbol
	types           map[string]*ir.Type
	userFBs         map[string]*ir.FBDef  // optional; consulted for FB type resolution
	userFuncs       map[string]*ir.FuncDef // optional; consulted at call sites for bare-name lookup
	implicitGlobals map[string]*ir.Type   // optional; PLC project vars surfaced as globals
	// returnSlot, when >= 0, marks the slot the bare function-name
	// identifier should bind to inside a FUNCTION body so `Name := value`
	// assigns the return value rather than failing as undeclared.
	returnSlot     int
	returnSlotName string
}

type symbol struct {
	slot   int // -1 for globals
	typ    *ir.Type
	kind   ir.VarKind
	global string
}

func newLowerer(prog *Program, userFBs map[string]*ir.FBDef) *lowerer {
	return &lowerer{
		prog:       prog,
		irProg:     &ir.Program{Name: prog.Name, SlotIndex: map[string]int{}},
		scope:      map[string]symbol{},
		types:      map[string]*ir.Type{},
		userFBs:    userFBs,
		returnSlot: -1,
	}
}

// ─── Type resolution ──────────────────────────────────────────────────────

// collectTypes resolves program-level TypeDecls in two passes so struct
// fields can reference peer UDTs declared in the same TYPE block.
func (l *lowerer) collectTypes() error {
	for _, td := range l.prog.TypeDecls {
		if _, dup := l.types[td.Name]; dup {
			return errAt(td.Pos, fmt.Errorf("duplicate TYPE declaration %q", td.Name))
		}
		l.types[td.Name] = &ir.Type{
			Kind:   ir.TypeStruct,
			Struct: &ir.StructDef{Name: td.Name, FieldIndex: map[string]int{}},
		}
	}
	for _, td := range l.prog.TypeDecls {
		t := l.types[td.Name]
		switch body := td.Type.(type) {
		case *StructType:
			for i, f := range body.Fields {
				ft, err := l.resolveType(f.Type)
				if err != nil {
					return errAt(f.Pos, fmt.Errorf("TYPE %s field %q: %w", td.Name, f.Name, err))
				}
				t.Struct.Fields = append(t.Struct.Fields, ir.StructField{Name: f.Name, Type: ft})
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

func (l *lowerer) resolveType(te TypeExpr) (*ir.Type, error) {
	switch t := te.(type) {
	case *ScalarType:
		return resolveScalar(t.Name)
	case *NamedType:
		if udt, ok := l.types[t.Name]; ok {
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
			if def, ok := l.userFBs[t.Name]; ok && def != nil {
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
			lo, err := evalConstInt(t.Dims[i].Lo)
			if err != nil {
				return nil, fmt.Errorf("array dim lo: %w", err)
			}
			hi, err := evalConstInt(t.Dims[i].Hi)
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
			def.Fields = append(def.Fields, ir.StructField{Name: f.Name, Type: ft})
			def.FieldIndex[f.Name] = i
		}
		return &ir.Type{Kind: ir.TypeStruct, Struct: def}, nil
	}
	return nil, fmt.Errorf("unsupported type %T", te)
}

func resolveScalar(name string) (*ir.Type, error) {
	switch strings.ToUpper(name) {
	case "BOOL":
		return ir.BoolT, nil
	case "BYTE", "SINT", "USINT", "INT", "UINT", "WORD", "DINT", "UDINT", "DWORD", "LINT", "ULINT", "LWORD":
		return ir.IntT, nil
	case "REAL", "LREAL":
		return ir.RealT, nil
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
		}
	}
	return ir.Value{}, fmt.Errorf("initial value must be a literal constant: %T", e)
}

// ─── Slot / symbol table ───────────────────────────────────────────────────

func (l *lowerer) collectVars() error {
	for _, vb := range l.prog.VarBlocks {
		kind := varKindFor(vb.Kind)
		for _, vd := range vb.Variables {
			if _, dup := l.scope[vd.Name]; dup {
				return errAt(vd.Pos, fmt.Errorf("duplicate declaration %q", vd.Name))
			}
			t, err := l.resolveType(vd.Type)
			if err != nil {
				return errAt(vd.Pos, fmt.Errorf("VAR %s: %w", vd.Name, err))
			}
			var init ir.Value
			if vd.Initial != nil {
				init, err = evalConstValue(vd.Initial, t)
				if err != nil {
					return errAt(vd.Pos, fmt.Errorf("VAR %s initial: %w", vd.Name, err))
				}
			}
			if kind == ir.VarGlobal {
				l.scope[vd.Name] = symbol{slot: -1, typ: t, kind: ir.VarGlobal, global: vd.Name}
				continue
			}
			slot := len(l.irProg.Slots)
			l.irProg.Slots = append(l.irProg.Slots, ir.VarSlot{
				Name:     vd.Name,
				Type:     t,
				Init:     init,
				Retained: vb.Retain,
				Kind:     kind,
			})
			l.irProg.SlotIndex[vd.Name] = slot
			l.scope[vd.Name] = symbol{slot: slot, typ: t, kind: kind}
		}
	}
	// Inject PLC project variables as implicit globals so unqualified
	// references resolve without a matching VAR_GLOBAL declaration.
	// Any name already declared explicitly (above) wins.
	for name, t := range l.implicitGlobals {
		if _, exists := l.scope[name]; exists {
			continue
		}
		if t == nil {
			continue
		}
		l.scope[name] = symbol{slot: -1, typ: t, kind: ir.VarGlobal, global: name}
	}
	return nil
}

func varKindFor(blockKind string) ir.VarKind {
	switch blockKind {
	case "VAR_INPUT":
		return ir.VarInput
	case "VAR_OUTPUT":
		return ir.VarOutput
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
			return nil, fmt.Errorf("IF condition must be BOOL, got %s", cond.ExprType())
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
				return nil, fmt.Errorf("ELSIF condition must be BOOL, got %s", ec.ExprType())
			}
			eb, err := l.lowerStmts(n.ElsIfs[i].Body)
			if err != nil {
				return nil, err
			}
			elseB = []ir.Stmt{&ir.If{Cond: ec, Then: eb, Else: elseB}}
		}
		return &ir.If{Cond: cond, Then: thenB, Else: elseB}, nil

	case *ForStmt:
		sym, ok := l.scope[n.Variable]
		if !ok {
			return nil, fmt.Errorf("FOR: undeclared loop variable %q", n.Variable)
		}
		if sym.kind == ir.VarGlobal {
			return nil, fmt.Errorf("FOR: loop variable %q must be local, not global", n.Variable)
		}
		if sym.typ.Kind != ir.TypeInt {
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
			return nil, fmt.Errorf("WHILE condition must be BOOL, got %s", cond.ExprType())
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
			return nil, fmt.Errorf("UNTIL condition must be BOOL, got %s", cond.ExprType())
		}
		return &ir.Repeat{Body: body, Cond: cond}, nil

	case *CaseStmt:
		expr, err := l.lowerExpr(n.Expression)
		if err != nil {
			return nil, err
		}
		var clauses []ir.CaseClause
		for _, c := range n.Cases {
			var vals []ir.Expr
			for _, v := range c.Values {
				lv, err := l.lowerExpr(v)
				if err != nil {
					return nil, err
				}
				vals = append(vals, lv)
			}
			body, err := l.lowerStmts(c.Body)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, ir.CaseClause{Values: vals, Body: body})
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
	if l.userFuncs != nil {
		if def, ok := l.userFuncs[n.Call.Name]; ok && def != nil {
			expr, err := l.lowerUserFuncCall(n.Call, def)
			if err != nil {
				return nil, err
			}
			return &ir.ExprStmt{X: expr}, nil
		}
	}
	sym, ok := l.scope[n.Call.Name]
	if !ok {
		return nil, fmt.Errorf("call to undeclared name %q", n.Call.Name)
	}
	if sym.typ == nil || sym.typ.Kind != ir.TypeFB {
		return nil, fmt.Errorf("%q is not a function-block instance (declare e.g. `t1 : TON;`)", n.Call.Name)
	}
	if sym.kind == ir.VarGlobal {
		return nil, fmt.Errorf("FB instance %q must be a local variable, not VAR_GLOBAL", n.Call.Name)
	}
	def := sym.typ.FB
	if len(n.Call.Args) > 0 {
		return nil, fmt.Errorf("FB call %q must use named args (IN := …, PT := …)", n.Call.Name)
	}
	bindings := make([]ir.FBInput, 0, len(n.Call.NamedArgs))
	for _, na := range n.Call.NamedArgs {
		idx, ok := def.SlotIndex[na.Name]
		if !ok {
			return nil, fmt.Errorf("FB %s has no input %q", def.Name, na.Name)
		}
		if idx >= len(def.Inputs) {
			return nil, fmt.Errorf("FB %s field %q is not an input", def.Name, na.Name)
		}
		v, err := l.lowerExpr(na.Value)
		if err != nil {
			return nil, fmt.Errorf("FB %s arg %q: %w", def.Name, na.Name, err)
		}
		v = coerce(v, def.Inputs[idx].Type)
		bindings = append(bindings, ir.FBInput{SlotIdx: idx, Value: v})
	}
	outputs := make([]ir.FBOutput, 0, len(n.Call.OutputBindings))
	for _, ob := range n.Call.OutputBindings {
		idx, ok := def.SlotIndex[ob.Name]
		if !ok {
			return nil, fmt.Errorf("FB %s has no member %q", def.Name, ob.Name)
		}
		if idx < len(def.Inputs) || idx >= len(def.Inputs)+len(def.Outputs) {
			return nil, fmt.Errorf("FB %s field %q is not an output (=> binds outputs only)", def.Name, ob.Name)
		}
		target, err := l.lowerLValue(ob.Target)
		if err != nil {
			return nil, fmt.Errorf("FB %s output %q target: %w", def.Name, ob.Name, err)
		}
		outputs = append(outputs, ir.FBOutput{SlotIdx: idx, Target: target})
	}
	return &ir.FBCall{InstanceSlot: sym.slot, Def: def, Inputs: bindings, Outputs: outputs}, nil
}

func (l *lowerer) lowerAssign(a *AssignStmt) (ir.Stmt, error) {
	if a.TargetExpr == nil {
		return nil, fmt.Errorf("assignment has no structured target (parser bug)")
	}
	target, err := l.lowerLValue(a.TargetExpr)
	if err != nil {
		return nil, err
	}
	value, err := l.lowerExpr(a.Value)
	if err != nil {
		return nil, err
	}
	value = coerce(value, target.ExprType())
	if !assignable(target.ExprType(), value.ExprType()) {
		return nil, fmt.Errorf("cannot assign %s to %s", value.ExprType(), target.ExprType())
	}
	return &ir.Assign{Target: target, Value: value}, nil
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
		return l.lowerExpr(n.Inner)
	case *IdentExpr:
		return l.lowerIdent(n.Name)
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
	if l.userFuncs != nil {
		if def, ok := l.userFuncs[n.Name]; ok && def != nil {
			return l.lowerUserFuncCall(n, def)
		}
	}
	sig, ok := ir.Builtins[strings.ToUpper(n.Name)]
	if !ok {
		// FB instance "calls" inside expressions are illegal — outputs
		// are read via member access (t1.Q), and bare `t1(...)` produces
		// no value. Surface a clearer message when this is the case.
		if sym, defined := l.scope[n.Name]; defined && sym.typ != nil && sym.typ.Kind == ir.TypeFB {
			return nil, fmt.Errorf("FB instance %q can't be used as an expression — invoke it as a statement and read outputs (e.g. %s.Q)", n.Name, n.Name)
		}
		return nil, fmt.Errorf("unknown function %q", n.Name)
	}
	if len(n.NamedArgs) > 0 {
		return nil, fmt.Errorf("function %s does not accept named args", sig.Name)
	}
	args := make([]ir.Expr, 0, len(n.Args))
	argTypes := make([]*ir.Type, 0, len(n.Args))
	for _, a := range n.Args {
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
	resultT := sig.Result
	if sig.Coerce != nil {
		t, err := sig.Coerce(argTypes)
		if err != nil {
			return nil, err
		}
		resultT = t
	}
	for i, p := range sig.Params {
		if p == nil || i >= len(args) {
			continue
		}
		args[i] = coerce(args[i], p)
		if !assignable(p, args[i].ExprType()) {
			return nil, fmt.Errorf("function %s arg %d: cannot pass %s as %s", sig.Name, i+1, args[i].ExprType(), p)
		}
	}
	return &ir.Call{Name: sig.Name, Args: args, Fn: sig.Fn, T: resultT}, nil
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
				if in.Name == na.Name {
					idx = i
					break
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("FUNCTION %s has no input %q", def.Name, na.Name)
			}
			if have[idx] {
				return nil, fmt.Errorf("FUNCTION %s: input %q given twice", def.Name, na.Name)
			}
			v, err := l.lowerExpr(na.Value)
			if err != nil {
				return nil, fmt.Errorf("FUNCTION %s arg %s: %w", def.Name, na.Name, err)
			}
			v = coerce(v, def.Inputs[idx].Type)
			if !assignable(def.Inputs[idx].Type, v.ExprType()) {
				return nil, fmt.Errorf("FUNCTION %s arg %s: cannot pass %s as %s", def.Name, na.Name, v.ExprType(), def.Inputs[idx].Type)
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
			v, err := l.lowerExpr(a)
			if err != nil {
				return nil, fmt.Errorf("FUNCTION %s arg %d: %w", def.Name, i+1, err)
			}
			v = coerce(v, def.Inputs[i].Type)
			if !assignable(def.Inputs[i].Type, v.ExprType()) {
				return nil, fmt.Errorf("FUNCTION %s arg %d: cannot pass %s as %s", def.Name, i+1, v.ExprType(), def.Inputs[i].Type)
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

func (l *lowerer) lowerIdent(name string) (ir.Expr, error) {
	sym, ok := l.scope[name]
	if !ok {
		return nil, fmt.Errorf("undeclared identifier %q (declare in VAR_* or VAR_GLOBAL block)", name)
	}
	if sym.kind == ir.VarGlobal {
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
	switch ot.Kind {
	case ir.TypeStruct:
		idx, ok := ot.Struct.FieldIndex[m.Member]
		if !ok {
			label := ot.Struct.Name
			if label == "" {
				label = "STRUCT"
			}
			return nil, fmt.Errorf("field %q not found on %s", m.Member, label)
		}
		return &ir.MemberRef{Object: obj, FieldIdx: idx, T: ot.Struct.Fields[idx].Type}, nil
	case ir.TypeFB:
		idx, ok := ot.FB.SlotIndex[m.Member]
		if !ok {
			return nil, fmt.Errorf("FB %s has no field %q", ot.FB.Name, m.Member)
		}
		all := ot.FB.AllSlots()
		return &ir.MemberRef{Object: obj, FieldIdx: idx, T: all[idx].Type}, nil
	}
	return nil, fmt.Errorf("member access on non-struct type %s", ot)
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
		if idx.ExprType().Kind != ir.TypeInt {
			return nil, fmt.Errorf("array index must be integer, got %s", idx.ExprType())
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
	left, err := l.lowerExpr(b.Left)
	if err != nil {
		return nil, err
	}
	right, err := l.lowerExpr(b.Right)
	if err != nil {
		return nil, err
	}
	op, err := mapBinOp(b.Op)
	if err != nil {
		return nil, err
	}
	resultT, err := resolveBinType(op, left.ExprType(), right.ExprType())
	if err != nil {
		return nil, fmt.Errorf("operator %s on %s and %s: %w", b.Op, left.ExprType(), right.ExprType(), err)
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
			return nil, fmt.Errorf("unary - on non-numeric %s", x.ExprType())
		}
		return &ir.UnOp{Op: ir.OpNeg, X: x, T: x.ExprType()}, nil
	case "NOT":
		if x.ExprType().Kind != ir.TypeBool {
			return nil, fmt.Errorf("NOT on non-BOOL %s", x.ExprType())
		}
		return &ir.UnOp{Op: ir.OpNot, X: x, T: ir.BoolT}, nil
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

func resolveBinType(op ir.BinKind, lt, rt *ir.Type) (*ir.Type, error) {
	switch op {
	case ir.OpAdd, ir.OpSub, ir.OpMul, ir.OpDiv, ir.OpMod:
		if !lt.IsNumeric() || !rt.IsNumeric() {
			return nil, fmt.Errorf("arithmetic requires numeric operands")
		}
		if lt.Kind == ir.TypeReal || rt.Kind == ir.TypeReal {
			return ir.RealT, nil
		}
		if lt.Kind == ir.TypeTime && rt.Kind == ir.TypeTime {
			return ir.TimeT, nil
		}
		if lt.Kind == ir.TypeTime || rt.Kind == ir.TypeTime {
			return nil, fmt.Errorf("TIME may only be combined with TIME")
		}
		return ir.IntT, nil
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
		if lt.Kind == ir.TypeInt && rt.Kind == ir.TypeInt {
			return ir.IntT, nil
		}
		return nil, fmt.Errorf("logical op requires BOOL or INT operands")
	case ir.OpXor:
		if lt.Kind == ir.TypeBool && rt.Kind == ir.TypeBool {
			return ir.BoolT, nil
		}
		if lt.Kind == ir.TypeInt && rt.Kind == ir.TypeInt {
			return ir.IntT, nil
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
	if want.Kind == ir.TypeReal && e.ExprType().Kind == ir.TypeInt {
		return intToReal(e)
	}
	return e
}

func assignable(lhs, rhs *ir.Type) bool {
	if lhs.Equal(rhs) {
		return true
	}
	if lhs.Kind == ir.TypeReal && rhs.Kind == ir.TypeInt {
		return true
	}
	return false
}
