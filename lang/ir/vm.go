
package ir

import "fmt"

// Host exposes the surrounding PLC runtime to the IR VM.
// The engine integration (phase 3) adapts the tentacle variable store to this interface.
type Host interface {
	ReadGlobal(name string) (Value, error)
	WriteGlobal(name string, v Value) error
	NowMs() int64
}

// EvalCtx is the per-scan evaluation state. A fresh ctx is used per Run call;
// the Frame (retained state) is stable across scans.
type EvalCtx struct {
	Program *Program
	Frame   *Frame
	Host    Host

	// control-flow sentinels
	returning    bool
	exitLoop     bool // EXIT — break out of the innermost loop
	continueLoop bool // CONTINUE — skip to the next iteration
}

// Run executes one scan of the program body against the frame.
func Run(prog *Program, frame *Frame, host Host) error {
	ctx := &EvalCtx{Program: prog, Frame: frame, Host: host}
	return execBlock(ctx, prog.Body)
}

func execBlock(ctx *EvalCtx, stmts []Stmt) error {
	for _, s := range stmts {
		if err := execStmt(ctx, s); err != nil {
			return err
		}
		if ctx.returning || ctx.exitLoop || ctx.continueLoop {
			return nil
		}
	}
	return nil
}

func execStmt(ctx *EvalCtx, s Stmt) error {
	switch n := s.(type) {
	case *Assign:
		v, err := evalExpr(ctx, n.Value)
		if err != nil {
			return err
		}
		return writeLValue(ctx, n.Target, v)

	case *If:
		cv, err := evalExpr(ctx, n.Cond)
		if err != nil {
			return err
		}
		if cv.B {
			return execBlock(ctx, n.Then)
		}
		return execBlock(ctx, n.Else)

	case *For:
		startV, err := evalExpr(ctx, n.Start)
		if err != nil {
			return err
		}
		endV, err := evalExpr(ctx, n.End)
		if err != nil {
			return err
		}
		step := int64(1)
		if n.Step != nil {
			sv, err := evalExpr(ctx, n.Step)
			if err != nil {
				return err
			}
			step = sv.I
		}
		if step == 0 {
			return fmt.Errorf("FOR step is zero")
		}
		for i := startV.I; (step > 0 && i <= endV.I) || (step < 0 && i >= endV.I); i += step {
			ctx.Frame.Slots[n.Slot] = IntVal(i)
			if err := execBlock(ctx, n.Body); err != nil {
				return err
			}
			if ctx.returning {
				return nil
			}
			if ctx.exitLoop {
				ctx.exitLoop = false
				return nil
			}
			if ctx.continueLoop {
				ctx.continueLoop = false
			}
		}
		return nil

	case *While:
		const guard = 1_000_000 // bound to prevent scan-hang from runaway loop
		for i := 0; i < guard; i++ {
			cv, err := evalExpr(ctx, n.Cond)
			if err != nil {
				return err
			}
			if !cv.B {
				return nil
			}
			if err := execBlock(ctx, n.Body); err != nil {
				return err
			}
			if ctx.returning {
				return nil
			}
			if ctx.exitLoop {
				ctx.exitLoop = false
				return nil
			}
			if ctx.continueLoop {
				ctx.continueLoop = false
			}
		}
		return fmt.Errorf("WHILE exceeded iteration guard")

	case *Repeat:
		const guard = 1_000_000
		for i := 0; i < guard; i++ {
			if err := execBlock(ctx, n.Body); err != nil {
				return err
			}
			if ctx.returning {
				return nil
			}
			if ctx.exitLoop {
				ctx.exitLoop = false
				return nil
			}
			if ctx.continueLoop {
				ctx.continueLoop = false
			}
			cv, err := evalExpr(ctx, n.Cond)
			if err != nil {
				return err
			}
			if cv.B {
				return nil
			}
		}
		return fmt.Errorf("REPEAT exceeded iteration guard")

	case *Case:
		sv, err := evalExpr(ctx, n.Expr)
		if err != nil {
			return err
		}
		for _, cl := range n.Clauses {
			for _, vExpr := range cl.Values {
				vv, err := evalExpr(ctx, vExpr)
				if err != nil {
					return err
				}
				if valEq(sv, vv) {
					return execBlock(ctx, cl.Body)
				}
			}
		}
		return execBlock(ctx, n.Else)

	case *Return:
		ctx.returning = true
		return nil

	case *Exit:
		ctx.exitLoop = true
		return nil

	case *Continue:
		ctx.continueLoop = true
		return nil

	case *ExprStmt:
		_, err := evalExpr(ctx, n.X)
		return err

	case *FBCall:
		inst := ctx.Frame.Slots[n.InstanceSlot].FB
		if inst == nil {
			return fmt.Errorf("FB call on uninitialised instance at slot %d", n.InstanceSlot)
		}
		for _, in := range n.Inputs {
			v, err := evalExpr(ctx, in.Value)
			if err != nil {
				return err
			}
			if in.SlotIdx < 0 || in.SlotIdx >= len(inst.Slots) {
				return fmt.Errorf("FB input slot %d out of range", in.SlotIdx)
			}
			inst.Slots[in.SlotIdx] = coerceValue(v, n.Def.AllSlots()[in.SlotIdx].Type)
		}
		stepCtx := FBStepCtx{NowMs: ctx.Host.NowMs(), Host: ctx.Host}
		if err := n.Def.Step(inst, stepCtx); err != nil {
			return err
		}
		for _, out := range n.Outputs {
			if out.SlotIdx < 0 || out.SlotIdx >= len(inst.Slots) {
				return fmt.Errorf("FB output slot %d out of range", out.SlotIdx)
			}
			if err := writeLValue(ctx, out.Target, inst.Slots[out.SlotIdx]); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("unknown stmt %T", s)
}

// accessor is one step along a nested lvalue path: either an array subscript
// (arrIdx set) or a struct field (fieldIdx set, isField=true).
type accessor struct {
	arrIdx   int
	fieldIdx int
	isField  bool
}

func writeLValue(ctx *EvalCtx, lv LValue, v Value) error {
	// Collect accessors while descending to the root slot / global.
	// The chain is built leaf-first and applied root-first below.
	var chain []accessor
	cur := lv
	for {
		switch n := cur.(type) {
		case *SlotRef:
			return storeAtRoot(&ctx.Frame.Slots[n.Slot], chain, v)
		case *GlobalRef:
			if len(chain) != 0 {
				return fmt.Errorf("cannot assign to a field/element of global %q without a declared composite type", n.Name)
			}
			return ctx.Host.WriteGlobal(n.Name, v)
		case *IndexRef:
			iv, err := evalExpr(ctx, n.Index)
			if err != nil {
				return err
			}
			chain = append(chain, accessor{arrIdx: int(iv.I)})
			inner, ok := n.Array.(LValue)
			if !ok {
				return fmt.Errorf("cannot assign through non-lvalue array base %T", n.Array)
			}
			cur = inner
		case *MemberRef:
			chain = append(chain, accessor{fieldIdx: n.FieldIdx, isField: true})
			inner, ok := n.Object.(LValue)
			if !ok {
				return fmt.Errorf("cannot assign through non-lvalue object base %T", n.Object)
			}
			cur = inner
		default:
			return fmt.Errorf("unknown lvalue %T", lv)
		}
	}
}

// storeAtRoot walks the chain (built leaf-first) in reverse so we traverse
// the aggregate from root outward, then writes v at the leaf position.
func storeAtRoot(root *Value, chain []accessor, v Value) error {
	target := root
	for i := len(chain) - 1; i >= 0; i-- {
		a := chain[i]
		if a.isField {
			if a.fieldIdx < 0 || a.fieldIdx >= len(target.Fld) {
				return fmt.Errorf("field index %d out of bounds", a.fieldIdx)
			}
			target = &target.Fld[a.fieldIdx]
		} else {
			if a.arrIdx < 0 || a.arrIdx >= len(target.Arr) {
				return fmt.Errorf("index %d out of bounds [0..%d]", a.arrIdx, len(target.Arr)-1)
			}
			target = &target.Arr[a.arrIdx]
		}
	}
	*target = v
	return nil
}

func evalExpr(ctx *EvalCtx, e Expr) (Value, error) {
	switch n := e.(type) {
	case *Lit:
		return n.V, nil
	case *SlotRef:
		return ctx.Frame.Slots[n.Slot], nil
	case *GlobalRef:
		v, err := ctx.Host.ReadGlobal(n.Name)
		if err != nil {
			return Value{}, err
		}
		return coerceValue(v, n.T), nil
	case *BinOp:
		l, err := evalExpr(ctx, n.L)
		if err != nil {
			return Value{}, err
		}
		r, err := evalExpr(ctx, n.R)
		if err != nil {
			return Value{}, err
		}
		return evalBin(n.Op, l, r, n.T), nil
	case *UnOp:
		x, err := evalExpr(ctx, n.X)
		if err != nil {
			return Value{}, err
		}
		return evalUn(n.Op, x, n.T), nil
	case *IndexRef:
		arr, err := evalExpr(ctx, n.Array)
		if err != nil {
			return Value{}, err
		}
		iv, err := evalExpr(ctx, n.Index)
		if err != nil {
			return Value{}, err
		}
		if iv.I < 0 || int(iv.I) >= len(arr.Arr) {
			return Value{}, fmt.Errorf("index %d out of bounds [0..%d]", iv.I, len(arr.Arr)-1)
		}
		return arr.Arr[iv.I], nil
	case *MemberRef:
		obj, err := evalExpr(ctx, n.Object)
		if err != nil {
			return Value{}, err
		}
		if obj.Kind == TypeFB {
			if obj.FB == nil || n.FieldIdx < 0 || n.FieldIdx >= len(obj.FB.Slots) {
				return Value{}, fmt.Errorf("FB member index %d out of bounds", n.FieldIdx)
			}
			return obj.FB.Slots[n.FieldIdx], nil
		}
		if n.FieldIdx < 0 || n.FieldIdx >= len(obj.Fld) {
			return Value{}, fmt.Errorf("field index %d out of bounds", n.FieldIdx)
		}
		return obj.Fld[n.FieldIdx], nil
	case *Call:
		args := make([]Value, len(n.Args))
		for i, a := range n.Args {
			v, err := evalExpr(ctx, a)
			if err != nil {
				return Value{}, err
			}
			args[i] = v
		}
		if n.Fn == nil {
			return Value{}, fmt.Errorf("call %q has no resolved Fn", n.Name)
		}
		return n.Fn(args)
	case *UserCall:
		if n.Def == nil || n.Def.Run == nil {
			return Value{}, fmt.Errorf("user function call has no resolved Def")
		}
		frame := NewFuncFrame(n.Def)
		for i, a := range n.Args {
			v, err := evalExpr(ctx, a)
			if err != nil {
				return Value{}, err
			}
			frame.Slots[i] = coerceValue(v, n.Def.Inputs[i].Type)
		}
		if err := n.Def.Run(frame, ctx.Host); err != nil {
			return Value{}, err
		}
		return frame.Slots[n.Def.ReturnSlot], nil
	}
	return Value{}, fmt.Errorf("unknown expr %T", e)
}

func evalBin(op BinKind, l, r Value, t *Type) Value {
	switch op {
	case OpAdd:
		if t.Kind == TypeReal {
			return RealVal(asFloat(l) + asFloat(r))
		}
		return Value{Kind: t.Kind, I: l.I + r.I}
	case OpSub:
		if t.Kind == TypeReal {
			return RealVal(asFloat(l) - asFloat(r))
		}
		return Value{Kind: t.Kind, I: l.I - r.I}
	case OpMul:
		if t.Kind == TypeReal {
			return RealVal(asFloat(l) * asFloat(r))
		}
		return Value{Kind: t.Kind, I: l.I * r.I}
	case OpDiv:
		if t.Kind == TypeReal {
			rf := asFloat(r)
			if rf == 0 {
				return RealVal(0)
			}
			return RealVal(asFloat(l) / rf)
		}
		if r.I == 0 {
			return Value{Kind: t.Kind}
		}
		return Value{Kind: t.Kind, I: l.I / r.I}
	case OpMod:
		if r.I == 0 {
			return Value{Kind: t.Kind}
		}
		return Value{Kind: t.Kind, I: l.I % r.I}
	case OpEq:
		return BoolVal(valEq(l, r))
	case OpNeq:
		return BoolVal(!valEq(l, r))
	case OpLt:
		if l.Kind == TypeReal || r.Kind == TypeReal {
			return BoolVal(asFloat(l) < asFloat(r))
		}
		return BoolVal(l.I < r.I)
	case OpLte:
		if l.Kind == TypeReal || r.Kind == TypeReal {
			return BoolVal(asFloat(l) <= asFloat(r))
		}
		return BoolVal(l.I <= r.I)
	case OpGt:
		if l.Kind == TypeReal || r.Kind == TypeReal {
			return BoolVal(asFloat(l) > asFloat(r))
		}
		return BoolVal(l.I > r.I)
	case OpGte:
		if l.Kind == TypeReal || r.Kind == TypeReal {
			return BoolVal(asFloat(l) >= asFloat(r))
		}
		return BoolVal(l.I >= r.I)
	case OpAnd:
		return BoolVal(l.B && r.B)
	case OpOr:
		return BoolVal(l.B || r.B)
	case OpXor:
		if t.Kind == TypeBool {
			return BoolVal(l.B != r.B)
		}
		return Value{Kind: t.Kind, I: l.I ^ r.I}
	}
	return Value{}
}

func evalUn(op UnKind, x Value, t *Type) Value {
	switch op {
	case OpNeg:
		if t.Kind == TypeReal {
			return RealVal(-x.F)
		}
		return Value{Kind: t.Kind, I: -x.I}
	case OpNot:
		return BoolVal(!x.B)
	}
	return Value{}
}

func valEq(l, r Value) bool {
	if l.Kind == TypeReal || r.Kind == TypeReal {
		return asFloat(l) == asFloat(r)
	}
	if l.Kind != r.Kind {
		return false
	}
	switch l.Kind {
	case TypeBool:
		return l.B == r.B
	case TypeInt, TypeTime:
		return l.I == r.I
	case TypeString:
		return l.S == r.S
	}
	return false
}

func asFloat(v Value) float64 {
	if v.Kind == TypeReal {
		return v.F
	}
	return float64(v.I)
}

// coerceValue narrows a host-supplied Value to the IR-declared type t.
// The host doesn't know which static type a program assigned to a global,
// so we may receive (e.g.) a Real for an INT-declared var. This function
// reconciles Kind, falling back to Zero(t) when coercion isn't sensible.
// CoerceValue converts v to type t using IEC 61131-3 implicit coercion
// rules. Exported so the test-runner can bind Starlark-supplied arguments
// to FB/FUNCTION input slots in a way that matches in-program calls.
func CoerceValue(v Value, t *Type) Value { return coerceValue(v, t) }

func coerceValue(v Value, t *Type) Value {
	if t == nil || v.Kind == t.Kind {
		return v
	}
	switch t.Kind {
	case TypeInt, TypeTime:
		switch v.Kind {
		case TypeInt, TypeTime:
			return Value{Kind: t.Kind, I: v.I}
		case TypeReal:
			return Value{Kind: t.Kind, I: int64(v.F)}
		case TypeBool:
			if v.B {
				return Value{Kind: t.Kind, I: 1}
			}
			return Value{Kind: t.Kind}
		}
	case TypeReal:
		switch v.Kind {
		case TypeInt, TypeTime:
			return RealVal(float64(v.I))
		case TypeBool:
			if v.B {
				return RealVal(1)
			}
			return RealVal(0)
		}
	case TypeBool:
		switch v.Kind {
		case TypeInt, TypeTime:
			return BoolVal(v.I != 0)
		case TypeReal:
			return BoolVal(v.F != 0)
		}
	case TypeString:
		if v.Kind == TypeString {
			return v
		}
	}
	return Zero(t)
}
