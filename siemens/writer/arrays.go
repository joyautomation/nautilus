package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// Arrays (#1299).
//
// An ARRAY is TIA's Array of the same bounds: ARRAY[1..3, 0..4] OF REAL is
// Array[1..3, 0..4] of Real, a[i, j] is #a[i, j]. Bounds are folded to
// literals as nautilus folds them (a named constant, a project constant,
// arithmetic on them). Elements are what the writer declares anywhere
// else: an elementary type, String, a STRUCT's PLC data type, an
// enumeration's integer (each element starts at the enumeration's first
// member, as nautilus starts it). An array of function block instances,
// an array of arrays and an array temp are #1305.
//
// Out of bounds. nautilus faults the scan on an index out of bounds: the
// rest of the scan is skipped, what it wrote before stays, LogicErrors
// counts it, and the next scan runs (runtime/runtime.go, faultlog.go). The
// bench CPU STOPs on one instead (randd/siemens-target/oob-1299: every
// access out of bounds, read or write, high or low, puts an S7-1500 with
// no OB121 in STOP). So every index the writer cannot prove in bounds is
// computed into a temp first (as LINT: nautilus computes it at 64 bits),
// and checked:
//
//	#naut_ix1 := <index>;
//	IF #naut_ix1 < lo OR #naut_ix1 > hi THEN
//		#naut_fault.faulted := TRUE; ... line, block ...
//		RETURN;
//	END_IF;
//
// Each block that can fault keeps its fault in its own naut_fault (a
// "<prefix>Fault": faulted, line, block), cleared first thing: a static
// of an FB or a program, an output of an FC. RETURN ends the block; each
// caller reads the callee's naut_fault right after the call, takes it
// over and returns too, up to the program's FB. Its OB counts the
// program's fault into the task's record in the faults DB
// ("<prefix>Faults": LogicErrors, Faulting, LastLine, LastBlock, the
// counterpart of nautilus's task stats), after the task's shared tags are
// copied out: nautilus commits what a faulted scan wrote. The fault never
// passes through a global, so an OB of a higher priority that interrupts
// another between a call and its check cannot clear or overwrite it, and
// a block run by two tasks keeps one fault per instance (naut-lead).
//
// What has to be computed before the index is computed first, in
// nautilus's order (lang/ir vm.go): an assignment's value before its
// target's index; an FB's inputs one by one (each copied into the
// instance as it goes, so a fault in a later input leaves the earlier
// ones in the instance: the writer then assigns each input to the
// instance's pin before the call); a FUNCTION that can fault into a temp,
// its flag checked before the expression that uses it goes on (nautilus
// never assigns the target). nautilus does not short-circuit AND/OR, so a
// check lifted above its statement is exact. An index is proved in
// bounds, with no check, when it is a literal inside the bounds, or the
// variable of a FOR loop whose constant start and end are inside the
// bounds and whose body never writes it.
//
// What would move a check across something nautilus does first is
// refused (#1305): a FUNCTION that writes a tag, called before the check
// in one statement; an index in a REPEAT's UNTIL; an index inside an
// in-out argument (nautilus reads it twice); an in-out bound to a block
// that can fault (nautilus copies it back only when the block finishes,
// TIA passes it by reference).

// faultsDB is the faults DB's name without the prefix, faultType the
// PLC data type a block keeps its fault in, faultVar that variable.
const (
	faultsDB  = "Faults"
	faultType = "Fault"
	faultVar  = "naut_fault"
)

// arrayDim is one dimension's bounds, folded.
type arrayDim struct{ lo, hi int64 }

// arrayDef is a declared ARRAY: its dimensions and its element type as
// declared.
type arrayDef struct {
	dims []arrayDim
	elem string
}

// isArrayType reports a declared type that is an ARRAY.
func isArrayType(t string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(t)), "ARRAY")
}

// typeString is an array's type with its first n dimensions taken off:
// the type of a[i] when a has more dimensions than that.
func (a *arrayDef) typeString(n int) string {
	var parts []string
	for _, d := range a.dims[n:] {
		parts = append(parts, fmt.Sprintf("%d..%d", d.lo, d.hi))
	}
	return "ARRAY[" + strings.Join(parts, ", ") + "] OF " + strings.ToUpper(a.elem)
}

// constLookup resolves a name to an integer constant, for an array bound.
type constLookup func(name string) (int64, bool)

// arrayOf parses a declared ARRAY type, its bounds folded through local
// (a block's own constants; nil for none) and the project's constants.
func (w *writer) arrayOf(typ string, local constLookup) (*arrayDef, string) {
	prog, err := st.Parse("PROGRAM x\nVAR\n    v : " + strings.TrimSpace(typ) + ";\nEND_VAR\nEND_PROGRAM\n")
	if err != nil || len(prog.VarBlocks) != 1 || len(prog.VarBlocks[0].Variables) != 1 {
		return nil, gapMessage(gapOther, "the type "+typ, "")
	}
	at, ok := prog.VarBlocks[0].Variables[0].Type.(*st.ArrayType)
	if !ok {
		return nil, gapMessage(gapOther, "the type "+typ, "")
	}
	if _, nested := at.Elem.(*st.ArrayType); nested {
		return nil, gapMessage(gapOther, "an array of arrays", "declare ARRAY[a..b, c..d] OF … instead")
	}
	def := &arrayDef{elem: at.Elem.String()}
	for _, d := range at.Dims {
		lo, ok1 := w.foldInt(d.Lo, local, map[string]bool{})
		hi, ok2 := w.foldInt(d.Hi, local, map[string]bool{})
		if !ok1 || !ok2 {
			return nil, gapMessage(gapOther, "the array bounds "+d.String(), "a bound the writer folds is a literal, a constant or arithmetic on them")
		}
		def.dims = append(def.dims, arrayDim{lo, hi})
	}
	return def, ""
}

