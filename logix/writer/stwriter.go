package writer

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Structured Text routines (logix-authoring.md §7, Phase D). Logix ST is
// close to IEC ST, and the writer keeps it close: assignments, IF / CASE /
// FOR / WHILE / REPEAT, EXIT, the operators, the maths functions, member
// and index access all come out as written. The places the dialects part:
//
//	BOOL literals            TRUE / FALSE → 1 / 0
//	TIME                     a DINT of milliseconds, everywhere in ST
//	t(IN := x, PT := T#2S)   t is an FBD_TIMER; the call becomes
//	                         t.PRE := 2000; t.TimerEnable := x;
//	                         t.Reset := NOT x; TONR(t);
//	                         (Reset mirrors IEC TON: ET and Q clear the
//	                         scan IN drops; TONR alone would hold ACC)
//	t:TOF                    TOFR(t), no Reset — TOFR already restarts
//	                         from the input's rising edge
//	c(CU := x, PV := 3, R := r)   c is an FBD_COUNTER; CUEnable, PRE,
//	                         Reset, then CTUD(c)
//	t.Q t.ET t.IN c.CV c.CU  t.DN t.ACC t.TimerEnable c.ACC c.CUEnable
//	Q => y                   y := t.DN; after the call
//	EXPT(a, b)               a ** b
//	X_TO_Y(v), TO_Y(v)       Logix that computes the same, or refused
//	                         (logix/conversion): narrowing as a wrap in
//	                         integer arithmetic, int → REAL as v or
//	                         (v * 1.0), BOOL → number as an IF; REAL →
//	                         integer as a plain store of the whole
//	                         right-hand side (convert.go)
//	Run, Mode#Run            the enumeration member's value (enum.go)
//
// Refused by name: STRING and the string functions, MIN / MAX / LIMIT /
// SEL / MUX (no Logix ST function; write the IF), ATAN2, user FUNCTIONs
// and FUNCTION_BLOCKs (Phase D step 3: AOIs), RETURN, CONTINUE, and a
// positional FB argument.

// stBlockTypes are the Logix structures an ST routine drives.
var stBlockTypes = map[string]string{"TON": "FBD_TIMER", "TOF": "FBD_TIMER", "CTU": "FBD_COUNTER", "TONR": "FBD_TIMER"}

var stMemberRewrite = map[string]map[string]string{
	"FBD_TIMER":   {"Q": "DN", "ET": "ACC", "IN": "TimerEnable", "PT": "PRE"},
	"FBD_COUNTER": {"Q": "DN", "CV": "ACC", "CU": "CUEnable", "PV": "PRE"},
}

// stOutputs are the outputs an ST call can capture with =>.
var stOutputs = map[string]string{"FBD_TIMER": "Q and ET", "FBD_COUNTER": "Q and CV"}

// stMembersOf lists the members an ST routine reads, and their Logix names.
func stMembersOf(structType string) string {
	if structType == "FBD_TIMER" {
		return "Q (DN), ET (ACC), IN (TimerEnable), PT (PRE)"
	}
	return "Q (DN), CV (ACC), CU (CUEnable), PV (PRE)"
}

// stFuncs are the functions Logix ST spells the same way.
var stFuncs = map[string]bool{"ABS": true, "SQRT": true, "LN": true, "LOG": true, "EXP": true, "SIN": true, "COS": true, "TAN": true, "TRUNC": true}

// stRejected names the functions with no Logix ST equivalent.
var stRejected = map[string]string{
	"MIN": "write the IF", "MAX": "write the IF", "LIMIT": "write the IFs", "SEL": "write the IF", "MUX": "write the CASE",
	"ATAN2": "Logix ST has ATAN only", "LEN": "STRING is not in the subset", "LEFT": "STRING is not in the subset",
	"RIGHT": "STRING is not in the subset", "MID": "STRING is not in the subset", "CONCAT": "STRING is not in the subset",
	"INSERT": "STRING is not in the subset", "DELETE": "STRING is not in the subset", "REPLACE": "STRING is not in the subset",
	"FIND": "STRING is not in the subset",
}

