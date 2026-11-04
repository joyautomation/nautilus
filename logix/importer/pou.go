package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/l5x"
	"github.com/joyautomation/nautilus/lang/lint"
)

// scope is where a routine's operands resolve: the program's own tags
// over the controller's, or an Add-On Instruction's parameters and
// locals and nothing else.
type scope struct {
	owner  string // "Program/Routine" or "AOI:Name"
	local  map[string]*l5x.Tag
	params map[string]*l5x.Parameter
	ctrl   map[string]*l5x.Tag
	// blockOwner maps a timer/counter instance (lower-cased) to the
	// routine that runs its instruction, for a program whose routines
	// share program tags.
	blockOwner map[string]string
	// promoted renames program tags to <Program>_<Tag> controller tags,
	// for a program with more than one routine.
	promoted string
	// im types a controller alias tag through its target (alias.go); nil
	// in an Add-On Instruction, which has no alias tags.
	im *importer
}

// found is one resolved operand base.
type found struct {
	tag   *l5x.Tag
	param *l5x.Parameter
	ext   bool // a controller tag (VAR_EXTERNAL), not a local
	name  string
	dtype string
	dims  string
	// alias is why an alias tag cannot be carried (alias.go): an operand
	// naming it is refused with this, by name, never as an untyped tag.
	alias *refusal
}

func (s *scope) lookup(base string) *found {
	k := strings.ToLower(base)
	if p, ok := s.params[k]; ok {
		return &found{param: p, name: l5x.Ident(p.Name), dtype: p.DataType, dims: dimText(p.Dimension)}
	}
	if t, ok := s.local[k]; ok {
		f := &found{tag: t, name: l5x.Ident(t.Name), dtype: t.DataType, dims: t.Dimensions}
		if s.promoted != "" {
			f.name = l5x.Ident(s.promoted) + "_" + f.name
			f.ext = true
		}
		if t.AliasFor != "" {
			f.alias = refuse("alias", "%s is a program-scoped alias tag (of %s); nautilus binds an alias on a controller tag", t.Name, t.AliasFor)
		}
		return f
	}
	if t, ok := s.ctrl[k]; ok {
		f := &found{tag: t, ext: true, name: l5x.Ident(t.Name), dtype: t.DataType, dims: t.Dimensions}
		if t.AliasFor != "" && s.im != nil {
			// An alias tag has no DataType in an export: its target's.
			f.dtype, f.dims, f.alias = s.im.aliasType(t)
		}
		return f
	}
	return nil
}

func dimText(d int) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprint(d)
}

// decl is one declaration the emitted POU needs.
type decl struct {
	Name, Type, Init string
	Section          string // VAR_EXTERNAL | VAR | VAR_INPUT | VAR_OUTPUT | VAR_IN_OUT
	Comment          string
}

// pou is one emitted POU: a PROGRAM per routine, a FUNCTION_BLOCK per
// Add-On Instruction.
type pou struct {
	kind     string // "PROGRAM" | "FUNCTION_BLOCK"
	name     string
	header   []string // comment lines under the POU line
	decls    []*decl
	declIdx  map[string]*decl
	rungs    []outRung
	stText   string // an ST routine's text, identifiers mapped
	complete bool
	total    int
	imported int
}

type outRung struct {
	Name    string
	Comment string   // the header's (* … *)
	Lines   []string // // lines above the rung
	Body    string
	// Skipped is a rung kept as a comment only: Body is the Logix text.
	Skipped string // the reason
}

func (p *pou) declare(d decl) {
	if p.declIdx == nil {
		p.declIdx = map[string]*decl{}
	}
	k := strings.ToLower(d.Section + ":" + d.Name)
	if _, ok := p.declIdx[k]; ok {
		return
	}
	dd := d
	p.declIdx[k] = &dd
	p.decls = append(p.decls, &dd)
}

var sectionOrder = []string{"VAR_INPUT", "VAR_OUTPUT", "VAR_IN_OUT", "VAR_EXTERNAL", "VAR"}

