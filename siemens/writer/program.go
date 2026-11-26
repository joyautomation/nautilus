package writer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	// VAR_INPUT/OUTPUT), which the Web API can read, with what each holds
	// below it; temps are not.
	statics map[string]*shape
	temps   []st.VarDecl
	// last names the latch of each timer instance the block declares,
	// by lower-cased instance name (timers.go).
	last map[string]string
	// lines is the source line (of the parsed ST) of each line written
	// after BEGIN, in order.
	lines []int
	// uses are the manifest tags the program names, lower-cased, and
	// share what it shares with other tasks (nil: nothing) (tasks.go).
	uses  map[string]*tagUse
	share *share

	// What is written: a PROGRAM, or one of the project's blocks (pou,
	// pous.go) first used at site. name is the POU's name as declared;
	// srcLines the line map of the source it comes from (nil for ST).
	kind     int
	name     string
	pou      *pou
	site     site
	srcLines []int
	// deps are the project's blocks this one uses (lower-cased).
	deps map[string]bool
	// consts are the VAR CONSTANT declarations, constVals their values
	// by lower-cased name; gconsts the project's global constants named,
	// each with the first line naming it.
	consts    []string
	constVals map[string]string
	gconsts   map[string]int
	// enums are the enumeration members the block names, each its own
	// VAR CONSTANT (enums.go); enumHint the enumeration the expression
	// being written is expected to be, for a member two enumerations
	// declare.
	enums    map[string]enumConst
	enumHint string
	// structTag is the first STRUCT or ARRAY manifest tag this block
	// writes (whole, a member or an element of it), "" for none (pous.go,
	// aliasing).
	structTag string

	// Arrays (arrays.go): consts0 resolves the block's own constants for
	// an array bound; ixTemps are the temps that hold a checked index;
	// forRange the FOR variables whose values are proved, while their loop
	// is written; hoistN numbers the temps a statement computes first, and
	// hoistDecls declares them; faults is set once the block checks or
	// propagates a fault.
	consts0    constLookup
	ixTemps    map[string]bool
	forRange   map[string]forBounds
	hoistN     int
	hoistDecls []string
	faults     bool
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
	// nautilus's counter value is its 64-bit int. TIA refuses the
	// CTU_LINT/CTD_LINT/CTUD_LINT instances with a LINT variable as PV
	// (TIA V21 compile, #1338, #1371); a counter object IEC_LCOUNTER,
	// called by its CTU/CTD/CTUD method, compiles in every form
	// (counterCall).
	"CTU": "IEC_LCOUNTER", "CTD": "IEC_LCOUNTER", "CTUD": "IEC_LCOUNTER",
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
	if u == "STRING" {
		// TIA's String holds 254 bytes, a nautilus STRING any number: a
		// literal longer is refused, a result that grows past it warned
		// (strings.go).
		return "String", ""
	}
	if strings.HasPrefix(u, "STRING") || strings.HasPrefix(u, "WSTRING") {
		return "", gapMessage(gapString, t, "")
	}
	if u == "LTIME" {
		return "", gapMessage(gapTime, "LTIME", "nautilus's LTIME is milliseconds, TIA's nanoseconds")
	}
	if u == "DT" || u == "DATE_AND_TIME" || u == "LDT" || u == "DTL" {
		return "", gapMessage(gapTime, u, "which of DT, LDT and DTL it maps to is still to be decided")
	}
	return "", gapMessage(gapOther, "the type "+t, "")
}

