package writer

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// Type conversions in an ST routine (#255). nautilus defines every X_TO_Y
// (lang/ir/conversions.go): the argument is read as X, integer narrowing
// wraps to Y's width, REAL → integer rounds halves to even, BOOL is 0/1.
// Logix has no conversion functions; it converts when it stores a value
// or mixes operand types, by rules of its own. So each conversion is
// written as Logix that computes nautilus's result, or refused
// (logix/conversion) with what to write instead:
//
//	integer → integer, TIME   the argument as written when it fits the
//	                          narrower of the two widths (it is a tag of
//	                          that width or narrower, or a literal in
//	                          range); else the wrap, in integer arithmetic:
//	                          INT  ((((v MOD 65536) + 98304) MOD 65536) - 32768)
//	                          SINT ((((v MOD 256) + 384) MOD 256) - 128)
//	                          The first MOD leaves a remainder of either
//	                          sign; the offset makes it positive, so the
//	                          second MOD is the same whichever sign rule
//	                          Logix uses. A DINT needs no wrap: Logix
//	                          integers are 32 bits. Refused inside an
//	                          expression that mixes in a REAL, which
//	                          Logix may evaluate the MODs in.
//	integer → BOOL            (v <> 0), ((v MOD 65536) <> 0) for a wrap
//	REAL → BOOL               (v <> 0.0)
//	integer → REAL            a tag or literal only (an integer expression
//	                          may be evaluated in REAL once it meets one,
//	                          so a / b would not divide as integers): as
//	                          written as the whole right-hand side or
//	                          beside a REAL operand, where Logix converts
//	                          it the same way; else (v * 1.0)
//	integer → LREAL, REAL ↔ LREAL   as written, as the whole right-hand
//	                          side only; the store converts
//	BOOL → number             the whole right-hand side only, as
//	                          IF b THEN x := 1; ELSE x := 0; END_IF;
//	TRUNC_<int>(r)            TRUNC(r), as the whole right-hand side of
//	                          an integer assignment: the value stored is
//	                          already whole, so no rounding rule applies
//	REAL → integer, TIME      the REAL as written, as the whole right-hand
//	                          side of an assignment to a tag of the
//	                          conversion's own type (REAL_TO_INT into an
//	                          INT): the store rounds halves to even, as
//	                          nautilus does (verified on a v38 controller).
//	                          Refused inside an expression (Logix rounds
//	                          only as it stores), into another type (it
//	                          rounds to the destination's width) and from
//	                          an LREAL (not verified)
//	unsigned, bit strings, LINT, LTIME, STRING   refused: no Logix type
//	TO_<Enum>(n), TO_DINT(e)  as written: an enumeration is its DINT
//
// An integer / whose quotient meets a REAL (stored into a REAL, beside a
// REAL operand, compared with a REAL) is refused too, conversion or not:
// Logix then divides in REAL, 7 / 2 = 3.5, where nautilus truncates (#361).
//
// What stays a divergence of every value, not of the conversions: a
// REAL is 32 bits on the controller and 64 in nautilus, so an integer
// beyond 2^24 converts to the nearest float32; and a REAL with no value in
// the target faults the nautilus scan, where the controller rounds it and
// stores the low bits of the result (3e9 into a DINT is −1294967296, 1e20
// is 0), silently with the writer's ReportMinorOverflow="false".

// convWidth is an integer type's width as the controller holds it. TIME is
// a DINT of milliseconds; anyInt is an integer of no declared width, which
// nautilus reads as LINT (wider than anything Logix holds).
var convWidth = map[string]int{"SINT": 8, "INT": 16, "DINT": 32, "TIME": 32, anyInt: 64}

// anyInt is the type of an integer expression with no declared width
// (a + b, a counter's CV).
const anyInt = "ANY_INT"

// convOutside explains, for a conversion type the Logix subset lacks, what
// to write instead.
var convOutside = map[string]string{
	"LINT": "the subset's widest integer is DINT", "LTIME": "use TIME, a DINT of milliseconds",
	"STRING": "a Logix STRING is a fixed 82-byte structure with no nautilus equivalent in v1",
}

func init() {
	for _, t := range []string{"USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD"} {
		convOutside[t] = "Logix has no unsigned or bit-string atomics; convert to the signed width (SINT, INT, DINT)"
	}
}

