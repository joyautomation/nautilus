package writer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

type local struct {
	name string // as declared
	typ  string // IEC type as declared, upper-cased
}

type progWriter struct {
	w      *writer
	path   string
	libs   []string // the project's library sources (Program.Libs)
	locals map[string]local
	fb     []byte
	// wide is set while printing integer arithmetic that nautilus
	// computes at 64 bits (types.go).
	wide bool
	// statics are the program's static variables (VAR, VAR RETAIN,
	// VAR_INPUT/OUTPUT), which the Web API can read; temps are not.
	statics map[string]bool
	temps   []st.VarDecl
	// lines is the source line (of the parsed ST) of each line written
	// after BEGIN, in order.
	lines []int
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
		return "", gapMessage(gapString, "STRING", "TIA's STRING holds at most 254 characters; a nautilus STRING has no such length")
	}
	if u == "LTIME" {
		return "", gapMessage(gapTime, "LTIME", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
	}
	if u == "DT" || u == "DATE_AND_TIME" || u == "LDT" || u == "DTL" {
		return "", gapMessage(gapTime, u, "which of DT, LDT and DTL it maps to is still to be decided")
	}
	if strings.HasPrefix(u, "ARRAY") {
		return "", gapMessage(gapStructured, "arrays", "")
	}
	return "", gapMessage(gapStructured, "the type "+t, "UDTs, enums and user function blocks")
}

func (w *writer) program(path string, libs []string, prog *st.Program) *progWriter {
	pw := &progWriter{w: w, path: path, libs: libs, locals: map[string]local{}, statics: map[string]bool{}}
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
		if pw.libraryMiss(err) {
			// The writer lowers the program alone, without the project's
			// library files (their TYPEs, FUNCTIONs, FUNCTION_BLOCKs and
			// global constants), which nautilus compiles with it: what
			// the program takes from there is the writer's gap.
			pw.diag("siemens/library", line, fmt.Sprintf("the Siemens writer doesn't translate the project's library (its own types, functions, function blocks and constants) yet (#%d, #%d): %s", gapIssue[gapStructured], gapIssue[gapPOUs], err.Error()))
			return nil
		}
		pw.diag("siemens/nautilus", line, "nautilus refuses this program: "+err.Error())
		return nil
	}
	if len(prog.FBDecls) > 0 || len(prog.FuncDecls) > 0 {
		pw.gap("siemens/user-pou", 1, gapPOUs, "user FUNCTIONs and FUNCTION_BLOCKs", "")
	}
	if len(prog.TypeDecls) > 0 {
		pw.gap("siemens/type", prog.TypeDecls[0].Pos.Line, gapStructured, "TYPE declarations (UDTs, enums)", "")
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
			pw.gap("siemens/var", lineOf(vb), gapPOUs, kind, "")
			continue
		}
		if vb.Constant {
			pw.gap("siemens/var", lineOf(vb), gapPOUs, "VAR CONSTANT", "")
			continue
		}
		header := kind
		if vb.Retain {
			if kind != "VAR" {
				pw.gap("siemens/var", lineOf(vb), gapPOUs, kind+" RETAIN", "only VAR RETAIN is written")
				continue
			}
			header = "VAR RETAIN"
		}
		var lines []string
		for _, v := range vb.Variables {
			u := strings.ToUpper(strings.TrimSpace(v.Datatype))
			var typ string
			if latch, ok := latches[u]; ok {
				if kind != "VAR" {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapPOUs, "a "+u+" instance in "+kind, "only a static (VAR) instance is written"))
					continue
				}
				if v.Initial != nil {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapPOUs, "an instance initializer", ""))
					continue
				}
				pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
				pw.statics[strings.ToLower(v.Name)] = true
				lines = append(lines, fmt.Sprintf("      %s : Struct\n%s      END_STRUCT;", v.Name, latch.members()))
				continue
			}
			if inst, ok := instances[u]; ok {
				if kind != "VAR" {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapPOUs, "a "+u+" instance in "+kind, "only a static (VAR) instance is written"))
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
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapPOUs, "a "+u+" temp", ""))
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
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapPOUs, "an instance initializer", ""))
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
		pw.emit(&body, v.Pos.Line, "\t#%s := %s;\n", v.Name, init)
	}
	pw.stmts(&body, prog.Statements, 1)
	if len(w.diags) > before {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "FUNCTION_BLOCK %s\n", quote(w.prefix+prog.Name))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString(decls.String())
	b.WriteString("\nBEGIN\n")
	b.WriteString(body.String())
	b.WriteString("END_FUNCTION_BLOCK\n")
	pw.fb = []byte(b.String())
	return pw
}

