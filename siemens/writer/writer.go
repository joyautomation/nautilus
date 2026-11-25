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
// that closes it (gaps.go): enums, arrays, WSTRING,
// LTIME, TIME arithmetic, SFC, and every builtin outside the table in
// expr.go. The project's FUNCTION_BLOCKs and FUNCTIONs are written as FBs
// (multi-instances in their callers) and FCs (pous.go). Each task is its
// own cyclic interrupt OB, and
// the tags tasks share are copied per task as nautilus copies them
// (tasks.go). STRUCTs the project
// declares are written inline (structs.go), with nautilus's start
// values; programs are lowered with the project's library in scope, as
// naut check compiles them.
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
	Name string
	// Type is an IEC type name (REAL, BOOL, INT) or a STRUCT the project
	// declares (#1299).
	Type string
	Init string // an elementary tag's start value, as an ST literal, or ""
	// Start is a structured tag's start value as the manifest gives it
	// (init: { Depth: 0.45 }): the writer types each member from the
	// struct, as nautilus seeds it. nil when the tag has none.
	Start  any
	Retain bool
}

// Task schedules one program at Interval. nautilus has no free-running
// task (a manifest without scan: gets the default), so Interval must be
// set; the CLI passes the manifest's effective scan.
type Task struct {
	Name     string
	Program  string // the PROGRAM's name
	Interval time.Duration
	// DtTag is the task's dt-tag: the tag nautilus writes the measured
	// scan-to-scan seconds into each scan. "" when it has none.
	DtTag string
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
	// manifest tags under "", a program's statics under its name, each
	// with what it holds below it (nil: a scalar).
	names map[string]map[string]*shape
}

// Symbol maps a nautilus tag path to the Web API symbol of the same value:
// a manifest tag "Level" to `"naut_Tags".Level`, a program variable
// "Main.count" to `"naut_Main_DB".count` (with the default block prefix).
//
// It answers only for what the CPU holds where the Web API can read it: a
// manifest tag, or a program's static (not a temp), and members below
// them: a struct's at any depth, an FB instance's one level down.
func (o Output) Symbol(path string) (string, bool) {
	if head, rest, ok := strings.Cut(path, "."); ok {
		for k, root := range o.Roots {
			if k == "" || !strings.EqualFold(k, head) {
				continue
			}
			if sym, ok := o.member(k, root, rest); ok {
				return sym, true
			}
		}
	}
	if root, ok := o.Roots[""]; ok {
		return o.member("", root, path)
	}
	return "", false
}