// splitConversion reads a conversion's name: X_TO_Y, TO_Y (from ""),
// TRUNC_Y and X_TRUNC_Y (trunc).
func splitConversion(name string) (from, to string, trunc, ok bool) {
	up := strings.ToUpper(name)
	switch {
	case strings.HasPrefix(up, "TO_"):
		return "", name[3:], false, true
	case strings.HasPrefix(up, "TRUNC_"):
		return "REAL", up[len("TRUNC_"):], true, true
	case strings.Contains(up, "_TRUNC_"):
		from, to, _ = strings.Cut(up, "_TRUNC_")
		return from, to, true, true
	}
	from, to, ok = strings.Cut(up, "_TO_")
	return from, to, false, ok
}

func isRealType(t string) bool { return t == "REAL" || t == "LREAL" }

// isConvInt reports an integer conversion type (TIME, a DINT of
// milliseconds, included).
func isConvInt(t string) bool { return convWidth[t] > 0 }

func isCompare(op string) bool {
	switch op {
	case "=", "<>", "<", ">", "<=", ">=":
		return true
	}
	return false
}

// conversion writes a conversion call, or refuses it; ok is false for a
// call that is not a conversion. beside: the call is an operand whose
// sibling is a REAL tag or literal.
func (w *stWriter) conversion(c *st.CallExpr, beside bool) (string, bool) {
	from, to, trunc, ok := splitConversion(c.Name)
	if !ok || len(c.Args) != 1 || len(c.NamedArgs) > 0 {
		return "", false
	}
	arg := c.Args[0]
	if from == "" && w.lw.enumOf(to) != nil {
		// TO_Mode(n): the enumeration is the DINT n.
		return w.expr(arg), true
	}
	if !ir.IsConversion(c.Name) {
		return "", false
	}
	to = strings.ToUpper(to)
	at := w.typeOf(arg)
	if from == "" {
		if at == "" {
			w.diag(ruleConversion, w.line, "%s(): the writer cannot tell the argument's type, so not how Logix should convert it; write the typed form (DINT_TO_INT, REAL_TO_DINT, …)", c.Name)
			return "0", true
		}
		from = at
	}
	for _, t := range []string{from, to} {
		if why, bad := convOutside[t]; bad {
			if (t == "DWORD" || t == "LWORD") && (isRealType(from) || isRealType(to)) {
				why = "the IEEE bit-pattern transfer is a COP on Logix, which the writer does not write"
			}
			w.diag(ruleConversion, w.line, "%s(): %s is not in the Logix v1 subset; %s", c.Name, t, why)
			return "0", true
		}
	}
	whole := c == w.rhs
	switch {
	case trunc:
		if from == "LREAL" || at == "LREAL" {
			w.diag(ruleConversion, w.line, "%s(): truncating an LREAL is not yet verified on a controller; convert from a REAL", c.Name)
			return "0", true
		}
		if !whole || !isConvInt(w.dest) || at == "" {
			w.diag(ruleConversion, w.line, "%s(): Logix's TRUNC is written only as the whole right-hand side of an assignment to an integer tag, where the whole number it gives is stored as is; assign it to a tag of its own (n := %s(x);) and use the tag", c.Name, c.Name)
			return "0", true
		}
		return "TRUNC(" + w.expr(arg) + ")", true
	case from == "BOOL" && to == "BOOL":
		return w.expr(arg), true
	case from == "BOOL":
		// The whole right-hand side never reaches here (boolAssign).
		w.diag(ruleConversion, w.line, "%s(): Logix has no BOOL-to-number conversion inside an expression; assign it to a tag of its own (n := %s(b);), which the writer writes as an IF, and use the tag", c.Name, c.Name)
		return "0", true
	case to == "BOOL":
		return w.toBool(c, from, arg), true
	case isConvInt(from) && isConvInt(to):
		return w.narrow(c, arg, min(convWidth[from], convWidth[to])), true
	case isConvInt(from) && to == "REAL":
		return w.intToReal(c, from, arg, whole && isRealType(w.dest), beside), true
	case isConvInt(from) && to == "LREAL":
		return w.intToReal(c, from, arg, whole && isRealType(w.dest), false), true
	case isRealType(from) && isRealType(to):
		if from == to || whole && isRealType(w.dest) {
			return w.expr(arg), true
		}
		w.diag(ruleConversion, w.line, "%s(): how Logix mixes REAL and LREAL inside an expression is not yet verified; assign the conversion to a %s tag of its own (x := %s(v);) and use the tag", c.Name, to, c.Name)
		return "0", true
	case isRealType(from):
		return w.realToInt(c, from, to, at, arg, whole), true
	}
	w.diag(ruleConversion, w.line, "%s(): no Logix form", c.Name)
	return "0", true
}