// WriteST lowers an ST PROGRAM to an L5X controller export with one ST
// routine. Diagnostics are returned with a nil document when any rule
// fires.
func WriteST(src string, opts Options) ([]byte, []Diag, error) {
	lw, err := lowerST(src, opts)
	if err != nil {
		return nil, nil, err
	}
	if len(lw.diags) > 0 {
		return nil, lw.diags, nil
	}
	return emit(lw, lw.opts), nil, nil
}

// CheckST runs the ST rules over source.
func CheckST(src string, libs ...string) ([]Diag, error) {
	lw, err := lowerST(src, Options{Libs: libs})
	if err != nil {
		return nil, err
	}
	return lw.diags, nil
}

func lowerST(src string, opts Options) (*lowered, error) {
	prog, err := st.Parse(src)
	if err != nil {
		return nil, err
	}
	if prog.Name == "" || !strings.EqualFold(prog.TopKeyword, "PROGRAM") {
		return nil, fmt.Errorf("logix writer: source declares no PROGRAM")
	}
	opts = opts.withDefaults(prog.Name)
	m := &ld.Model{Name: prog.Name}
	for _, vb := range prog.VarBlocks {
		for _, v := range vb.Variables {
			init := ""
			if v.Initial != nil {
				init = exprText(v.Initial)
			}
			m.Vars = append(m.Vars, ld.VarDecl{Name: v.Name, Type: v.Datatype, Init: init, Section: vb.Kind, Line: v.Pos.Line})
		}
	}
	lw := &lowered{model: m, opts: opts, vars: map[string]ld.VarDecl{},
		presetVars: map[string]bool{}, genNames: map[string]bool{}, st: true, aois: map[string]*aoiDef{}, src: src,
		clocks: map[string]bool{}, clockUsed: map[string]bool{}}
	if len(prog.FBDecls) > 0 || len(prog.TypeDecls) > 0 {
		// Blocks and types declared beside an ST program are its own
		// library.
		lw.opts.Libs = append([]string{src}, lw.opts.Libs...)
	}
	lw.loadTypes()
	for _, v := range m.Vars {
		lw.vars[strings.ToLower(v.Name)] = v
	}
	// The manifest tags the body names undeclared are controller tags
	// too (uses.go); they join the scope before the body is written, as
	// the declared ones do.
	implicit := lw.implicitFrom(prog, func(n int) int { return n })
	for _, v := range implicit {
		lw.vars[strings.ToLower(v.Name)] = v
	}
	for _, fn := range prog.FuncDecls {
		lw.diag(ruleFunctionBlock, fn.Pos.Line, "", "FUNCTION %s: user functions are not in the Logix v1 subset; inline it", fn.Name)
	}
	for _, v := range append(m.Vars, implicit...) {
		lw.declare(v)
	}
	lw.declareUnnamed()
	lw.checkControllerScoped()
	lw.checkHardware()
	w := &stWriter{lw: lw}
	w.block(prog.Statements, 0)
	lw.stLines = w.lines
	lw.side()
	return lw, nil
}

// exprText renders an initializer the way the declaration carries it.
func exprText(e st.Expression) string {
	switch v := e.(type) {
	case *st.NumberLit:
		return numberText(v)
	case *st.BoolLit:
		if v.Value {
			return "TRUE"
		}
		return "FALSE"
	case *st.TimeLit:
		return "T#" + v.Raw
	case *st.TypedLit:
		if id, ok := v.Inner.(*st.IdentExpr); ok {
			return v.TypeName + "#" + id.Name // an enumeration member
		}
		return exprText(v.Inner)
	case *st.IdentExpr:
		return v.Name // an enumeration member (enum.go)
	case *st.UnaryExpr:
		return v.Op + exprText(v.Operand)
	case *st.StringLit:
		return st.QuoteString(v.Value)
	}
	return ""
}