// boundsRefusal checks a declaration's ARRAY bounds as the source wrote
// them: the writer reads a declared type from its text (VarDecl.Datatype),
// which spells a bound that is not a decimal literal, a name or a negated
// one as "?" or without its base, so such a bound is refused here rather
// than read wrong.
func boundsRefusal(te st.TypeExpr) string {
	at, ok := te.(*st.ArrayType)
	if !ok {
		return ""
	}
	simple := func(e st.Expression) bool {
		if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
			e = u.Operand
		}
		switch e := e.(type) {
		case *st.NumberLit:
			return e.Base == 10 || e.Base == 0
		case *st.IdentExpr:
			return true
		}
		return false
	}
	for _, d := range at.Dims {
		if !simple(d.Lo) || !simple(d.Hi) {
			return gapMessage(gapOther, "an array bound written as an expression or a based literal", "name it as a constant (N : INT := 2 + 1;) and use the name")
		}
	}
	return ""
}

// foldInt folds a constant integer expression as nautilus's lowering does:
// literals, the project's and the block's constants, + - * / MOD and
// negation at 64 bits.
func (w *writer) foldInt(e st.Expression, local constLookup, seen map[string]bool) (int64, bool) {
	switch e := e.(type) {
	case *st.NumberLit:
		s := strings.ReplaceAll(e.Value, "_", "")
		base := e.Base
		if base == 0 {
			base = 10
		}
		if base == 10 && strings.ContainsAny(s, ".eE") {
			return 0, false
		}
		n, err := strconv.ParseInt(s, base, 64)
		return n, err == nil
	case *st.TypedLit:
		if !isInt(strings.ToUpper(e.TypeName)) {
			return 0, false
		}
		return w.foldInt(e.Inner, local, seen)
	case *st.UnaryExpr:
		n, ok := w.foldInt(e.Operand, local, seen)
		switch e.Op {
		case "-":
			return -n, ok
		case "+":
			return n, ok
		}
	case *st.BinaryExpr:
		l, ok1 := w.foldInt(e.Left, local, seen)
		r, ok2 := w.foldInt(e.Right, local, seen)
		if !ok1 || !ok2 {
			return 0, false
		}
		switch strings.ToUpper(e.Op) {
		case "+":
			return l + r, true
		case "-":
			return l - r, true
		case "*":
			return l * r, true
		case "/":
			if r == 0 {
				return 0, false
			}
			return l / r, true
		case "MOD":
			if r == 0 {
				return 0, false
			}
			return l % r, true
		}
	case *st.IdentExpr:
		k := strings.ToLower(e.Name)
		if local != nil {
			if n, ok := local(k); ok {
				return n, true
			}
		}
		if d, ok := w.lib.constDecls[k]; ok && !seen[k] && d.Initial != nil && isInt(strings.ToUpper(strings.TrimSpace(d.Datatype))) {
			seen[k] = true
			defer delete(seen, k)
			return w.foldInt(d.Initial, local, seen)
		}
	}
	return 0, false
}

// localConst resolves the block's own VAR CONSTANTs (declared anywhere in
// it) for an array bound.
func (pw *progWriter) localConst(vbs []st.VarBlock) constLookup { return pw.w.localConsts(vbs) }

// localConsts resolves the VAR CONSTANTs that vbs declare.
func (w *writer) localConsts(vbs []st.VarBlock) constLookup {
	decls := map[string]st.VarDecl{}
	for _, vb := range vbs {
		if !vb.Constant {
			continue
		}
		for _, v := range vb.Variables {
			decls[strings.ToLower(v.Name)] = v
		}
	}
	var look constLookup
	seen := map[string]bool{}
	look = func(k string) (int64, bool) {
		d, ok := decls[k]
		if !ok || seen[k] || d.Initial == nil || !isInt(strings.ToUpper(strings.TrimSpace(d.Datatype))) {
			return 0, false
		}
		seen[k] = true
		defer delete(seen, k)
		return w.foldInt(d.Initial, look, map[string]bool{})
	}
	return look
}

// arrayOf is the array a type names at a use. The types the block's
// expressions carry are folded where they are declared (canonArray: a
// local in its block, a pin in its block, a member or a tag with the
// project's constants only), so no constant is looked up here: a bound
// folded in the using block's scope could name another N.
func (pw *progWriter) arrayOf(typ string) (*arrayDef, string) {
	return pw.w.arrayOf(typ, nil)
}

// canonArray is an ARRAY type with its bounds folded in the scope that
// declares it (local: that block's constants; nil for a member or a tag),
// as literals: ARRAY[0..N] OF INT is ARRAY[0..3] OF INT. Any other type,
// or one whose bounds do not fold, is returned as it is (arrayOf then
// refuses it).
func (w *writer) canonArray(typ string, local constLookup) string {
	if !isArrayType(typ) {
		return typ
	}
	a, why := w.arrayOf(typ, local)
	if why != "" {
		return typ
	}
	return a.typeString(0)
}

// fold folds a constant integer expression written in this block: a name
// the block declares as a variable (an input n shadows a project constant
// N), or a tag, is no constant, whatever the project declares.
func (pw *progWriter) fold(e st.Expression) (int64, bool) {
	if pw.consts0 == nil {
		return 0, false
	}
	shadowed := false
	walkExpr(e, func(x st.Expression) {
		id, ok := x.(*st.IdentExpr)
		if !ok {
			return
		}
		k := strings.ToLower(id.Name)
		if _, isConst := pw.consts0(k); isConst {
			return
		}
		if _, local := pw.locals[k]; local {
			if _, isConst := pw.constVals[k]; !isConst {
				shadowed = true
			}
		}
		if _, tag := pw.w.globals[k]; tag {
			shadowed = true
		}
		if _, isConst := pw.w.lib.constDecls[k]; !isConst {
			shadowed = true
		}
	})
	if shadowed {
		return 0, false
	}
	return pw.w.foldInt(e, pw.consts0, map[string]bool{})
}

// arrayDecl prints an ARRAY type for a declaration, or says why not.
func (w *writer) arrayDecl(typ string, local constLookup) (string, string) {
	a, why := w.arrayOf(typ, local)
	if why != "" {
		return "", why
	}
	u := strings.ToUpper(strings.TrimSpace(a.elem))
	_, inst := instances[u]
	_, latch := latches[u]
	if inst || latch || timers[u] || w.userFB(u) != nil || w.lib.fbs[strings.ToLower(u)] != "" {
		return "", gapMessage(gapOther, "an array of function block instances", "")
	}
	et, why := w.declType(a.elem, "")
	if why != "" {
		return "", why
	}
	var dims []string
	for _, d := range a.dims {
		dims = append(dims, fmt.Sprintf("%d..%d", d.lo, d.hi))
	}
	return "Array[" + strings.Join(dims, ", ") + "] of " + et, ""
}