// realToInt writes a REAL → integer or TIME conversion: the REAL as
// written, as the whole right-hand side of an assignment to a tag of the
// conversion's own type. Logix rounds a REAL to an integer as it stores it,
// halves to even, as nautilus does (verified on a v38 controller: 2.5 → 2,
// 3.5 → 4, −2.5 → −2); TIME is a DINT of milliseconds, as REAL_TO_TIME
// counts them. Anywhere else Logix would not round, or would round to
// another width, so it is refused.
func (w *stWriter) realToInt(c *st.CallExpr, from, to, at string, arg st.Expression, whole bool) string {
	if from == "LREAL" || at == "LREAL" {
		w.diag(ruleConversion, w.line, "%s(): how Logix stores an LREAL into an integer is not yet verified on a controller; convert from a REAL", c.Name)
		return "0"
	}
	if !whole {
		w.diag(ruleConversion, w.line, "%s(): Logix rounds a REAL to an integer only as it stores it, so inside an expression it would compute with the unrounded REAL; assign the conversion to a %s tag of its own (n := %s(x);) and use the tag", c.Name, to, c.Name)
		return "0"
	}
	if at == "" {
		w.diag(ruleConversion, w.line, "%s(): the writer cannot tell the argument's type, so not whether a plain store rounds it as nautilus does; convert a REAL tag or literal", c.Name)
		return "0"
	}
	if w.dest != to {
		dest := w.dest
		if dest == "" {
			dest = "a tag the writer cannot type"
		}
		w.diag(ruleConversion, w.line, "%s(): written as a plain store, Logix rounds the REAL to the destination's width, and the destination is %s, not %s; assign the conversion to a %s tag (n := %s(x);), then convert that tag", c.Name, dest, to, to, c.Name)
		return "0"
	}
	return w.expr(arg)
}

// narrow writes an integer conversion: the argument read at width bits,
// signed.
func (w *stWriter) narrow(c *st.CallExpr, arg st.Expression, width int) string {
	if n, ok := intLiteral(arg); ok {
		return strconv.FormatInt(wrapTo(n, width), 10)
	}
	x := w.expr(arg)
	if width >= 32 || w.fits(arg, width) {
		return x
	}
	if w.realCtx {
		w.diag(ruleConversion, w.line, "%s(): the writer wraps to %d bits with integer arithmetic, and this expression mixes in a REAL, which Logix may evaluate it in; assign the conversion to an integer tag of its own and use the tag", c.Name, width)
		return "0"
	}
	m := int64(1) << width
	return fmt.Sprintf("((((%s MOD %d) + %d) MOD %d) - %d)", x, m, m+m/2, m, m/2)
}

// toBool writes X_TO_BOOL: nonzero, with the argument read as X.
func (w *stWriter) toBool(c *st.CallExpr, from string, arg st.Expression) string {
	saved, savedKnown := w.realCtx, w.realKnown
	w.realCtx, w.realKnown = w.numReal(arg), w.knownReal(arg)
	defer func() { w.realCtx, w.realKnown = saved, savedKnown }()
	if isRealType(from) {
		return "(" + w.expr(arg) + " <> 0.0)"
	}
	width := min(convWidth[from], 32)
	if n, ok := intLiteral(arg); ok {
		if wrapTo(n, convWidth[from]) != 0 {
			return "1"
		}
		return "0"
	}
	x := w.expr(arg)
	if width >= 32 || w.fits(arg, width) {
		return "(" + x + " <> 0)"
	}
	if w.realCtx {
		w.diag(ruleConversion, w.line, "%s(): the writer reads the argument at %d bits with integer arithmetic, and it mixes in a REAL, which Logix may evaluate it in; assign it to an integer tag of its own first", c.Name, width)
		return "0"
	}
	return fmt.Sprintf("((%s MOD %d) <> 0)", x, int64(1)<<width)
}

