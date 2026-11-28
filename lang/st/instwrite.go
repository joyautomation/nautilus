package st

import (
	"strconv"
	"strings"

	"github.com/joyautomation/nautilus/lang/ir"
)

// The call-site write check of struct wiring (#1458, one-struct-model §2.4
// and §15): a program must not assign a member its wiring reads from a
// device (from:) or computes (expr:), whether it assigns it itself or
// passes the instance to a block that does.
//
// The runtime already strips such a write when the scan commits (it never
// reaches a driver), so this layer exists to say so before the program
// runs: a write that is silently dropped looks like a bug in the logic.
// It is a check-time pass over the parsed program, not part of lowering,
// so Load, hot install and the scan are untouched.
//
// The walk is MAY-write: an assignment on any branch counts, because the
// runtime's exact copy-back (slice 3) only helps a conditional assignment
// that did not run; a program that can assign a read-only member is wrong
// whichever way the branch goes.

// InstanceKinds is what the check needs of a project's wired instances:
// each instance tag's name, then the kind of each member path its wiring
// names ("from", "to", "both", "memory" or "expr"; a member it does not
// name is memory). runtime.WiredKinds builds it from the tag definitions.
type InstanceKinds map[string]map[string]string

// InstanceWrite is one refused write.
type InstanceWrite struct {
	// Pos and End span the offending assignment target, or the instance
	// argument at a call.
	Pos, End Pos
	// Instance is the wired instance as written, Member the read-only
	// member path (relative to it, as the wiring names it) the write would
	// reach, and Kind that member's kind ("from" or "expr").
	Instance string
	Member   string
	Kind     string
	// Text is the target or argument as the program wrote it ("P101.Cmd").
	Text string
	// Whole is set when the write covers more than that member (the whole
	// instance, or a sub-struct holding it): `P101 := Spare`.
	Whole bool
	// Block is the function block that assigns, when the write is through
	// a call, with BlockPos the assignment inside it and BlockWrites the
	// text of that assignment's target ("P.Running"). Call is the block
	// called at Pos (the same as Block unless the assignment is in a block
	// that block calls); Builtin is set for a standard block that writes
	// its VAR_IN_OUT pin itself.
	Block, Call, BlockWrites string
	BlockPos                 Pos
	Builtin                  bool
}

// Message is the sentence `naut check` and the editor show. blockAt says
// where BlockPos is ("line 12", or "lib/pump.st:12" for a library block);
// the caller knows the file layout, the pass does not.
func (w InstanceWrite) Message(blockAt string) string {
	who := w.Instance
	if w.Member != "" {
		who += "." + w.Member
	}
	how, fix := kindWords(w.Kind)
	switch {
	case w.Block == "" && !w.Builtin:
		if w.Whole {
			return "assigning " + w.written() + " would overwrite " + who + ", which is " + how +
				"; assign the writable members one at a time (for example " + w.Instance + ".Cmd.Start := ...)"
		}
		return who + " is " + how + ", so the program cannot assign it; " + fix
	case w.Builtin:
		return w.Call + " writes its VAR_IN_OUT pin, and " + w.written() + " is passed to it, but " +
			who + " is " + how + "; pass a local variable of that type instead"
	default:
		at := ""
		if blockAt != "" {
			at = " (" + blockAt + ")"
		}
		return "calling " + w.Call + " with " + w.written() + " would assign " + who + ", which is " + how +
			": " + w.Block + " assigns " + w.BlockWrites + at + ". " + capFirst(fix) +
			", or pass a local variable of that type, which a block may assign freely"
	}
}

// written is the instance expression as the program wrote it.
func (w InstanceWrite) written() string {
	if w.Text != "" {
		return w.Text
	}
	return w.Instance
}

