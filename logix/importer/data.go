package importer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/lang/ld"
)

// Data instructions come across as the ladder assignment `{ y := expr }`:
// MOVE(a,b) is `{ b := a }`, ADD(a,b,d) is `{ d := a + b }`, CPT(d, expr)
// carries its expression with Logix's function names turned into IEC's,
// CLR(d) is `{ d := 0 }`. A compare with an expression (CMP) is the
// compare it means, with its sides as IEC expressions.

// dataInstr lowers one data instruction to an assignment element, or
// reports that this one has no mapping.
func (rt *routine) dataInstr(in *l5x.Instr) (string, *refusal) {
	m := strings.ToUpper(in.Mnemonic)
	args := in.Args
	need := func(n int) *refusal {
		if len(args) != n {
			return refuse(m, "%d operands", len(args))
		}
		return nil
	}
	var target string
	var value string
	switch m {
	case "MOVE", "MOV":
		if err := need(2); err != nil {
			return "", err
		}
		src, err := rt.exprOperand(args[0])
		if err != nil {
			return "", err
		}
		target, value = args[1], src
	case "ADD", "SUB", "MUL", "DIV", "MOD", "XPY":
		if err := need(3); err != nil {
			return "", err
		}
		a, err := rt.exprOperand(args[0])
		if err != nil {
			return "", err
		}
		b, err := rt.exprOperand(args[1])
		if err != nil {
			return "", err
		}
		op := map[string]string{"ADD": "+", "SUB": "-", "MUL": "*", "DIV": "/", "MOD": "MOD", "XPY": "**"}[m]
		target, value = args[2], a+" "+op+" "+b
	case "NEG":
		if err := need(2); err != nil {
			return "", err
		}
		a, err := rt.exprOperand(args[0])
		if err != nil {
			return "", err
		}
		target, value = args[1], "-"+a
	case "ABS", "SQR":
		if err := need(2); err != nil {
			return "", err
		}
		a, err := rt.exprOperand(args[0])
		if err != nil {
			return "", err
		}
		fn := map[string]string{"ABS": "ABS", "SQR": "SQRT"}[m]
		target, value = args[1], fn+"("+a+")"
	case "CLR":
		if err := need(1); err != nil {
			return "", err
		}
		target, value = args[0], "0"
		if f := rt.sc.lookup(baseOf(args[0])); f != nil {
			switch strings.ToUpper(f.dtype) {
			case "REAL", "LREAL":
				value = "0.0"
			}
		}
	case "CPT":
		if err := need(2); err != nil {
			return "", err
		}
		v, err := rt.logixExpr(args[1])
		if err != nil {
			return "", err
		}
		target, value = args[0], v
	default:
		return "", refuse(m, "a data operation with no assignment form")
	}
	t, err := rt.ref(target, "")
	if err != nil {
		return "", err
	}
	rt.markWritten(target)
	return "{ " + t + " := " + value + " }", nil
}

// exprOperand renders one instruction operand: a literal as is, a
// reference rewritten.
func (rt *routine) exprOperand(a string) (string, *refusal) {
	if numLit.MatchString(a) {
		return a, nil
	}
	return rt.ref(a, "")
}

// logixFnToIEC maps Logix expression functions to IEC's.
var logixFnToIEC = map[string]string{
	"ABS": "ABS", "SQR": "SQRT", "LN": "LN", "LOG": "LOG", "SIN": "SIN", "COS": "COS", "TAN": "TAN",
	"ASN": "ASIN", "ACS": "ACOS", "ATN": "ATAN", "TRN": "TRUNC",
}

// logixExpr parses a Logix expression (CPT's or CMP's) and prints it as
// IEC, references rewritten through the scope.
func (rt *routine) logixExpr(src string) (string, *refusal) {
	e, err := ld.ParseExpr(src)
	if err != nil {
		return "", refuse("expr", "%q: %v", src, err)
	}
	out, bad := rt.iecExpr(e)
	if bad != nil {
		return "", bad
	}
	return out.IEC(), nil
}