// intToReal writes an integer → REAL or LREAL conversion. plain: Logix
// converts the value itself, as the whole right-hand side of a REAL
// assignment or (REAL only) beside a REAL operand.
func (w *stWriter) intToReal(c *st.CallExpr, from string, arg st.Expression, whole, beside bool) string {
	n, lit := intLiteral(arg)
	if !lit && !isLeaf(arg) {
		w.diag(ruleConversion, w.line, "%s(): Logix may evaluate an integer expression in REAL once it meets a REAL, so a / b would not divide as integers; assign the integer expression to a DINT tag first and convert the tag", c.Name)
		return "0"
	}
	width := min(convWidth[from], 32)
	x := ""
	if lit {
		v := wrapTo(n, convWidth[from])
		if wrapTo(v, 32) != v {
			w.diag(ruleConversion, w.line, "%s(): %d is beyond a DINT, the widest integer Logix holds; write it as a REAL literal", c.Name, v)
			return "0"
		}
		x = strconv.FormatInt(v, 10)
	} else {
		// At 32 bits every Logix integer fits.
		if width < 32 && !w.fits(arg, width) {
			at := w.typeOf(arg)
			w.diag(ruleConversion, w.line, "%s(): the argument's type is %s, which %s reads as %s first and Logix would convert whole; write %s_TO_%s, or narrow it in an assignment of its own", c.Name, at, c.Name, from, at, strings.ToUpper(c.Name[strings.LastIndex(c.Name, "_")+1:]))
			return "0"
		}
		x = w.expr(arg)
	}
	switch {
	case whole:
		return x
	case strings.HasSuffix(strings.ToUpper(c.Name), "LREAL"):
		w.diag(ruleConversion, w.line, "%s(): how Logix mixes an integer into LREAL arithmetic is not yet verified; assign the conversion to an LREAL tag of its own (x := %s(v);) and use the tag", c.Name, c.Name)
		return "0"
	case beside:
		return x
	}
	return "(" + x + " * 1.0)"
}

// boolAssign writes `x := BOOL_TO_DINT(b);` (any BOOL → number as the whole
// right-hand side) as the IF it is; false when value is no such call.
func (w *stWriter) boolAssign(lhs string, value st.Expression, depth int) bool {
	c, ok := value.(*st.CallExpr)
	if !ok || len(c.Args) != 1 || len(c.NamedArgs) > 0 || !ir.IsConversion(c.Name) {
		return false
	}
	from, to, trunc, _ := splitConversion(c.Name)
	to = strings.ToUpper(to)
	if from == "" {
		from = w.typeOf(c.Args[0])
	}
	if trunc || from != "BOOL" || !(isConvInt(to) || isRealType(to)) {
		return false
	}
	one, zero := "1", "0"
	if isRealType(to) {
		one, zero = "1.0", "0.0"
	}
	w.emitf(depth, "IF %s THEN", w.expr(c.Args[0]))
	w.emitf(depth+1, "%s := %s;", lhs, one)
	w.emitf(depth, "ELSE")
	w.emitf(depth+1, "%s := %s;", lhs, zero)
	w.emitf(depth, "END_IF;")
	return true
}

// wrapTo is n's low width bits, read signed (lang/ir fitInt).
func wrapTo(n int64, width int) int64 {
	if width >= 64 {
		return n
	}
	return n << (64 - width) >> (64 - width)
}

// intLiteral is an integer literal's value: 70000, -5, 16#FF, INT#5.
func intLiteral(e st.Expression) (int64, bool) {
	switch v := e.(type) {
	case *st.NumberLit:
		base := v.Base
		if base == 0 {
			base = 10
		}
		n, err := strconv.ParseInt(strings.ReplaceAll(v.Value, "_", ""), base, 64)
		return n, err == nil
	case *st.UnaryExpr:
		if v.Op == "-" {
			n, ok := intLiteral(v.Operand)
			return -n, ok
		}
	case *st.TypedLit:
		if isConvInt(strings.ToUpper(v.TypeName)) {
			return intLiteral(v.Inner)
		}
	}
	return 0, false
}

// isLeaf reports a reference or a literal: what converts the same whether
// Logix evaluates its expression per operator or all in REAL.
func isLeaf(e st.Expression) bool {
	switch v := e.(type) {
	case *st.IdentExpr, *st.MemberExpr, *st.IndexExpr, *st.NumberLit, *st.TypedLit, *st.TimeLit:
		return true
	case *st.UnaryExpr:
		_, ok := intLiteral(v)
		return ok
	}
	return false
}

// fits reports an argument whose value Logix holds within width bits,
// signed: a tag of that width or narrower, an enumeration (DINT), a BOOL.
func (w *stWriter) fits(e st.Expression, width int) bool {
	if n, ok := intLiteral(e); ok {
		return wrapTo(n, width) == n
	}
	t := w.typeOf(e)
	if t == "BOOL" {
		return true
	}
	return t != anyInt && convWidth[t] > 0 && convWidth[t] <= width
}

