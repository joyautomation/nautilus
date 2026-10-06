package writer

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbcatalog"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// User function blocks as Add-On Instructions (logix-authoring.md Phase
// D, step 3). A nautilus FUNCTION_BLOCK — ladder or ST, in a library or
// the program's own file — becomes an AOI definition: VAR_INPUT are
// Required input parameters, VAR_OUTPUT are output parameters read as
// instance.Name, VAR_IN_OUT are InOut parameters (a structure or an
// array, which is all Logix passes by reference), VAR are local tags, and
// the body is the Logic routine in its own language. An instance is a
// tag of the AOI's type.
//
// A ladder call `inst:Block(Pin := tag, ...)` becomes the instruction
// `Block(inst,in1,in2,...)` with the inputs as operands in declaration
// order. The semantics that need a rule:
//
//   - Logix skips an AOI whose rung-in is false; nautilus runs the block
//     every scan with its power pin false. So the call always sits at the
//     head of its rung and the rung's condition is written to a generated
//     tag on a helper rung, which is the power pin's operand.
//   - Power continues from the block's BOOL output (ENO or the first
//     BOOL VAR_OUTPUT) as XIC(inst.Out), or from the condition tag when
//     the block has no BOOL output.
//   - `Out => tag` is a copy rung after the call (OTE for a BOOL, MOVE
//     otherwise).
//   - VAR_EXTERNAL inside a block has no Logix equivalent (an AOI cannot
//     reach a controller tag) and is refused; promote it to a parameter.
//
// An ST call `inst(Pin := x, ...)` becomes `Block(inst, x, ...);` with the
// same operand order, outputs read as `inst.Out` afterwards.

type aoiDef struct {
	Name   string
	Params []aoiParam
	Locals []tagDef
	Rungs  []rungOut // ladder body
	Lines  []string  // ST body
	ST     bool
	// powerOut is the pin a ladder rung's power leaves on (ENO, else the
	// first BOOL output); the power-in pin is chosen per call.
	powerOut string
}

type aoiParam struct {
	Name     string
	DataType string
	Usage    string // Input | Output | InOut
	Dim      int
	Struct   *udt
}

func (a *aoiDef) param(name string) *aoiParam {
	for i := range a.Params {
		if strings.EqualFold(a.Params[i].Name, name) {
			return &a.Params[i]
		}
	}
	return nil
}

// resolveAOI builds (once) the AOI for a user block name, from the
// libraries or the program's own file. ok is false with diagnostics
// raised when the block cannot be an AOI.
func (lw *lowered) resolveAOI(name string, line int) (*aoiDef, bool) {
	key := strings.ToLower(name)
	if a, seen := lw.aois[key]; seen {
		return a, a != nil
	}
	lw.aois[key] = nil
	src, lang := lw.blockSource(name)
	if src == "" {
		return nil, false
	}
	var a *aoiDef
	var ok bool
	switch lang {
	case "ld":
		a, ok = lw.aoiFromLadder(name, src, line)
	case "st":
		a, ok = lw.aoiFromST(name, src, line)
	}
	if !ok {
		return nil, false
	}
	lw.aois[key] = a
	lw.aoiOrder = append(lw.aoiOrder, a)
	return a, true
}

// blockSource finds the source that declares a FUNCTION_BLOCK: the
// program's own ladder file first, then each library.
func (lw *lowered) blockSource(name string) (string, string) {
	if lw.model != nil && !lw.st {
		for _, b := range lw.model.Blocks {
			if strings.EqualFold(b.Name, name) {
				return lw.src, "ld"
			}
		}
	}
	if b, ok := lw.libs().blocks[strings.ToLower(name)]; ok {
		return b.src, b.lang
	}
	return "", ""
}

// libIndex is what the libraries declare, read once per lowering. The
// rules ask per operand, and the editor runs them on every keystroke:
// rescanning every library per question cost 100 ms on a 45-line routine
// with a large project library (corpus, 2026-10-05).
type libIndex struct {
	blocks   map[string]libBlock // by lower-cased name; the first library wins
	fbs, fns map[string]bool     // FUNCTION_BLOCKs and FUNCTIONs an ST library declares
}

type libBlock struct{ src, lang string }