func (w *writer) program(path string, libs []string, src string, lines []int, prog *st.Program) *progWriter {
	pw := &progWriter{w: w, path: path, libs: libs, locals: map[string]local{}, statics: map[string]*shape{}, uses: map[string]*tagUse{}, share: w.shares[strings.ToLower(prog.Name)],
		kind: kindProgram, name: prog.Name, srcLines: lines}

	// nautilus's own lowering first: what nautilus cannot run is not
	// written for the CPU either. It compiles the program with the
	// project's library ahead of it, as naut check composes them, so the
	// library's TYPEs, blocks and constants are in scope; the writer then
	// refuses what it does not translate of them by name.
	if !pw.lower(src) {
		return nil
	}
	// An enumeration or alias the program declares is the writer's gap;
	// the rest of the program is still checked, so one run lists all.
	typeGap := false
	for _, td := range prog.TypeDecls {
		switch td.Type.(type) {
		case *st.StructType, *st.EnumType:
		default:
			pw.gap("siemens/type", td.Pos.Line, gapOther, "the type alias "+td.Name, "")
			typeGap = true
		}
	}
	if !pw.block("FUNCTION_BLOCK "+quote(w.prefix+prog.Name)+"\n", "END_FUNCTION_BLOCK", prog.VarBlocks, prog.Statements) || typeGap {
		return nil
	}
	return pw
}

// sectionRank orders the declaration sections as TIA's own sources do.
var sectionRank = map[string]int{"VAR_INPUT": 0, "VAR_OUTPUT": 1, "VAR_IN_OUT": 2, "VAR": 3, "VAR RETAIN": 3, "VAR_TEMP": 4}

// block writes one POU into pw.fb: header (the FUNCTION_BLOCK or
// FUNCTION line), the declarations, the body, footer. It reports false
// when anything was refused.
func (pw *progWriter) block(header, footer string, vbs []st.VarBlock, stmts []st.Statement) bool {
	w := pw.w
	before := len(w.diags)
	pw.consts0 = pw.localConst(vbs)
	pw.ixTemps = map[string]bool{}
	pw.forRange = map[string]forBounds{}
	sections := pw.declare(vbs)
	if pw.kind == kindFC {
		// The return value, as nautilus names it inside the body: a temp
		// that starts at its type's zero (the body sets it first) and is
		// copied to the FC's return value on the way out.
		u := strings.ToUpper(strings.TrimSpace(pw.pou.ret))
		t, _ := w.declType(pw.pou.ret, pw.path)
		if w.structOf(u) != nil {
			// A STRUCT result: the temp is reset member by member, as a
			// struct temp is.
			if _, why := w.leaves(u, ""); why != "" {
				pw.diag("siemens/type", pw.pou.lineOf(), pw.name+": "+why)
			} else {
				pw.locals[strings.ToLower(pw.name)] = local{name: pw.name, typ: u}
				pw.temps = append([]st.VarDecl{{Name: pw.name, Datatype: pw.pou.ret}}, pw.temps...)
				sections = append(sections, section{"VAR_TEMP", []string{fmt.Sprintf("      %s : %s;", sclName(pw.name), t)}})
			}
		} else if _, ok := w.zeroOf(u); !ok {
			pw.diag("siemens/type", pw.pou.lineOf(), pw.name+": "+gapMessage(gapOther, "a FUNCTION returning a "+u, ""))
		} else {
			pw.locals[strings.ToLower(pw.name)] = local{name: pw.name, typ: u}
			pw.temps = append([]st.VarDecl{{Name: pw.name, Datatype: pw.pou.ret}}, pw.temps...)
			sections = append(sections, section{"VAR_TEMP", []string{fmt.Sprintf("      %s : %s;", sclName(pw.name), t)}})
		}
	}
	if len(w.diags) > before {
		return false
	}

	var body strings.Builder
	for _, v := range pw.temps {
		if w.structOf(v.Datatype) != nil {
			ls, _ := w.leaves(v.Datatype, "")
			for _, l := range ls {
				pw.emit(&body, v.Pos.Line, "\t#%s%s := %s;\n", sclName(v.Name), l.path, l.init)
			}
			continue
		}
		init, _ := w.zeroOf(v.Datatype)
		if v.Initial != nil {
			init = pw.expr(v.Initial, 0)
		}
		pw.emit(&body, v.Pos.Line, "\t#%s := %s;\n", sclName(v.Name), init)
	}
	pw.stmts(&body, stmts, 1)
	if pw.kind == kindFC {
		pw.emit(&body, 0, "\t%s;\n", pw.fcReturn())
	}
	consts := append(append(pw.consts, pw.globalConsts()...), pw.enumConsts()...)
	if len(w.diags) > before {
		return false
	}
	if len(pw.hoistDecls) > 0 {
		sections = append(sections, section{"VAR_TEMP", pw.hoistDecls})
	}

	sort.SliceStable(sections, func(i, j int) bool { return sectionRank[sections[i].header] < sectionRank[sections[j].header] })
	// One section of each kind, as TIA's own sources have.
	var merged []section
	for _, s := range sections {
		if n := len(merged); n > 0 && merged[n-1].header == s.header {
			merged[n-1].lines = append(merged[n-1].lines, s.lines...)
			continue
		}
		merged = append(merged, s)
	}
	sections = merged
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "   %s \n%s\n   END_VAR\n", s.header, strings.Join(s.lines, "\n"))
	}
	if len(consts) > 0 {
		fmt.Fprintf(&b, "   VAR CONSTANT \n%s\n   END_VAR\n", strings.Join(consts, "\n"))
	}
	b.WriteString("\nBEGIN\n")
	b.WriteString(body.String())
	b.WriteString(footer + "\n")
	pw.fb = []byte(b.String())
	return true
}