func numberText(n *st.NumberLit) string {
	if n.Base == 10 || n.Base == 0 {
		return n.Value
	}
	if v, err := strconv.ParseInt(strings.ReplaceAll(n.Value, "_", ""), n.Base, 64); err == nil {
		return strconv.FormatInt(v, 10)
	}
	return n.Value
}

// stWriter renders statements.
type stWriter struct {
	lw    *lowered
	lines []string
	line  int // source line being rendered, for diagnostics
	// rhs is the assignment's right-hand side being rendered and dest
	// its target's type; realCtx marks an expression Logix may compute
	// in REAL; beside an operand whose sibling is a REAL reference or
	// literal; depth counts nested expr calls (convert.go). realKnown
	// is realCtx for a REAL the writer can see: an integer / there is
	// refused (#361).
	rhs       st.Expression
	dest      string
	realCtx   bool
	realKnown bool
	beside    bool
	depth     int
}

func (w *stWriter) emitf(depth int, format string, a ...any) {
	w.lines = append(w.lines, strings.Repeat("  ", depth)+fmt.Sprintf(format, a...))
}

func (w *stWriter) diag(rule string, line int, format string, a ...any) {
	w.lw.diag(rule, line, "", format, a...)
}

func (w *stWriter) block(stmts []st.Statement, depth int) {
	for _, s := range stmts {
		w.stmt(s, depth)
	}
}

func (w *stWriter) stmt(s st.Statement, depth int) {
	switch v := s.(type) {
	case *st.AssignStmt:
		w.line = v.Pos.Line
		lhs := v.Target
		if v.TargetExpr != nil {
			lhs = w.expr(v.TargetExpr)
		} else {
			lhs = w.ref(lhs)
		}
		if w.boolAssign(lhs, v.Value, depth) {
			break
		}
		var target st.Expression = &st.IdentExpr{Name: v.Target}
		if v.TargetExpr != nil {
			target = v.TargetExpr
		}
		w.rhs, w.dest = v.Value, w.typeOf(target)
		rhs := w.expr(v.Value)
		w.rhs, w.dest = nil, ""
		w.emitf(depth, "%s := %s;", lhs, rhs)
	case *st.IfStmt:
		w.line = v.Pos.Line
		w.emitf(depth, "IF %s THEN", w.expr(v.Condition))
		w.block(v.Then, depth+1)
		for _, e := range v.ElsIfs {
			w.emitf(depth, "ELSIF %s THEN", w.expr(e.Condition))
			w.block(e.Body, depth+1)
		}
		if len(v.Else) > 0 {
			w.emitf(depth, "ELSE")
			w.block(v.Else, depth+1)
		}
		w.emitf(depth, "END_IF;")
	case *st.CaseStmt:
		w.line = v.Pos.Line
		w.emitf(depth, "CASE %s OF", w.expr(v.Expression))
		for _, c := range v.Cases {
			var labels []string
			for _, x := range c.Values {
				labels = append(labels, w.expr(x))
			}
			for _, r := range c.Ranges {
				labels = append(labels, w.expr(r.Lo)+".."+w.expr(r.Hi))
			}
			w.emitf(depth+1, "%s:", strings.Join(labels, ", "))
			w.block(c.Body, depth+2)
		}
		if len(v.Else) > 0 {
			w.emitf(depth, "ELSE")
			w.block(v.Else, depth+1)
		}
		w.emitf(depth, "END_CASE;")
	case *st.ForStmt:
		w.line = v.Pos.Line
		step := ""
		if v.Step != nil {
			step = " BY " + w.expr(v.Step)
		}
		w.emitf(depth, "FOR %s := %s TO %s%s DO", w.ref(v.Variable), w.expr(v.Start), w.expr(v.End), step)
		w.block(v.Body, depth+1)
		w.emitf(depth, "END_FOR;")
	case *st.WhileStmt:
		w.line = v.Pos.Line
		w.emitf(depth, "WHILE %s DO", w.expr(v.Condition))
		w.block(v.Body, depth+1)
		w.emitf(depth, "END_WHILE;")
	case *st.RepeatStmt:
		w.line = v.Pos.Line
		w.emitf(depth, "REPEAT")
		w.block(v.Body, depth+1)
		w.emitf(depth, "UNTIL %s", w.expr(v.Condition))
		w.emitf(depth, "END_REPEAT;")
	case *st.CallStmt:
		w.line = v.Pos.Line
		w.call(v.Call, depth)
	case *st.ExitStmt:
		w.emitf(depth, "EXIT;")
	case *st.ReturnStmt:
		w.diag(ruleST, v.Pos.Line, "RETURN is not in the Logix ST subset; restructure with IF")
	case *st.ContinueStmt:
		w.diag(ruleST, v.Pos.Line, "CONTINUE has no Logix ST equivalent; restructure the loop with IF")
	default:
		w.diag(ruleST, w.line, "statement %T is not in the Logix ST subset", s)
	}
}