// arrayStart is the start value of an array whose elements are an
// enumeration starting at a member other than 0, "" for any other array
// (TIA starts every element at its type's own start value, as nautilus
// does: 0, FALSE, ”, a PLC data type's member start values).
func (w *writer) arrayStart(typ string, local constLookup) string {
	a, why := w.arrayOf(typ, local)
	if why != "" {
		return ""
	}
	n, ok := w.enumStart(a.elem)
	if !ok || n == "0" {
		return ""
	}
	count := int64(1)
	for _, d := range a.dims {
		count *= d.hi - d.lo + 1
	}
	return fmt.Sprintf("[%d(%s)]", count, n)
}

// arrayOverrides are the BEGIN-section assignments of an array's start
// value (a manifest init: list, nested for more dimensions): element by
// element from the lower bound, as nautilus seeds it (ir.SeedFromInit,
// which has checked the list already); what the list leaves out keeps its
// start value.
func (w *writer) arrayOverrides(typ, path string, start any) ([]string, string) {
	a, why := w.arrayOf(typ, nil)
	if why != "" {
		return nil, why
	}
	var out []string
	var walk func(dim int, idx []string, v any) string
	walk = func(dim int, idx []string, v any) string {
		if dim == len(a.dims) {
			ls, why := w.overrides(a.elem, path+"["+strings.Join(idx, ", ")+"]", v)
			if why != "" {
				return why
			}
			out = append(out, ls...)
			return ""
		}
		list, ok := v.([]any)
		d := a.dims[dim]
		if !ok || int64(len(list)) > d.hi-d.lo+1 {
			return fmt.Sprintf("the start value %v of %s is not a list of at most %d values", v, typ, d.hi-d.lo+1)
		}
		for i, e := range list {
			if why := walk(dim+1, append(append([]string(nil), idx...), strconv.FormatInt(d.lo+int64(i), 10)), e); why != "" {
				return why
			}
		}
		return ""
	}
	if why := walk(0, nil, start); why != "" {
		return nil, why
	}
	return out, ""
}

// indexChain is an index expression with the indices of a partial index
// before it merged in (a[i][j] on a two-dimensional a is a[i, j]): the
// array, and every index in order.
func indexChain(e *st.IndexExpr, pw *progWriter) (st.Expression, []st.Expression) {
	if inner, ok := e.Array.(*st.IndexExpr); ok {
		if a, _ := pw.arrayOf(pw.rawTyp(inner.Array)); a != nil && len(inner.Indices) < len(a.dims) {
			base, idx := indexChain(inner, pw)
			return base, append(idx, e.Indices...)
		}
	}
	return e.Array, e.Indices
}

// index prints an array access. Every index it prints is one the
// statement has checked (lift) or one proved in bounds; anything else is
// refused, never written unchecked: an index out of bounds STOPs the CPU.
func (pw *progWriter) index(e *st.IndexExpr) string {
	base, idx := indexChain(e, pw)
	a, why := pw.arrayOf(pw.rawTyp(base))
	if why != "" || a == nil {
		if why == "" {
			why = "indexing something that is not an array"
		}
		pw.diag("siemens/array", e.Pos.Line, why)
		return ""
	}
	if len(idx) != len(a.dims) {
		pw.gap("siemens/array", e.Pos.Line, gapOther, "a row of a multi-dimensional array", "index every dimension")
		return ""
	}
	// An index is an index whatever arithmetic the access sits in.
	wide := pw.wide
	pw.wide = false
	defer func() { pw.wide = wide }()
	parts := make([]string, len(idx))
	for i, ix := range idx {
		if !pw.checkedIndex(ix, a.dims[i]) {
			pw.diag("siemens/array", e.Pos.Line, "an index the writer did not check against the array's bounds (a writer bug, please report it): refused, since the CPU stops on an index out of bounds")
			return ""
		}
		if _, ident := ix.(*st.IdentExpr); !ident {
			// A literal proved in bounds: one literal (-2, not the
			// negation nautilus computes at 64 bits).
			if n, ok := pw.fold(ix); ok {
				parts[i] = strconv.FormatInt(n, 10)
				continue
			}
		}
		parts[i] = pw.expr(ix, 0)
	}
	return pw.expr(base, unaryPrec+1) + "[" + strings.Join(parts, ", ") + "]"
}

// checkedIndex reports an index that needs no check here: a temp a check
// has just set, or an index proved inside d.
func (pw *progWriter) checkedIndex(ix st.Expression, d arrayDim) bool {
	if id, ok := ix.(*st.IdentExpr); ok && pw.ixTemps[strings.ToLower(id.Name)] {
		return true
	}
	return pw.inBounds(ix, d)
}

// inBounds proves an index inside d: a literal (or a constant) inside it,
// or a FOR variable whose whole range is (forRange).
func (pw *progWriter) inBounds(ix st.Expression, d arrayDim) bool {
	if id, ok := ix.(*st.IdentExpr); ok {
		if r, ok := pw.forRange[strings.ToLower(id.Name)]; ok {
			return r.lo >= d.lo && r.hi <= d.hi
		}
		if _, local := pw.locals[strings.ToLower(id.Name)]; local {
			if _, isConst := pw.constVals[strings.ToLower(id.Name)]; !isConst {
				return false
			}
		} else if _, isConst := pw.w.lib.constDecls[strings.ToLower(id.Name)]; !isConst {
			return false
		}
	}
	switch ix.(type) {
	case *st.NumberLit, *st.UnaryExpr, *st.TypedLit, *st.IdentExpr:
	default:
		return false
	}
	n, ok := pw.fold(ix)
	return ok && n >= d.lo && n <= d.hi
}

// liftScope is what a statement has evaluated so far, in nautilus's
// order, for lift: whether it has read a variable, and whether it has
// called a FUNCTION that writes a tag, left in place.
type liftScope struct {
	read, impure bool
}