// ldPragma is the first line of every ladder file the import writes: an
// @allow pragma above the POU, so it covers the file, switching off every
// ladder structure rule (lang/lint, #561). The vendor's rungs come across
// as they were written — two coils on one tag, literal trip points, rungs
// with no comment — and an import that warns on every routine would bury
// what `naut check` says about the import itself. The reason says where
// the rungs came from and how to have them linted: delete the line.
func (im *importer) ldPragma() string {
	var ids []string
	for _, r := range lint.Rules {
		if strings.HasPrefix(r.ID, "LD") {
			ids = append(ids, r.ID)
		}
	}
	src := im.opts.Source
	if src == "" {
		src = "the " + im.file.Controller.Name + " export"
	}
	return fmt.Sprintf("(* @allow %s: imported from %s by `naut logix import`; delete this line to lint it *)",
		strings.Join(ids, " "), safeComment(src))
}

func (p *pou) source(pragma string) []byte {
	var b strings.Builder
	if pragma != "" {
		b.WriteString(pragma + "\n")
	}
	fmt.Fprintf(&b, "%s %s\n", p.kind, p.name)
	if len(p.header) > 0 {
		b.WriteString("(* ")
		for i, line := range p.header {
			if i > 0 {
				b.WriteString("   ")
			}
			b.WriteString(line)
			if i < len(p.header)-1 {
				b.WriteString("\n")
			}
		}
		b.WriteString(" *)\n")
	}
	for _, sec := range sectionOrder {
		var ds []*decl
		for _, d := range p.decls {
			if d.Section == sec {
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 {
			continue
		}
		if sec == "VAR_EXTERNAL" {
			sort.Slice(ds, func(i, j int) bool { return strings.ToLower(ds[i].Name) < strings.ToLower(ds[j].Name) })
		}
		fmt.Fprintf(&b, "%s\n", sec)
		width := 0
		for _, d := range ds {
			if len(d.Name) > width {
				width = len(d.Name)
			}
		}
		for _, d := range ds {
			fmt.Fprintf(&b, "    %-*s : %s", width, d.Name, d.Type)
			if d.Init != "" {
				fmt.Fprintf(&b, " := %s", d.Init)
			}
			b.WriteString(";")
			if d.Comment != "" {
				fmt.Fprintf(&b, " (* %s *)", safeComment(d.Comment))
			}
			b.WriteString("\n")
		}
		b.WriteString("END_VAR\n")
	}
	b.WriteString("LD\n")
	for i, r := range p.rungs {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, line := range r.Lines {
			fmt.Fprintf(&b, "  // %s\n", line)
		}
		if r.Skipped != "" {
			fmt.Fprintf(&b, "  // not imported (%s): %s\n", r.Skipped, r.Body)
			continue
		}
		if r.Comment != "" {
			fmt.Fprintf(&b, "  RUNG %s (* %s *)\n", r.Name, r.Comment)
		} else {
			fmt.Fprintf(&b, "  RUNG %s\n", r.Name)
		}
		fmt.Fprintf(&b, "    %s\n", r.Body)
	}
	b.WriteString("END_LD\n")
	fmt.Fprintf(&b, "END_%s\n", p.kind)
	return []byte(b.String())
}

func safeComment(s string) string {
	s = strings.ReplaceAll(s, "*)", "* )")
	s = strings.ReplaceAll(s, "(*", "( *")
	return strings.TrimSpace(s)
}

// rungComment splits a Logix rung comment into the header's one-liner
// or the // lines above the rung.
func rungComment(text string) (header string, lines []string) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return "", nil
	}
	if !strings.Contains(text, "\n") && !strings.Contains(text, "*)") && !strings.Contains(text, "(*") && len(text) <= 72 {
		return text, nil
	}
	return "", strings.Split(text, "\n")
}

// ── programs ────────────────────────────────────────────────────────────────

var blockInstr = map[string]bool{"TON": true, "TOF": true, "RTO": true, "CTU": true, "CTD": true}

// progFile is one imported routine: the file it became and where it was.
type progFile struct{ File, Program, Routine string }