// member maps path, below the root of scope k, to its symbol: each part
// must be a name the writer declared there.
func (o Output) member(k, root, path string) (string, bool) {
	parts := strings.Split(path, ".")
	s, known := o.names[strings.ToLower(k)][strings.ToLower(parts[0])]
	if !known {
		return "", false
	}
	// Each part as the writer declared it: the caller's case may differ.
	sym := root + "." + sclName(s.name)
	for i, p := range parts[1:] {
		switch {
		case s.instance:
			if i != len(parts)-2 || p == "" {
				return "", false
			}
			sym += "." + p // an IEC pin, as nautilus names it
		case s.members == nil:
			return "", false
		default:
			next, ok := s.members[strings.ToLower(p)]
			if !ok {
				return "", false
			}
			s = next
			sym += "." + sclName(s.name)
		}
	}
	return sym, true
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
	w := &writer{globals: map[string]Global{}, tagsDB: tagsDB, prefix: prefix, tasks: len(in.Tasks)}
	for _, g := range in.Globals {
		w.globals[strings.ToLower(g.Name)] = g
	}
	// The project's library (every program is given the same one) and
	// the programs' own TYPE blocks: the types the tags and the programs
	// use (#1299).
	var libs []string
	seenLib := map[string]bool{}
	var own []*st.Program
	for _, p := range in.Programs {
		for _, l := range p.Libs {
			if !seenLib[l] {
				seenLib[l] = true
				libs = append(libs, l)
			}
		}
		if src, _, err := toST(p); err == nil {
			if prog, err := st.Parse(src); err == nil {
				own = append(own, prog)
			}
		}
	}
	w.lib = loadLibrary(libs, own)
	out := Output{Roots: map[string]string{"": quote(tagsDB)}, names: map[string]map[string]*shape{"": {}}, Sources: map[string]Source{}}
	for _, g := range in.Globals {
		out.names[""][strings.ToLower(g.Name)] = w.shapeOf(g.Name, g.Type)
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

	var ready []parsed
	for _, p := range in.Programs {
		src, lines, err := toST(p)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		prog, err := st.Parse(src)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		if !strings.EqualFold(prog.TopKeyword, "PROGRAM") || prog.Name == "" {
			w.diag("siemens/program", p.Path, 1, "the source declares no PROGRAM")
			continue
		}
		ready = append(ready, parsed{path: p.Path, src: src, libs: p.Libs, lines: lines, prog: prog})
		// The blocks the program file declares beside its PROGRAM.
		w.lib.addPOUs(prog, p.Path, lines, false, false)
	}
	// With more than one task, what each task shares with the others
	// (tasks.go), before any program is written: a program's FB reads a
	// shared tag from its task's copy.
	if len(in.Tasks) > 1 {
		w.shares = w.sharing(in, ready)
	}

	progs := map[string]bool{}
	for _, p := range ready {
		prog, lines := p.prog, p.lines
		mark, wmark := len(w.diags), len(w.warnings)
		pw := w.program(p.path, p.libs, p.src, lines, prog)
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
		out.Sources[fbName+".scl"] = Source{Path: p.path, Body: body}
		out.Sources[dbName+".db"] = Source{Path: p.path}
		out.Roots[prog.Name] = quote(dbName)
	}

	// The project's blocks the programs reach, callees first.
	pouOrder := w.writePOUs(&out, claim)

	// The helper functions the programs call: the guarded divisions and
	// the conversions to STRING. Files come out with these first (the
	// sort below), so tiad, which generates the sources in the order
	// given, has each FC before an FB that calls it.
	fns := make([]string, 0, len(w.guarded))
	for fn := range w.guarded {
		fns = append(fns, fn)
	}
	sort.Strings(fns)
	for _, fn := range fns {
		name := prefix + fn
		src, what := guardedSource, "the guarded division function "
		if _, ok := stringHelpers[fn]; ok {
			src, what = stringHelperSource, "the STRING conversion function "
		}
		if !claim(name, what+fn) {
			continue
		}
		out.Files = append(out.Files, File{Path: name + ".scl", Data: src(prefix, fn)})
		out.Sources[name+".scl"] = Source{Path: w.guarded[fn]}
	}

	// nautilus writes a task's dt-tag every scan; the task's OB does it
	// here with RUNTIME() (dt.go), the start times kept in a clock DB.
	clock := prefix + "Clock"
	dts := map[string]*dtOf{}
	for _, t := range in.Tasks {
		if t.DtTag == "" {
			continue
		}
		d, why := w.dtFor(t, clock)
		if why != "" {
			w.diag("siemens/dt-tag", "nautilus.yaml", 0, fmt.Sprintf("task %s's dt-tag %s: %s", t.Name, t.DtTag, why))
			continue
		}
		dts[t.Name] = d
	}
	if len(dts) > 0 && claim(clock, "the clock DB of the tasks' dt-tags") {
		out.Files = append(out.Files, File{Path: clock + ".db", Data: clockDB(clock, in.Tasks, dts)})
		out.Sources[clock+".db"] = Source{Path: "nautilus.yaml"}
	}
	// One cyclic interrupt OB per task: OB30 to OB38, then from 123, the
	// numbers an S7-1500 gives cyclic interrupts.
	runBy := map[string]string{}
	for i, t := range in.Tasks {
		pk := strings.ToLower(t.Program)
		if other, twice := runBy[pk]; twice {
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s runs program %s, which task %s runs too", t.Name, t.Program, other))
			continue
		}
		runBy[pk] = t.Name
		if !progs[pk] {
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
		number := 30 + i
		if i > 8 {
			number = 123 + i - 9
		}
		ob := OB{Name: prefix + "OB_" + t.Name, Number: number, Cycle: t.Interval}
		if !claim(ob.Name, "the OB of task "+t.Name) {
			continue
		}
		sh := w.shares[pk]
		if sh != nil {
			if !claim(sh.db, "the scan DB of task "+t.Name) {
				continue
			}
			db, why := w.scanDB(sh)
			if why != "" {
				w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s: %s", t.Name, why))
				continue
			}
			out.Files = append(out.Files, File{Path: sh.db + ".db", Data: db})
			out.Sources[sh.db+".db"] = Source{Path: "nautilus.yaml"}
		}
		src, why := w.obSource(ob, prefix+t.Program+"_DB", sh, dts[t.Name])
		if why != "" {
			w.diag("siemens/task", "nautilus.yaml", 0, fmt.Sprintf("task %s: %s", t.Name, why))
			continue
		}
		out.OBs = append(out.OBs, ob)
		out.Files = append(out.Files, File{Path: ob.Name + ".scl", Data: src})
		out.Sources[ob.Name+".scl"] = Source{Path: "nautilus.yaml"}
	}

	if len(in.Globals) > 0 {
		db, ok := w.globalDB()
		if ok {
			out.Files = append(out.Files, File{Path: tagsDB + ".db", Data: db})
			out.Sources[tagsDB+".db"] = Source{Path: "nautilus.yaml"}
		}
	}

	// The PLC data types of the STRUCTs everything above named, last:
	// the tags DB, the scan DBs, the programs and the project's blocks
	// each reach some.
	typeOrder := w.writeTypes(&out, claim)

	if len(w.diags) > 0 {
		return Output{Warnings: w.warnings}, w.diags, nil
	}
	out.Warnings = w.warnings
	// tiad generates the sources in the order given, and TIA generates a
	// block only once what it calls exists: the data types first (each
	// after the types it holds), then the guarded division FCs, then the
	// project's blocks callees first, then the rest.
	rank := map[string]int{}
	for i, f := range typeOrder {
		rank[f] = i - len(typeOrder)
	}
	for _, fn := range fns {
		rank[prefix+fn+".scl"] = 1
	}
	for i, f := range pouOrder {
		rank[f] = 2 + i
	}
	last := 2 + len(pouOrder)
	rankOf := func(p string) int {
		if r, ok := rank[p]; ok {
			return r
		}
		return last
	}
	sort.SliceStable(out.Files, func(i, j int) bool {
		a, b := out.Files[i].Path, out.Files[j].Path
		if ra, rb := rankOf(a), rankOf(b); ra != rb {
			return ra < rb
		}
		return a < b
	})
	return out, nil, nil
}

type writer struct {
	globals map[string]Global
	lib     *library
	tagsDB  string // with the prefix
	prefix  string
	diags   []Diag
	// warnings: Output.Warnings.
	warnings []Diag
	// pairWarned: the pair-state warning went out (expr.go).
	pairWarned bool
	// guarded are the guarded division functions the programs call
	// (ops.go), each with the first program that called it.
	guarded map[string]string
	// shares is what each program (lower-cased) shares with the other
	// tasks (tasks.go).
	shares map[string]*share
	// tasks is how many tasks the project has (pous.go: a block of a
	// project with several names no manifest tag).
	tasks int
	// needed are the project's blocks the programs reach, each with where
	// it is first used, and queue those still to write (pous.go).
	needed map[string]site
	queue  []string
	// types are the STRUCTs the written SCL names (lower-cased), each
	// with the first nautilus file that names it (structs.go).
	types map[string]string
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

// globalDB writes the manifest's tags as one optimized global DB, retained
// tags in a RETAIN section.
func (w *writer) globalDB() ([]byte, bool) {
	names := make([]string, 0, len(w.globals))
	for k := range w.globals {
		names = append(names, k)
	}
	sort.Strings(names)
	// begin are a struct tag's start values from its init: map, as
	// assignments in the DB's BEGIN section (TIA keeps them: measured on
	// V21, #1301 D1b); the members it does not name start at the data
	// type's own start values.
	var plain, retained, begin []string
	ok := true
	// A type that cannot be written is reported once, at its first tag,
	// not once per tag of the type.
	refused := map[string]bool{}
	for _, k := range names {
		g := w.globals[k]
		if sd := w.structOf(g.Type); sd != nil {
			if g.Init != "" {
				w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: init %s is not a struct's start value", g.Name, g.Init))
				ok = false
				continue
			}
			tk := strings.ToLower(strings.TrimSpace(g.Type))
			if why := w.typeRefusal(g.Type, map[string]bool{}); why != "" {
				ok = false
				if !refused[tk] {
					refused[tk] = true
					w.diag("siemens/type", "nautilus.yaml", 0, fmt.Sprintf("tag %s: the type %s: %s", g.Name, strings.TrimSpace(g.Type), why))
				}
				continue
			}
			if g.Start != nil {
				if why := w.seedCheck(g.Type, g.Start); why != "" {
					w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: nautilus refuses the start value: %s", g.Name, why))
					ok = false
					continue
				}
				ls, why := w.overrides(g.Type, sclName(g.Name), g.Start)
				if why != "" {
					w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, why))
					ok = false
					continue
				}
				begin = append(begin, ls...)
			}
			line := fmt.Sprintf("      %s : %s;", sclName(g.Name), w.typeName(sd, "nautilus.yaml"))
			if g.Retain {
				retained = append(retained, line)
			} else {
				plain = append(plain, line)
			}
			continue
		}
		typ, why := w.sclType(g.Type)
		if why != "" {
			w.diag("siemens/type", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, why))
			ok = false
			continue
		}
		if g.Start != nil {
			w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, gapMessage(gapStructured, fmt.Sprintf("the start value %v of a %s", g.Start, g.Type), "")))
			ok = false
			continue
		}
		line := fmt.Sprintf("      %s : %s", sclName(g.Name), typ)
		if g.Init != "" {
			init, why := literal(g.Init)
			if w.enumOf(g.Type) != nil {
				init, why = w.enumInit(g.Type, g.Init)
			}
			if why != "" {
				w.diag("siemens/init", "nautilus.yaml", 0, fmt.Sprintf("tag %s: %s", g.Name, why))
				ok = false
				continue
			}
			line += " := " + init
		} else if n, isEnum := w.enumStart(g.Type); isEnum {
			line += " := " + n
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
	b.WriteString("\nBEGIN\n")
	for _, l := range begin {
		b.WriteString("   " + l + "\n")
	}
	b.WriteString("\nEND_DATA_BLOCK\n")
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
	return constValue(loadLibrary(nil, nil), a.Value)
}