// needsLift reports whether lift would write anything ahead of e: an
// index it cannot prove in bounds, or a FUNCTION that can fault.
func (pw *progWriter) needsLift(e st.Expression) bool {
	found := false
	walkExpr(e, func(x st.Expression) {
		switch x := x.(type) {
		case *st.IndexExpr:
			base, idx := indexChain(x, pw)
			a, _ := pw.arrayOf(pw.rawTyp(base))
			if a == nil || len(idx) != len(a.dims) {
				return // the printer refuses it
			}
			for i, ix := range idx {
				if !pw.inBounds(ix, a.dims[i]) {
					found = true
				}
			}
		case *st.CallExpr:
			if p := pw.w.userFC(x.Name); p != nil && x.Callee == nil && pw.w.canFault(p) {
				found = true
			}
		}
	})
	return found
}

// lift writes, ahead of the statement being written, what e needs done
// first: a check of every index not proved in bounds, a call of a
// FUNCTION that can fault into a temp with its flag checked, each in
// nautilus's order of evaluation. It answers e with those replaced by the
// temps (a copy: the parsed program is never changed). ok is false when e
// cannot be lifted exactly (refused).
func (pw *progWriter) lift(b *strings.Builder, e st.Expression, in string, at int, sc *liftScope) (st.Expression, bool) {
	switch x := e.(type) {
	case nil:
		return nil, true
	case *st.IdentExpr:
		k := strings.ToLower(x.Name)
		if _, isConst := pw.constVals[k]; !isConst {
			if _, local := pw.locals[k]; local {
				sc.read = true
			} else if _, tag := pw.w.globals[k]; tag {
				sc.read = true
			}
		}
		return x, true
	case *st.MemberExpr:
		o, ok := pw.lift(b, x.Object, in, at, sc)
		if !ok {
			return nil, false
		}
		if o == x.Object {
			return x, true
		}
		return &st.MemberExpr{Object: o, Member: x.Member, Pos: x.Pos}, true
	case *st.UnaryExpr:
		o, ok := pw.lift(b, x.Operand, in, at, sc)
		if !ok {
			return nil, false
		}
		if o == x.Operand {
			return x, true
		}
		return &st.UnaryExpr{Op: x.Op, Operand: o, Pos: x.Pos}, true
	case *st.BinaryExpr:
		l, ok := pw.lift(b, x.Left, in, at, sc)
		if !ok {
			return nil, false
		}
		r, ok := pw.lift(b, x.Right, in, at, sc)
		if !ok {
			return nil, false
		}
		if l == x.Left && r == x.Right {
			return x, true
		}
		return &st.BinaryExpr{Op: x.Op, Left: l, Right: r, Pos: x.Pos}, true
	case *st.IndexExpr:
		return pw.liftIndex(b, x, in, at, sc)
	case *st.CallExpr:
		return pw.liftCall(b, x, in, at, sc)
	}
	return e, true
}

func (pw *progWriter) liftIndex(b *strings.Builder, x *st.IndexExpr, in string, at int, sc *liftScope) (st.Expression, bool) {
	arr, ok := pw.lift(b, x.Array, in, at, sc)
	if !ok {
		return nil, false
	}
	out := &st.IndexExpr{Array: arr, Pos: x.Pos}
	// The dimensions this access indexes: after those a partial index
	// before it took (indexChain).
	base, all := indexChain(&st.IndexExpr{Array: arr, Indices: x.Indices, Pos: x.Pos}, pw)
	a, _ := pw.arrayOf(pw.rawTyp(base))
	first := len(all) - len(x.Indices)
	for i, ix := range x.Indices {
		if a == nil || first+i >= len(a.dims) {
			out.Indices = append(out.Indices, ix) // the printer refuses it
			continue
		}
		d := a.dims[first+i]
		if pw.callsUserFC(ix) {
			pw.gap("siemens/array", at, gapOther, "a FUNCTION called inside an array index", "compute the index into a variable first")
			return nil, false
		}
		lx, ok := pw.lift(b, ix, in, at, sc)
		if !ok {
			return nil, false
		}
		if pw.inBounds(lx, d) {
			out.Indices = append(out.Indices, lx)
			continue
		}
		if sc.impure {
			pw.gap("siemens/array", at, gapOther, "an array index checked after a FUNCTION that writes a tag, in one statement", "nautilus calls the FUNCTION first; call it in a statement of its own")
			return nil, false
		}
		t := pw.typ(lx)
		if unsigned64(t) || !(isIntish(t) || isBits(t)) {
			pw.gap("siemens/array", at, gapOther, "an array index of type "+orUnknown(t), "")
			return nil, false
		}
		tmp := pw.hoistTemp("naut_ix", "LINT")
		pw.ixTemps[strings.ToLower(tmp)] = true
		v := ""
		if isBits(t) {
			v = toLINT(t, pw.expr(lx, 0))
		} else {
			v = pw.store(lx, "LINT", at)
		}
		if v == "" {
			return nil, false
		}
		pw.emit(b, at, "%s#%s := %s;\n", in, tmp, v)
		pw.faultIf(b, in, at, fmt.Sprintf("#%s < %d OR #%s > %d", tmp, d.lo, tmp, d.hi))
		out.Indices = append(out.Indices, &st.IdentExpr{Name: tmp, Pos: x.Pos})
	}
	return out, true
}

