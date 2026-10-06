package st

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// Execution control — EN/ENO (IEC 61131-3 §6.6.1.2.4; docs/functions.md
// "EN/ENO").
//
// Any function or function-block call may bind EN (BOOL, default TRUE) and
// ENO (a BOOL output):
//
//	t1(EN := Enable, IN := Start, PT := T#5S, ENO => t1Ok);
//	Out := LIMIT(EN := Enable, MN := 0.0, IN := Raw, MX := 100.0, ENO => ok);
//
// When EN is FALSE the call does not execute: an FB's body does not run
// and its outputs (and every `=>` binding) keep their values; a function's
// result is not assigned, so the assignment's target keeps its value. ENO
// is EN AND "no error" — and a call that errors faults the scan here (MUX
// out of range, an unparseable STRING_TO_*), so a call that returns has
// ENO = EN. FBD adds DIV/MOD's divide-by-zero (lang/fbd).
//
// A function's EN/ENO needs the call to be a statement of its own — the
// whole right-hand side of an assignment, or a user FUNCTION called as a
// statement — because "not assigned" has to mean something. A block that
// DECLARES its own EN input or ENO output (a ladder-style user block, see
// docs/functions.md "Power pins in ladder") keeps them as ordinary pins:
// its body decides what they mean.

// execControl is a call's EN argument and ENO binding, split off the call.
type execControl struct {
	en  Expression // nil: EN not bound (TRUE)
	eno Expression // nil: ENO not bound
	pos Pos        // the EN (or ENO) argument, for diagnostics
}

func (x execControl) any() bool { return x.en != nil || x.eno != nil }

// splitExecControl removes EN := … and ENO => … from call (when the callee
// does not declare its own pins of those names) and returns them. call is
// copied, never modified in place: the AST may be lowered again.
func splitExecControl(call *CallExpr, ownEN, ownENO bool) (*CallExpr, execControl, error) {
	var x execControl
	out := *call
	out.NamedArgs = nil
	out.OutputBindings = nil
	for _, na := range call.NamedArgs {
		if !ownEN && strings.EqualFold(na.Name, "EN") {
			if x.en != nil {
				return nil, x, errName(na.Pos, na.Name, fmt.Errorf("%s: EN given twice", call.Name))
			}
			x.en, x.pos = na.Value, na.Pos
			continue
		}
		out.NamedArgs = append(out.NamedArgs, na)
	}
	for _, ob := range call.OutputBindings {
		if !ownENO && strings.EqualFold(ob.Name, "ENO") {
			if x.eno != nil {
				return nil, x, errName(ob.Pos, ob.Name, fmt.Errorf("%s: ENO bound twice", call.Name))
			}
			x.eno = ob.Target
			if x.en == nil {
				x.pos = ob.Pos
			}
			continue
		}
		out.OutputBindings = append(out.OutputBindings, ob)
	}
	return &out, x, nil
}

// hasExecControl reports whether call binds EN or ENO at all (before any
// callee-specific exemption) — the cheap test lowerAssign uses to decide
// whether an assignment needs the gated path.
func hasExecControl(call *CallExpr) bool {
	for _, na := range call.NamedArgs {
		if strings.EqualFold(na.Name, "EN") {
			return true
		}
	}
	for _, ob := range call.OutputBindings {
		if strings.EqualFold(ob.Name, "ENO") {
			return true
		}
	}
	return false
}

// gate wraps body in execution control: IF EN THEN body; ENO := TRUE ELSE
// ENO := FALSE END_IF. EN is evaluated once, before the call.
func (l *lowerer) gate(name string, x execControl, body []ir.Stmt) (ir.Stmt, error) {
	var cond ir.Expr = &ir.Lit{V: ir.BoolVal(true), T: ir.BoolT}
	if x.en != nil {
		c, err := l.lowerExpr(x.en)
		if err != nil {
			return nil, fmt.Errorf("%s EN: %w", name, err)
		}
		if c.ExprType().Kind != ir.TypeBool {
			return nil, errNode(x.en, fmt.Errorf("%s: EN must be BOOL, got %s", name, c.ExprType()))
		}
		cond = c
	}
	var elseB []ir.Stmt
	if x.eno != nil {
		target, err := l.lowerLValue(x.eno)
		if err != nil {
			return nil, fmt.Errorf("%s ENO target: %w", name, err)
		}
		if target.ExprType().Kind != ir.TypeBool {
			return nil, errNode(x.eno, fmt.Errorf("%s: ENO is BOOL, its target is %s", name, target.ExprType()))
		}
		body = append(body, &ir.Assign{Target: target, Value: &ir.Lit{V: ir.BoolVal(true), T: ir.BoolT}})
		elseB = []ir.Stmt{&ir.Assign{Target: target, Value: &ir.Lit{V: ir.BoolVal(false), T: ir.BoolT}}}
	}
	return &ir.If{Cond: cond, Then: body, Else: elseB}, nil
}