// fcReturn is the statement that hands a FUNCTION's result back: the FC's
// return value (#<block name>) from the temp the body works on.
func (pw *progWriter) fcReturn() string {
	return "#" + sclName(pw.w.prefix+pw.name) + " := #" + sclName(pw.name)
}

type section struct {
	header string
	lines  []string
}

// declare writes the declaration sections of vbs, in source order.
func (pw *progWriter) declare(vbs []st.VarBlock) []section {
	w := pw.w
	var sections []section
	// unretained are the PIDs a program declares VAR RETAIN, written in
	// its plain VAR section.
	var unretained []string
	typeRefused := map[string]bool{}
	block := pw.kind != kindProgram
	pw.last = w.lastNames(vbs)
	for _, vb := range vbs {
		kind := vb.Kind
		if vb.Constant && kind != "VAR_EXTERNAL" {
			// VAR CONSTANT, and a VAR_GLOBAL CONSTANT inside a POU, which
			// nautilus reads the same way.
			if kind != "VAR" && kind != "VAR_GLOBAL" {
				pw.gap("siemens/var", lineOf(vb), gapOther, kind+" CONSTANT", "")
				continue
			}
			pw.constants(vb)
			continue
		}
		switch kind {
		case "VAR_EXTERNAL", "VAR_GLOBAL":
			// Manifest tags: they live in the global DB and need no
			// declaration here.
			continue
		case "VAR", "VAR_TEMP", "VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT":
		default:
			pw.gap("siemens/var", lineOf(vb), gapOther, kind, "")
			continue
		}
		header := kind
		temp := kind == "VAR_TEMP"
		where := kind
		if pw.kind == kindFC {
			where = "a FUNCTION"
			if kind == "VAR" {
				// An FC has no statics; nautilus starts a function's
				// locals afresh on every call, as a temp is set here.
				header, temp = "VAR_TEMP", true
			}
		}
		static := kind == "VAR" && !temp
		if vb.Retain {
			if kind != "VAR" {
				pw.gap("siemens/var", lineOf(vb), gapOther, kind+" RETAIN", "only VAR RETAIN is written")
				continue
			}
			if block {
				pw.gap("siemens/var", lineOf(vb), gapOther, "VAR RETAIN in "+pw.what(), "TIA keeps a multi-instance's retentivity with the block that holds it")
				continue
			}
			header = "VAR RETAIN"
		}
		var lines []string
		for _, v := range vb.Variables {
			u := strings.ToUpper(strings.TrimSpace(v.Datatype))
			var typ string
			if latch, ok := latches[u]; ok {
				if !static {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "a "+u+" instance in "+where, "only a static (VAR) instance is written"))
					continue
				}
				if v.Initial != nil {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "an instance initializer", ""))
					continue
				}
				pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
				pw.statics[strings.ToLower(v.Name)] = &shape{name: v.Name, instance: true}
				lines = append(lines, fmt.Sprintf("      %s : Struct\n%s      END_STRUCT;", sclName(v.Name), latch.members()))
				continue
			}
			if sd := w.structOf(v.Datatype); sd != nil {
				if why := w.typeRefusal(v.Datatype, map[string]bool{}); why != "" {
					if !typeRefused[why] {
						typeRefused[why] = true
						pw.diag("siemens/type", v.Pos.Line, fmt.Sprintf("%s: the type %s: %s", v.Name, sd.name, why))
					}
					continue
				}
				if v.Initial != nil {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "a struct variable's initializer", ""))
					continue
				}
				if kind == "VAR_IN_OUT" && !block {
					// The task's OB calls the program's FB with no
					// arguments: nothing would bind it.
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "a struct in a program's VAR_IN_OUT", "the task's OB calls the program with nothing to bind it to"))
					continue
				}
				pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
				line := fmt.Sprintf("      %s : %s;", sclName(v.Name), w.typeName(sd, pw.path))
				if temp {
					// Reset member by member at the start of the body, as
					// nautilus resets a temp on every scan (#203).
					if _, why := w.leaves(v.Datatype, ""); why != "" {
						pw.diag("siemens/var", v.Pos.Line, v.Name+": "+why)
						continue
					}
					pw.temps = append(pw.temps, v)
				} else if kind != "VAR_IN_OUT" {
					pw.statics[strings.ToLower(v.Name)] = w.shapeOf(v.Name, v.Datatype)
				}
				lines = append(lines, line)
				continue
			}
			if p := w.userFB(u); p != nil {
				if vb.Retain && isPID(p) && static && v.Initial == nil {
					// nautilus never keeps a PID across a restart (it starts
					// every clock block over: runtime/retainlocals.go), and TIA
					// would keep one held in a RETAIN section: it goes in the
					// plain VAR section instead (pid.go).
					pw.need(p, v.Pos.Line)
					pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
					pw.statics[strings.ToLower(v.Name)] = w.fbShape(v.Name, p, map[string]bool{})
					unretained = append(unretained, fmt.Sprintf("      %s : %s;   // VAR RETAIN in nautilus, which starts a PID over after a restart", sclName(v.Name), quote(w.prefix+p.name)))
					continue
				}
				if vb.Retain {
					// Not in any example (#1301 D1b): nautilus retains
					// the whole instance. TIA keeps a multi-instance in
					// a RETAIN section whole too (it compiles, V21), a
					// PID inside it included, which nautilus never keeps:
					// lifting this needs pid.go's rule first.
					pw.gap("siemens/var", v.Pos.Line, gapOther, "an instance of "+p.what()+" in VAR RETAIN ("+v.Name+")", "")
					continue
				}
				if !static {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "an instance of "+p.what()+" in "+where, "only a static (VAR) instance is written"))
					continue
				}
				if v.Initial != nil {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "an instance initializer", ""))
					continue
				}
				pw.need(p, v.Pos.Line)
				pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
				pw.statics[strings.ToLower(v.Name)] = w.fbShape(v.Name, p, map[string]bool{})
				lines = append(lines, fmt.Sprintf("      %s : %s;", sclName(v.Name), quote(w.prefix+p.name)))
				continue
			}
			if timers[u] && static {
				if v.Initial != nil {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "an instance initializer", ""))
					continue
				}
				last := pw.last[strings.ToLower(v.Name)]
				pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
				pw.statics[strings.ToLower(v.Name)] = &shape{name: v.Name, instance: true}
				decl := []string{fmt.Sprintf("      %s : %s;", sclName(v.Name), instances[u]), lastDecl(last)}
				if vb.Retain {
					// nautilus never keeps a timer across a restart (it
					// starts every clock block over: runtime/retainlocals.go),
					// and TIA would keep one held in a RETAIN section: the
					// timer and its latch go in the plain VAR section.
					decl[0] = strings.TrimSuffix(decl[0], ";") + ";   // VAR RETAIN in nautilus, which starts a timer over after a restart"
					unretained = append(unretained, decl...)
					continue
				}
				lines = append(lines, decl...)
				continue
			}
			if inst, ok := instances[u]; ok {
				if !static {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "a "+u+" instance in "+where, "only a static (VAR) instance is written"))
					continue
				}
				typ = inst
			} else {
				t, why := w.sclType(v.Datatype)
				if isArrayType(u) {
					if why = boundsRefusal(v.Type); why == "" {
						t, why = w.arrayDecl(v.Datatype, pw.consts0)
					}
					if why == "" && kind == "VAR_IN_OUT" && !block {
						why = gapMessage(gapOther, "an array in a program's VAR_IN_OUT", "the task's OB calls the program with nothing to bind it to")
					}
				}
				if why != "" {
					// Once per type in a program: a plant model with 84
					// instances of one block says so once.
					if !typeRefused[why] {
						typeRefused[why] = true
						pw.diag("siemens/type", v.Pos.Line, fmt.Sprintf("%s: %s", v.Name, why))
					}
					continue
				}
				typ = t
			}
			// An array's bounds folded here, in the block that declares it.
			pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: w.canonArray(u, pw.consts0)}
			line := fmt.Sprintf("      %s : %s", sclName(v.Name), typ)
			if temp {
				// TIA has no start values for temps, and nautilus resets a
				// temp to its init on every scan (#203): the body starts
				// with an assignment for each instead.
				if _, ok := w.zeroOf(u); !ok {
					pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "a "+u+" temp", ""))
					continue
				}
				pw.temps = append(pw.temps, v)
				lines = append(lines, line+";")
				continue
			}
			if _, isInst := instances[u]; isInst {
				pw.statics[strings.ToLower(v.Name)] = &shape{name: v.Name, instance: true}
			} else if kind != "VAR_IN_OUT" {
				pw.statics[strings.ToLower(v.Name)] = w.shapeOf(v.Name, v.Datatype)
			}
			if v.Initial != nil && !(pw.kind == kindFC && kind == "VAR_INPUT") {
				// (An FC's input has no start value in TIA; nautilus binds
				// every input of a function call.)
				if _, isInst := instances[u]; isInst {
					pw.diag("siemens/init", v.Pos.Line, v.Name+": "+gapMessage(gapOther, "an instance initializer", ""))
					continue
				}
				if n, ok := w.enumValue(v.Datatype, v.Initial); ok {
					line += " := " + n
				} else {
					line += " := " + pw.initValue(v.Initial)
				}
			} else if n, ok := w.enumStart(v.Datatype); ok && !(pw.kind == kindFC && kind == "VAR_INPUT") {
				line += " := " + n
			} else if n := w.arrayStart(v.Datatype, pw.consts0); n != "" && !(pw.kind == kindFC && kind == "VAR_INPUT") {
				line += " := " + n
			}
			lines = append(lines, line+";")
		}
		if len(lines) > 0 {
			sections = append(sections, section{header, lines})
		}
	}
	if len(unretained) > 0 {
		for i := range sections {
			if sections[i].header == "VAR" {
				sections[i].lines = append(sections[i].lines, unretained...)
				return sections
			}
		}
		sections = append(sections, section{"VAR", unretained})
	}
	return sections
}