func kindWords(kind string) (how, fix string) {
	if kind == "expr" {
		return "computed by its expr: each scan",
			"assign the members the expression reads, or wire the member as memory: if the program is meant to set it"
	}
	return "read from the device (from:)",
		"remove the assignment, or wire the member as memory: (or to: for a command) if the program is meant to set it"
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// InstanceWrites finds every write to a read-only member of a wired
// instance in prog (the library prelude and the program parsed together,
// so a block declared in a library is followed). Positions are in prog's
// coordinates; a caller drops the ones inside the prelude. An instance the
// program shadows with a local of the same name is not an instance there.
func InstanceWrites(prog *Program, kinds InstanceKinds) []InstanceWrite {
	if prog == nil || len(kinds) == 0 {
		return nil
	}
	a := &iwAnalysis{
		kinds: map[string]*iwInstance{},
		fbs:   map[string]*FunctionBlockDecl{},
		pous:  map[*FunctionBlockDecl]*iwPOU{},
		memo:  map[iwPinKey][]iwPinWrite{},
	}
	for name, members := range kinds {
		in := &iwInstance{name: name}
		for p, k := range members {
			in.entries = append(in.entries, iwEntry{path: iwParseKey(p), text: p, kind: k})
		}
		// A stable walk, so the first refused member named is the same each run.
		sortEntries(in.entries)
		a.kinds[ir.NameKey(name)] = in
	}
	for _, fb := range prog.FBDecls {
		a.fbs[ir.NameKey(fb.Name)] = fb
	}
	for _, fb := range prog.FBDecls {
		a.pous[fb] = a.newPOU(fb.Name, fb.VarBlocks, fb.Statements, true)
	}
	var out []InstanceWrite
	seen := map[string]bool{}
	add := func(w InstanceWrite, key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, w)
	}
	report := func(p *iwPOU) {
		for _, d := range p.direct {
			if d.root.pin || d.root.local {
				continue
			}
			in := a.kinds[ir.NameKey(d.root.name)]
			if in == nil {
				continue
			}
			if e, whole, bad := in.refuses(d.path); bad {
				w := InstanceWrite{Pos: d.pos, End: d.end, Instance: d.root.name, Text: d.text, Member: e.text, Kind: e.kind, Whole: whole,
					Builtin: d.builtin != "", Call: d.builtin}
				add(w, posKey(d.pos, e.text, ""))
			}
		}
		for _, c := range p.calls {
			for _, b := range c.binds {
				if b.root.pin || b.root.local {
					continue
				}
				in := a.kinds[ir.NameKey(b.root.name)]
				if in == nil {
					continue
				}
				for _, pw := range a.pinWrites(c.callee, b.pin, map[*FunctionBlockDecl]bool{}) {
					full := append(append([]iwSeg(nil), b.path...), pw.path...)
					if e, whole, bad := in.refuses(full); bad {
						w := InstanceWrite{Pos: b.pos, End: b.end, Instance: b.root.name, Text: b.text, Member: e.text, Kind: e.kind, Whole: whole,
							Block: pw.block, Call: c.callee.Name, BlockWrites: pw.text, BlockPos: pw.pos}
						add(w, posKey(b.pos, e.text, pw.block))
					}
				}
			}
		}
	}
	for _, fb := range prog.FBDecls {
		report(a.pous[fb])
	}
	for _, fd := range prog.FuncDecls {
		report(a.newPOU(fd.Name, fd.VarBlocks, fd.Statements, false))
	}
	report(a.newPOU(prog.Name, prog.VarBlocks, prog.Statements, false))
	return out
}

func posKey(p Pos, member, block string) string {
	return strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Col) + "|" + member + "|" + block
}

// ─── path model ─────────────────────────────────────────────────────────────

// iwSeg is one step of a member path: a field, or an index. "[*]" is an
// index the pass cannot read as a constant: it may be any element.
type iwSeg struct {
	field bool
	text  string // the field's name, or the bracket text "[1]", "[1,2]", "[*]"
}

