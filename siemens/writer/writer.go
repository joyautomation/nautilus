// Package writer turns a nautilus project into SCL sources TIA Portal can
// import and compile (docs/design/siemens-ide.md §2): each program becomes
// a FUNCTION_BLOCK with an instance DB, each task an organization block
// that calls its program, and the manifest's tags one global DB. tiad
// imports the files, generates the blocks and compiles them.
//
// The rule is "compute what nautilus computes, or refuse". Integer
// arithmetic is the main case: nautilus evaluates it at 64 bits and wraps
// only at the store, so the writer widens every operand to LINT and wraps
// explicitly at the store (types.go), and refuses integer arithmetic
// anywhere it cannot do that (an FB input, a function argument, a CASE
// selector). nautilus's own lowering runs first, so what nautilus cannot
// run is refused before anything else.
//
// What it does not translate yet it refuses with a rule-tagged
// diagnostic, never a guess, worded as the writer's gap with the issue
// that closes it (gaps.go): more than one task, user FUNCTIONs and
// FUNCTION_BLOCKs, UDTs and enums, arrays, STRING, LTIME, TIME
// arithmetic, SFC, and every builtin outside the table in expr.go.
// Bitwise operators, the guarded divisions (division by anything but a
// non-zero constant: nautilus gives 0), SR/RS, the shifts, MUX, TRUNC and
// the overloaded TO_* conversions are written (ops.go, expr.go).
//
// Known divergences, documented rather than refused: where nautilus
// faults the scan the CPU sets ENO FALSE and carries on: a REAL → integer
// conversion out of range (TRUNC included), a MUX selector out of range,
// a negative shift count. Each is a warning at its use site
// (Output.Warnings), which check and the deploy print.
//
// Comments in the nautilus source are not carried into the SCL: nautilus
// is where the program is read and edited.
//
// The SCL spellings here are what TIA's own exported sources look like,
// to the best of public knowledge; the golden files are unverified
// against a TIA compile until the first TIA session (S2b) and will be
// corrected against its exports.
//
// The writer imports only the language packages: the CLI hands it the
// program sources and the manifest's tags (context map: the Siemens target
// is an anti-corruption layer and never imports internal/project).
package writer

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
)

// Program is one nautilus program to write. The language follows the
// path's extension: .st as it is, .ld and .fbd through their transpilers
// to ST (nautilus's own lowering, so the ladder the person edits and the
// SCL the CPU runs come from one source). Diagnostics point at lines of
// the original file.
type Program struct {
	Path   string
	Source string
	// Libs are project library sources (ST or LD) whose function block
	// signatures a ladder file needs in scope. A name the program takes
	// from one (a TYPE, FUNCTION, block or global constant) is a writer
	// gap, not nautilus's refusal.
	Libs []string
}

// Global is one manifest tag, which becomes a member of the global DB.
type Global struct {
	Name   string
	Type   string // IEC type name, e.g. REAL, BOOL, INT
	Init   string // ST literal text, or ""
	Retain bool
}

// Task schedules one program at Interval. nautilus has no free-running
// task (a manifest without scan: gets the default), so Interval must be
// set; the CLI passes the manifest's effective scan.
type Task struct {
	Name     string
	Program  string // the PROGRAM's name
	Interval time.Duration
}

// Input is everything the writer needs from a project.
type Input struct {
	Programs []Program
	Globals  []Global
	Tasks    []Task
	// TagsDB names the global DB. Default "Tags".
	TagsDB string
	// BlockPrefix is put in front of every generated block name: the
	// program FBs, their instance DBs, the tags DB and the OBs, and every
	// reference to them in the SCL. It keeps a generated block clear of
	// TIA's own ("Main" [OB1]) and of a customer's. The writer applies it
	// as given; the CLI supplies the default ("naut_") and refuses an
	// empty one.
	BlockPrefix string
}

// File is one generated source, named as tiad imports it.
type File struct {
	Path string // e.g. "naut_Main.scl", "naut_Main_DB.db", "naut_Tags.db", "naut_OB_main.scl"
	Data []byte
}

// OB is an organization block the writer generated. A cyclic interrupt
// OB's cycle is not part of SCL source; tiad sets it from here.
type OB struct {
	Name   string
	Number int
	Cycle  time.Duration // 0 for OB1
}