// call lowers a block call: the instance's structure members are set,
// then the Logix instruction runs, then output bindings are copied out.
func (w *stWriter) call(c *st.CallExpr, depth int) {
	v, ok := w.lw.vars[strings.ToLower(c.Name)]
	if !ok {
		w.diag(ruleST, w.line, "%s(...): a call statement must invoke a declared block instance", c.Name)
		return
	}
	typ := strings.ToUpper(strings.TrimSpace(v.Type))
	if typ == localTimeType {
		if len(c.Args) > 0 || len(c.NamedArgs) > 0 {
			w.diag(ruleFBPin, w.line, "%s: LOCAL_TIME takes no inputs", c.Name)
			return
		}
		w.lw.clockUsed[strings.ToLower(c.Name)] = true
		w.emitf(depth, "%s;", gsvLocalTime(c.Name+"[0]"))
		return
	}
	structType := stBlockTypes[typ]
	if structType == "" {
		if w.lw.blockSourceExists(v.Type) {
			w.aoiCall(c, v.Type, depth)
			return
		}
		w.diag(ruleFB, w.line, "%s:%s: not in the Logix v1 subset; the v1 blocks are TON, TOF, CTU and user FUNCTION_BLOCKs declared in a library", c.Name, v.Type)
		return
	}
	if len(c.Args) > 0 {
		w.diag(ruleST, w.line, "%s: bind the block's inputs by name (IN :=, PT :=)", c.Name)
		return
	}
	inputs := map[string]string{}
	for _, a := range c.NamedArgs {
		inputs[strings.ToUpper(a.Name)] = w.expr(a.Value)
	}
	inst := c.Name
	switch typ {
	case "TON", "TOF", "TONR":
		for pin := range inputs {
			if pin == "RESET" && typ == "TONR" {
				continue
			}
			if pin != "IN" && pin != "PT" {
				w.diag(ruleFBPin, w.line, "%s:%s: pin %s has no Logix mapping (timers take IN and PT)", inst, typ, pin)
				return
			}
		}
		if pt, ok := inputs["PT"]; ok {
			w.emitf(depth, "%s.PRE := %s;", inst, pt)
		}
		in := inputs["IN"]
		if in == "" {
			in = "0"
		}
		w.emitf(depth, "%s.TimerEnable := %s;", inst, in)
		switch typ {
		case "TON":
			w.emitf(depth, "%s.Reset := NOT (%s);", inst, in)
			w.emitf(depth, "TONR(%s);", inst)
		case "TONR":
			// The dialect's resettable timer: its Reset pin, or IN dropping.
			reset := inputs["RESET"]
			if reset == "" {
				reset = "0"
			}
			w.emitf(depth, "%s.Reset := NOT (%s) OR (%s);", inst, in, reset)
			w.emitf(depth, "TONR(%s);", inst)
		default:
			w.emitf(depth, "TOFR(%s);", inst)
		}
	case "CTU":
		for pin := range inputs {
			if pin != "CU" && pin != "PV" && pin != "R" {
				w.diag(ruleFBPin, w.line, "%s:CTU: pin %s has no Logix mapping (counters take CU, PV and R)", inst, pin)
				return
			}
		}
		if pv, ok := inputs["PV"]; ok {
			w.emitf(depth, "%s.PRE := %s;", inst, pv)
		}
		cu := inputs["CU"]
		if cu == "" {
			cu = "0"
		}
		w.emitf(depth, "%s.CUEnable := %s;", inst, cu)
		r := inputs["R"]
		if r == "" {
			r = "0"
		}
		w.emitf(depth, "%s.Reset := %s;", inst, r)
		w.emitf(depth, "CTUD(%s);", inst)
	}
	for _, ob := range c.OutputBindings {
		member, ok := stMemberRewrite[structType][strings.ToUpper(ob.Name)]
		if !ok {
			w.diag(ruleFBPin, w.line, "%s:%s: output %s has no Logix member; the outputs are %s", inst, typ, ob.Name, stOutputs[structType])
			return
		}
		w.emitf(depth, "%s := %s.%s;", w.expr(ob.Target), inst, member)
	}
}

