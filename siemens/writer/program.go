package writer

import (
	"fmt"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

type local struct {
	name string // as declared
	typ  string // IEC type as declared, upper-cased
}

type progWriter struct {
	w      *writer
	path   string
	locals map[string]local
	fb     []byte
	// wide is set while printing integer arithmetic that nautilus
	// computes at 64 bits (types.go).
	wide bool
	// statics are the program's static variables (VAR, VAR RETAIN,
	// VAR_INPUT/OUTPUT), which the Web API can read; temps are not.
	statics map[string]bool
	temps   []st.VarDecl
}

// elementary maps the IEC elementary types the subset writes. SCL spells
// them the same; TIA is case-insensitive about type names.
var elementary = map[string]bool{
	"BOOL": true, "BYTE": true, "WORD": true, "DWORD": true, "LWORD": true,
	"SINT": true, "INT": true, "DINT": true, "LINT": true,
	"USINT": true, "UINT": true, "UDINT": true, "ULINT": true,
	"REAL": true, "LREAL": true, "TIME": true, "LTIME": true,
	"DATE": true, "TOD": true, "TIME_OF_DAY": true, "CHAR": true, "WCHAR": true,
}

// instances maps the IEC function blocks the subset writes to the S7-1500
// instance type of the same instruction (verify in S2b: whether TIA also
// takes the bare IEC name).
var instances = map[string]string{
	"TON": "TON_TIME", "TOF": "TOF_TIME", "TP": "TP_TIME",
	// nautilus's counter value is its 64-bit int and keeps counting past
	// 32767; CTU_INT would stop there. CTU_LINT matches (verify in S2b
	// that it saturates only at the LINT maximum).
	"CTU": "CTU_LINT", "CTD": "CTD_LINT", "CTUD": "CTUD_LINT",
	"R_TRIG": "R_TRIG", "F_TRIG": "F_TRIG",
}

// sclType maps a declared type to its SCL spelling, or says why not.
func sclType(t string) (string, string) {
	u := strings.ToUpper(strings.TrimSpace(t))
	if elementary[u] && u != "LTIME" {
		if u == "TIME_OF_DAY" {
			return "TOD", ""
		}
		return u, ""
	}
	if strings.HasPrefix(u, "STRING") || strings.HasPrefix(u, "WSTRING") {
		return "", "STRING is not in the first subset (TIA's STRING holds at most 254 characters; a nautilus STRING has no such length)"
	}
	if u == "LTIME" {
		return "", "LTIME is not in the first subset (nautilus's LTIME is milliseconds, TIA's nanoseconds)"
	}
	if u == "DT" || u == "DATE_AND_TIME" || u == "LDT" || u == "DTL" {
		return "", u + " is not in the first subset (which of DT, LDT and DTL it maps to is still to be decided)"
	}
	if strings.HasPrefix(u, "ARRAY") {
		return "", "arrays are not in the first subset"
	}
	if u == "SR" || u == "RS" {
		return "", u + " is not callable from SCL; write the set/reset logic (an inline form is planned)"
	}
	return "", fmt.Sprintf("type %s is not in the first subset (UDTs, enums and user function blocks come later)", t)
}

func (w *writer) program(path string, prog *st.Program) *progWriter {
	pw := &progWriter{w: w, path: path, locals: map[string]local{}, statics: map[string]bool{}}
	before := len(w.diags)

	// nautilus's own lowering first: what nautilus cannot run is not
	// written for the CPU either.
	globals := map[string]*ir.Type{}
	for _, g := range w.globals {
		if t := st.DatatypeToIRType(g.Type); t != nil {
			globals[g.Name] = t
		}
	}
	if _, err := st.LowerWithOpts(prog, st.LowerOpts{ImplicitGlobals: globals}); err != nil {
		line := 0
		if le, ok := st.AsLowerError(err); ok {
			line = le.Pos.Line
		}
		pw.diag("siemens/nautilus", line, "nautilus refuses this program: "+err.Error())
		return nil
	}
	if len(prog.FBDecls) > 0 || len(prog.FuncDecls) > 0 {
		pw.diag("siemens/user-pou", 1, "user FUNCTION and FUNCTION_BLOCK declarations are not in the first subset")
	}
	if len(prog.TypeDecls) > 0 {
		pw.diag("siemens/type", prog.TypeDecls[0].Pos.Line, "TYPE declarations (UDTs, enums) are not in the first subset")
	}

	var decls strings.Builder
	for _, vb := range prog.VarBlocks {
		kind := vb.Kind
		switch kind {
		case "VAR_EXTERNAL", "VAR_GLOBAL":
			// Manifest tags: they live in the global DB and need no
			// declaration here.
			continue
		case "VAR", "VAR_TEMP", "VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT":
		default:
			pw.diag("siemens/var", lineOf(vb), fmt.Sprintf("%s is not in the first subset", kind))
			continue
		}
		if vb.Constant {
			pw.diag("siemens/var", lineOf(vb), "VAR CONSTANT is not in the first subset")
			continue
		}
		header := kind
		if vb.Retain {
			if kind != "VAR" {
				pw.diag("siemens/var", lineOf(vb), "RETAIN is written only for VAR")
				continue
			}
			header = "VAR RETAIN"
		}
		var lines []string
		for _, v := range vb.Variables {
			u := strings.ToUpper(strings.TrimSpace(v.Datatype))
			var typ string
			if inst, ok := instances[u]; ok {
				if kind != "VAR" {
					pw.diag("siemens/var", v.Pos.Line, fmt.Sprintf("%s: a %s instance must be a static (VAR), not in %s", v.Name, u, kind))
					continue
				}
				typ = inst
			} else {
				t, why := sclType(v.Datatype)
				if why != "" {
					pw.diag("siemens/type", v.Pos.Line, fmt.Sprintf("%s: %s", v.Name, why))
					continue
				}
				typ = t
			}
			pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
			line := fmt.Sprintf("      %s : %s", v.Name, typ)
			if kind == "VAR_TEMP" {
				// TIA has no start values for temps, and nautilus resets a
				// temp to its init on every scan (#203): the body starts
				// with an assignment for each instead.
				if _, ok := zero(u); !ok {
					pw.diag("siemens/var", v.Pos.Line, fmt.Sprintf("%s: a %s temp is not in the first subset", v.Name, u))
					continue
				}
				pw.temps = append(pw.temps, v)
				lines = append(lines, line+";")
				continue
			}
			_, isInst := instances[u]
			pw.statics[strings.ToLower(v.Name)] = isInst
			if v.Initial != nil {
				if _, isInst := instances[u]; isInst {
					pw.diag("siemens/init", v.Pos.Line, fmt.Sprintf("%s: an instance initializer is not in the first subset", v.Name))
					continue
				}
				line += " := " + pw.expr(v.Initial, 0)
			}
			lines = append(lines, line+";")
		}
		if len(lines) > 0 {
			fmt.Fprintf(&decls, "   %s \n%s\n   END_VAR\n", header, strings.Join(lines, "\n"))
		}
	}
	if len(w.diags) > before {
		return nil
	}

	var body strings.Builder
	for _, v := range pw.temps {
		init, _ := zero(strings.ToUpper(strings.TrimSpace(v.Datatype)))
		if v.Initial != nil {
			init = pw.expr(v.Initial, 0)
		}
		fmt.Fprintf(&body, "\t#%s := %s;\n", v.Name, init)
	}
	pw.stmts(&body, prog.Statements, 1)
	if len(w.diags) > before {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION_BLOCK %s\n", quote(prog.Name))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString(decls.String())
	b.WriteString("\nBEGIN\n")
	b.WriteString(body.String())
	b.WriteString("END_FUNCTION_BLOCK\n")
	pw.fb = []byte(b.String())
	return pw
}

func lineOf(vb st.VarBlock) int {
	if len(vb.Variables) > 0 {
		return vb.Variables[0].Pos.Line
	}
	return 0
}

func (pw *progWriter) diag(rule string, line int, msg string) {
	pw.w.diag(rule, pw.path, line, msg)
}

func indent(n int) string { return strings.Repeat("\t", n) }

func (pw *progWriter) stmts(b *strings.Builder, ss []st.Statement, depth int) {
	for _, s := range ss {
		pw.stmt(b, s, depth)
	}
}

func (pw *progWriter) stmt(b *strings.Builder, s st.Statement, d int) {
	in := indent(d)
	switch s := s.(type) {
	case *st.AssignStmt:
		target, tt := s.Target, ""
		if s.TargetExpr != nil {
			target, tt = pw.expr(s.TargetExpr, 0), pw.typ(s.TargetExpr)
		} else {
			tt = pw.typ(&st.IdentExpr{Name: s.Target, Pos: s.Pos})
			target = pw.ident(target, s.Pos.Line)
		}
		fmt.Fprintf(b, "%s%s := %s;\n", in, target, pw.store(s.Value, tt, s.Pos.Line))
	case *st.IfStmt:
		fmt.Fprintf(b, "%sIF %s THEN\n", in, pw.expr(s.Condition, 0))
		pw.stmts(b, s.Then, d+1)
		for _, e := range s.ElsIfs {
			fmt.Fprintf(b, "%sELSIF %s THEN\n", in, pw.expr(e.Condition, 0))
			pw.stmts(b, e.Body, d+1)
		}
		if len(s.Else) > 0 {
			fmt.Fprintf(b, "%sELSE\n", in)
			pw.stmts(b, s.Else, d+1)
		}
		fmt.Fprintf(b, "%sEND_IF;\n", in)
	case *st.ForStmt:
		step := ""
		if s.Step != nil {
			step = " BY " + pw.expr(s.Step, 0)
		}
		fmt.Fprintf(b, "%sFOR %s := %s TO %s%s DO\n", in, pw.ident(s.Variable, s.Pos.Line), pw.expr(s.Start, 0), pw.expr(s.End, 0), step)
		pw.stmts(b, s.Body, d+1)
		fmt.Fprintf(b, "%sEND_FOR;\n", in)
	case *st.WhileStmt:
		fmt.Fprintf(b, "%sWHILE %s DO\n", in, pw.expr(s.Condition, 0))
		pw.stmts(b, s.Body, d+1)
		fmt.Fprintf(b, "%sEND_WHILE;\n", in)
	case *st.RepeatStmt:
		fmt.Fprintf(b, "%sREPEAT\n", in)
		pw.stmts(b, s.Body, d+1)
		fmt.Fprintf(b, "%sUNTIL %s\n%sEND_REPEAT;\n", in, pw.expr(s.Condition, 0), in)
	case *st.CaseStmt:
		fmt.Fprintf(b, "%sCASE %s OF\n", in, pw.expr(s.Expression, 0))
		for _, c := range s.Cases {
			var labels []string
			for _, v := range c.Values {
				labels = append(labels, pw.expr(v, 0))
			}
			for _, r := range c.Ranges {
				labels = append(labels, pw.expr(r.Lo, 0)+".."+pw.expr(r.Hi, 0))
			}
			fmt.Fprintf(b, "%s\t%s:\n", in, strings.Join(labels, ", "))
			pw.stmts(b, c.Body, d+2)
		}
		if len(s.Else) > 0 {
			fmt.Fprintf(b, "%s\tELSE\n", in)
			pw.stmts(b, s.Else, d+2)
		}
		fmt.Fprintf(b, "%sEND_CASE;\n", in)
	case *st.CallStmt:
		fmt.Fprintf(b, "%s%s;\n", in, pw.call(s.Call, true))
	case *st.ExitStmt:
		fmt.Fprintf(b, "%sEXIT;\n", in)
	case *st.ContinueStmt:
		fmt.Fprintf(b, "%sCONTINUE;\n", in)
	case *st.ReturnStmt:
		fmt.Fprintf(b, "%sRETURN;\n", in)
	default:
		pw.diag("siemens/statement", lineAt(s), fmt.Sprintf("statement %T is not in the first subset", s))
	}
}

func lineAt(n any) int {
	if p, ok := n.(interface{ NodePos() st.Pos }); ok {
		return p.NodePos().Line
	}
	return 0
}

// ident prints a name: a local as #name, a manifest tag as a member of
// the global DB.
func (pw *progWriter) ident(name string, line int) string {
	if l, ok := pw.locals[strings.ToLower(name)]; ok {
		return "#" + l.name
	}
	if g, ok := pw.w.globals[strings.ToLower(name)]; ok {
		return quote(pw.w.tagsDB) + "." + g.Name
	}
	pw.diag("siemens/name", line, fmt.Sprintf("%s is neither a variable of the program nor a manifest tag", name))
	return name
}

// store prints the value of an assignment to a target of type tt the way
// nautilus stores it (#800): integer arithmetic at 64 bits, and any
// integer whose width or signedness differs from the target's, wrapped to
// the target. nautilus's own lowering, which runs first, already refuses
// an implicit narrowing (u := a, INT into UINT) and an out-of-range
// literal; what reaches the wrap here is a value nautilus types as its
// 64-bit int (a counter's CV) and the result of integer arithmetic.
func (pw *progWriter) store(v st.Expression, tt string, line int) string {
	vt := pw.typ(v)
	switch {
	case vt == tWide:
		if !isInt(tt) || tt == "ULINT" {
			pw.diag("siemens/int-width", line, fmt.Sprintf("integer arithmetic stored into a %s: convert it explicitly (…_TO_…)", orUnknown(tt)))
			return ""
		}
		pw.wide = true
		s := pw.expr(v, 0)
		pw.wide = false
		return wrap(s, tt)
	case isInt(vt) && isInt(tt) && vt != tt:
		if vt == "ULINT" || tt == "ULINT" {
			pw.diag("siemens/int-width", line, "a store between ULINT and another integer type is not in the first subset")
			return ""
		}
		if holds(tt, vt) {
			return pw.expr(v, 0) // exact: TIA converts a widening store implicitly
		}
		return wrap(toLINT(vt, pw.expr(v, 0)), tt)
	}
	return pw.expr(v, 0)
}

// toLINT widens an integer expression of type t to LINT.
func toLINT(t, s string) string {
	if t == "LINT" {
		return s
	}
	return t + "_TO_LINT(" + s + ")"
}