// Output is the generated project software.
type Output struct {
	Files []File
	OBs   []OB
	// Roots maps a nautilus scope to its Siemens symbol: a program name to
	// its instance DB (`"naut_Main_DB"`), and "" to the global DB
	// (`"naut_Tags"`); the names carry Input.BlockPrefix.
	// Symbol uses it to turn a nautilus tag path into a Web API symbol.
	Roots map[string]string
	// Sources says where each generated file came from, by its Path:
	// Locate reads a TIA compile message back through it.
	Sources map[string]Source
	// Warnings are the places where the CPU behaves differently from
	// nautilus in a case the writer documents rather than refuses (a MUX
	// selector out of range, a negative shift count, a REAL beyond an
	// integer's range: nautilus faults the scan, the CPU sets ENO FALSE
	// and carries on). They come with the files, and with the
	// diagnostics when Write refuses.
	Warnings []Diag
	// names are the readable names under each root, lower-cased: the
	// manifest tags under "", a program's statics under its name. The
	// value says whether the name has members (an FB instance).
	names map[string]map[string]bool
}

// Symbol maps a nautilus tag path to the Web API symbol of the same value:
// a manifest tag "Level" to `"naut_Tags".Level`, a program variable
// "Main.count" to `"naut_Main_DB".count` (with the default block prefix).
//
// It answers only for what the CPU holds where the Web API can read it: a
// manifest tag, or a program's static (not a temp), and members below
// them.
func (o Output) Symbol(path string) (string, bool) {
	if head, rest, ok := strings.Cut(path, "."); ok {
		for k, root := range o.Roots {
			if k == "" || !strings.EqualFold(k, head) {
				continue
			}
			v, member, deep := strings.Cut(rest, ".")
			hasMembers, known := o.names[strings.ToLower(k)][strings.ToLower(v)]
			if !known || deep && (!hasMembers || strings.Contains(member, ".")) {
				return "", false
			}
			return root + "." + rest, true
		}
	}
	// Tags are scalars so far (#1299): no member path below one.
	if root, ok := o.Roots[""]; ok && !strings.Contains(path, ".") {
		if _, known := o.names[""][strings.ToLower(path)]; known {
			return root + "." + path, true
		}
	}
	return "", false
}

// Source is where one generated file came from: the nautilus file, and
// for an FB the nautilus line of each line after its BEGIN (0: none).
type Source struct {
	Path string
	Body []int
}

// Locate maps a TIA compile message back to nautilus: block is the
// block's name as TIA names it ("naut_Main"), line the line TIA's
// compiler gives in the block's body, where 1 is the BEGIN line itself
// (measured on TIA V21, #789: an error on the line after BEGIN is line
// 2). It answers the nautilus file and its line, 0 when the message has
// no line or the line has none (a declaration, the OB, a DB); ok is false
// for a block the writer did not generate.
func (o Output) Locate(block string, line int) (file string, srcLine int, ok bool) {
	for name, src := range o.Sources {
		if !strings.EqualFold(strings.TrimSuffix(name, path.Ext(name)), block) {
			continue
		}
		if i := line - 2; i >= 0 && i < len(src.Body) {
			srcLine = src.Body[i]
		}
		return src.Path, srcLine, true
	}
	return "", 0, false
}

// Diag is one reason the writer refused, tied to a source line.
type Diag struct {
	Rule    string // "siemens/<rule>"
	Path    string
	Line    int
	Message string
}

func (d Diag) String() string {
	return fmt.Sprintf("%s:%d: siemens target: %s [%s]", d.Path, d.Line, d.Message, d.Rule)
}