// constValue prints a constant (a manifest init, a TYPE member's initial
// value) for a declaration, or says why it cannot: a negative number is
// folded to one literal (SCL's declarations take -5, not an expression),
// and whatever the expression printer refuses is refused here, never
// printed as an empty value.
func constValue(lib *library, e st.Expression) (string, string) {
	if u, ok := e.(*st.UnaryExpr); ok && u.Op == "-" {
		if n, ok := u.Operand.(*st.NumberLit); ok {
			return "-" + number(n), ""
		}
	}
	switch e.(type) {
	case *st.NumberLit, *st.BoolLit, *st.StringLit, *st.TimeLit, *st.TypedLit:
	default:
		return "", gapMessage(gapStructured, "an initial value written as an expression", "a start value is one literal in SCL")
	}
	scratch := &writer{globals: map[string]Global{}, lib: lib}
	pw := &progWriter{w: scratch, locals: map[string]local{}}
	s := pw.expr(e, 0)
	if len(scratch.diags) > 0 {
		return "", scratch.diags[0].Message
	}
	if s == "" {
		return "", fmt.Sprintf("the value %s has no SCL form here", exprShort(e))
	}
	return s, ""
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

// toST returns a program's source as ST, with lines[i] the original line
// of ST line i+1 (nil when the source is ST already). A chart is the ST
// state machine nautilus itself runs (sfc.TranspileWithLines, as the
// runtime's program loader), so the CPU runs the same per-scan evolution:
// step flags, Step.T and the timed qualifiers are plain statics and TONs
// of that ST (#1302).
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
	case ".sfc":
		return sfc.TranspileWithLines(p.Source)
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
