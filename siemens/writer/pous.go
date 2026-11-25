package writer

import (
	"fmt"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/st"
)

// The project's own FUNCTION_BLOCKs and FUNCTIONs (#1301), from its
// library files, the program files that declare them, and a dialect's
// block library (TONR).
//
// A FUNCTION_BLOCK becomes an SCL FUNCTION_BLOCK "<prefix><Name>", and
// each instance a multi-instance in the block that declares it (`m101 :
// "naut_MotorStarter";`, called `#m101(...)`): its data lives inside the
// caller's, as a nautilus instance lives in its program, and TIA shows it
// the way nautilus reads. A FUNCTION becomes an FC with its return type;
// nautilus starts every local of a call at its initial value, while an
// FC's temps start undefined, so the FC's body sets each one first, the
// return value included. The body works on a temp named like the
// function (nautilus's `F := …`) and copies it to the FC's return value
// at the end and at each RETURN.
//
// Only the blocks a program reaches are written: a library block nothing
// calls is not the CPU's business, and what it uses that the writer does
// not translate refuses nothing. They are written callees first, since
// TIA generates a block from source only when the blocks it instantiates
// exist (measured on V21: "Data type "naut_X" is unknown").
//
// Arguments are stored the way nautilus stores them (lang/st storeInput is
// the assignment's store rule): an input through store(), an output bound
// with => as an assignment after the call. A VAR_IN_OUT is TIA's InOut,
// passed by reference; nautilus copies it back after the call, so the
// variable bound to one counts as assigned (tasks.go). By reference and
// nautilus's copy-in/copy-out differ only when one variable is bound to
// two in-outs of a call, or a block also names the tag it was handed;
// neither is refused.
//
// A STRUCT pin or result is the struct's PLC data type (structs.go). TIA
// hands a FUNCTION a STRUCT input by reference, where nautilus hands it a
// copy: the FC writing to one would write the caller's variable, so that
// write is refused (assigned, tasks.go), and so is an FC with a STRUCT
// input that writes a STRUCT manifest tag, itself or through a function
// it calls: handed that tag, it would read its own write
// (structInputAliases).
//
// What is refused here, with its reason: VAR RETAIN in a block (TIA keeps a
// multi-instance's retentivity with the block that holds it), a manifest
// tag named inside a block of a project with several tasks (each task
// works on its own copy of the tags it shares, and a block called from
// two tasks has no one copy to name), EN/ENO on a call.

const (
	kindProgram = iota
	kindFB
	kindFC
)

// pou is one FUNCTION_BLOCK or FUNCTION the project declares.
type pou struct {
	kind  int // kindFB or kindFC
	name  string
	vbs   []st.VarBlock
	stmts []st.Statement
	ret   string // a FUNCTION's return type, as declared
	// path and lines: the program file that declares it, with that file's
	// line map (nil for ST). lib is set for a library block, whose file
	// the writer is not told; libLines when its lines are the library
	// file's own (an ST library, not a transpiled ladder).
	path     string
	lines    []int
	lib      bool
	libLines bool
	// builtin is nautilus's PID (pid.go): its body is fixed SCL.
	builtin bool
}

// what names the block in a message.
func (p *pou) what() string {
	if p.kind == kindFC {
		return "FUNCTION " + p.name
	}
	return "FUNCTION_BLOCK " + p.name
}

// pin is one variable of a block's interface or statics, as declared.
type pin struct {
	name string
	kind string // the var block's kind: VAR_INPUT, VAR_OUTPUT, VAR_IN_OUT, VAR, VAR_TEMP
	typ  string // upper-cased
	decl st.VarDecl
}

// pins are a block's variables by upper-cased name (constants and tag
// bindings left out).
func (p *pou) pins() map[string]pin {
	out := map[string]pin{}
	for _, vb := range p.vbs {
		if vb.Constant || vb.Kind == "VAR_EXTERNAL" || vb.Kind == "VAR_GLOBAL" {
			continue
		}
		for _, v := range vb.Variables {
			out[strings.ToUpper(v.Name)] = pin{name: v.Name, kind: vb.Kind, typ: strings.ToUpper(strings.TrimSpace(v.Datatype)), decl: v}
		}
	}
	return out
}