// Write generates the SCL sources. When any rule fires it returns the
// diagnostics and no files: nothing half-written reaches TIA.
func Write(in Input) (Output, []Diag, error) {
	tagsDB := in.TagsDB
	if tagsDB == "" {
		tagsDB = "Tags"
	}
	tagsDB = in.BlockPrefix + tagsDB
	prefix := in.BlockPrefix
	w := &writer{globals: map[string]Global{}, tagsDB: tagsDB, prefix: prefix}
	for _, g := range in.Globals {
		w.globals[strings.ToLower(g.Name)] = g
	}
	out := Output{Roots: map[string]string{"": quote(tagsDB)}, names: map[string]map[string]bool{"": {}}, Sources: map[string]Source{}}
	for _, g := range in.Globals {
		out.names[""][strings.ToLower(g.Name)] = false
	}
	// Every block name, lower-cased (TIA's are case-insensitive): a
	// program named Tags would make an FB and a DB both "Tags".
	blocks := map[string]string{strings.ToLower(tagsDB): "the tags DB"}
	claim := func(name, what string) bool {
		k := strings.ToLower(name)
		if other, taken := blocks[k]; taken {
			w.diag("siemens/name", "", 0, fmt.Sprintf("block %q (%s) collides with %s", name, what, other))
			return false
		}
		blocks[k] = what
		return true
	}

	progs := map[string]bool{}
	for _, p := range in.Programs {
		if isSFC(p) {
			w.diag("siemens/sfc", p.Path, 1, gapMessage(gapSFC, "SFC programs", "they will be written as SCL state machines"))
			continue
		}
		src, lines, err := toST(p)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		prog, err := st.Parse(src)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		mark, wmark := len(w.diags), len(w.warnings)
		if !strings.EqualFold(prog.TopKeyword, "PROGRAM") || prog.Name == "" {
			w.diag("siemens/program", p.Path, 1, "the source declares no PROGRAM")
			continue
		}
		pw := w.program(p.Path, p.Libs, prog)
		for i := mark; i < len(w.diags); i++ {
			w.diags[i].Line = mapLine(lines, w.diags[i].Line)
		}
		for i := wmark; i < len(w.warnings); i++ {
			w.warnings[i].Line = mapLine(lines, w.warnings[i].Line)
		}
		if pw == nil {
			continue
		}
		fbName, dbName := prefix+prog.Name, prefix+prog.Name+"_DB"
		if !claim(fbName, "program "+prog.Name) || !claim(dbName, "the instance DB of "+prog.Name) {
			continue
		}
		progs[strings.ToLower(prog.Name)] = true
		out.names[strings.ToLower(prog.Name)] = pw.statics
		out.Files = append(out.Files,
			File{Path: fbName + ".scl", Data: pw.fb},
			File{Path: dbName + ".db", Data: instanceDB(fbName, dbName)})
		body := make([]int, len(pw.lines))
		for i, l := range pw.lines {
			body[i] = mapLine(lines, l)
		}
		out.Sources[fbName+".scl"] = Source{Path: p.Path, Body: body}
		out.Sources[dbName+".db"] = Source{Path: p.Path}
		out.Roots[prog.Name] = quote(dbName)
	}

	// The guarded division functions the programs call. Files come out
	// with these first (the sort below), so tiad, which generates the
	// sources in the order given, has each FC before an FB that calls it.
	fns := make([]string, 0, len(w.guarded))
	for fn := range w.guarded {
		fns = append(fns, fn)
	}
	sort.Strings(fns)
	for _, fn := range fns {
		name := prefix + fn
		if !claim(name, "the guarded division function "+fn) {
			continue
		}
		out.Files = append(out.Files, File{Path: name + ".scl", Data: guardedSource(prefix, fn)})
		out.Sources[name+".scl"] = Source{Path: w.guarded[fn]}
	}

	// One task in v1. nautilus copies shared tags in at a task's scan
	// start and commits them at its end, while cyclic interrupt OBs
	// preempt each other and touch the tags DB in place: with two tasks
	// the CPU would see a different data model. Copy-in/copy-out comes
	// later, as its own item.
	if len(in.Tasks) > 1 {
		w.diag("siemens/task", "nautilus.yaml", 0, gapMessage(gapTasks, fmt.Sprintf("more than one task (%d here)", len(in.Tasks)), "nautilus's per-task copy of shared tags has no SCL form yet"))
	}
	for i, t := range in.Tasks {
		if i > 0 {
			break
		}
		if !progs[strings.ToLower(t.Program)] {
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s runs program %s, which the writer could not write (its diagnostics are above)", t.Name, t.Program))
			continue
		}
		switch {
		case t.Interval <= 0:
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s has no interval; nautilus has no free-running task", t.Name))
			continue
		case t.Interval%time.Millisecond != 0:
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s: interval %s is not a whole number of milliseconds", t.Name, t.Interval))
			continue
		case t.Interval > 60*time.Second:
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s: interval %s is over the 60 s an S7-1500 cyclic interrupt allows", t.Name, t.Interval))
			continue
		}
		ob := OB{Name: prefix + "OB_" + t.Name, Number: 30, Cycle: t.Interval}
		if !claim(ob.Name, "the OB of task "+t.Name) {
			continue
		}
		out.OBs = append(out.OBs, ob)
		out.Files = append(out.Files, File{Path: ob.Name + ".scl", Data: obSource(ob, prefix+t.Program+"_DB")})
		out.Sources[ob.Name+".scl"] = Source{Path: "nautilus.yaml"}
	}

	if len(in.Globals) > 0 {
		db, ok := w.globalDB()
		if ok {
			out.Files = append(out.Files, File{Path: tagsDB + ".db", Data: db})
			out.Sources[tagsDB+".db"] = Source{Path: "nautilus.yaml"}
		}
	}

	if len(w.diags) > 0 {
		return Output{Warnings: w.warnings}, w.diags, nil
	}
	out.Warnings = w.warnings
	fc := map[string]bool{}
	for _, fn := range fns {
		fc[prefix+fn+".scl"] = true
	}
	sort.Slice(out.Files, func(i, j int) bool {
		a, b := out.Files[i].Path, out.Files[j].Path
		if fc[a] != fc[b] {
			return fc[a]
		}
		return a < b
	})
	return out, nil, nil
}