// ref renders a plain variable reference in its declared spelling
// (lang/ir/names.go: a name is compared folded, shown as declared), so
// `level` reads Level on the controller as it does in nautilus.
func (w *stWriter) ref(name string) string {
	if v, ok := w.lw.vars[strings.ToLower(name)]; ok {
		return v.Name
	}
	return name
}

// expr renders an expression. Every binary operation is parenthesized so
// precedence never depends on the dialect.
func (w *stWriter) expr(e st.Expression) string {
	if w.depth == 0 {
		// A statement's expression: REAL when it mixes one in, or when
		// it is stored into one.
		stored := e == w.rhs && isRealType(w.dest)
		w.realCtx = w.numReal(e) || stored
		w.realKnown = w.knownReal(e) || stored
	}
	w.depth++
	defer func() { w.depth-- }()
	beside := w.beside // for this node only
	w.beside = false
	switch v := e.(type) {
	case *st.NumberLit:
		return numberText(v)
	case *st.BoolLit:
		if v.Value {
			return "1"
		}
		return "0"
	case *st.StringLit:
		w.diag(ruleType, w.line, "'%s': STRING is not in the Logix v1 subset", v.Value)
		return "0"
	case *st.TimeLit:
		ms, ok := parseTime("T#" + v.Raw)
		if !ok {
			w.diag(ruleInit, w.line, "T#%s is not a TIME literal the writer reads; write T#10S, T#1h30m or T#500ms", v.Raw)
			return "0"
		}
		return strconv.FormatInt(ms, 10)
	case *st.TypedLit:
		if id, ok := v.Inner.(*st.IdentExpr); ok && w.lw.enumOf(v.TypeName) != nil {
			n, ok := w.lw.enumLiteral(v.TypeName + "#" + id.Name)
			if !ok {
				w.diag(ruleST, w.line, "%s#%s: %s has no member %s (members: %s)", v.TypeName, id.Name, w.lw.enumOf(v.TypeName).Name, id.Name, enumMembers(w.lw.enumOf(v.TypeName)))
				return "0"
			}
			return strconv.FormatInt(n, 10)
		}
		return w.expr(v.Inner)
	case *st.IdentExpr:
		if _, declared := w.lw.vars[strings.ToLower(v.Name)]; !declared {
			// Not a variable: an enumeration member is its value (enum.go).
			if n, ok, ambiguous := w.lw.enumMember(v.Name); ok {
				return strconv.FormatInt(n, 10)
			} else if ambiguous {
				w.diag(ruleST, w.line, "%s: several enumerations have a member %s with different values; write Type#%s", v.Name, v.Name, v.Name)
				return "0"
			}
		}
		return w.ref(v.Name)
	case *st.MemberExpr:
		return w.member(v)
	case *st.IndexExpr:
		if len(v.Indices) != 1 {
			w.diag(ruleArrayShape, w.line, "multi-dimensional arrays are not in the v1 subset; declare one array per dimension")
			return "0"
		}
		arr := w.expr(v.Array)
		saved, savedKnown := w.realCtx, w.realKnown
		w.realCtx = w.numReal(v.Indices[0]) // an index is a computation of its own
		w.realKnown = w.knownReal(v.Indices[0])
		idx := w.expr(v.Indices[0])
		w.realCtx, w.realKnown = saved, savedKnown
		return arr + "[" + idx + "]"
	case *st.BinaryExpr:
		saved, savedKnown := w.realCtx, w.realKnown
		if isCompare(v.Op) {
			// So are a comparison's operands.
			w.realCtx = w.numReal(v.Left) || w.numReal(v.Right)
			w.realKnown = w.knownReal(v.Left) || w.knownReal(v.Right)
		}
		if w.intDivision(v) {
			w.realCtx, w.realKnown = saved, savedKnown
			return "0"
		}
		mixes := isCompare(v.Op) || v.Op == "+" || v.Op == "-" || v.Op == "*" || v.Op == "/"
		w.beside = mixes && w.realLeaf(v.Right)
		l := w.expr(v.Left)
		w.beside = mixes && w.realLeaf(v.Left)
		r := w.expr(v.Right)
		w.realCtx, w.realKnown = saved, savedKnown
		return "(" + l + " " + v.Op + " " + r + ")"
	case *st.UnaryExpr:
		return v.Op + " (" + w.expr(v.Operand) + ")"
	case *st.CallExpr:
		if x, ok := w.conversion(v, beside); ok {
			return x
		}
		return w.callExpr(v)
	}
	w.diag(ruleST, w.line, "expression %T is not in the Logix ST subset", e)
	return "0"
}