// importProgram emits one POU per routine and returns their files.
func (im *importer) importProgram(prog *l5x.Program) []progFile {
	local := map[string]*l5x.Tag{}
	for _, t := range prog.Tags {
		local[strings.ToLower(t.Name)] = t
	}
	var logic []*l5x.Routine
	for _, r := range prog.Routines {
		if r.Type == "RLL" || r.Type == "ST" {
			logic = append(logic, r)
		} else {
			im.proj.note(prog.Name+"/"+r.Name, -1, r.Type, "routine type has no nautilus import yet")
		}
	}
	multi := len(logic) > 1
	owners := map[string]string{}
	for _, r := range logic {
		if r.Type != "RLL" {
			continue
		}
		for _, rg := range r.Rungs {
			terms, err := l5x.ParseRung(rg.Text)
			if err != nil {
				continue
			}
			walkTerms(terms, func(in *l5x.Instr) {
				if blockInstr[strings.ToUpper(in.Mnemonic)] && len(in.Args) > 0 {
					k := strings.ToLower(in.Args[0])
					if _, taken := owners[k]; !taken {
						owners[k] = r.Name
					}
				}
			})
		}
	}
	var files []progFile
	for _, r := range logic {
		sc := &scope{owner: prog.Name + "/" + r.Name, local: local, ctrl: im.ctrl, blockOwner: owners, im: im}
		name := l5x.Ident(prog.Name)
		if multi {
			sc.promoted = prog.Name
			name = l5x.Ident(prog.Name) + "_" + l5x.Ident(r.Name)
		}
		p := &pou{kind: "PROGRAM", name: name}
		var file string
		if r.Type == "ST" {
			im.lowerST(sc, r, p)
			file = im.fileName(name, ".st")
			im.proj.Files[file] = p.stSource(r.Text)
		} else {
			im.lowerRoutine(sc, r, p)
			file = im.fileName(name, ".ld")
			im.proj.Files[file] = p.source(im.ldPragma())
		}
		files = append(files, progFile{File: file, Program: prog.Name, Routine: r.Name})
	}
	if multi {
		im.proj.note(prog.Name, -1, "promoted", fmt.Sprintf("%d routines: the program's tags become controller tags named %s_<Tag>, one PROGRAM per routine", len(logic), l5x.Ident(prog.Name)))
	}
	return files
}

func walkTerms(terms []l5x.Term, fn func(*l5x.Instr)) {
	for _, t := range terms {
		if t.Instr != nil {
			fn(t.Instr)
		}
		for _, leg := range t.Legs {
			walkTerms(leg, fn)
		}
	}
}

// ── Add-On Instructions ─────────────────────────────────────────────────────