// inputs are a FUNCTION's inputs in declaration order.
func (p *pou) inputs() []pin {
	var out []pin
	for _, vb := range p.vbs {
		if vb.Kind != "VAR_INPUT" || vb.Constant {
			continue
		}
		for _, v := range vb.Variables {
			out = append(out, pin{name: v.Name, kind: vb.Kind, typ: strings.ToUpper(strings.TrimSpace(v.Datatype)), decl: v})
		}
	}
	return out
}

// addPOUs registers the blocks one source declares.
func (lib *library) addPOUs(prog *st.Program, path string, lines []int, isLib, libLines bool) {
	for _, f := range prog.FBDecls {
		lib.addPOU(&pou{kind: kindFB, name: f.Name, vbs: f.VarBlocks, stmts: f.Statements, path: path, lines: lines, lib: isLib, libLines: libLines})
	}
	for _, f := range prog.FuncDecls {
		ret := ""
		if f.ReturnType != nil {
			ret = f.ReturnType.String()
		}
		lib.addPOU(&pou{kind: kindFC, name: f.Name, vbs: f.VarBlocks, stmts: f.Statements, ret: ret, path: path, lines: lines, lib: isLib, libLines: libLines})
	}
}

func (lib *library) addPOU(p *pou) {
	k := strings.ToLower(p.name)
	lib.pous[k] = append(lib.pous[k], p)
}

// userFB is the project's FUNCTION_BLOCK a declared type names, or nil.
func (w *writer) userFB(typ string) *pou {
	k := strings.ToLower(strings.TrimSpace(typ))
	if _, _, ok := ir.Lookup(ir.FBs, k); ok {
		// nautilus resolves its own blocks (TON, PID, …) before the
		// project's (lang/st resolveType), so a project block of such a
		// name is never the instance's type. PID is written as a block of
		// its own (pid.go), registered on its first use in place of any
		// project block of that name; the rest are TIA instructions.
		if k != strings.ToLower(pidName) {
			return nil
		}
		if c := w.lib.pous[k]; len(c) == 1 && isPID(c[0]) {
			return c[0]
		}
		p := pidPOU()
		w.lib.pous[k] = []*pou{p}
		return p
	}
	if c := w.lib.pous[k]; len(c) > 0 && c[0].kind == kindFB {
		return c[0]
	}
	return nil
}

// userFC is the project's FUNCTION a call names, or nil.
func (w *writer) userFC(name string) *pou {
	if c := w.lib.pous[strings.ToLower(strings.TrimSpace(name))]; len(c) > 0 && c[0].kind == kindFC {
		return c[0]
	}
	return nil
}

// site is where a block is first used: a nautilus file and line.
type site struct {
	path string
	line int
}

// at is the original line of line l of the source pw writes: a program's
// or a program file's own block's through its line map; a library
// block's uses are reported where the library block itself is.
func (pw *progWriter) at(l int) site {
	if pw.pou != nil && pw.pou.lib {
		return pw.site
	}
	return site{path: pw.path, line: mapLine(pw.srcLines, l)}
}

// need records that the block pw writes uses the project's block p.
func (pw *progWriter) need(p *pou, line int) {
	k := strings.ToLower(p.name)
	if pw.deps == nil {
		pw.deps = map[string]bool{}
	}
	pw.deps[k] = true
	w := pw.w
	if w.needed == nil {
		w.needed = map[string]site{}
	}
	if _, ok := w.needed[k]; !ok {
		w.needed[k] = pw.at(line)
		w.queue = append(w.queue, k)
	}
}

// pouFile is one written block.
type pouFile struct {
	file File
	src  Source
	deps map[string]bool
	// structTag is the first STRUCT manifest tag the block writes, "" for
	// none.
	structTag string
}