// numReal reports an expression that may be computed in REAL on Logix: one
// with a REAL operand, or one the writer cannot type. A comparison is a
// BOOL and starts afresh, as does an index.
func (w *stWriter) numReal(e st.Expression) bool { return w.realIn(e, true) }

// knownReal reports an expression with a REAL operand the writer can see:
// numReal without the operands it cannot type.
func (w *stWriter) knownReal(e st.Expression) bool { return w.realIn(e, false) }

func (w *stWriter) realIn(e st.Expression, unknown bool) bool {
	switch v := e.(type) {
	case *st.BinaryExpr:
		if isCompare(v.Op) {
			return false
		}
		return w.realIn(v.Left, unknown) || w.realIn(v.Right, unknown)
	case *st.UnaryExpr:
		return w.realIn(v.Operand, unknown)
	case *st.CallExpr:
		t := w.typeOf(v)
		if isRealType(t) {
			return true
		}
		if t == "BOOL" {
			return false
		}
		for _, a := range v.Args {
			if w.realIn(a, unknown) {
				return true
			}
		}
		return unknown && t == ""
	}
	t := w.typeOf(e)
	return unknown && t == "" || isRealType(t)
}

// intDivision refuses an integer / whose result meets a REAL (#361): Logix
// then divides in REAL, so 7 / 2 is 3.5 where nautilus truncates to 3. It
// meets one when it is stored into a REAL or LREAL, or computed beside or
// compared with a REAL operand. Unless an operand of its own is a REAL (a
// division nautilus also does in REAL), it is refused.
func (w *stWriter) intDivision(v *st.BinaryExpr) bool {
	if v.Op != "/" || !w.realKnown || isRealType(w.typeOf(v.Left)) || isRealType(w.typeOf(v.Right)) {
		return false
	}
	w.diag(ruleConversion, w.line, "integer division (/): Logix divides integers in REAL once the quotient meets a REAL (a REAL destination, a REAL operand beside it or a REAL it is compared with), so 7 / 2 gives 3.5 where nautilus gives 3; compute the quotient into a DINT tag of its own first (q := a / b;) and use the tag, or divide by a REAL (a / 2.0) if a REAL quotient is meant")
	return true
}

// realDivision reports an integer / in e that Logix divides in REAL: its
// quotient meets a REAL the writer can see (inReal: e is stored into a
// REAL, or has a REAL operand). A comparison's operands and an index are
// computations of their own. It is intDivision's test for an expression
// written as a whole, as a rung's DIV, CPT and CMP are (#368).
func (w *stWriter) realDivision(e st.Expression, inReal bool) bool {
	switch v := e.(type) {
	case *st.BinaryExpr:
		if isCompare(v.Op) {
			inReal = w.knownReal(v.Left) || w.knownReal(v.Right)
		} else if v.Op == "/" && inReal && !isRealType(w.typeOf(v.Left)) && !isRealType(w.typeOf(v.Right)) {
			return true
		}
		return w.realDivision(v.Left, inReal) || w.realDivision(v.Right, inReal)
	case *st.UnaryExpr:
		return w.realDivision(v.Operand, inReal)
	case *st.CallExpr:
		for _, a := range v.Args {
			if w.realDivision(a, inReal) {
				return true
			}
		}
	case *st.IndexExpr:
		for _, i := range v.Indices {
			if w.realDivision(i, w.knownReal(i)) {
				return true
			}
		}
		return w.realDivision(v.Array, false)
	case *st.MemberExpr:
		return w.realDivision(v.Object, false)
	}
	return false
}

