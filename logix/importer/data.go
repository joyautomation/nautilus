package importer

import (
	"fmt"
	"strconv"
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
	if m == "MOVE" || m == "MOV" {
		// MOVE(M.Flags, D): the message's status word (msg.go).
		if text, ok, err := rt.msgFlags(in); ok {
			return text, err
		}
	}
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
	if err := rt.presetWrite(target); err != nil {
		return "", err
	}
	t, err := rt.ref(target, "")
	if err != nil {
		return "", err
	}
	if conv := rt.storeConversion(m, args, target); conv != "" {
		value = conv + "(" + value + ")"
		if strings.HasPrefix(conv, "REAL_TO_") || strings.HasPrefix(conv, "LREAL_TO_") {
			rt.realStoreNote()
		}
	}
	rt.markWritten(target)
	return "{ " + t + " := " + value + " }", nil
}

// storeConversion is the IEC conversion a data box's store makes, or ""
// when it makes none nautilus would not make too (#777). A Logix box
// converts as it stores: a REAL result into an integer rounds, halves to
// even (REAL_TO_INT); an integer result into a narrower integer keeps the
// low bits (DINT_TO_INT, which wraps). nautilus refuses the first as a
// bare assignment and does not narrow the second, so the import writes the
// conversion; the writer folds it back into the box. The result's type is
// REAL when an operand is, else the widest operand that names a tag (an
// integer literal is read at the destination's width).
func (rt *routine) storeConversion(m string, args []string, target string) string {
	var srcs []string
	switch m {
	case "MOVE", "MOV", "NEG", "ABS":
		srcs = args[:1]
	case "ADD", "SUB", "MUL", "DIV", "MOD":
		srcs = args[:2]
	case "CPT":
		// The expression's operands: tags and literals, as they appear.
		e, err := ld.ParseExpr(args[1])
		if err != nil {
			return ""
		}
		srcs = exprOperands(e)
		for _, a := range srcs {
			if rt.operandType(a) == "TIME" {
				return "" // a timer's ACC in arithmetic: left as it is
			}
		}
	default:
		return ""
	}
	dst := rt.operandType(target)
	if dst == "TIME" {
		dst = "DINT" // a timer's PRE as a destination: its DINT preset variable
	}
	if intWidth[dst] == 0 {
		return ""
	}
	from := ""
	for _, a := range srcs {
		t := rt.operandType(a)
		if t == "" && numLit.MatchString(a) && !fitsInt(a, dst) {
			// An integer literal the destination cannot hold is a DINT:
			// MOV(70000,i) keeps the low 16 bits on the controller.
			t = "DINT"
		}
		switch {
		case t == "TIME":
			// A timer's ACC or PRE: DINT milliseconds in Logix, a TIME in
			// nautilus, which no integer takes without the conversion.
			if len(srcs) > 1 {
				return ""
			}
			return "TIME_TO_" + dst
		case t == "LREAL" || from == "LREAL":
			from = "LREAL"
		case t == "REAL" || from == "REAL":
			from = "REAL"
		case intWidth[t] > intWidth[from]:
			from = t
		}
	}
	if from == "REAL" || from == "LREAL" || intWidth[from] > intWidth[dst] {
		return from + "_TO_" + dst
	}
	return ""
}

// realStoreNote says, once per import, where a REAL stored into an
// integer still differs: out of the integer's range, the controller keeps
// the low bits of the rounded value, and nautilus faults the scan.
func (rt *routine) realStoreNote() {
	if rt.im.realNoted {
		return
	}
	rt.im.realNoted = true
	rt.im.proj.note("", -1, "conversion", "a REAL stored into an integer comes across as REAL_TO_<type> (it rounds halves to even, as the controller does); a value out of the integer's range faults the nautilus scan, where the controller stores the low bits of the result")
}

// exprOperands lists an expression's tag references and literals, in
// their Logix spelling.
func exprOperands(e ld.Expr) []string {
	var out []string
	var walk func(ld.Expr)
	walk = func(e ld.Expr) {
		switch x := e.(type) {
		case *ld.Lit:
			out = append(out, x.Text)
		case *ld.Ref:
			out = append(out, x.Text)
		case *ld.Call:
			for _, a := range x.Args {
				walk(a)
			}
		case *ld.Unary:
			walk(x.X)
		case *ld.Binary:
			walk(x.L)
			walk(x.R)
		}
	}
	walk(e)
	return out
}

// fitsInt reports an integer literal that a dst integer holds as is
// (true for anything it cannot read, which then stays as written).
func fitsInt(lit, dst string) bool {
	n, ok := parseIntLit(lit)
	if !ok {
		return true
	}
	w := intWidth[dst]
	if w == 0 || w >= 64 {
		return true
	}
	lo, hi := -(int64(1) << (w - 1)), int64(1)<<(w-1)-1
	return n >= lo && n <= hi
}

// parseIntLit reads a decimal or base#digits integer literal.
func parseIntLit(s string) (int64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "_", "")
	base := 10
	if i := strings.Index(s, "#"); i > 0 {
		b, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, false
		}
		base, s = b, s[i+1:]
	}
	n, err := strconv.ParseInt(s, base, 64)
	return n, err == nil
}

// intWidth is a Logix integer type's width, for which way a store narrows.
var intWidth = map[string]int{"SINT": 8, "INT": 16, "DINT": 32, "LINT": 64}

// operandType is the Logix elementary type an operand reads as: a
// literal's (REAL when it has a point or an exponent, "" for an integer,
// which takes the width it is stored at), or the type a tag reference
// reaches through its indexes, members and bits (a timer's ACC or PRE is
// TIME, as nautilus holds it). "" when it is not an elementary value or
// cannot be told.
func (rt *routine) operandType(a string) string {
	a = strings.TrimSpace(a)
	if numLit.MatchString(a) {
		if !strings.Contains(a, "#") && strings.ContainsAny(a, ".eE") {
			return "REAL"
		}
		return ""
	}
	f := rt.sc.lookup(baseOf(a))
	if f == nil || f.alias != nil {
		return ""
	}
	dtype, dims, rest := f.dtype, f.dims, a[len(baseOf(a)):]
	for rest != "" {
		switch rest[0] {
		case '[':
			j := strings.IndexByte(rest, ']')
			if j < 0 || dims == "" {
				return ""
			}
			dims, rest = "", rest[j+1:]
		case '.':
			j := 1
			for j < len(rest) && rest[j] != '.' && rest[j] != '[' {
				j++
			}
			name := rest[1:j]
			rest = rest[j:]
			if name != "" && name[0] >= '0' && name[0] <= '9' {
				dtype, dims = "BOOL", "" // a bit of an integer
				continue
			}
			if strings.EqualFold(dtype, "TIMER") && (strings.EqualFold(name, "ACC") || strings.EqualFold(name, "PRE")) {
				dtype, dims = "TIME", "" // ET and PT in nautilus
				continue
			}
			dt, dm, ok := rt.im.memberType(dtype, name)
			if !ok {
				return ""
			}
			dtype, dims = dt, dm
		default:
			return ""
		}
	}
	if dims != "" {
		return ""
	}
	up := strings.ToUpper(dtype)
	if up == "REAL" || up == "LREAL" || up == "TIME" || intWidth[up] > 0 {
		return up
	}
	return ""
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