// initValue prints a declaration's start value: a constant it names as
// that constant's literal (a start value is a literal in SCL), else the
// expression.
func (pw *progWriter) initValue(e st.Expression) string {
	if id, ok := e.(*st.IdentExpr); ok {
		k := strings.ToLower(id.Name)
		_, isConst := pw.constVals[k]
		_, isGlobal := pw.w.lib.constDecls[k]
		if _, isLocal := pw.locals[k]; isConst || isGlobal && !isLocal {
			if v, why := pw.constInit(e, "", map[string]bool{}); why == "" {
				return v
			}
		}
	}
	return pw.expr(e, 0)
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
	pw.divergesAs("siemens/divergence", line, what, cond, onSiemens)
}

// ConversionRangeRule tags the REAL → integer conversion warnings (TRUNC
// included). They are common and seldom provably in range, so check and
// the deploy summarize them per program and list the sites on request,
// while a MUX or shift warning stands at its line.
const ConversionRangeRule = "siemens/conversion-range"

func (pw *progWriter) divergesAs(rule string, line int, what, cond, onSiemens string) {
	pw.w.warnings = append(pw.w.warnings, Diag{Rule: rule, Path: pw.path, Line: line,
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
	// What the statement computes first: its array checks and the
	// FUNCTIONs that can fault (arrays.go).
	orig := s
	if s = pw.liftStmt(b, s, d); s == nil {
		return
	}
	switch s := s.(type) {
	case *st.AssignStmt:
		// A write to a counter's member outside its call: whether TIA's
		// IEC_LCOUNTER keeps it is not measured (#1338).
		if m, ok := s.TargetExpr.(*st.MemberExpr); ok {
			if _, counter := counterInputs[pw.typ(m.Object)]; counter {
				pw.gap("siemens/member", s.Pos.Line, gapOther, "a write to "+exprShort(m)+", a member of a counter, outside its call", "set an input in the counter's call (PV := …)")
				break
			}
		}
		if pw.refusedTimerWrite(s.TargetExpr, s.Pos.Line) || pw.refusedTimerCopy(s) {
			break
		}
		target, tt := s.Target, ""
		if s.TargetExpr != nil {
			target, tt = pw.expr(s.TargetExpr, 0), pw.typ(s.TargetExpr)
		} else {
			tt = pw.typ(&st.IdentExpr{Name: s.Target, Pos: s.Pos})
			target = pw.ident(target, s.Pos.Line)
		}
		te := s.TargetExpr
		if te == nil {
			te = &st.IdentExpr{Name: s.Target, Pos: s.Pos}
		}
		restore := pw.hint(pw.rawTyp(te))
		defer restore()
		if pw.w.structOf(tt) != nil || isArrayType(tt) {
			pw.assignStruct(b, target, s.Value, in, at)
		} else {
			pw.emit(b, at, "%s%s := %s;\n", in, target, pw.store(s.Value, tt, s.Pos.Line))
		}
		pw.assigned(b, in, at, te)
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
			step = " BY " + pw.forBound(s.Step, s.Variable)
		}
		pw.emit(b, at, "%sFOR %s := %s TO %s%s DO\n", in, pw.ident(s.Variable, s.Pos.Line), pw.forBound(s.Start, s.Variable), pw.forBound(s.End, s.Variable), step)
		// The loop variable's values, proved for the body's indices.
		k := strings.ToLower(s.Variable)
		prev, had := pw.forRange[k]
		delete(pw.forRange, k)
		if r, ok := pw.forBoundsOf(orig.(*st.ForStmt)); ok {
			pw.forRange[k] = r
		}
		pw.stmts(b, s.Body, d+1)
		delete(pw.forRange, k)
		if had {
			pw.forRange[k] = prev
		}
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
		sel := pw.rawTyp(s.Expression)
		for _, c := range s.Cases {
			restore := pw.hint(sel)
			var labels []string
			for _, v := range c.Values {
				labels = append(labels, pw.expr(v, 0))
			}
			for _, r := range c.Ranges {
				labels = append(labels, pw.expr(r.Lo, 0)+".."+pw.expr(r.Hi, 0))
			}
			restore()
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
			if p := pw.w.userFB(l.typ); p != nil {
				pw.fbCall(b, s.Call, l, p, d, at)
				break
			}
		}
		pw.emit(b, at, "%s%s;\n", in, pw.call(s.Call, true))
		if l, ok := pw.locals[strings.ToLower(s.Call.Name)]; ok && s.Call.Callee == nil {
			if _, counter := counterInputs[l.typ]; counter || timers[l.typ] {
				// TIA takes no output binding at a counter's method call:
				// each is an assignment from the counter after it, in
				// source order, under the store rules of any assignment
				// (as a user FB's, pous.go). A timer's call binds its Q
				// and ET to its latch (timers.go): each of its bindings is
				// an assignment from the latch after the call.
				for _, o := range s.Call.OutputBindings {
					pos := o.Pos
					if pos.Line == 0 {
						pos = s.Call.Pos
					}
					pw.stmt(b, &st.AssignStmt{TargetExpr: o.Target, Value: &st.MemberExpr{Object: &st.IdentExpr{Name: l.name, Pos: pos}, Member: o.Name, Pos: pos}, Pos: pos}, d)
				}
				break
			}
		}
		for _, o := range s.Call.OutputBindings {
			pw.assigned(b, in, at, o.Target)
		}
	case *st.ExitStmt:
		pw.emit(b, at, "%sEXIT;\n", in)
	case *st.ContinueStmt:
		pw.emit(b, at, "%sCONTINUE;\n", in)
	case *st.ReturnStmt:
		if pw.kind == kindFC {
			pw.emit(b, at, "%s%s;\n", in, pw.fcReturn())
		}
		pw.emit(b, at, "%sRETURN;\n", in)
	default:
		pw.gap("siemens/statement", lineAt(s), gapOther, fmt.Sprintf("the statement %T", s), "")
	}
}

// forBound prints a FOR loop's start, end or step: a negated literal or
// constant arithmetic (FOR i := -2 TO N - 1, the bounds of an array with
// a negative lower bound) as the one literal nautilus folds it to, when
// the loop variable's type holds it; anything else as an expression.
func (pw *progWriter) forBound(e st.Expression, v string) string {
	switch e.(type) {
	case *st.UnaryExpr, *st.BinaryExpr:
		t := pw.typ(&st.IdentExpr{Name: v})
		if n, ok := pw.fold(e); ok && isInt(t) && inRange(n, t) {
			return strconv.FormatInt(n, 10)
		}
	}
	return pw.expr(e, 0)
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

// assignStruct writes an assignment of a whole struct or array: both
// sides are of one PLC data type, or arrays of one shape (nautilus's
// lowering, which ran first, has checked the value's type), so TIA copies
// it whole, as nautilus does. The value is a variable, a member, an
// element, or a function returning the struct.
func (pw *progWriter) assignStruct(b *strings.Builder, target string, v st.Expression, in string, at int) {
	switch v.(type) {
	case *st.IdentExpr, *st.MemberExpr, *st.IndexExpr, *st.CallExpr:
	default:
		pw.gap("siemens/struct", at, gapOther, "a struct assigned from "+exprShort(v), "")
		return
	}
	pw.emit(b, at, "%s%s := %s;\n", in, target, pw.expr(v, 0))
}

// ident prints a name: a local as #name, a manifest tag as a member of
// the global DB.
func (pw *progWriter) ident(name string, line int) string {
	if l, ok := pw.locals[strings.ToLower(name)]; ok {
		return "#" + sclName(l.name)
	}
	if g, ok := pw.w.globals[strings.ToLower(name)]; ok {
		if pw.kind != kindProgram && pw.w.tasks > 1 {
			pw.gap("siemens/task", line, gapOther, "the manifest tag "+g.Name+" named inside "+pw.what()+" in a project with several tasks", "each task works on its own copy of the tags it shares, and a block has no one task's copy to name; pass the tag to the block as a pin instead")
			return name
		}
		pw.use(g)
		if sh := pw.share; sh != nil && sh.tags[strings.ToLower(g.Name)] != nil {
			return quote(sh.db) + ".tags." + sclName(g.Name)
		}
		return quote(pw.w.tagsDB) + "." + sclName(g.Name)
	}
	if d, ok := pw.w.lib.constDecls[strings.ToLower(name)]; ok {
		pw.useConst(strings.ToLower(name), line)
		return "#" + sclName(d.Name)
	}
	if c, ok := pw.w.lib.consts[strings.ToLower(name)]; ok {
		pw.gap("siemens/library", line, gapOther, "the project's global variable "+c, "a library's VAR_GLOBAL that is not CONSTANT")
		return name
	}
	// After the project's constants, as nautilus resolves a name.
	if _, ok := pw.w.lib.enumMembers[strings.ToLower(name)]; ok {
		return pw.enumMember("", name, line)
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