// fbDeclares reports whether an FB type declares an input named EN and an
// output named ENO (case-insensitively, as the IEC formal names are).
func fbDeclares(def *ir.FBDef) (en, eno bool) {
	for _, s := range def.Inputs {
		if strings.EqualFold(s.Name, "EN") {
			en = true
		}
	}
	for _, s := range def.Outputs {
		if strings.EqualFold(s.Name, "ENO") {
			eno = true
		}
	}
	return en, eno
}

// funcDeclaresEN reports whether a user FUNCTION declares its own EN input.
func funcDeclaresEN(def *ir.FuncDef) bool {
	for _, s := range def.Inputs {
		if strings.EqualFold(s.Name, "EN") {
			return true
		}
	}
	return false
}

// lowerGatedAssign lowers `target := F(EN := …, …, ENO => …)`: the
// assignment is the call's body, skipped while EN is FALSE. ok is false
// when the right-hand side is no call with execution control (the caller
// lowers the assignment as usual).
func (l *lowerer) lowerGatedAssign(a *AssignStmt) (ir.Stmt, bool, error) {
	call, isCall := a.Value.(*CallExpr)
	if !isCall || !hasExecControl(call) {
		return nil, false, nil
	}
	ownEN := false
	if l.userFuncs != nil {
		if def, ok := l.userFuncs[call.Name]; ok && def != nil {
			ownEN = funcDeclaresEN(def)
		}
	}
	rest, x, err := splitExecControl(call, ownEN, false)
	if err != nil {
		return nil, true, err
	}
	if !x.any() {
		return nil, false, nil
	}
	inner := *a
	inner.Value = rest
	assign, err := l.lowerAssign(&inner)
	if err != nil {
		return nil, true, err
	}
	s, err := l.gate(call.Name, x, []ir.Stmt{assign})
	return s, true, err
}

// lowerFormalBuiltinArgs turns a formal call of a standard function —
// `LIMIT(MN := 0.0, IN := x, MX := 10.0)` — into its positional
// arguments, by ir.FormalNames. Formal and non-formal arguments do not mix
// (IEC 61131-3 §6.6.1.4.2: a call is one or the other).
func lowerFormalBuiltinArgs(n *CallExpr, sig ir.BuiltinSig) ([]Expression, error) {
	if len(n.NamedArgs) == 0 {
		return n.Args, nil
	}
	if len(n.Args) > 0 {
		return nil, errName(n.Pos, n.Name, fmt.Errorf("function %s: give every input by name or none (a call is formal or positional, not both)", sig.Name))
	}
	count := len(sig.Params)
	if sig.Variadic {
		count = max(len(n.NamedArgs), len(sig.Params))
	}
	names := ir.FormalNames(sig.Name, count)
	args := make([]Expression, count)
	for _, na := range n.NamedArgs {
		idx := -1
		for i, f := range names {
			if strings.EqualFold(f, na.Name) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, errName(na.Pos, na.Name, fmt.Errorf("function %s has no input %q (its inputs are %s)", sig.Name, na.Name, strings.Join(names, ", ")))
		}
		if args[idx] != nil {
			return nil, errName(na.Pos, na.Name, fmt.Errorf("function %s: input %q given twice", sig.Name, na.Name))
		}
		args[idx] = na.Value
	}
	for i, a := range args {
		if a == nil {
			return nil, errName(n.Pos, n.Name, fmt.Errorf("function %s: missing input %s", sig.Name, names[i]))
		}
	}
	return args, nil
}

// errExecInExpr is the diagnostic for EN/ENO on a call nested inside an
// expression: there is no "not assigned" there.
func errExecInExpr(n *CallExpr) error {
	return errName(n.Pos, n.Name, fmt.Errorf("%s: EN/ENO needs the call to be a statement of its own "+
		"(Out := %s(EN := …, …)) — while EN is FALSE its result is not assigned, and inside a larger "+
		"expression there is nothing to leave unassigned", n.Name, n.Name))
}