// libraryMiss reports a lowering error for a name the project's library
// sources (Program.Libs) declare as a TYPE, FUNCTION, FUNCTION_BLOCK or
// VAR_GLOBAL: the writer lowers the program without them. Any other
// unknown name, a typo included, is nautilus's own refusal.
func (pw *progWriter) libraryMiss(err error) bool {
	m := libraryName.FindStringSubmatch(err.Error())
	return m != nil && libraryDecls(pw.libs)[strings.ToLower(m[1])]
}

var libraryName = regexp.MustCompile(`(?:unknown type|unknown function|undeclared identifier) "([A-Za-z_][A-Za-z0-9_]*)"`)

// libraryDecls is every name the library sources declare at the top
// level, lower-cased. A ladder or FBD library is read through its
// transpiler; a source that reads as none declares nothing.
func libraryDecls(libs []string) map[string]bool {
	out := map[string]bool{}
	for _, src := range libs {
		prog, err := st.Parse(src)
		if err != nil {
			prog = nil
			if f, _, err := ld.TranspileWithLines(src, libs...); err == nil {
				if s, _, err := fbd.TranspileWithLines(f); err == nil {
					prog, _ = st.Parse(s)
				}
			}
			if prog == nil {
				if s, _, err := fbd.TranspileWithLines(src); err == nil {
					prog, _ = st.Parse(s)
				}
			}
		}
		if prog == nil {
			continue
		}
		for _, t := range prog.TypeDecls {
			out[strings.ToLower(t.Name)] = true
		}
		for _, f := range prog.FuncDecls {
			out[strings.ToLower(f.Name)] = true
		}
		for _, f := range prog.FBDecls {
			out[strings.ToLower(f.Name)] = true
		}
		for _, vb := range append(prog.GlobalConsts, prog.VarBlocks...) {
			if vb.Kind == "VAR_GLOBAL" {
				for _, v := range vb.Variables {
					out[strings.ToLower(v.Name)] = true
				}
			}
		}
	}
	return out
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

// diverges warns where the CPU can behave differently from nautilus, in
// an edge case the writer documents rather than refuses: when cond holds,
// nautilus faults the scan and the CPU carries on (Output.Warnings). The
// callers leave out what is provably in range (a constant).
func (pw *progWriter) diverges(line int, what, cond, onSiemens string) {
	pw.w.warnings = append(pw.w.warnings, Diag{Rule: "siemens/divergence", Path: pw.path, Line: line,
		Message: fmt.Sprintf("%s can differ if %s: nautilus faults the scan; on Siemens %s and the scan carries on", what, cond, onSiemens)})
}

func indent(n int) string { return strings.Repeat("\t", n) }

func (pw *progWriter) stmts(b *strings.Builder, ss []st.Statement, depth int) {
	for _, s := range ss {
		pw.stmt(b, s, depth)
	}
}

func (pw *progWriter) stmt(b *strings.Builder, s st.Statement, d int) {
	in := indent(d)
	at := lineAt(s)
	switch s := s.(type) {
	case *st.AssignStmt:
		target, tt := s.Target, ""
		if s.TargetExpr != nil {
			target, tt = pw.expr(s.TargetExpr, 0), pw.typ(s.TargetExpr)
		} else {
			tt = pw.typ(&st.IdentExpr{Name: s.Target, Pos: s.Pos})
			target = pw.ident(target, s.Pos.Line)
		}
		pw.emit(b, at, "%s%s := %s;\n", in, target, pw.store(s.Value, tt, s.Pos.Line))
	case *st.IfStmt:
		pw.emit(b, at, "%sIF %s THEN\n", in, pw.expr(s.Condition, 0))
		pw.stmts(b, s.Then, d+1)
		for _, e := range s.ElsIfs {
			pw.emit(b, lineOr(e.Condition, at), "%sELSIF %s THEN\n", in, pw.expr(e.Condition, 0))
			pw.stmts(b, e.Body, d+1)
		}
		if len(s.Else) > 0 {
			pw.emit(b, at, "%sELSE\n", in)
			pw.stmts(b, s.Else, d+1)
		}
		pw.emit(b, at, "%sEND_IF;\n", in)
	case *st.ForStmt:
		step := ""
		if s.Step != nil {
			step = " BY " + pw.expr(s.Step, 0)
		}
		pw.emit(b, at, "%sFOR %s := %s TO %s%s DO\n", in, pw.ident(s.Variable, s.Pos.Line), pw.expr(s.Start, 0), pw.expr(s.End, 0), step)
		pw.stmts(b, s.Body, d+1)
		pw.emit(b, at, "%sEND_FOR;\n", in)
	case *st.WhileStmt:
		pw.emit(b, at, "%sWHILE %s DO\n", in, pw.expr(s.Condition, 0))
		pw.stmts(b, s.Body, d+1)
		pw.emit(b, at, "%sEND_WHILE;\n", in)
	case *st.RepeatStmt:
		pw.emit(b, at, "%sREPEAT\n", in)
		pw.stmts(b, s.Body, d+1)
		pw.emit(b, at, "%sUNTIL %s\n%sEND_REPEAT;\n", in, pw.expr(s.Condition, 0), in)
	case *st.CaseStmt:
		pw.emit(b, at, "%sCASE %s OF\n", in, pw.expr(s.Expression, 0))
		for _, c := range s.Cases {
			var labels []string
			for _, v := range c.Values {
				labels = append(labels, pw.expr(v, 0))
			}
			for _, r := range c.Ranges {
				labels = append(labels, pw.expr(r.Lo, 0)+".."+pw.expr(r.Hi, 0))
			}
			pw.emit(b, at, "%s\t%s:\n", in, strings.Join(labels, ", "))
			pw.stmts(b, c.Body, d+2)
		}
		if len(s.Else) > 0 {
			pw.emit(b, at, "%s\tELSE\n", in)
			pw.stmts(b, s.Else, d+2)
		}
		pw.emit(b, at, "%sEND_CASE;\n", in)
	case *st.CallStmt:
		if l, ok := pw.locals[strings.ToLower(s.Call.Name)]; ok && s.Call.Callee == nil {
			if latch, ok := latches[l.typ]; ok {
				pw.latch(b, s.Call, l, latch, in, at)
				break
			}
		}
		pw.emit(b, at, "%s%s;\n", in, pw.call(s.Call, true))
	case *st.ExitStmt:
		pw.emit(b, at, "%sEXIT;\n", in)
	case *st.ContinueStmt:
		pw.emit(b, at, "%sCONTINUE;\n", in)
	case *st.ReturnStmt:
		pw.emit(b, at, "%sRETURN;\n", in)
	default:
		pw.gap("siemens/statement", lineAt(s), gapOther, fmt.Sprintf("the statement %T", s), "")
	}
}

// emit writes one or more body lines, each from source line at (in the
// ST the writer parsed): the line map a TIA compile message is read back
// through (Output.Locate).
func (pw *progWriter) emit(b *strings.Builder, at int, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	b.WriteString(text)
	for range strings.Count(text, "\n") {
		pw.lines = append(pw.lines, at)
	}
}

// lineOr is n's line, or or when it has none.
func lineOr(n any, or int) int {
	if l := lineAt(n); l > 0 {
		return l
	}
	return or
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
		if s, ok := pw.narrowNot(v, tt); ok {
			return s
		}
		if isBits(tt) {
			pw.wide = true
			s := pw.expr(v, 0)
			pw.wide = false
			if tt == "LWORD" {
				return "LINT_TO_LWORD(" + s + ")"
			}
			u := unsignedOf(tt)
			return u + "_TO_" + tt + "(" + wrap(s, u) + ")"
		}
		if !isInt(tt) || unsigned64(tt) {
			pw.gap("siemens/int-width", line, gapOther, "integer arithmetic stored into a "+orUnknown(tt), "until then, convert it explicitly (…_TO_…)")
			return ""
		}
		pw.wide = true
		s := pw.expr(v, 0)
		pw.wide = false
		return wrap(s, tt)
	case isInt(vt) && isInt(tt) && vt != tt:
		if vt == "ULINT" || tt == "ULINT" {
			pw.gap("siemens/int-width", line, gapOther, "a store between ULINT and another integer type", "")
			return ""
		}
		if holds(tt, vt) {
			return pw.expr(v, 0) // exact: TIA converts a widening store implicitly
		}
		return wrap(toLINT(vt, pw.expr(v, 0)), tt)
	}
	return pw.expr(v, 0)
}

// toLINT widens an integer or bit string expression of type t to LINT;
// a bit string by its unsigned value, as nautilus holds it.
func toLINT(t, s string) string {
	switch {
	case t == "LINT":
		return s
	case isBits(t):
		u := unsignedOf(t)
		return u + "_TO_LINT(" + t + "_TO_" + u + "(" + s + "))"
	}
	return t + "_TO_LINT(" + s + ")"
}