func (im *importer) importAOI(a *l5x.AddOnInstruction) {
	owner := "AOI:" + a.Name
	sc := &scope{owner: owner, local: map[string]*l5x.Tag{}, params: map[string]*l5x.Parameter{}}
	for _, t := range a.LocalTags {
		sc.local[strings.ToLower(t.Name)] = t
	}
	for i := range a.Parameters {
		p := &a.Parameters[i]
		sc.params[strings.ToLower(p.Name)] = p
	}
	var logic *l5x.Routine
	for _, r := range a.Routines {
		if strings.EqualFold(r.Name, "Logic") {
			logic = r
		} else {
			im.proj.note(owner, -1, "routine", r.Name+": only the Logic routine is imported")
		}
	}
	if logic == nil {
		im.proj.note(owner, -1, "routine", "no Logic routine")
		return
	}
	p := &pou{kind: "FUNCTION_BLOCK", name: l5x.Ident(a.Name)}
	if a.Description != "" {
		p.header = strings.Split(safeComment(a.Description), "\n")
	}
	// Every parameter is part of the interface whether or not the logic
	// touches it: a caller binds by name.
	for i := range a.Parameters {
		pr := &a.Parameters[i]
		if strings.EqualFold(pr.Name, "EnableIn") || strings.EqualFold(pr.Name, "EnableOut") {
			continue
		}
		typ, ok := im.typeText(pr.DataType, dimText(pr.Dimension))
		if !ok {
			im.proj.note(owner, -1, "type", pr.Name+": parameter type "+pr.DataType+" has no nautilus declaration")
			continue
		}
		sec := map[string]string{"Input": "VAR_INPUT", "Output": "VAR_OUTPUT", "InOut": "VAR_IN_OUT"}[pr.Usage]
		if sec == "" {
			continue
		}
		p.declare(decl{Name: l5x.Ident(pr.Name), Type: typ, Section: sec, Comment: firstLine(pr.Description)})
	}
	if logic.Type == "ST" {
		im.lowerST(sc, logic, p)
		im.proj.Files["lib/"+im.fileName(p.name, ".st")] = p.stSource(logic.Text)
		return
	}
	im.lowerRoutine(sc, logic, p)
	im.proj.Files["lib/"+im.fileName(p.name, ".ld")] = p.source(im.ldPragma())
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// ── ST routines ─────────────────────────────────────────────────────────────

var identRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])([A-Za-z_][A-Za-z0-9_]*)`)

// lowerST declares what a verbatim ST routine refers to. The text itself
// is not translated: Logix ST and nautilus ST share the IEC core, and the
// dialect differences (TONR, the BOOL spellings) are `naut check`'s to
// report rung by rung — honestly, not by a rewrite that guesses.
func (im *importer) lowerST(sc *scope, r *l5x.Routine, p *pou) {
	seen := map[string]bool{}
	renames := map[string]string{}
	for _, m := range identRe.FindAllStringSubmatch(r.Text, -1) {
		name := m[1]
		k := strings.ToLower(name)
		if seen[k] {
			continue
		}
		seen[k] = true
		f := sc.lookup(name)
		if f == nil {
			continue
		}
		im.declareFound(sc, p, f, "")
		if f.name != name {
			renames[name] = f.name
		}
	}
	text := r.Text
	if len(renames) > 0 {
		// A promoted program tag or an escaped keyword: the text follows
		// the declaration, identifier by identifier.
		text = identRe.ReplaceAllStringFunc(text, func(tok string) string {
			i := strings.IndexFunc(tok, func(r rune) bool { return r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' })
			if i < 0 {
				return tok
			}
			if to, ok := renames[tok[i:]]; ok {
				return tok[:i] + to
			}
			return tok
		})
	}
	p.stText = text
	p.total = strings.Count(r.Text, "\n") + 1
	p.imported = p.total
	p.complete = true
	im.proj.note(sc.owner, -1, "st-verbatim", "the ST routine is carried as written; `naut check` reports what nautilus ST lacks")
}

func (p *pou) stSource(text string) []byte {
	if p.stText != "" {
		text = p.stText
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", p.kind, p.name)
	b.WriteString("(* Imported verbatim from the Logix ST routine by `naut logix import`.\n   Logix dialect — TONR, 1/0 for BOOL — is reported by `naut check`. *)\n")
	for _, sec := range sectionOrder {
		var ds []*decl
		for _, d := range p.decls {
			if d.Section == sec {
				ds = append(ds, d)
			}
		}
		if len(ds) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", sec)
		for _, d := range ds {
			fmt.Fprintf(&b, "    %s : %s;\n", d.Name, d.Type)
		}
		b.WriteString("END_VAR\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "END_%s\n", p.kind)
	return []byte(b.String())
}

// ── declarations ────────────────────────────────────────────────────────────

var elementary = map[string]bool{"BOOL": true, "SINT": true, "INT": true, "DINT": true, "LINT": true, "USINT": true, "UINT": true, "UDINT": true, "ULINT": true, "REAL": true, "LREAL": true}

// typeText renders a Logix data type as the nautilus declaration, or
// reports that it has none (an opaque firmware structure, a STRING, a
// multi-dimensional array).
func (im *importer) typeText(dtype, dims string) (string, bool) {
	up := strings.ToUpper(dtype)
	base := ""
	switch {
	case elementary[up]:
		base = up
	case im.types[l5x.Ident(dtype)]:
		base = l5x.Ident(dtype)
	case im.aois[strings.ToLower(dtype)] != nil:
		base = l5x.Ident(im.aois[strings.ToLower(dtype)].Name)
	default:
		return "", false
	}
	if dims == "" {
		return base, true
	}
	if strings.Contains(strings.TrimSpace(dims), " ") {
		return "", false
	}
	n := 0
	fmt.Sscan(dims, &n)
	if n <= 0 {
		return "", false
	}
	return fmt.Sprintf("ARRAY [0..%d] OF %s", n-1, base), true
}

// declareFound adds the declaration an operand needs. blockType is the
// instruction that drives a TIMER/COUNTER instance ("TON", "CTU"), or ""
// for a plain read of its members.
func (im *importer) declareFound(sc *scope, p *pou, f *found, blockType string) bool {
	up := strings.ToUpper(f.dtype)
	if f.param != nil {
		return true // declared from the interface
	}
	if up == "TIMER" || up == "COUNTER" {
		typ := blockType
		if typ == "" {
			typ = "TON"
			if up == "COUNTER" {
				typ = "CTU"
			}
		}
		k := strings.ToLower("VAR:" + l5x.Ident(f.tag.Name))
		if d, ok := p.declIdx[k]; ok {
			have := d.Type
			if i := strings.LastIndex(have, " OF "); strings.HasPrefix(have, "ARRAY") && i >= 0 {
				have = have[i+4:]
			}
			if blockType != "" && have != blockType {
				if have == "TON" && blockType == "TONR" {
					d.Type = strings.Replace(d.Type, "TON", "TONR", 1)
					return true
				}
				return false
			}
			return true
		}
		if f.dims != "" {
			n := 0
			fmt.Sscan(f.dims, &n)
			if n <= 0 || strings.Contains(strings.TrimSpace(f.dims), " ") {
				return false
			}
			typ = fmt.Sprintf("ARRAY [0..%d] OF %s", n-1, typ)
		}
		d := decl{Name: l5x.Ident(f.tag.Name), Type: typ, Section: "VAR", Comment: firstLine(f.tag.Description)}
		if f.ext && sc.ctrl != nil {
			d.Comment = strings.TrimSpace(movedComment + d.Comment)
			im.movedInstance(f)
		}
		p.declare(d)
		return true
	}
	typ, ok := im.typeText(f.dtype, f.dims)
	if !ok {
		return false
	}
	d := decl{Name: f.name, Type: typ, Section: "VAR", Comment: firstLine(descOf(f))}
	if a := im.aois[strings.ToLower(f.dtype)]; a != nil {
		// An Add-On Instruction instance is a block instance: program-local.
		if f.dims != "" {
			return false
		}
		if f.ext && sc.ctrl != nil {
			d.Comment = strings.TrimSpace(movedComment + d.Comment)
			im.movedInstance(f)
		}
		p.declare(d)
		return true
	}
	if f.ext {
		if f.tag != nil {
			im.external[strings.ToLower(f.tag.Name)] = true
		}
		d.Section = "VAR_EXTERNAL"
		d.Comment = ""
	} else if f.tag != nil {
		d.Init = initText(f.tag.Value, up)
	}
	p.declare(d)
	return true
}

// movedComment heads the declaration of a controller-scoped block
// instance: a block instance is a program's in nautilus, and the
// manifest's target.logix.controller-scoped puts it back at controller
// scope when the program is written as Logix (#371).
const movedComment = "controller-scoped in the export, and written back there (target.logix.controller-scoped in nautilus.yaml). "

// movedInstance records a controller tag the import declared as a block
// instance inside a program: it is program-scoped now, so the tag file
// must not also carry it, or every program declaring it shadows a
// project tag of the same name; the manifest lists it under
// target.logix.controller-scoped instead.
func (im *importer) movedInstance(f *found) {
	if f.tag != nil && im.ctrl[strings.ToLower(f.tag.Name)] == f.tag {
		im.moved[strings.ToLower(f.tag.Name)] = true
	}
}

func descOf(f *found) string {
	if f.tag != nil {
		return f.tag.Description
	}
	return ""
}

// initText renders a scalar initial value, or "" for zero / none.
func initText(v any, dtype string) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "TRUE"
		}
	case int64:
		if x != 0 {
			return fmt.Sprint(x)
		}
	case float64:
		if x != 0 {
			s := fmt.Sprintf("%g", x)
			if !strings.ContainsAny(s, ".eE") {
				s += ".0"
			}
			return s
		}
	}
	return ""
}