// member renders a dotted path, renaming timer and counter members.
func (w *stWriter) member(m *st.MemberExpr) string {
	base, ok := m.Object.(*st.IdentExpr)
	if ok {
		if v, declared := w.lw.vars[strings.ToLower(base.Name)]; declared {
			if isLocalTime(v.Type) {
				if s, ok := w.lw.localTimeMember(base.Name, m.Member, w.line, ""); ok {
					return s
				}
				return "0"
			}
			if structType := stBlockTypes[strings.ToUpper(strings.TrimSpace(v.Type))]; structType != "" {
				if to, ok := stMemberRewrite[structType][strings.ToUpper(m.Member)]; ok {
					return w.ref(base.Name) + "." + to
				}
				w.diag(ruleMember, w.line, "%s.%s: %s has no %s member the Logix %s carries; the readable members are %s", base.Name, m.Member, v.Type, m.Member, structType, stMembersOf(structType))
				return base.Name + "." + m.Member
			}
		}
	}
	return w.expr(m.Object) + "." + m.Member
}

func (w *stWriter) callExpr(c *st.CallExpr) string {
	name := strings.ToUpper(c.Name)
	var args []string
	for _, a := range c.Args {
		args = append(args, w.expr(a))
	}
	switch {
	case isFirstScan(name, strings.Join(args, "")):
		return firstScanFlag
	case stFuncs[name]:
		return name + "(" + strings.Join(args, ", ") + ")"
	case name == "EXPT" && len(args) == 2:
		return "(" + args[0] + " ** " + args[1] + ")"
	}
	// No conversion reaches here: Logix converts only as it stores a
	// value or mixes types, by rules of its own, so expr writes each one
	// as Logix that computes nautilus's result or refuses it (convert.go).
	if why, bad := stRejected[name]; bad {
		w.diag(ruleST, w.line, "%s(): no Logix ST equivalent — %s", c.Name, why)
		return "0"
	}
	if w.lw.userBlock(c.Name) || w.lw.userFunction(c.Name) {
		w.diag(ruleFB, w.line, "%s(): user functions and blocks are not in the Logix v1 subset; inline the logic", c.Name)
		return "0"
	}
	w.diag(ruleST, w.line, "%s(): not a function the Logix ST subset knows; it has ABS, SQRT, LN, LOG, EXP, SIN, COS, TAN, TRUNC, EXPT and the X_TO_Y conversions", c.Name)
	return "0"
}