// writePOUs writes every block the programs reach, and what those reach,
// and adds them to out callees first (order). claim reserves a block name.
func (w *writer) writePOUs(out *Output, claim func(name, what string) bool) (order []string) {
	written := map[string]*pouFile{}
	for len(w.queue) > 0 {
		k := w.queue[0]
		w.queue = w.queue[1:]
		if pf := w.writePOU(k); pf != nil {
			written[k] = pf
		}
	}
	keys := make([]string, 0, len(written))
	for k := range written {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	state := map[string]int{} // 1 visiting, 2 done
	var visit func(k string, stack []string) bool
	visit = func(k string, stack []string) bool {
		switch state[k] {
		case 1:
			s := w.needed[k]
			w.diag("siemens/call", s.path, s.line, fmt.Sprintf("%s reaches itself (%s): TIA cannot hold a block inside itself", w.lib.pous[k][0].what(), strings.Join(append(stack, k), " -> ")))
			return false
		case 2:
			return true
		}
		state[k] = 1
		pf := written[k]
		deps := make([]string, 0, len(pf.deps))
		for d := range pf.deps {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		for _, d := range deps {
			if written[d] == nil {
				continue // refused: its diagnostics are reported
			}
			if !visit(d, append(stack, k)) {
				state[k] = 2 // reported once, at the block that reaches itself
				return false
			}
		}
		state[k] = 2
		order = append(order, k)
		return true
	}
	for _, k := range keys {
		if state[k] == 0 {
			visit(k, nil)
		}
	}
	w.structInputAliases(written, order)
	var paths []string
	for _, k := range order {
		pf := written[k]
		p := w.lib.pous[k][0]
		what := "the project's function block " + p.name
		switch {
		case p.builtin:
			what = "nautilus's " + p.name
		case p.kind == kindFC:
			what = "the project's function " + p.name
		}
		if !claim(strings.TrimSuffix(pf.file.Path, ".scl"), what) {
			continue
		}
		out.Files = append(out.Files, pf.file)
		out.Sources[pf.file.Path] = pf.src
		paths = append(paths, pf.file.Path)
	}
	return paths
}

// writePOU writes one block. Two declarations of one name (two program
// files, or one and the library) are one block only when they write the
// same SCL: the CPU holds one block of a name.
func (w *writer) writePOU(k string) *pouFile {
	cands := w.lib.pous[k]
	s := w.needed[k]
	var first *pouFile
	for i, p := range cands {
		if p.kind != cands[0].kind {
			w.diag("siemens/name", s.path, s.line, fmt.Sprintf("the project declares %s both as a FUNCTION_BLOCK and as a FUNCTION; the CPU holds one block of a name", p.name))
			return nil
		}
		pf := w.writeOne(p, s)
		if pf == nil {
			return nil
		}
		if i == 0 {
			first = pf
			continue
		}
		if string(pf.file.Data) != string(first.file.Data) {
			w.diag("siemens/name", s.path, s.line, fmt.Sprintf("the project declares %s twice, differently (%s and %s); the CPU holds one block of a name", p.what(), where(cands[0]), where(p)))
			return nil
		}
	}
	return first
}

// structInputAliases refuses a FUNCTION with a STRUCT input that writes a
// STRUCT manifest tag, itself or through a function it calls: TIA hands
// the FC its STRUCT input by reference, so a caller binding that tag (or
// a member of it) to the input would have the FC read its own write,
// where nautilus reads the copy it was handed. (An FC holds no instance,
// so what it calls is FCs only.)
func (w *writer) structInputAliases(written map[string]*pouFile, order []string) {
	for _, k := range order {
		p := w.lib.pous[k][0]
		if p.kind != kindFC {
			continue
		}
		input := ""
		for _, in := range p.inputs() {
			if w.structOf(in.typ) != nil {
				input = in.name
				break
			}
		}
		if input == "" {
			continue
		}
		seen := map[string]bool{}
		var find func(k string) (tag, via string)
		find = func(k string) (string, string) {
			if seen[k] || written[k] == nil {
				return "", ""
			}
			seen[k] = true
			if t := written[k].structTag; t != "" {
				return t, w.lib.pous[k][0].name
			}
			deps := make([]string, 0, len(written[k].deps))
			for d := range written[k].deps {
				deps = append(deps, d)
			}
			sort.Strings(deps)
			for _, d := range deps {
				if t, via := find(d); t != "" {
					return t, via
				}
			}
			return "", ""
		}
		tag, via := find(k)
		if tag == "" {
			continue
		}
		what := "writes the STRUCT tag " + tag
		if !strings.EqualFold(via, p.name) {
			what += " (through FUNCTION " + via + ")"
		}
		s := w.needed[k]
		w.diag("siemens/var", s.path, s.line, gapMessage(gapStructured, fmt.Sprintf("%s, which takes the STRUCT input %s and %s", p.what(), input, what),
			"TIA hands a function its STRUCT input by reference, so a caller passing that tag would have the function read its own write; nautilus reads the copy it was handed"))
	}
}

func where(p *pou) string {
	if p.lib {
		return "the library"
	}
	return p.path
}

// writeOne writes block p, first used at s. Its diagnostics point at its
// own line in a program file; a library block's at the use, naming the
// block and its library line.
func (w *writer) writeOne(p *pou, s site) *pouFile {
	if p.builtin {
		return w.pidFile(p, s)
	}
	path := p.path
	if p.lib {
		path = s.path
	}
	pw := &progWriter{w: w, path: path, srcLines: p.lines, locals: map[string]local{}, statics: map[string]*shape{}, uses: map[string]*tagUse{},
		kind: p.kind, name: p.name, pou: p, site: s}
	mark, wmark := len(w.diags), len(w.warnings)
	name := quote(w.prefix + p.name)
	var header, footer string
	if p.kind == kindFC {
		header, footer = "", "END_FUNCTION"
		if why := w.typeRefusal(p.ret, map[string]bool{}); why != "" {
			pw.diag("siemens/type", p.lineOf(), p.name+": "+why)
		} else if t, why := w.declType(p.ret, path); why != "" {
			pw.diag("siemens/type", p.lineOf(), p.name+": "+why)
		} else {
			header = "FUNCTION " + name + " : " + t + "\n"
		}
	} else {
		header, footer = "FUNCTION_BLOCK "+name+"\n", "END_FUNCTION_BLOCK"
	}
	ok := len(w.diags) == mark && pw.block(header, footer, p.vbs, p.stmts)
	for i := mark; i < len(w.diags); i++ {
		w.diags[i] = pw.relocate(w.diags[i])
	}
	for i := wmark; i < len(w.warnings); i++ {
		w.warnings[i] = pw.relocate(w.warnings[i])
	}
	if !ok {
		return nil
	}
	body := make([]int, len(pw.lines))
	if !p.lib {
		for i, l := range pw.lines {
			body[i] = mapLine(p.lines, l)
		}
	}
	file := w.prefix + p.name + ".scl"
	return &pouFile{file: File{Path: file, Data: pw.fb}, src: Source{Path: path, Body: body}, deps: pw.deps, structTag: pw.structTag}
}

// lineOf is the line a block's declaration starts on (its first
// variable's), for a message about the block as a whole.
func (p *pou) lineOf() int {
	for _, vb := range p.vbs {
		if l := lineOf(vb); l > 0 {
			return l
		}
	}
	return 0
}

// relocate points a diagnostic from a block's body at the nautilus file:
// a program file's own block through the file's line map, a library
// block at its first use, with the block and its library line named.
func (pw *progWriter) relocate(d Diag) Diag {
	p := pw.pou
	if !p.lib {
		d.Line = mapLine(p.lines, d.Line)
		return d
	}
	where := "the project's library"
	if p.libLines && d.Line > 0 {
		where += fmt.Sprintf(", line %d", d.Line)
	}
	d.Line = pw.site.line // (pw.path is the site's already)
	d.Message = fmt.Sprintf("in %s (%s): %s", p.what(), where, d.Message)
	return d
}

// what names the block pw writes, for a message.
func (pw *progWriter) what() string {
	if pw.pou != nil {
		return pw.pou.what()
	}
	return "PROGRAM " + pw.name
}

// fbShape is what an instance of the project's block p holds, for
// Output.Symbol: its inputs, outputs and statics, each with what it
// holds below it (an in-out is the caller's variable, a temp no one's).
func (w *writer) fbShape(name string, p *pou, seen map[string]bool) *shape {
	s := &shape{name: name, members: map[string]*shape{}}
	k := strings.ToLower(p.name)
	if seen[k] {
		return s
	}
	seen[k] = true
	defer delete(seen, k)
	for _, vb := range p.vbs {
		switch {
		case vb.Constant, vb.Kind == "VAR_IN_OUT", vb.Kind == "VAR_TEMP", vb.Kind == "VAR_EXTERNAL", vb.Kind == "VAR_GLOBAL":
			continue
		}
		for _, v := range vb.Variables {
			u := strings.ToUpper(strings.TrimSpace(v.Datatype))
			var m *shape
			switch _, latch := latches[u]; {
			case w.userFB(u) != nil:
				m = w.fbShape(v.Name, w.userFB(u), seen)
			case latch:
				m = &shape{name: v.Name, instance: true}
			default:
				if _, inst := instances[u]; inst {
					m = &shape{name: v.Name, instance: true}
				} else {
					m = w.shapeOf(v.Name, v.Datatype)
				}
			}
			s.members[strings.ToLower(v.Name)] = m
		}
	}
	return s
}

// fbCall writes a call of an instance of the project's block p: the
// inputs through store(), the in-outs by reference, then each => output
// as nautilus stores it, an assignment after the call.
func (pw *progWriter) fbCall(b *strings.Builder, c *st.CallExpr, l local, p *pou, d, at int) {
	in := indent(d)
	line := c.Pos.Line
	if len(c.Args) > 0 {
		pw.diag("siemens/call", line, l.name+": "+gapMessage(gapOther, "positional instance arguments", "name the inputs"))
		return
	}
	pins := p.pins()
	var parts []string
	var inouts []st.Expression
	for _, a := range c.NamedArgs {
		pn, ok := pins[strings.ToUpper(a.Name)]
		switch {
		case !ok && (strings.EqualFold(a.Name, "EN") || strings.EqualFold(a.Name, "ENO")):
			pw.gap("siemens/call", line, gapOther, "EN/ENO on a call of "+p.what(), "")
			return
		case !ok:
			pw.diag("siemens/call", line, fmt.Sprintf("%s has no input %s", p.what(), a.Name))
			return
		case pn.kind == "VAR_INPUT":
			v := pw.store(a.Value, pn.typ, line)
			if v == "" {
				return
			}
			parts = append(parts, sclName(pn.name)+" := "+v)
		case pn.kind == "VAR_IN_OUT":
			if pw.refusedTimerWrite(a.Value, line) {
				return
			}
			parts = append(parts, sclName(pn.name)+" := "+pw.expr(a.Value, 0))
			inouts = append(inouts, a.Value)
		default:
			pw.diag("siemens/call", line, fmt.Sprintf("%s's %s is not an input", p.what(), pn.name))
			return
		}
	}
	pw.emit(b, at, "%s#%s(%s);\n", in, sclName(l.name), strings.Join(parts, ", "))
	for _, t := range inouts {
		pw.assigned(b, in, at, t)
	}
	for _, o := range c.OutputBindings {
		pn, ok := pins[strings.ToUpper(o.Name)]
		if !ok || pn.kind != "VAR_OUTPUT" {
			pw.diag("siemens/call", line, fmt.Sprintf("%s has no output %s", p.what(), o.Name))
			return
		}
		pos := o.Pos
		if pos.Line == 0 {
			pos = c.Pos
		}
		pw.stmt(b, &st.AssignStmt{TargetExpr: o.Target, Value: &st.MemberExpr{Object: &st.IdentExpr{Name: l.name, Pos: pos}, Member: pn.name, Pos: pos}, Pos: pos}, d)
	}
}

// fcCall writes a call of the project's FUNCTION p: every input named,
// in declaration order (nautilus binds them all), each stored as nautilus
// stores an argument.
func (pw *progWriter) fcCall(c *st.CallExpr, p *pou, stmt bool) string {
	line := c.Pos.Line
	if stmt {
		pw.gap("siemens/call", line, gapOther, c.Name+"(…) as a statement", "only function block instances are called that way so far")
		return ""
	}
	if len(c.OutputBindings) > 0 {
		pw.diag("siemens/call", line, p.what()+" has no outputs to bind with =>")
		return ""
	}
	ins := p.inputs()
	args := make([]st.Expression, len(ins))
	switch {
	case len(c.NamedArgs) > 0:
		for _, a := range c.NamedArgs {
			i := -1
			for j, pn := range ins {
				if strings.EqualFold(pn.name, a.Name) {
					i = j
				}
			}
			if i < 0 {
				pw.diag("siemens/call", line, fmt.Sprintf("%s has no input %s", p.what(), a.Name))
				return ""
			}
			args[i] = a.Value
		}
	default:
		copy(args, c.Args)
	}
	parts := make([]string, len(ins))
	for i, pn := range ins {
		if args[i] == nil || len(c.Args) > len(ins) {
			pw.diag("siemens/call", line, fmt.Sprintf("%s takes %d inputs", p.what(), len(ins)))
			return ""
		}
		v := pw.store(args[i], pn.typ, line)
		if v == "" {
			return ""
		}
		parts[i] = sclName(pn.name) + " := " + v
	}
	pw.need(p, line)
	return quote(pw.w.prefix+p.name) + "(" + strings.Join(parts, ", ") + ")"
}

// constants declares a VAR CONSTANT block: SCL's VAR CONSTANT, each with
// its value as one literal.
func (pw *progWriter) constants(vb st.VarBlock) {
	for _, v := range vb.Variables {
		u := strings.ToUpper(strings.TrimSpace(v.Datatype))
		if pw.w.structOf(v.Datatype) != nil {
			pw.diag("siemens/var", v.Pos.Line, v.Name+": "+gapMessage(gapStructured, "a STRUCT constant", ""))
			continue
		}
		t, why := pw.w.sclType(v.Datatype)
		if why != "" {
			pw.diag("siemens/type", v.Pos.Line, v.Name+": "+why)
			continue
		}
		val, why := pw.constInit(v.Initial, u, map[string]bool{})
		if why != "" {
			pw.diag("siemens/var", v.Pos.Line, v.Name+": "+why)
			continue
		}
		pw.locals[strings.ToLower(v.Name)] = local{name: v.Name, typ: u}
		if pw.constVals == nil {
			pw.constVals = map[string]string{}
		}
		pw.constVals[strings.ToLower(v.Name)] = val
		pw.consts = append(pw.consts, fmt.Sprintf("      %s : %s := %s;", sclName(v.Name), t, val))
	}
}

// constInit is a constant's value as one SCL literal: its type's zero
// when it has none, the value of another constant it names, or the
// literal it is.
func (pw *progWriter) constInit(e st.Expression, typ string, seen map[string]bool) (string, string) {
	if e == nil {
		z, ok := zero(typ)
		if !ok {
			return "", gapMessage(gapOther, "a "+typ+" constant", "")
		}
		return z, ""
	}
	if id, ok := e.(*st.IdentExpr); ok {
		k := strings.ToLower(id.Name)
		if v, ok := pw.constVals[k]; ok {
			return v, ""
		}
		if d, ok := pw.w.lib.constDecls[k]; ok && !seen[k] {
			seen[k] = true
			return pw.constInit(d.Initial, strings.ToUpper(strings.TrimSpace(d.Datatype)), seen)
		}
	}
	return constValue(pw.w.lib, e)
}

// useConst records the project's global constant k (a library's
// VAR_GLOBAL CONSTANT) as used: each block that names one declares its
// own VAR CONSTANT copy of it, with the same name.
func (pw *progWriter) useConst(k string, line int) {
	if pw.gconsts == nil {
		pw.gconsts = map[string]int{}
	}
	if _, ok := pw.gconsts[k]; !ok {
		pw.gconsts[k] = line
	}
}

// globalConsts declares the global constants the block named, in name
// order.
func (pw *progWriter) globalConsts() []string {
	keys := make([]string, 0, len(pw.gconsts))
	for k := range pw.gconsts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		d := pw.w.lib.constDecls[k]
		u := strings.ToUpper(strings.TrimSpace(d.Datatype))
		if pw.w.structOf(d.Datatype) != nil {
			pw.gap("siemens/library", pw.gconsts[k], gapStructured, "the project's STRUCT constant "+d.Name, "")
			continue
		}
		t, why := pw.w.sclType(d.Datatype)
		if why != "" {
			pw.diag("siemens/library", pw.gconsts[k], fmt.Sprintf("the project's global constant %s: %s", d.Name, why))
			continue
		}
		val, why := pw.constInit(d.Initial, u, map[string]bool{k: true})
		if why != "" {
			pw.diag("siemens/library", pw.gconsts[k], fmt.Sprintf("the project's global constant %s: %s", d.Name, why))
			continue
		}
		out = append(out, fmt.Sprintf("      %s : %s := %s;", sclName(d.Name), t, val))
	}
	return out
}