// libs builds the index on first use, after the lowering has settled its
// library list (an ST program's own blocks join it), and shares it with
// the block bodies lowered beneath.
func (lw *lowered) libs() *libIndex {
	if lw.libIx != nil {
		return lw.libIx
	}
	ix := &libIndex{blocks: map[string]libBlock{}, fbs: map[string]bool{}, fns: map[string]bool{}}
	for _, lib := range lw.opts.Libs {
		lang := "st"
		if ld.HasBlock(lib) {
			lang = "ld"
		}
		for _, sig := range fbcatalog.ScanSigs(lib) {
			if k := strings.ToLower(sig.Name); ix.blocks[k] == (libBlock{}) {
				ix.blocks[k] = libBlock{lib, lang}
			}
		}
		if prog, err := st.Parse(lib); err == nil {
			for _, fb := range prog.FBDecls {
				ix.fbs[strings.ToLower(fb.Name)] = true
			}
			for _, fn := range prog.FuncDecls {
				ix.fns[strings.ToLower(fn.Name)] = true
			}
		}
	}
	lw.libIx = ix
	return ix
}

// child is a lowering scope for one block body: its own variables, the
// parent's types and options.
func (lw *lowered) child(name string, vars []ld.VarDecl) *lowered {
	c := &lowered{model: &ld.Model{Name: name}, opts: lw.opts, vars: map[string]ld.VarDecl{},
		presetVars: map[string]bool{}, genNames: map[string]bool{}, types: lw.types, rawTypes: lw.rawTypes,
		aois: lw.aois, inAOI: true, st: lw.st, libIx: lw.libs()}
	c.opts.Program = name
	for _, v := range vars {
		c.vars[strings.ToLower(v.Name)] = v
	}
	return c
}

// aoiParams declares a block's interface from its VarDecls, returning the
// parameters and the locals; everything else is a diagnostic.
func (c *lowered) aoiParams(block string, vars []ld.VarDecl, line int) ([]aoiParam, bool) {
	var params []aoiParam
	good := true
	for _, v := range vars {
		sec := strings.ToUpper(v.Section)
		switch sec {
		case "VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT":
		case "VAR", "VAR_TEMP":
			c.declare(v) // a local tag
			continue
		case "VAR_EXTERNAL":
			c.diag(ruleFunctionBlock, v.Line, "", "block %s: VAR_EXTERNAL %s has no Logix equivalent (an Add-On Instruction cannot reach a controller tag); promote it to a VAR_INPUT or VAR_IN_OUT", block, v.Name)
			good = false
			continue
		default:
			c.diag(ruleVarSection, v.Line, "", "block %s: %s %s has no place in an Add-On Instruction", block, v.Section, v.Name)
			good = false
			continue
		}
		if !c.checkName(v.Name, v.Line, "") {
			good = false
			continue
		}
		p := aoiParam{Name: v.Name}
		typ := strings.TrimSpace(v.Type)
		if m := arrayRe.FindStringSubmatch(typ); m != nil {
			lo, hi := atoiSafe(m[1]), atoiSafe(m[2])
			if m[3] != "" || lo != 0 {
				c.diag(ruleArrayShape, v.Line, "", "block %s: parameter %s: a Logix array is one-dimensional from 0", block, v.Name)
				good = false
				continue
			}
			p.Dim = hi + 1
			typ = m[4]
		}
		u := strings.ToUpper(typ)
		switch {
		case scalarTypes[u] != "":
			p.DataType = scalarTypes[u]
		case u == "TIME":
			p.DataType = "DINT"
		case c.rawTypes[strings.ToLower(u)] != nil:
			udt, ok := c.resolveType(typ, v.Line, "block "+block+" parameter "+v.Name)
			if !ok {
				good = false
				continue
			}
			p.DataType, p.Struct = udt.Name, udt
		default:
			c.diag(ruleType, v.Line, "", "block %s: parameter %s: type %s cannot be an Add-On Instruction parameter", block, v.Name, v.Type)
			good = false
			continue
		}
		switch sec {
		case "VAR_INPUT":
			p.Usage = "Input"
			if p.Struct != nil || p.Dim > 0 {
				c.diag(ruleType, v.Line, "", "block %s: VAR_INPUT %s: a structure or array parameter must be VAR_IN_OUT (Logix passes those by reference)", block, v.Name)
				good = false
				continue
			}
		case "VAR_OUTPUT":
			p.Usage = "Output"
			if p.Struct != nil || p.Dim > 0 {
				c.diag(ruleType, v.Line, "", "block %s: VAR_OUTPUT %s: a structure or array parameter must be VAR_IN_OUT", block, v.Name)
				good = false
				continue
			}
		case "VAR_IN_OUT":
			p.Usage = "InOut"
			if p.Struct == nil && p.Dim == 0 {
				c.diag(ruleType, v.Line, "", "block %s: VAR_IN_OUT %s: Logix passes only structures and arrays by reference; make a scalar a VAR_INPUT or VAR_OUTPUT", block, v.Name)
				good = false
				continue
			}
		}
		params = append(params, p)
	}
	return params, good
}