func (pw *progWriter) liftCall(b *strings.Builder, x *st.CallExpr, in string, at int, sc *liftScope) (st.Expression, bool) {
	if x.Callee != nil {
		return x, true // the printer refuses it
	}
	out := *x
	p := pw.w.userFC(x.Name)
	// What the statement evaluated before this call: its arguments go
	// ahead of the statement with it, in their own order.
	before := *sc
	if p != nil && len(x.NamedArgs) > 0 {
		// nautilus evaluates a FUNCTION's arguments in its inputs' order.
		ins := p.inputs()
		order := make([]int, 0, len(x.NamedArgs))
		for _, pn := range ins {
			for j, a := range x.NamedArgs {
				if strings.EqualFold(a.Name, pn.name) {
					order = append(order, j)
				}
			}
		}
		if len(order) != len(x.NamedArgs) {
			return x, true // fcCall refuses it
		}
		out.NamedArgs = append([]st.NamedArg(nil), x.NamedArgs...)
		for _, j := range order {
			v, ok := pw.lift(b, x.NamedArgs[j].Value, in, at, sc)
			if !ok {
				return nil, false
			}
			out.NamedArgs[j].Value = v
		}
	} else {
		out.Args = make([]st.Expression, len(x.Args))
		for i, a := range x.Args {
			v, ok := pw.lift(b, a, in, at, sc)
			if !ok {
				return nil, false
			}
			out.Args[i] = v
		}
		if len(x.NamedArgs) > 0 {
			// A builtin's formal call: in the IEC order of its inputs.
			out.NamedArgs = append([]st.NamedArg(nil), x.NamedArgs...)
			if pos, ok := pw.positional(x); ok {
				for _, pa := range pos {
					for j := range out.NamedArgs {
						if out.NamedArgs[j].Value == pa {
							v, ok := pw.lift(b, pa, in, at, sc)
							if !ok {
								return nil, false
							}
							out.NamedArgs[j].Value = v
						}
					}
				}
			} else {
				for j := range out.NamedArgs {
					v, ok := pw.lift(b, out.NamedArgs[j].Value, in, at, sc)
					if !ok {
						return nil, false
					}
					out.NamedArgs[j].Value = v
				}
			}
		}
	}
	if p == nil {
		return &out, true
	}
	impure := pw.w.fcImpure(p)
	if !pw.w.canFault(p) {
		if impure {
			sc.impure = true
		}
		return &out, true
	}
	// The FUNCTION goes ahead of the statement with its arguments: one
	// that writes a tag (itself, or a FUNCTION in its arguments) must not
	// pass a read nautilus did before it.
	if before.impure || (impure || sc.impure) && before.read {
		pw.gap("siemens/call", at, gapOther, p.what()+", which can fault the scan, called after another FUNCTION that writes a tag or after a read its own write could change, in one statement",
			"the writer calls it ahead of the statement to stop where nautilus stops; call it in a statement of its own")
		return nil, false
	}
	ret := strings.ToUpper(strings.TrimSpace(p.ret))
	// The FUNCTION hands its fault back through its output naut_fault,
	// into a temp of this block.
	ff := pw.hoistTemp("naut_ff", quote(pw.w.prefix+faultType))
	pw.fcFault = "#" + ff
	call := pw.expr(&out, 0)
	pw.fcFault = ""
	if call == "" {
		return nil, false
	}
	tmp := pw.hoistTemp("naut_fc", p.ret)
	pw.locals[strings.ToLower(tmp)] = local{name: tmp, typ: ret}
	pw.emit(b, at, "%s#%s := %s;\n", in, tmp, call)
	pw.faultCheck(b, in, at, "#"+ff)
	return &st.IdentExpr{Name: tmp, Pos: x.Pos}, true
}

// callsUserFC reports a call of one of the project's FUNCTIONs in e.
func (pw *progWriter) callsUserFC(e st.Expression) bool {
	found := false
	walkExpr(e, func(x st.Expression) {
		if c, ok := x.(*st.CallExpr); ok && pw.w.userFC(c.Name) != nil {
			found = true
		}
	})
	return found
}

// hoistTemp declares a temp of type typ for what a statement computes
// first, named prefix<n> with n the first number no variable takes.
func (pw *progWriter) hoistTemp(prefix, typ string) string {
	for {
		pw.hoistN++
		name := fmt.Sprintf("%s%d", prefix, pw.hoistN)
		if pw.nameTaken(name) {
			continue
		}
		t, why := pw.w.declType(typ, pw.path)
		if why != "" {
			t = typ
		}
		pw.locals[strings.ToLower(name)] = local{name: name, typ: strings.ToUpper(strings.TrimSpace(typ))}
		pw.hoistDecls = append(pw.hoistDecls, fmt.Sprintf("      %s : %s;", name, t))
		return name
	}
}

// nameTaken reports a name the block cannot take for a variable of its
// own: one of its variables or constants, or a project constant (each
// block that names one declares a copy of it under its name).
func (pw *progWriter) nameTaken(name string) bool {
	k := strings.ToLower(name)
	_, local := pw.locals[k]
	_, global := pw.w.lib.constDecls[k]
	return local || global
}

// faultIf writes the check of one index: when cond holds, the scan
// faults where nautilus's would. The block records the fault in its own
// naut_fault (a static of an FB, an output of an FC) and returns.
func (pw *progWriter) faultIf(b *strings.Builder, in string, at int, cond string) {
	pw.faults = true
	pw.emit(b, at, "%sIF %s THEN\n", in, cond)
	pw.emit(b, at, "%s\t#%s.faulted := TRUE;\n", in, faultVar)
	pw.emit(b, at, "%s\t#%s.line := %d;\n", in, faultVar, pw.srcLine(at))
	pw.emit(b, at, "%s\t#%s.block := '%s';\n", in, faultVar, pw.name)
	pw.returnLines(b, in+"\t", at)
	pw.emit(b, at, "%sEND_IF;\n", in)
}

// faultCheck follows a call of a block that can fault, whose fault is
// callee (#inst.naut_fault, or the temp an FC's output went to): when it
// faulted, this block takes the fault over and returns too.
func (pw *progWriter) faultCheck(b *strings.Builder, in string, at int, callee string) {
	pw.faults = true
	pw.emit(b, at, "%sIF %s.faulted THEN\n", in, callee)
	pw.emit(b, at, "%s\t#%s := %s;\n", in, faultVar, callee)
	pw.returnLines(b, in+"\t", at)
	pw.emit(b, at, "%sEND_IF;\n", in)
}