func (s iwSeg) match(o iwSeg) bool {
	if s.field != o.field {
		return false
	}
	if s.field {
		return ir.SameName(s.text, o.text)
	}
	return s.text == "[*]" || o.text == "[*]" || s.text == o.text
}

// iwParseKey reads a wiring member key ("Cmd.Start", "Levels[1]").
func iwParseKey(key string) []iwSeg {
	var out []iwSeg
	for _, part := range strings.Split(key, ".") {
		name, rest, _ := strings.Cut(part, "[")
		if name != "" {
			out = append(out, iwSeg{field: true, text: name})
		}
		for rest != "" {
			idx, tail, ok := strings.Cut(rest, "]")
			if !ok {
				break
			}
			out = append(out, iwSeg{text: "[" + strings.ReplaceAll(idx, " ", "") + "]"})
			rest = strings.TrimPrefix(tail, "[")
		}
	}
	return out
}

// isPrefix reports whether p is a prefix of q (equal counts).
func isPrefix(p, q []iwSeg) bool {
	if len(p) > len(q) {
		return false
	}
	for i := range p {
		if !p[i].match(q[i]) {
			return false
		}
	}
	return true
}

type iwEntry struct {
	path []iwSeg
	text string
	kind string
}

func (e iwEntry) readOnly() bool { return e.kind == "from" || e.kind == "expr" }

func sortEntries(es []iwEntry) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].text < es[j-1].text; j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

type iwInstance struct {
	name    string
	entries []iwEntry
}

// refuses says whether writing path (relative to the instance; empty is the
// whole instance) reaches a read-only member. A member takes its own
// entry's kind, else its nearest listed ancestor's, else memory. A write
// into a read-only member (or a bit or element of it) is refused; so is one
// that covers a read-only member (whole is then set).
func (in *iwInstance) refuses(path []iwSeg) (e iwEntry, whole, bad bool) {
	best := -1
	for i, en := range in.entries {
		if isPrefix(en.path, path) && (best < 0 || len(en.path) > len(in.entries[best].path)) {
			best = i
		}
	}
	if best >= 0 && in.entries[best].readOnly() {
		return in.entries[best], false, true
	}
	for _, en := range in.entries {
		if en.readOnly() && len(en.path) > len(path) && isPrefix(path, en.path) {
			return en, true, true
		}
	}
	return iwEntry{}, false, false
}

// ─── facts about each POU ───────────────────────────────────────────────────

type iwAnalysis struct {
	kinds map[string]*iwInstance
	fbs   map[string]*FunctionBlockDecl
	pous  map[*FunctionBlockDecl]*iwPOU
	memo  map[iwPinKey][]iwPinWrite
}

type iwPinKey struct {
	fb  *FunctionBlockDecl
	pin string
}

// iwRoot says what a name in a POU refers to.
type iwRoot struct {
	name  string
	pin   bool // a VAR_IN_OUT of the block
	local bool // a variable of the POU: never a wired instance
}

type iwWrite struct {
	root     iwRoot
	path     []iwSeg
	pos, end Pos
	text     string
	builtin  string // the standard block that writes the pin itself
}

type iwBind struct {
	pin      string // the callee's VAR_IN_OUT
	root     iwRoot
	path     []iwSeg
	pos, end Pos
	text     string
}

type iwCall struct {
	callee *FunctionBlockDecl
	binds  []iwBind
}

type iwPOU struct {
	name   string
	locals map[string]TypeExpr // NameKey → declared type (every non-external variable)
	pins   map[string]string   // NameKey → declared spelling
	direct []iwWrite
	calls  []iwCall
}

func (a *iwAnalysis) newPOU(name string, blocks []VarBlock, stmts []Statement, isFB bool) *iwPOU {
	p := &iwPOU{name: name, locals: map[string]TypeExpr{}, pins: map[string]string{}}
	for _, vb := range blocks {
		if vb.Kind == "VAR_EXTERNAL" || vb.Kind == "VAR_GLOBAL" {
			continue
		}
		for _, vd := range vb.Variables {
			p.locals[ir.NameKey(vd.Name)] = vd.Type
			if vb.Kind == "VAR_IN_OUT" && isFB {
				p.pins[ir.NameKey(vd.Name)] = vd.Name
			}
		}
	}
	p.block(a, stmts)
	return p
}