// typeOf is what the writer knows of an expression's type: an elementary
// type by name (BOOL, SINT, INT, DINT, REAL, LREAL, TIME, or one outside
// the subset), anyInt, or "" when it cannot tell. An enumeration is DINT.
func (w *stWriter) typeOf(e st.Expression) string {
	switch v := e.(type) {
	case *st.NumberLit:
		if (v.Base == 10 || v.Base == 0) && strings.ContainsAny(v.Value, ".eE") {
			return "REAL"
		}
		return anyInt
	case *st.BoolLit:
		return "BOOL"
	case *st.TimeLit:
		return "TIME"
	case *st.TypedLit:
		return w.elementary(v.TypeName)
	case *st.BinaryExpr:
		l, r := w.typeOf(v.Left), w.typeOf(v.Right)
		switch {
		case isCompare(v.Op):
			return "BOOL"
		case v.Op == "AND" || v.Op == "OR" || v.Op == "XOR":
			if l == "BOOL" && r == "BOOL" {
				return "BOOL"
			}
		case isRealType(l) || isRealType(r):
			return "REAL"
		case l == "TIME" && r == "TIME":
			return "TIME"
		}
		if l == "" || r == "" {
			return ""
		}
		return anyInt
	case *st.UnaryExpr:
		t := w.typeOf(v.Operand)
		if t == "" || t == "BOOL" || isRealType(t) {
			return t
		}
		return anyInt
	case *st.CallExpr:
		name := strings.ToUpper(v.Name)
		if from, to, _, ok := splitConversion(name); ok && len(v.Args) == 1 {
			if from == "" && w.lw.enumOf(v.Name[3:]) != nil {
				return "DINT"
			}
			if ir.IsConversion(name) {
				return strings.ToUpper(to)
			}
		}
		switch name {
		case "TRUNC":
			return anyInt
		case "ABS":
			if len(v.Args) == 1 {
				if t := w.typeOf(v.Args[0]); isRealType(t) || t == "" {
					return t
				}
				return anyInt
			}
		case "SQRT", "LN", "LOG", "EXP", "SIN", "COS", "TAN", "EXPT":
			return "REAL"
		}
		if isFirstScan(name, "") {
			return "BOOL"
		}
		if ir.IsSystemFunction(name) {
			if name == "TAKEOVERS" {
				return "DINT"
			}
			return "BOOL"
		}
		return ""
	}
	return w.elementary(w.rawType(e))
}

// elementary normalizes a declared type: an enumeration is DINT, an
// elementary type its name, anything else (a structure, an array) "".
func (w *stWriter) elementary(typ string) string {
	if typ == anyInt {
		return anyInt
	}
	if w.lw.enumOf(typ) != nil {
		return "DINT"
	}
	up := strings.ToUpper(strings.TrimSpace(typ))
	for _, t := range ir.ConversionTypeNames() {
		if t == up {
			return up
		}
	}
	return ""
}

// rawType is a reference's declared type as written (ARRAY [0..9] OF INT,
// a STRUCT's name), followed through members and indices.
func (w *stWriter) rawType(e st.Expression) string {
	switch v := e.(type) {
	case *st.IdentExpr:
		if d, ok := w.lw.vars[strings.ToLower(v.Name)]; ok {
			return d.Type
		}
		if _, ok, _ := w.lw.enumMember(v.Name); ok {
			return "DINT"
		}
	case *st.IndexExpr:
		base := strings.TrimSpace(w.rawType(v.Array))
		if m := arrayRe.FindStringSubmatch(base); m != nil {
			return m[4]
		}
	case *st.MemberExpr:
		if isBitMember(v.Member) {
			return "BOOL"
		}
		if strings.HasPrefix(v.Member, "%") && len(v.Member) > 1 {
			return map[byte]string{'X': "BOOL", 'B': "BYTE", 'W': "WORD", 'D': "DWORD", 'L': "LWORD"}[v.Member[1]]
		}
		base := strings.TrimSpace(w.rawType(v.Object))
		member := strings.ToUpper(v.Member)
		if stBlockTypes[strings.ToUpper(base)] != "" {
			switch member {
			case "Q", "IN", "CU", "R":
				return "BOOL"
			case "ET", "PT":
				return "TIME"
			case "CV", "PV":
				return anyInt
			}
			return ""
		}
		if u := w.lw.types[strings.ToLower(base)]; u != nil {
			for _, m := range u.Members {
				if strings.EqualFold(m.Name, v.Member) {
					if m.Dim > 0 {
						return fmt.Sprintf("ARRAY [0..%d] OF %s", m.Dim-1, m.DataType)
					}
					return m.DataType
				}
			}
		}
		if a := w.lw.aois[strings.ToLower(base)]; a != nil {
			if p := a.param(v.Member); p != nil && p.Dim == 0 {
				return p.DataType
			}
		}
	}
	return ""
}

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

// realLeaf reports a REAL reference or literal: beside one, Logix converts
// an integer operand to REAL as nautilus does.
func (w *stWriter) realLeaf(e st.Expression) bool {
	return isLeaf(e) && isRealType(w.typeOf(e))
}
