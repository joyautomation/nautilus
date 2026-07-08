package ir

// Stmt is the interface implemented by all statement nodes.
type Stmt interface{ stmtNode() }

// Expr is the interface implemented by all expression nodes.
// Every expression carries its resolved type (set by the lowering pass).
type Expr interface {
	ExprType() *Type
	exprNode()
}

// LValue is an expression that can appear on the left of an assignment.
type LValue interface {
	Expr
	lvalueNode()
}

// ─── Statements ─────────────────────────────────────────────────────────────

type Assign struct {
	Target LValue
	Value  Expr
}

func (*Assign) stmtNode() {}

type If struct {
	Cond Expr
	Then []Stmt
	Else []Stmt // nil if absent
}

func (*If) stmtNode() {}

type For struct {
	Slot       int // loop variable slot (always TypeInt in phase 1)
	Start, End Expr
	Step       Expr // nil ⇒ 1
	Body       []Stmt
}

func (*For) stmtNode() {}

type While struct {
	Cond Expr
	Body []Stmt
}

func (*While) stmtNode() {}

type Repeat struct {
	Body []Stmt
	Cond Expr // exit when Cond is true
}

func (*Repeat) stmtNode() {}

// Case models ST CASE ... OF. Each clause matches when the expression equals any listed value.
type Case struct {
	Expr    Expr
	Clauses []CaseClause
	Else    []Stmt
}

func (*Case) stmtNode() {}

type CaseClause struct {
	Values []Expr
	Body   []Stmt
}

type Return struct{}

func (*Return) stmtNode() {}

// Break / Continue / ExprStmt are reserved for later phases; omitting until needed.

// ─── Expressions ────────────────────────────────────────────────────────────

type Lit struct {
	V Value
	T *Type
}

func (l *Lit) ExprType() *Type { return l.T }
func (l *Lit) exprNode()       {}

// SlotRef reads or writes a local/input/output slot.
type SlotRef struct {
	Slot int
	T    *Type
}

func (s *SlotRef) ExprType() *Type { return s.T }
func (s *SlotRef) exprNode()       {}
func (s *SlotRef) lvalueNode()     {}

// GlobalRef reads or writes a PLC-wide variable through the Host.
type GlobalRef struct {
	Name string
	T    *Type
}

func (g *GlobalRef) ExprType() *Type { return g.T }
func (g *GlobalRef) exprNode()       {}
func (g *GlobalRef) lvalueNode()     {}

// BinKind enumerates the binary operators the VM understands.
type BinKind uint8

const (
	OpAdd BinKind = iota
	OpSub
	OpMul
	OpDiv
	OpMod
	OpEq
	OpNeq
	OpLt
	OpLte
	OpGt
	OpGte
	OpAnd
	OpOr
	OpXor
)

type BinOp struct {
	Op   BinKind
	L, R Expr
	T    *Type // result type
}

func (b *BinOp) ExprType() *Type { return b.T }
func (b *BinOp) exprNode()       {}

type UnKind uint8

const (
	OpNeg UnKind = iota
	OpNot
)

type UnOp struct {
	Op UnKind
	X  Expr
	T  *Type
}

func (u *UnOp) ExprType() *Type { return u.T }
func (u *UnOp) exprNode()       {}

// IndexRef reads or writes an element of an array value.
// Index is 0-based after lowering has subtracted the array's lower bound.
type IndexRef struct {
	Array Expr
	Index Expr
	T     *Type // element type
}

func (i *IndexRef) ExprType() *Type { return i.T }
func (i *IndexRef) exprNode()       {}
func (i *IndexRef) lvalueNode()     {}

// MemberRef reads or writes a UDT field. FieldIdx is the pre-resolved slot
// into the struct's Fld slice, matching StructDef.Fields order.
type MemberRef struct {
	Object   Expr
	FieldIdx int
	T        *Type
}

func (m *MemberRef) ExprType() *Type { return m.T }
func (m *MemberRef) exprNode()       {}
func (m *MemberRef) lvalueNode()     {}

// Call invokes a built-in stateless function. Fn is resolved at
// lowering time (so the VM doesn't pay for a map lookup per scan)
// and given the evaluated arg values directly.
type Call struct {
	Name string
	Args []Expr
	Fn   BuiltinFn
	T    *Type
}

func (c *Call) ExprType() *Type { return c.T }
func (c *Call) exprNode()       {}

// UserCall invokes a user-defined FUNCTION. The lowering pass binds
// each positional argument to an input-slot index on the FuncDef. The
// VM allocates a fresh per-call frame, evaluates args into the input
// slots, runs the body, and returns the value left in ReturnSlot.
type UserCall struct {
	Def  *FuncDef
	Args []Expr // positional, aligned with Def.Inputs order
	T    *Type
}

func (u *UserCall) ExprType() *Type { return u.T }
func (u *UserCall) exprNode()       {}

// FBCall invokes a function block instance. The lowering pass binds
// each named arg to an input-slot index on the FB type so the VM can
// evaluate args, write them into the instance's input slots, and run
// Step in a fixed order.
type FBCall struct {
	InstanceSlot int        // slot in Frame.Slots holding the FBInstance
	Def          *FBDef     // resolved FB type
	Inputs       []FBInput  // bindings for this invocation, in source order
	Outputs      []FBOutput // post-step copies from FB output slots to caller lvalues
}

func (*FBCall) stmtNode() {}

// FBInput pairs an input-slot index on the FB with the expression
// providing its value at the call site.
type FBInput struct {
	SlotIdx int
	Value   Expr
}

// FBOutput pairs an output-slot index on the FB with a caller lvalue.
// After Step, the VM copies inst.Slots[SlotIdx] into Target.
type FBOutput struct {
	SlotIdx int
	Target  LValue
}

// Exit and Continue — loop control.
type Exit struct{}

func (*Exit) stmtNode() {}

type Continue struct{}

func (*Continue) stmtNode() {}

// ExprStmt evaluates X for its side effects and discards the result.
// Currently produced only for user-FUNCTION calls used as statements.
type ExprStmt struct {
	X Expr
}

func (*ExprStmt) stmtNode() {}
