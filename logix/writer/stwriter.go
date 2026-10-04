package writer

import (
	"fmt"
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
//	EXPT(a, b)               a ** b;   X_TO_Y(v) → v (Logix converts on
//	                         assignment, rounding a REAL as IEC does)
//
// Refused by name: STRING and the string functions, MIN / MAX / LIMIT /
// SEL / MUX (no Logix ST function; write the IF), ATAN2, user FUNCTIONs
// and FUNCTION_BLOCKs (Phase D step 3: AOIs), RETURN, CONTINUE, and a
// positional FB argument.

// stBlockTypes are the Logix structures an ST routine drives.
var stBlockTypes = map[string]string{"TON": "FBD_TIMER", "TOF": "FBD_TIMER", "CTU": "FBD_COUNTER"}

var stMemberRewrite = map[string]map[string]string{
	"FBD_TIMER":   {"Q": "DN", "ET": "ACC", "IN": "TimerEnable", "PT": "PRE"},
	"FBD_COUNTER": {"Q": "DN", "CV": "ACC", "CU": "CUEnable", "PV": "PRE"},
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
		presetVars: map[string]bool{}, genNames: map[string]bool{}, st: true, aois: map[string]*aoiDef{}, src: src}
	lw.loadTypes()
	for _, v := range m.Vars {
		lw.vars[strings.ToLower(v.Name)] = v
	}
	if len(prog.FBDecls) > 0 {
		// Blocks declared beside an ST program are its own library.
		lw.opts.Libs = append([]string{src}, lw.opts.Libs...)
	}
	for _, fn := range prog.FuncDecls {
		lw.diag(ruleFunctionBlock, fn.Pos.Line, "", "FUNCTION %s: user functions are not in the Logix v1 subset; inline it", fn.Name)
	}
	for _, v := range m.Vars {
		lw.declare(v)
	}
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
		return exprText(v.Inner)
	case *st.UnaryExpr:
		return v.Op + exprText(v.Operand)
	case *st.StringLit:
		return "'" + v.Value + "'"
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
		w.emitf(depth, "%s := %s;", lhs, w.expr(v.Value))
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
	case "TON", "TOF":
		for pin := range inputs {
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
		if typ == "TON" {
			w.emitf(depth, "%s.Reset := NOT (%s);", inst, in)
			w.emitf(depth, "TONR(%s);", inst)
		} else {
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
			w.diag(ruleFBPin, w.line, "%s:%s: output %s has no Logix member", inst, typ, ob.Name)
			return
		}
		w.emitf(depth, "%s := %s.%s;", w.expr(ob.Target), inst, member)
	}
}

// ref renders a plain variable reference, refusing what has no home.
func (w *stWriter) ref(name string) string {
	return name
}

// expr renders an expression. Every binary operation is parenthesized so
// precedence never depends on the dialect.
func (w *stWriter) expr(e st.Expression) string {
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
			w.diag(ruleInit, w.line, "T#%s is not a TIME literal the writer reads", v.Raw)
			return "0"
		}
		return strconv.FormatInt(ms, 10)
	case *st.TypedLit:
		return w.expr(v.Inner)
	case *st.IdentExpr:
		return v.Name
	case *st.MemberExpr:
		return w.member(v)
	case *st.IndexExpr:
		if len(v.Indices) != 1 {
			w.diag(ruleArrayShape, w.line, "multi-dimensional arrays are not in the v1 subset")
			return "0"
		}
		return w.expr(v.Array) + "[" + w.expr(v.Indices[0]) + "]"
	case *st.BinaryExpr:
		return "(" + w.expr(v.Left) + " " + v.Op + " " + w.expr(v.Right) + ")"
	case *st.UnaryExpr:
		return v.Op + " (" + w.expr(v.Operand) + ")"
	case *st.CallExpr:
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
			if structType := stBlockTypes[strings.ToUpper(strings.TrimSpace(v.Type))]; structType != "" {
				if to, ok := stMemberRewrite[structType][strings.ToUpper(m.Member)]; ok {
					return base.Name + "." + to
				}
				w.diag(ruleMember, w.line, "%s.%s: %s has no %s member the Logix %s carries", base.Name, m.Member, v.Type, m.Member, structType)
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
	case stFuncs[name]:
		return name + "(" + strings.Join(args, ", ") + ")"
	case name == "EXPT" && len(args) == 2:
		return "(" + args[0] + " ** " + args[1] + ")"
	case strings.Contains(name, "_TO_") && len(args) == 1:
		return args[0]
	}
	if why, bad := stRejected[name]; bad {
		w.diag(ruleST, w.line, "%s(): no Logix ST equivalent — %s", c.Name, why)
		return "0"
	}
	if w.lw.userBlock(c.Name) || w.lw.userFunction(c.Name) {
		w.diag(ruleFB, w.line, "%s(): user functions and blocks are not in the Logix v1 subset; inline the logic", c.Name)
		return "0"
	}
	w.diag(ruleST, w.line, "%s(): not a function the Logix ST subset knows", c.Name)
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
			w.diag(ruleFBPin, w.line, "%s:%s: no input parameter %s", c.Name, typ, na.Name)
			return
		}
		bound[strings.ToLower(p.Name)] = w.expr(na.Value)
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
				w.diag(ruleFBPin, w.line, "%s:%s: VAR_IN_OUT %s must be bound", c.Name, typ, p.Name)
				return
			}
			operands = append(operands, v)
		}
	}
	w.emitf(depth, "%s(%s);", a.Name, strings.Join(operands, ", "))
	for _, ob := range c.OutputBindings {
		p := a.param(ob.Name)
		if p == nil || p.Usage != "Output" {
			w.diag(ruleFBPin, w.line, "%s:%s: no output parameter %s", c.Name, typ, ob.Name)
			return
		}
		w.emitf(depth, "%s := %s.%s;", w.expr(ob.Target), c.Name, p.Name)
	}
}