// faultTail declares the block's naut_fault and clears it first thing,
// when the block can fault: an FB and a program keep it as a static (the
// caller, or the task's OB, reads it from the instance after the call),
// an FC as an output. The fault travels with the call chain, never
// through a global: a task that interrupts another cannot clear or
// overwrite it (naut-lead, #1394).
func (pw *progWriter) faultTail(sections []section, body string) ([]section, string) {
	need := pw.faults || pw.pou != nil && pw.w.canFault(pw.pou)
	if !need {
		return sections, body
	}
	if pw.nameTaken(faultVar) {
		line := 1
		if pw.pou != nil {
			line = pw.pou.lineOf()
		}
		pw.diag("siemens/name", line, fmt.Sprintf("%s: the writer keeps the block's fault in a variable of that name; rename yours", faultVar))
		return sections, body
	}
	pw.w.faultType = true
	header := "VAR"
	if pw.kind == kindFC {
		header = "VAR_OUTPUT"
	}
	sections = append(sections, section{header, []string{fmt.Sprintf("      %s : %s;", faultVar, quote(pw.w.prefix+faultType))}})
	pw.lines = append([]int{0}, pw.lines...)
	return sections, fmt.Sprintf("\t#%s.faulted := FALSE;\n", faultVar) + body
}

// faultTypeSource is the PLC data type a block's fault is kept in.
func faultTypeSource(name string) []byte {
	return []byte("TYPE " + quote(name) + "\nVERSION : 0.1\n   STRUCT\n      faulted : Bool;\n      line : DInt;\n      block : String;\n   END_STRUCT;\n\nEND_TYPE\n")
}

// returnLines leave the block as RETURN does (a FUNCTION hands back its
// result first).
func (pw *progWriter) returnLines(b *strings.Builder, in string, at int) {
	if pw.kind == kindFC {
		pw.emit(b, at, "%s%s;\n", in, pw.fcReturn())
	}
	pw.emit(b, at, "%sRETURN;\n", in)
}

// srcLine is the nautilus line of line at of the source pw writes, for the
// faults DB: a program's and a program file's own block's through the
// file's line map, a library block's its library line (0 for a block from
// a transpiled ladder library).
func (pw *progWriter) srcLine(at int) int {
	if pw.pou != nil && pw.pou.lib {
		if pw.pou.libLines {
			return at
		}
		return 0
	}
	return mapLine(pw.srcLines, at)
}

// walkExpr calls f on e and every expression below it.
func walkExpr(e st.Expression, f func(st.Expression)) {
	if e == nil {
		return
	}
	f(e)
	switch x := e.(type) {
	case *st.MemberExpr:
		walkExpr(x.Object, f)
	case *st.UnaryExpr:
		walkExpr(x.Operand, f)
	case *st.BinaryExpr:
		walkExpr(x.Left, f)
		walkExpr(x.Right, f)
	case *st.IndexExpr:
		walkExpr(x.Array, f)
		for _, i := range x.Indices {
			walkExpr(i, f)
		}
	case *st.CallExpr:
		walkExpr(x.Callee, f)
		for _, a := range x.Args {
			walkExpr(a, f)
		}
		for _, a := range x.NamedArgs {
			walkExpr(a.Value, f)
		}
		for _, o := range x.OutputBindings {
			walkExpr(o.Target, f)
		}
	case *st.TypedLit:
		walkExpr(x.Inner, f)
	}
}

// walkStmts calls fs on every statement of ss at any depth, and fe on
// every expression they hold.
func walkStmts(ss []st.Statement, fs func(st.Statement), fe func(st.Expression)) {
	for _, s := range ss {
		fs(s)
		switch s := s.(type) {
		case *st.AssignStmt:
			walkExpr(s.TargetExpr, fe)
			walkExpr(s.Value, fe)
		case *st.IfStmt:
			walkExpr(s.Condition, fe)
			walkStmts(s.Then, fs, fe)
			for _, e := range s.ElsIfs {
				walkExpr(e.Condition, fe)
				walkStmts(e.Body, fs, fe)
			}
			walkStmts(s.Else, fs, fe)
		case *st.ForStmt:
			walkExpr(s.Start, fe)
			walkExpr(s.End, fe)
			walkExpr(s.Step, fe)
			walkStmts(s.Body, fs, fe)
		case *st.WhileStmt:
			walkExpr(s.Condition, fe)
			walkStmts(s.Body, fs, fe)
		case *st.RepeatStmt:
			walkStmts(s.Body, fs, fe)
			walkExpr(s.Condition, fe)
		case *st.CaseStmt:
			walkExpr(s.Expression, fe)
			for _, c := range s.Cases {
				for _, v := range c.Values {
					walkExpr(v, fe)
				}
				walkStmts(c.Body, fs, fe)
			}
			walkStmts(s.Else, fs, fe)
		case *st.CallStmt:
			walkExpr(s.Call, fe)
		}
	}
}

// canFault reports a block that can fault the scan: one that indexes an
// array, or calls (or holds an instance of) a block that can. It
// over-approximates (an index proved in bounds counts): a caller that
// checks a flag that is never set loses nothing, one that misses a fault
// would carry on where nautilus stops.
func (w *writer) canFault(p *pou) bool {
	k := strings.ToLower(p.name)
	if w.faulty == nil {
		w.faulty = map[string]int{}
	}
	switch w.faulty[k] {
	case 1, 2:
		return false // computing (a block that reaches itself is refused) or no
	case 3:
		return true
	}
	if p.builtin {
		w.faulty[k] = 2
		return false
	}
	w.faulty[k] = 1
	r := w.bodyCanFault(p.vbs, p.stmts)
	w.faulty[k] = 2
	if r {
		w.faulty[k] = 3
	}
	return r
}

// bodyCanFault is canFault for a body with its declarations.
func (w *writer) bodyCanFault(vbs []st.VarBlock, stmts []st.Statement) bool {
	types := map[string]string{}
	for _, vb := range vbs {
		for _, v := range vb.Variables {
			types[strings.ToLower(v.Name)] = v.Datatype
		}
	}
	found := false
	walkStmts(stmts, func(s st.Statement) {
		if c, ok := s.(*st.CallStmt); ok && c.Call.Callee == nil {
			if p := w.userFB(types[strings.ToLower(c.Call.Name)]); p != nil && w.canFault(p) {
				found = true
			}
		}
	}, func(e st.Expression) {
		switch x := e.(type) {
		case *st.IndexExpr:
			found = true
		case *st.CallExpr:
			if p := w.userFC(x.Name); p != nil && x.Callee == nil && w.canFault(p) {
				found = true
			}
		}
	})
	return found
}

