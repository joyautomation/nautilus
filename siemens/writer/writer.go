// Package writer turns a nautilus project into SCL sources TIA Portal can
// import and compile (docs/design/siemens-ide.md §2): each program becomes
// a FUNCTION_BLOCK with an instance DB, each task an organization block
// that calls its program, and the manifest's tags one global DB. tiad
// imports the files, generates the blocks and compiles them.
//
// This is the first subset. What it does not write yet it refuses with a
// rule-tagged diagnostic, never a guess: user FUNCTIONs and
// FUNCTION_BLOCKs, UDTs and enums, STRING without a length, SR/RS, the
// overloaded TO_* conversions, integer division by a non-constant
// divisor (nautilus gives 0 and counts it; the CPU's answer is not yet
// pinned down), and every builtin outside the table in expr.go.
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
	// signatures a ladder file needs in scope.
	Libs []string
}

// Global is one manifest tag, which becomes a member of the global DB.
type Global struct {
	Name   string
	Type   string // IEC type name, e.g. REAL, BOOL, INT
	Init   string // ST literal text, or ""
	Retain bool
}

// Task schedules one program. Interval 0 means continuous (OB1).
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
}

// File is one generated source, named as tiad imports it.
type File struct {
	Path string // e.g. "Main.scl", "Main_DB.db", "Tags.db", "OB_main.scl"
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
	// its instance DB (`"Main_DB"`), and "" to the global DB (`"Tags"`).
	// Symbol uses it to turn a nautilus tag path into a Web API symbol.
	Roots map[string]string
}

// Symbol maps a nautilus tag path to the Web API symbol of the same value:
// a manifest tag "Level" to `"Tags".Level`, a program variable
// "Main.count" to `"Main_DB".count`.
func (o Output) Symbol(path string) (string, bool) {
	if head, rest, ok := strings.Cut(path, "."); ok {
		if root, ok := o.lookupRoot(head); ok {
			return root + "." + rest, true
		}
	}
	if root, ok := o.Roots[""]; ok {
		return root + "." + path, true
	}
	return "", false
}

func (o Output) lookupRoot(name string) (string, bool) {
	for k, v := range o.Roots {
		if k != "" && strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
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
	w := &writer{globals: map[string]Global{}, tagsDB: tagsDB}
	for _, g := range in.Globals {
		w.globals[strings.ToLower(g.Name)] = g
	}
	out := Output{Roots: map[string]string{"": quote(tagsDB)}}

	progs := map[string]bool{}
	for _, p := range in.Programs {
		src, lines, err := toST(p)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		prog, err := st.Parse(src)
		if err != nil {
			return Output{}, nil, fmt.Errorf("%s: %w", p.Path, err)
		}
		mark := len(w.diags)
		if !strings.EqualFold(prog.TopKeyword, "PROGRAM") || prog.Name == "" {
			w.diag("siemens/program", p.Path, 1, "the source declares no PROGRAM")
			continue
		}
		pw := w.program(p.Path, prog)
		for i := mark; i < len(w.diags); i++ {
			w.diags[i].Line = mapLine(lines, w.diags[i].Line)
		}
		if pw == nil {
			continue
		}
		progs[strings.ToLower(prog.Name)] = true
		out.Files = append(out.Files,
			File{Path: prog.Name + ".scl", Data: pw.fb},
			File{Path: prog.Name + "_DB.db", Data: instanceDB(prog.Name)})
		out.Roots[prog.Name] = quote(prog.Name + "_DB")
	}

	nextOB := 30
	for _, t := range in.Tasks {
		if !progs[strings.ToLower(t.Program)] {
			w.diag("siemens/task", "", 0, fmt.Sprintf("task %s runs program %s, which was not written", t.Name, t.Program))
			continue
		}
		ob := OB{Name: "OB_" + t.Name}
		if t.Interval == 0 {
			ob.Number = 1
		} else {
			if nextOB > 38 {
				w.diag("siemens/task", "", 0, fmt.Sprintf("task %s: more than nine timed tasks (cyclic interrupt OB30-OB38)", t.Name))
				continue
			}
			if t.Interval < time.Millisecond || t.Interval%time.Millisecond != 0 {
				w.diag("siemens/task", "", 0, fmt.Sprintf("task %s: interval %s is not a whole number of milliseconds of at least 1 ms", t.Name, t.Interval))
				continue
			}
			ob.Number, ob.Cycle = nextOB, t.Interval
			nextOB++
		}
		out.OBs = append(out.OBs, ob)
		out.Files = append(out.Files, File{Path: ob.Name + ".scl", Data: obSource(ob, t.Program)})
	}

	if len(in.Globals) > 0 {
		db, ok := w.globalDB()
		if ok {
			out.Files = append(out.Files, File{Path: tagsDB + ".db", Data: db})
		}
	}

	if len(w.diags) > 0 {
		return Output{}, w.diags, nil
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].Path < out.Files[j].Path })
	return out, nil, nil
}

type writer struct {
	globals map[string]Global
	tagsDB  string
	diags   []Diag
}

func (w *writer) diag(rule, path string, line int, msg string) {
	w.diags = append(w.diags, Diag{Rule: rule, Path: path, Line: line, Message: msg})
}

func quote(name string) string { return `"` + name + `"` }

func instanceDB(prog string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "DATA_BLOCK %s\n", quote(prog+"_DB"))
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	// No NON_RETAIN here: which members are retained is the FB's own
	// declaration (VAR RETAIN), and a block-level NON_RETAIN would
	// override it.
	fmt.Fprintf(&b, "%s\n", quote(prog))
	b.WriteString("\nBEGIN\n\nEND_DATA_BLOCK\n")
	return []byte(b.String())
}

func obSource(ob OB, prog string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "ORGANIZATION_BLOCK %s\n", quote(ob.Name))
	if ob.Number == 1 {
		b.WriteString("TITLE = \"Program cycle\"\n")
	} else {
		fmt.Fprintf(&b, "TITLE = \"Cyclic interrupt, %s\"\n", ob.Cycle)
	}
	b.WriteString("{ S7_Optimized_Access := 'TRUE' }\n")
	b.WriteString("VERSION : 0.1\n")
	b.WriteString("\nBEGIN\n")
	fmt.Fprintf(&b, "\t%s();\n", quote(prog+"_DB"))
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

func mapLine(lines []int, l int) int {
	if lines == nil || l < 1 || l > len(lines) {
		return l
	}
	return lines[l-1]
}