// iecExpr rewrites the tree: functions renamed, references resolved.
func (rt *routine) iecExpr(e ld.Expr) (ld.Expr, *refusal) {
	switch x := e.(type) {
	case *ld.Lit:
		return x, nil
	case *ld.Ref:
		ref, err := rt.ref(x.Text, "")
		if err != nil {
			return nil, err
		}
		return &ld.Ref{Text: ref}, nil
	case *ld.Unary:
		in, err := rt.iecExpr(x.X)
		if err != nil {
			return nil, err
		}
		return &ld.Unary{Op: x.Op, X: in}, nil
	case *ld.Binary:
		l, err := rt.iecExpr(x.L)
		if err != nil {
			return nil, err
		}
		r, err := rt.iecExpr(x.R)
		if err != nil {
			return nil, err
		}
		return &ld.Binary{Op: x.Op, L: l, R: r}, nil
	case *ld.Call:
		name := strings.ToUpper(x.Name)
		var args []ld.Expr
		for _, a := range x.Args {
			v, err := rt.iecExpr(a)
			if err != nil {
				return nil, err
			}
			args = append(args, v)
		}
		if name == "XPY" && len(args) == 2 {
			return &ld.Binary{Op: "**", L: args[0], R: args[1]}, nil
		}
		to, ok := logixFnToIEC[name]
		if !ok || len(args) != 1 {
			return nil, refuse("expr", "the function %s has no IEC form", x.Name)
		}
		return &ld.Call{Name: to, Args: args}, nil
	}
	return nil, refuse("expr", "unreadable expression")
}

// cmpInstr lowers CMP(expr): the top-level comparison becomes the
// compare contact, its sides IEC expressions.
func (rt *routine) cmpInstr(in *l5x.Instr) (string, *refusal) {
	if len(in.Args) != 1 {
		return "", refuse("CMP", "%d operands", len(in.Args))
	}
	e, err := ld.ParseExpr(in.Args[0])
	if err != nil {
		return "", refuse("CMP", "%q: %v", in.Args[0], err)
	}
	b, ok := e.(*ld.Binary)
	fn, isCmp := cmpOp[b2op(b)]
	if !ok || !isCmp {
		return "", refuse("CMP", "an expression that is not a comparison at its top: %s", in.Args[0])
	}
	l, bad := rt.iecExpr(b.L)
	if bad != nil {
		return "", bad
	}
	r, bad := rt.iecExpr(b.R)
	if bad != nil {
		return "", bad
	}
	return fmt.Sprintf("%s(%s, %s)", fn, l.IEC(), r.IEC()), nil
}

func b2op(b *ld.Binary) string {
	if b == nil {
		return ""
	}
	return b.Op
}

// limitInstr lowers LIMIT(low, test, high) with literal bounds to the two
// compares it is. With a variable bound Logix wraps when low > high
// (test >= low OR test <= high), which is not a clamp check; refused.
func (rt *routine) limitInstr(in *l5x.Instr) ([]string, *refusal) {
	if len(in.Args) != 3 {
		return nil, refuse("LIMIT", "%d operands", len(in.Args))
	}
	lo, test, hi := in.Args[0], in.Args[1], in.Args[2]
	if !numLit.MatchString(lo) || !numLit.MatchString(hi) {
		return nil, refuse("LIMIT", "a variable bound: Logix LIMIT wraps when low > high")
	}
	var loV, hiV float64
	fmt.Sscan(lo, &loV)
	fmt.Sscan(hi, &hiV)
	if loV > hiV {
		return nil, refuse("LIMIT", "low %s above high %s: Logix tests outside the band", lo, hi)
	}
	t, err := rt.exprOperand(test)
	if err != nil {
		return nil, err
	}
	return []string{"GE(" + t + ", " + lo + ")", "LE(" + t + ", " + hi + ")"}, nil
}