// fcImpure reports a FUNCTION that writes anything but its own variables
// (a tag), itself or through a FUNCTION it calls: called ahead of its
// statement, it would change what the statement read before it.
func (w *writer) fcImpure(p *pou) bool {
	k := strings.ToLower(p.name)
	if w.impure == nil {
		w.impure = map[string]int{}
	}
	switch w.impure[k] {
	case 1:
		return false
	case 2:
		return false
	case 3:
		return true
	}
	w.impure[k] = 1
	own := map[string]bool{strings.ToLower(p.name): true}
	for _, vb := range p.vbs {
		if vb.Kind == "VAR_EXTERNAL" || vb.Kind == "VAR_GLOBAL" && !vb.Constant {
			continue
		}
		for _, v := range vb.Variables {
			own[strings.ToLower(v.Name)] = true
		}
	}
	r := false
	walkStmts(p.stmts, func(s st.Statement) {
		if a, ok := s.(*st.AssignStmt); ok {
			root := a.TargetExpr
			if root == nil {
				root = &st.IdentExpr{Name: a.Target}
			}
			if id := rootIdent(root); id == nil || !own[strings.ToLower(id.Name)] {
				r = true
			}
		}
	}, func(e st.Expression) {
		if c, ok := e.(*st.CallExpr); ok {
			if q := w.userFC(c.Name); q != nil && w.fcImpure(q) {
				r = true
			}
		}
	})
	w.impure[k] = 2
	if r {
		w.impure[k] = 3
	}
	return r
}

// rootIdent is the variable an access starts from (a in a[i].b.%X3), or
// nil.
func rootIdent(e st.Expression) *st.IdentExpr {
	for {
		switch x := e.(type) {
		case *st.IdentExpr:
			return x
		case *st.MemberExpr:
			e = x.Object
		case *st.IndexExpr:
			e = x.Array
		default:
			return nil
		}
	}
}

// hasIndex reports an access that goes through an array element.
func hasIndex(e st.Expression) bool {
	for {
		switch x := e.(type) {
		case *st.IndexExpr:
			return true
		case *st.MemberExpr:
			e = x.Object
		default:
			return false
		}
	}
}

// forRange is the values a FOR variable takes, when the writer can prove
// them: constant start and end (and step), and a body that never writes
// the variable.
type forBounds struct{ lo, hi int64 }

func (pw *progWriter) forBoundsOf(s *st.ForStmt) (forBounds, bool) {
	k := strings.ToLower(s.Variable)
	if _, local := pw.locals[k]; !local {
		return forBounds{}, false
	}
	start, ok1 := pw.fold(s.Start)
	end, ok2 := pw.fold(s.End)
	step := int64(1)
	ok3 := true
	if s.Step != nil {
		step, ok3 = pw.fold(s.Step)
	}
	if !ok1 || !ok2 || !ok3 || step == 0 {
		return forBounds{}, false
	}
	written := false
	is := func(e st.Expression) bool {
		id := rootIdent(e)
		return id != nil && strings.EqualFold(id.Name, s.Variable)
	}
	walkStmts(s.Body, func(x st.Statement) {
		switch x := x.(type) {
		case *st.AssignStmt:
			t := x.TargetExpr
			if t == nil {
				t = &st.IdentExpr{Name: x.Target}
			}
			written = written || is(t)
		case *st.ForStmt:
			written = written || strings.EqualFold(x.Variable, s.Variable)
		case *st.CallStmt:
			for _, o := range x.Call.OutputBindings {
				written = written || is(o.Target)
			}
			for _, a := range x.Call.NamedArgs {
				written = written || is(a.Value) // an in-out could write it
			}
		}
	}, func(st.Expression) {})
	if written {
		return forBounds{}, false
	}
	// The CPU's FOR steps the variable past its end, in the variable's
	// type: a loop up to the type's last value could wrap round.
	if t := pw.typ(&st.IdentExpr{Name: s.Variable}); !isInt(t) || !inRange(end+step, t) || !inRange(start, t) {
		return forBounds{}, false
	}
	switch {
	case step > 0 && start <= end:
		return forBounds{start, end}, true
	case step < 0 && start >= end:
		return forBounds{end, start}, true
	}
	return forBounds{}, false
}

// faultsSource is the faults DB: the scan's fault (set where it faults,
// cleared by the task's OB before it calls its program), and for each
// task whose program can fault, nautilus's task stats: LogicErrors, the
// faults counted; Faulting, the last scan faulted; LastLine and LastBlock,
// where the last one was (the nautilus line, and the PROGRAM, FUNCTION or
// FUNCTION_BLOCK). Not retained: nautilus counts from its start.
func faultsSource(name string, tasks []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(name))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("NON_RETAIN\n")
	b.WriteString("   VAR \n")
	for _, t := range tasks {
		fmt.Fprintf(&b, "      %s : Struct\n         LogicErrors : LInt;\n         Faulting : Bool;\n         LastLine : DInt;\n         LastBlock : String;\n      END_STRUCT;\n", sclName(t))
	}
	b.WriteString("   END_VAR\n")
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String())
}

// faultCount is a task OB's part: the program's fault, read from its
// instance DB after the shared tags are copied out, counted into the
// task's record. Only this OB writes that record.
func faultCount(db, task, instDB string) string {
	f, t := quote(instDB)+"."+faultVar, quote(db)+"."+sclName(task)
	return fmt.Sprintf("\tIF %s.faulted THEN\n\t\t%s.LogicErrors := %s.LogicErrors + 1;\n\t\t%s.LastLine := %s.line;\n\t\t%s.LastBlock := %s.block;\n\tEND_IF;\n\t%s.Faulting := %s.faulted;\n",
		f, t, t, t, f, t, f, t, f)
}