func (p *iwPOU) rootOf(name string) iwRoot {
	k := ir.NameKey(name)
	if orig, ok := p.pins[k]; ok {
		return iwRoot{name: orig, pin: true}
	}
	if _, ok := p.locals[k]; ok {
		return iwRoot{name: name, local: true}
	}
	return iwRoot{name: name}
}

// lvalue reads an assignment target or an argument as root + path.
func (p *iwPOU) lvalue(e Expression) (iwRoot, []iwSeg, string, bool) {
	switch v := e.(type) {
	case *IdentExpr:
		return p.rootOf(v.Name), nil, v.Name, true
	case *MemberExpr:
		r, path, text, ok := p.lvalue(v.Object)
		if !ok {
			return r, nil, "", false
		}
		return r, append(path, iwSeg{field: true, text: v.Member}), text + "." + v.Member, true
	case *IndexExpr:
		r, path, text, ok := p.lvalue(v.Array)
		if !ok {
			return r, nil, "", false
		}
		var parts []string
		for _, ix := range v.Indices {
			if n, isNum := ix.(*NumberLit); isNum {
				parts = append(parts, strings.ReplaceAll(n.Value, " ", ""))
			} else {
				parts = []string{"*"}
				break
			}
		}
		seg := "[" + strings.Join(parts, ",") + "]"
		return r, append(path, iwSeg{text: seg}), text + seg, true
	}
	return iwRoot{}, nil, "", false
}

func (p *iwPOU) write(e Expression, pos Pos, builtin string) {
	r, path, text, ok := p.lvalue(e)
	if !ok {
		return
	}
	end := pos
	if pn, isPosed := e.(Posed); isPosed && pn.NodePos().Line > 0 {
		pos = pn.NodePos()
		end = pos
	}
	end.Col = pos.Col + len(text)
	p.direct = append(p.direct, iwWrite{root: r, path: path, pos: pos, end: end, text: text, builtin: builtin})
}

func (p *iwPOU) block(a *iwAnalysis, ss []Statement) {
	for _, s := range ss {
		p.stmt(a, s)
	}
}

func (p *iwPOU) stmt(a *iwAnalysis, s Statement) {
	switch v := s.(type) {
	case *AssignStmt:
		p.write(v.TargetExpr, v.Pos, "")
	case *IfStmt:
		p.block(a, v.Then)
		for _, ei := range v.ElsIfs {
			p.block(a, ei.Body)
		}
		p.block(a, v.Else)
	case *ForStmt:
		// The loop variable is a local counter, never a wired instance.
		p.block(a, v.Body)
	case *WhileStmt:
		p.block(a, v.Body)
	case *RepeatStmt:
		p.block(a, v.Body)
	case *CaseStmt:
		for _, c := range v.Cases {
			p.block(a, c.Body)
		}
		p.block(a, v.Else)
	case *CallStmt:
		p.call(a, v.Call)
	}
}