type writer struct {
	globals map[string]Global
	tagsDB  string // with the prefix
	prefix  string
	diags   []Diag
	// warnings: Output.Warnings.
	warnings []Diag
	// guarded are the guarded division functions the programs call
	// (ops.go), each with the first program that called it.
	guarded map[string]string
}

func (w *writer) useGuarded(fn, path string) {
	if w.guarded == nil {
		w.guarded = map[string]string{}
	}
	if _, ok := w.guarded[fn]; !ok {
		w.guarded[fn] = path
	}
}

func (w *writer) diag(rule, path string, line int, msg string) {
	w.diags = append(w.diags, Diag{Rule: rule, Path: path, Line: line, Message: msg})
}

func quote(name string) string { return `"` + name + `"` }

func instanceDB(fb, db string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(db))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	// No NON_RETAIN here: which members are retained is the FB's own
	// declaration (VAR RETAIN), and a block-level NON_RETAIN would
	// override it.
	fmt.Fprintf(&b, "%s\n", quote(fb))
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String())
}

func obSource(ob OB, instDB string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "ORGANIZATION_BLOCK %s\n", quote(ob.Name))
	fmt.Fprintf(&b, "TITLE = \"Cyclic interrupt, %s\"\n", ob.Cycle)
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("\nBEGIN\n")
	fmt.Fprintf(&b, "\t%s();\n", quote(instDB))
	b.WriteString("END_ORGANIZATION_BLOCK\n")
	return []byte(b.String())
}

// globalDB writes the manifest's tags as one optimized global DB, retained
// tags in a RETAIN section.
func (w *writer) globalDB() ([]byte, bool) {
	names := make([]string, 0, len(w.globals))
	for k := range w.globals {
		names = append(names, k)
	}
	sort.Strings(names)
	var plain, retained []string
	ok := true
	for _, k := range names {
		g := w.globals[k]
		typ, why := sclType(g.Type)
		if why != "" {
			w.diag("siemens/type", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, why))
			ok = false
			continue
		}
		line := fmt.Sprintf("      %s : %s", g.Name, typ)
		if g.Init != "" {
			init, why := literal(g.Init)
			if why != "" {
				w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, why))
				ok = false
				continue
			}
			line += " := " + init
		}
		line += ";"
		if g.Retain {
			retained = append(retained, line)
		} else {
			plain = append(plain, line)
		}
	}
	if !ok {
		return nil, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(w.tagsDB))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	if len(plain) > 0 {
		b.WriteString("   VAR \n")
		b.WriteString(strings.Join(plain, "\n") + "\n")
		b.WriteString("   END_VAR\n")
	}
	if len(retained) > 0 {
		b.WriteString("   VAR RETAIN\n")
		b.WriteString(strings.Join(retained, "\n") + "\n")
		b.WriteString("   END_VAR\n")
	}
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String()), true
}