// aoiCall lowers an ST call of a user block: the instruction with its
// instance and inputs as operands, then the output bindings as copies.
func (w *stWriter) aoiCall(c *st.CallExpr, typ string, depth int) {
	a, ok := w.lw.resolveAOI(typ, w.line)
	if !ok {
		return
	}
	if len(c.Args) > 0 {
		w.diag(ruleST, w.line, "%s: bind the block's inputs by name", c.Name)
		return
	}
	bound := map[string]string{}
	for _, na := range c.NamedArgs {
		p := a.param(na.Name)
		if p == nil || p.Usage == "Output" {
			w.diag(ruleFBPin, w.line, "%s:%s: no input parameter %s (inputs: %s)", c.Name, typ, na.Name, a.paramNames("Input"))
			return
		}
		// The input is stored into the parameter, as an assignment stores
		// into a tag: DINT_TO_REAL(a) is written as a.
		w.rhs, w.dest = na.Value, p.DataType
		arg, ok := w.aoiArg(na.Value)
		w.rhs, w.dest = nil, ""
		if !ok {
			w.diag(ruleFBPin, w.line, "%s:%s: input %s is bound to an expression; a Logix ST call takes a tag or a literal for each input (an expression fails the build with RxSTX_E_UNEXPECTED_TOKEN), so compute it into a tag first (t := ...;) and bind the tag", c.Name, typ, na.Name)
			return
		}
		bound[strings.ToLower(p.Name)] = arg
	}
	operands := []string{c.Name}
	for _, p := range a.Params {
		switch p.Usage {
		case "Input":
			v, ok := bound[strings.ToLower(p.Name)]
			if !ok {
				v = "0"
			}
			operands = append(operands, v)
		case "InOut":
			v, ok := bound[strings.ToLower(p.Name)]
			if !ok {
				w.diag(ruleFBPin, w.line, "%s:%s: VAR_IN_OUT %s must be bound; add %s := a tag", c.Name, typ, p.Name, p.Name)
				return
			}
			operands = append(operands, v)
		}
	}
	w.emitf(depth, "%s(%s);", a.Name, strings.Join(operands, ", "))
	for _, ob := range c.OutputBindings {
		p := a.param(ob.Name)
		if p == nil || p.Usage != "Output" {
			w.diag(ruleFBPin, w.line, "%s:%s: no output parameter %s (outputs: %s)", c.Name, typ, ob.Name, a.paramNames("Output"))
			return
		}
		w.emitf(depth, "%s := %s.%s;", w.expr(ob.Target), c.Name, p.Name)
	}
}

// aoiOperand is what a Logix ST call takes for an input: a tag, a member
// or element of one, or a number. Anything else, even (a) or -a, fails the
// SDK build with RxSTX_E_UNEXPECTED_TOKEN (verified on v38, #368).
var aoiOperand = regexp.MustCompile(`^(-?[0-9][0-9.]*([eE][-+]?[0-9]+)?|[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z0-9_]+|\[([A-Za-z_][A-Za-z0-9_]*|[0-9]+)\])*)$`)

// aoiArg renders an input bound in an ST call to a user block, and reports
// whether Logix takes it as an operand. A negative number is written as
// one (-5), not as a negation; an operator expression is refused without
// rendering it, so the refusal is the call's, not the expression's.
func (w *stWriter) aoiArg(e st.Expression) (string, bool) {
	switch v := e.(type) {
	case *st.UnaryExpr:
		n, ok := v.Operand.(*st.NumberLit)
		if !ok || v.Op != "-" {
			return "", false
		}
		e = &st.NumberLit{Value: "-" + numberText(n), Base: 10, Pos: n.Pos}
	case *st.BinaryExpr:
		return "", false
	}
	s := w.expr(e)
	return s, aoiOperand.MatchString(s)
}