// liftStmt writes what statement s needs computed first (lift) and
// answers s with those parts replaced, or nil when s is refused. Only the
// statement's own expressions are lifted: the statements in its bodies
// are written (and lifted) in their turn.
func (pw *progWriter) liftStmt(b *strings.Builder, s st.Statement, d int) st.Statement {
	in := indent(d)
	at := lineAt(s)
	sc := &liftScope{}
	switch s := s.(type) {
	case *st.AssignStmt:
		// The value first, then the target's indices (lang/ir vm.go).
		v, ok := pw.lift(b, s.Value, in, at, sc)
		if !ok {
			return nil
		}
		t, ok := pw.lift(b, s.TargetExpr, in, at, sc)
		if !ok {
			return nil
		}
		if v == s.Value && t == s.TargetExpr {
			return s
		}
		return &st.AssignStmt{Target: s.Target, TargetExpr: t, Value: v, Pos: s.Pos}
	case *st.IfStmt:
		c, ok := pw.lift(b, s.Condition, in, at, sc)
		if !ok {
			return nil
		}
		out := *s
		out.Condition = c
		for i, e := range s.ElsIfs {
			if !pw.needsLift(e.Condition) {
				continue
			}
			// nautilus evaluates an ELSIF's condition only when every
			// condition before it is FALSE: its checks go in the ELSE.
			out.ElsIfs = s.ElsIfs[:i]
			out.Else = []st.Statement{&st.IfStmt{Condition: e.Condition, Then: e.Body, ElsIfs: s.ElsIfs[i+1:], Else: s.Else, Pos: e.Pos}}
			break
		}
		return &out
	case *st.ForStmt:
		out := *s
		var ok bool
		if out.Start, ok = pw.lift(b, s.Start, in, at, sc); !ok {
			return nil
		}
		if out.End, ok = pw.lift(b, s.End, in, at, sc); !ok {
			return nil
		}
		if out.Step, ok = pw.lift(b, s.Step, in, at, sc); !ok {
			return nil
		}
		return &out
	case *st.WhileStmt:
		if !pw.needsLift(s.Condition) {
			return s
		}
		// The condition is evaluated before every pass: its checks go at
		// the top of each (CONTINUE comes back to them, EXIT leaves).
		body := append([]st.Statement{&st.IfStmt{Condition: &st.UnaryExpr{Op: "NOT", Operand: s.Condition, Pos: s.Pos}, Then: []st.Statement{&st.ExitStmt{Pos: s.Pos}}, Pos: s.Pos}}, s.Body...)
		return &st.WhileStmt{Condition: &st.BoolLit{Value: true, Pos: s.Pos}, Body: body, Pos: s.Pos}
	case *st.RepeatStmt:
		if pw.needsLift(s.Condition) {
			pw.gap("siemens/array", at, gapOther, "an array index or a FUNCTION that can fault in a REPEAT's UNTIL", "compute the condition into a variable at the end of the body")
			return nil
		}
		return s
	case *st.CaseStmt:
		c, ok := pw.lift(b, s.Expression, in, at, sc)
		if !ok {
			return nil
		}
		out := *s
		out.Expression = c
		return &out
	case *st.CallStmt:
		return pw.liftCallStmt(b, s, d, sc)
	}
	return s
}

// liftCallStmt lifts an instance call's arguments. nautilus copies each
// input into the instance as it evaluates it, so a fault in one leaves
// the inputs before it in the instance: when an input other than the
// first needs a check, each input of the project's block is written as an
// assignment to the instance's pin, in order, and the call takes none.
// For TIA's instructions (a timer, a counter, an edge) that cannot be
// done, so it is refused.
func (pw *progWriter) liftCallStmt(b *strings.Builder, s *st.CallStmt, d int, sc *liftScope) st.Statement {
	in := indent(d)
	at := lineAt(s)
	c := s.Call
	if c.Callee != nil {
		return s
	}
	l, isLocal := pw.locals[strings.ToLower(c.Name)]
	var p *pou
	if isLocal {
		p = pw.w.userFB(l.typ)
	}
	timerOrCounter := false
	if isLocal {
		_, counter := counterInputs[l.typ]
		timerOrCounter = counter || timers[l.typ]
	}
	// An output bound inline at the call (an edge's Q => a[i]) is written
	// by TIA at the call; nautilus checks its index after the block ran.
	if p == nil && !timerOrCounter {
		for _, o := range c.OutputBindings {
			if pw.needsLift(o.Target) {
				pw.gap("siemens/array", at, gapOther, "an array element bound with => to "+c.Name, "assign it from the instance after the call")
				return nil
			}
		}
	}
	lifts := 0
	last := -1
	for i, a := range c.NamedArgs {
		if pw.needsLift(a.Value) {
			lifts++
			last = i
		}
	}
	if lifts == 0 {
		return s
	}
	if p != nil {
		pins := p.pins()
		for _, a := range c.NamedArgs {
			if pn, ok := pins[strings.ToUpper(a.Name)]; ok && pn.kind == "VAR_IN_OUT" && pw.needsLift(a.Value) {
				pw.gap("siemens/array", at, gapOther, "an array element bound to the in-out "+pn.name+" of "+p.what(), "nautilus reads the index before and after the call; bind a variable")
				return nil
			}
		}
	}
	if last == 0 {
		// Only the first input: nothing is copied before it.
		out := *c
		out.NamedArgs = append([]st.NamedArg(nil), c.NamedArgs...)
		v, ok := pw.lift(b, c.NamedArgs[0].Value, in, at, sc)
		if !ok {
			return nil
		}
		out.NamedArgs[0].Value = v
		return &st.CallStmt{Call: &out, Pos: s.Pos}
	}
	if p == nil {
		pw.gap("siemens/array", at, gapOther, "an array index or a FUNCTION that can fault in an input of "+c.Name+" after its first", "nautilus copies the inputs before it into the instance first; compute it into a variable before the call")
		return nil
	}
	pins := p.pins()
	out := *c
	out.NamedArgs = nil
	for _, a := range c.NamedArgs {
		pn, ok := pins[strings.ToUpper(a.Name)]
		if !ok || pn.kind != "VAR_INPUT" {
			out.NamedArgs = append(out.NamedArgs, a) // fbCall binds or refuses it
			continue
		}
		pos := a.Pos
		if pos.Line == 0 {
			pos = c.Pos
		}
		pw.stmt(b, &st.AssignStmt{TargetExpr: &st.MemberExpr{Object: &st.IdentExpr{Name: l.name, Pos: pos}, Member: pn.name, Pos: pos}, Value: a.Value, Pos: pos}, d)
	}
	return &st.CallStmt{Call: &out, Pos: s.Pos}
}