// literal parses a manifest init as an ST literal and prints it in SCL.
func literal(src string) (string, string) {
	prog, err := st.Parse("PROGRAM x\nVAR v : BOOL; END_VAR\nv := " + src + ";\nEND_PROGRAM\n")
	if err != nil || len(prog.Statements) != 1 {
		return "", fmt.Sprintf("init %q is not a literal", src)
	}
	a, ok := prog.Statements[0].(*st.AssignStmt)
	if !ok {
		return "", fmt.Sprintf("init %q is not a literal", src)
	}
	switch a.Value.(type) {
	case *st.NumberLit, *st.BoolLit, *st.StringLit, *st.TimeLit, *st.TypedLit, *st.UnaryExpr:
	default:
		return "", fmt.Sprintf("init %q is not a literal", src)
	}
	pw := &progWriter{w: &writer{globals: map[string]Global{}}, locals: map[string]local{}}
	return pw.expr(a.Value, 0), ""
}

// ExternalTypes is the type the programs declare for each tag they bind
// in a VAR_EXTERNAL or VAR_GLOBAL block, by lower-cased name. nautilus
// gives a tag with no type: the type the logic binds it as (runtime
// tagdef.go: `init: 0` on a tag declared `Counter : DINT` is a DINT), so
// the CLI asks this before it types such a tag for the tags DB. A source
// that does not parse declares nothing here (check reports it). Two
// programs binding one tag as different types is the runtime's refusal
// (CheckBindings), not the writer's: the last one read wins here.
func ExternalTypes(progs []Program) map[string]string {
	out := map[string]string{}
	add := func(vbs []st.VarBlock) {
		for _, vb := range vbs {
			if vb.Kind != "VAR_EXTERNAL" && vb.Kind != "VAR_GLOBAL" {
				continue
			}
			for _, v := range vb.Variables {
				if t := strings.TrimSpace(v.Datatype); t != "" {
					out[strings.ToLower(v.Name)] = t
				}
			}
		}
	}
	for _, p := range progs {
		if isSFC(p) {
			if prog, err := sfc.Parse(p.Source); err == nil {
				add(prog.VarBlocks)
			}
			continue
		}
		src, _, err := toST(p)
		if err != nil {
			continue
		}
		if prog, err := st.Parse(src); err == nil {
			add(prog.VarBlocks)
		}
	}
	return out
}

// isSFC reports an SFC program, which the writer does not translate yet.
func isSFC(p Program) bool { return strings.EqualFold(path.Ext(p.Path), ".sfc") }

// StructuredInitGap words a manifest tag start value the writer does not
// translate yet (a structured tag's map), for the CLI that reads it.
func StructuredInitGap(v any) string {
	return gapMessage(gapStructured, fmt.Sprintf("the structured start value %v", v), "")
}

// toST returns a program's source as ST, with lines[i] the original line
// of ST line i+1 (nil when the source is ST already).
func toST(p Program) (string, []int, error) {
	switch strings.ToLower(path.Ext(p.Path)) {
	case ".ld":
		f, fl, err := ld.TranspileWithLines(p.Source, p.Libs...)
		if err != nil {
			return "", nil, err
		}
		s, sl, err := fbd.TranspileWithLines(f)
		if err != nil {
			return "", nil, err
		}
		out := make([]int, len(sl))
		for i, l := range sl {
			out[i] = mapLine(fl, l)
		}
		return s, out, nil
	case ".fbd":
		return fbd.TranspileWithLines(p.Source)
	}
	return p.Source, nil, nil
}

// mapLine is the original line of lowered line l: l itself for ST (no
// map), and 0 (no line) for a line outside the map, never the lowered
// line, which would name a wrong line of the ladder.
func mapLine(lines []int, l int) int {
	if lines == nil {
		return l
	}
	if l < 1 || l > len(lines) {
		return 0
	}
	return lines[l-1]
}

// ProgramName is the name of the PROGRAM a source declares, through the
// same lowering Write uses, so a caller can name a Task's Program from
// the file a manifest's task points at.
func ProgramName(p Program) (string, error) {
	if isSFC(p) {
		prog, err := sfc.Parse(p.Source)
		if err != nil {
			return "", fmt.Errorf("%s: %w", p.Path, err)
		}
		if prog.Name == "" {
			return "", fmt.Errorf("%s: the source declares no PROGRAM", p.Path)
		}
		return prog.Name, nil
	}
	src, _, err := toST(p)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.Path, err)
	}
	prog, err := st.Parse(src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", p.Path, err)
	}
	if !strings.EqualFold(prog.TopKeyword, "PROGRAM") || prog.Name == "" {
		return "", fmt.Errorf("%s: the source declares no PROGRAM", p.Path)
	}
	return prog.Name, nil
}