// call records an FB call's bindings: `=>` targets are written, and a
// VAR_IN_OUT argument is passed on for the callee's own writes to be
// followed (a standard block's in-out pin is written whole).
func (p *iwPOU) call(a *iwAnalysis, c *CallExpr) {
	if c == nil {
		return
	}
	for _, ob := range c.OutputBindings {
		p.write(ob.Target, ob.Pos, "")
	}
	typeName := p.fbTypeOf(c)
	if typeName == "" {
		return
	}
	if fb, ok := a.fbs[ir.NameKey(typeName)]; ok {
		pins := map[string]string{}
		for _, vb := range fb.VarBlocks {
			if vb.Kind == "VAR_IN_OUT" {
				for _, vd := range vb.Variables {
					pins[ir.NameKey(vd.Name)] = vd.Name
				}
			}
		}
		call := iwCall{callee: fb}
		for _, na := range c.NamedArgs {
			pin, ok := pins[ir.NameKey(na.Name)]
			if !ok {
				continue
			}
			r, path, text, ok := p.lvalue(na.Value)
			if !ok {
				continue
			}
			pos := na.Pos
			if pn, isPosed := na.Value.(Posed); isPosed && pn.NodePos().Line > 0 {
				pos = pn.NodePos()
			}
			end := pos
			end.Col = pos.Col + len(text)
			call.binds = append(call.binds, iwBind{pin: pin, root: r, path: path, pos: pos, end: end, text: text})
		}
		if len(call.binds) > 0 {
			p.calls = append(p.calls, call)
		}
		return
	}
	// A standard block: a VAR_IN_OUT pin it declares (READ's RD_1) is
	// written by Go code the pass cannot see, so it counts as written whole.
	if t := ir.LookupFBType(typeName); t != nil && t.FB != nil {
		for _, na := range c.NamedArgs {
			if idx, ok := t.FB.SlotOf(na.Name); ok && t.FB.IsInOut(idx) {
				p.write(na.Value, na.Pos, t.FB.Name)
			}
		}
	}
}

// fbTypeOf is the declared type name of the instance a call runs: a local
// or file-scope variable, or an element of an array of them.
func (p *iwPOU) fbTypeOf(c *CallExpr) string {
	var head Expression = c.Callee
	name := c.Name
	if head != nil {
		for {
			if ix, ok := head.(*IndexExpr); ok {
				head = ix.Array
				continue
			}
			break
		}
		if id, ok := head.(*IdentExpr); ok {
			name = id.Name
		} else {
			return ""
		}
	}
	te, ok := p.locals[ir.NameKey(name)]
	if !ok {
		return ""
	}
	for {
		at, isArr := te.(*ArrayType)
		if !isArr {
			break
		}
		te = at.Elem
	}
	if nt, ok := te.(*NamedType); ok {
		return nt.Name
	}
	return ""
}

// ─── what a block writes through its pins ───────────────────────────────────

// iwPinWrite is one assignment a block (or a block it passes the pin to)
// makes into a pin: path is relative to the pin.
type iwPinWrite struct {
	path  []iwSeg
	pos   Pos
	text  string
	block string // the block the assignment is in
}

// pinWrites lists what calling fb assigns through its VAR_IN_OUT pin, the
// blocks it passes the pin to included. A block already being walked
// (recursion) adds nothing.
func (a *iwAnalysis) pinWrites(fb *FunctionBlockDecl, pin string, stack map[*FunctionBlockDecl]bool) []iwPinWrite {
	key := iwPinKey{fb, ir.NameKey(pin)}
	if w, ok := a.memo[key]; ok {
		return w
	}
	if stack[fb] {
		return nil
	}
	stack[fb] = true
	defer delete(stack, fb)
	p := a.pous[fb]
	if p == nil {
		return nil
	}
	var out []iwPinWrite
	for _, d := range p.direct {
		if d.root.pin && ir.SameName(d.root.name, pin) {
			out = append(out, iwPinWrite{path: d.path, pos: d.pos, text: d.text, block: fb.Name})
		}
	}
	for _, c := range p.calls {
		for _, b := range c.binds {
			if !b.root.pin || !ir.SameName(b.root.name, pin) {
				continue
			}
			for _, w := range a.pinWrites(c.callee, b.pin, stack) {
				out = append(out, iwPinWrite{
					path: append(append([]iwSeg(nil), b.path...), w.path...),
					pos:  w.pos, text: w.text, block: w.block,
				})
			}
		}
	}
	// A recursion cut the walk short, so the answer is not the block's own:
	// keep it out of the memo.
	if len(stack) == 1 {
		a.memo[key] = out
	}
	return out
}