func atoiSafe(s string) int {
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

// aoiFromLadder lowers a ladder FUNCTION_BLOCK's rungs with its own
// variables in scope.
func (lw *lowered) aoiFromLadder(name, src string, line int) (*aoiDef, bool) {
	m, err := ld.Graph(src, lw.opts.Libs...)
	if err != nil {
		lw.diag(ruleFunctionBlock, line, "", "block %s: %v", name, err)
		return nil, false
	}
	var vars []ld.VarDecl
	for _, v := range m.Vars {
		if strings.EqualFold(v.POU, name) {
			vars = append(vars, v)
		}
	}
	c := lw.child(name, vars)
	c.model = m
	c.src = src
	params, ok := c.aoiParams(name, vars, line)
	for _, r := range m.Rungs {
		if strings.EqualFold(r.POU, name) {
			c.scanPresets(r.Elements)
		}
	}
	notes := c.notesByLine()
	for _, r := range m.Rungs {
		if strings.EqualFold(r.POU, name) {
			c.rung(r, notes[r.Line])
		}
	}
	lw.diags = append(lw.diags, c.diags...)
	if !ok || len(c.diags) > 0 {
		return nil, false
	}
	a := &aoiDef{Name: name, Params: params, Locals: append(c.ctrlTags, c.progTags...), Rungs: c.rungs}
	_, a.powerOut = powerPins(params)
	return a, true
}

// aoiFromST lowers an ST FUNCTION_BLOCK's statements.
func (lw *lowered) aoiFromST(name, src string, line int) (*aoiDef, bool) {
	prog, err := st.Parse(src)
	if err != nil {
		lw.diag(ruleFunctionBlock, line, "", "block %s: %v", name, err)
		return nil, false
	}
	var decl *st.FunctionBlockDecl
	for _, fb := range prog.FBDecls {
		if strings.EqualFold(fb.Name, name) {
			decl = fb
		}
	}
	if decl == nil {
		lw.diag(ruleFunctionBlock, line, "", "block %s: not found in its library", name)
		return nil, false
	}
	var vars []ld.VarDecl
	for _, vb := range decl.VarBlocks {
		for _, v := range vb.Variables {
			init := ""
			if v.Initial != nil {
				init = exprText(v.Initial)
			}
			vars = append(vars, ld.VarDecl{Name: v.Name, Type: v.Datatype, Init: init, Section: vb.Kind, Line: v.Pos.Line, POU: name})
		}
	}
	c := lw.child(name, vars)
	c.st = true
	params, ok := c.aoiParams(name, vars, line)
	w := &stWriter{lw: c}
	w.block(decl.Statements, 0)
	lw.diags = append(lw.diags, c.diags...)
	if !ok || len(c.diags) > 0 {
		return nil, false
	}
	a := &aoiDef{Name: name, Params: params, Locals: append(c.ctrlTags, c.progTags...), Lines: w.lines, ST: true}
	_, a.powerOut = powerPins(params)
	return a, true
}

// powerPins picks the pins a ladder rung's power enters and leaves on,
// the way lang/ld does: EN / ENO when declared, else the first BOOL
// input and the first BOOL output.
func powerPins(params []aoiParam) (in, out string) {
	for _, p := range params {
		if p.Usage == "Input" && p.DataType == "BOOL" && strings.EqualFold(p.Name, "EN") {
			in = p.Name
		}
		if p.Usage == "Output" && p.DataType == "BOOL" && strings.EqualFold(p.Name, "ENO") {
			out = p.Name
		}
	}
	for _, p := range params {
		if in == "" && p.Usage == "Input" && p.DataType == "BOOL" {
			in = p.Name
		}
		if out == "" && p.Usage == "Output" && p.DataType == "BOOL" {
			out = p.Name
		}
	}
	return in, out
}

var pinBindRe = regexp.MustCompile(`(?i)(^|[,(\s])([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=>)`)

// aoiCall lowers a ladder call of a user block: a helper rung for the
// condition when the block takes power, the instruction with its
// operands, copy rungs for output bindings. It returns the instruction
// text and the continuation contact.
func (c *rungCtx) aoiCall(e ld.Element, partsBefore []string, top bool) (text, cont string, ok bool) {
	a, ok := c.lw.resolveAOI(e.Type, c.r.Line)
	if !ok {
		if c.lw.aois[strings.ToLower(e.Type)] == nil && c.lw.blockSourceMissing(e.Type) {
			c.lw.diag(ruleFB, c.r.Line, c.r.Name, "%s:%s: not in the Logix v1 subset; the v1 blocks are TON, TOF and CTU, and user blocks declared in this file or a library", e.Inst, e.Type)
		}
		return "", "", false
	}
	if v, declared := c.lw.vars[strings.ToLower(e.Inst)]; !declared || !strings.EqualFold(strings.TrimSpace(v.Type), e.Type) {
		return "", "", false // the compiler's diagnostic
	}
	bound := map[string]string{}
	var captures []rungOut
	for _, arg := range splitArgs(e.Args) {
		pin, val, isOut, ok := splitBinding(arg)
		if !ok {
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: cannot read the binding %q", e.Inst, e.Type, arg)
			return "", "", false
		}
		p := a.param(pin)
		if p == nil {
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: no parameter %s", e.Inst, e.Type, pin)
			return "", "", false
		}
		if isOut {
			if p.Usage != "Output" {
				c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: %s is not an output", e.Inst, e.Type, pin)
				return "", "", false
			}
			target, ok := c.ref(val)
			if !ok {
				return "", "", false
			}
			if p.DataType == "BOOL" {
				captures = append(captures, rungOut{Text: "XIC(" + e.Inst + "." + p.Name + ")OTE(" + target + ")", Source: c.r.Name, Line: c.r.Line})
			} else {
				captures = append(captures, rungOut{Text: "MOVE(" + e.Inst + "." + p.Name + "," + target + ")", Source: c.r.Name, Line: c.r.Line})
			}
			continue
		}
		if p.Usage == "Output" {
			c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: %s is an output; capture it with =>", e.Inst, e.Type, pin)
			return "", "", false
		}
		operand, ok := c.operand(val, ld.Element{Fn: e.Type, Args: e.Args})
		if !ok {
			return "", "", false
		}
		bound[strings.ToLower(p.Name)] = operand
	}
	// The rung's power lands on EN, else the first BOOL input the call
	// leaves unbound (the ladder compiler's rule). Every input bound: the
	// block takes no power and must sit alone on the rail.
	powerIn := ""
	for _, p := range a.Params {
		if p.Usage == "Input" && p.DataType == "BOOL" && strings.EqualFold(p.Name, "EN") {
			powerIn = p.Name
		}
	}
	if powerIn == "" {
		for _, p := range a.Params {
			if p.Usage == "Input" && p.DataType == "BOOL" {
				if _, isBound := bound[strings.ToLower(p.Name)]; !isBound {
					powerIn = p.Name
					break
				}
			}
		}
	}
	enable := ""
	if powerIn != "" {
		if len(partsBefore) == 0 && top {
			enable = "1"
		} else {
			en := "en_" + sanitizeIdent(c.r.Name) + "_" + sanitizeIdent(e.Inst)
			c.lw.genTag(en, c.r.Line, c.r.Name)
			c.pre = append(c.pre, rungOut{Text: join(partsBefore, top) + "OTE(" + en + ")", Source: c.r.Name, Line: c.r.Line})
			enable = en
		}
		bound[strings.ToLower(powerIn)] = enable
	} else if len(partsBefore) > 0 {
		c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s has no free BOOL input for the rung's power; leave its power pin unbound, or give the block an EN input", e.Inst, e.Type)
		return "", "", false
	}
	operands := []string{e.Inst}
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
				c.lw.diag(ruleFBPin, c.r.Line, c.r.Name, "%s:%s: VAR_IN_OUT %s must be bound", e.Inst, e.Type, p.Name)
				return "", "", false
			}
			operands = append(operands, v)
		}
	}
	c.post = append(c.post, captures...)
	text = e.Type + "(" + strings.Join(operands, ",") + ")"
	switch {
	case a.powerOut != "":
		cont = "XIC(" + e.Inst + "." + a.powerOut + ")"
	case enable != "" && enable != "1":
		cont = "XIC(" + enable + ")"
	}
	return text, cont, true
}

func (lw *lowered) blockSourceMissing(name string) bool {
	src, _ := lw.blockSource(name)
	return src == ""
}
