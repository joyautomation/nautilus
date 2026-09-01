// Package stproject defines what "the project" means when compiling one
// Structured Text file: every other file in the same directory that is a
// pure library — TYPE / FUNCTION_BLOCK / FUNCTION declarations with no
// PROGRAM — is in scope. This mirrors how a runtime composes sources (e.g. a
// generated eip_types.st concatenated with program.st), so the LSP and
// `nautilus check` agree with what actually runs.
//
// A library need not be Structured Text. A `.ld` or `.fbd` file with no
// PROGRAM is a library too: its FUNCTION_BLOCKs are ladder (or netlist)
// SUBROUTINES, transpiled to ST on the way into the prelude so everything
// downstream sees ordinary blocks. That is what lets a program call a
// ladder-written block — the IEC answer to a JSR.
//
// COMPOSITION ORDER, which matters because declarations may reference one
// another across files: every `.st` library first, in file-name order, then
// every transpiled `.ld`/`.fbd` library, in file-name order. ST first
// because it is where a project's TYPE declarations live and a graphical
// block's pin may name a UDT; within a tier, name order so the composition
// is deterministic and a rebuild diffs clean. Blocks may reference each
// other in EITHER direction regardless of order — the ST front-end resolves
// every FUNCTION_BLOCK signature in the composed source before it lowers
// any body — so the order only decides what a duplicate-name collision
// reports, never whether a call resolves.
//
// Join is the single definition of that composition. The runtime (main.go),
// the LSP prelude, the VS Code online-edit download, and `nautilus pull` all
// route through it, so a program can be composed to send and split back apart
// losslessly — the round-trip guarantee `nautilus pull` relies on.
package stproject

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
)

// Join composes a project's canonical source: each library ahead of the
// program body, every library ensured to end in a newline so declarations
// never run together. Passing an empty program yields just the prelude —
// exactly the prefix SplitProgram strips.
func Join(libraries []string, program string) string {
	var b strings.Builder
	for _, lib := range libraries {
		b.WriteString(lib)
		if !strings.HasSuffix(lib, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString(program)
	return b.String()
}

// SplitProgram recovers the program body from composed source given the
// prelude that Join placed ahead of it — the inverse of Join for a known
// prelude. It tolerates a trailing-newline difference at the seam (a hand
// composition that added one); anything more divergent means the composed
// source's libraries don't match this project, and ok is false.
func SplitProgram(composed, prelude string) (program string, ok bool) {
	if strings.HasPrefix(composed, prelude) {
		return composed[len(prelude):], true
	}
	trimmed := strings.TrimRight(prelude, "\n")
	if trimmed != prelude && strings.HasPrefix(composed, trimmed) {
		rest := composed[len(trimmed):]
		return strings.TrimPrefix(rest, "\n"), true
	}
	return "", false
}

// Composition is a project decomposed the way the runtime composes it.
type Composition struct {
	Composed    string // Join(Libraries, ProgramBody)
	Prelude     string // Join(Libraries, "") — the SplitProgram prefix
	ProgramFile string // base name of the .st file holding the PROGRAM
	ProgramBody string // that file's source
	Libraries   []string
}

var pouRe = regexp.MustCompile(`(?im)^\s*PROGRAM\s+([A-Za-z_][A-Za-z0-9_]*)`)

// POU extracts the `PROGRAM <Name>` POU name from IEC source, "" if none.
// On a multi-task controller the POU name is a program's identity — pull
// and online edits match programs to files by it.
func POU(src string) string {
	if m := pouRe.FindStringSubmatch(src); m != nil {
		return m[1]
	}
	return ""
}

// ProgramFile is one program file in a (possibly multi-program) project.
type ProgramFile struct {
	File string // base name
	Body string // file source
	POU  string // `PROGRAM <Name>`
}

// MultiComposition is a project decomposed task-style: shared libraries
// plus every program file, each identified by POU name — the workspace
// shape of a multi-task controller (one program file per task).
type MultiComposition struct {
	Prelude   string // Join(Libraries, "") — the SplitProgram prefix
	Libraries []string
	Programs  []ProgramFile // sorted by file name
}

// ComposeAll reads a project directory and decomposes it: the .st files
// with no PROGRAM (sorted by name) are libraries shared by every program;
// each file with a PROGRAM — .st, .fbd, .ld, or .sfc — is one program.
// override maps a base file name to in-editor content so unsaved buffers
// win over disk.
func ComposeAll(dir string, override map[string]string) (MultiComposition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return MultiComposition{}, err
	}
	type stFile struct {
		name string
		src  string
	}
	var files []stFile
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".st" && ext != ".fbd" && ext != ".ld" && ext != ".sfc") {
			continue
		}
		src, ok := override[e.Name()]
		if !ok {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			src = string(raw)
		}
		files = append(files, stFile{name: e.Name(), src: src})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	var m MultiComposition
	var graphical []stFile // .ld / .fbd libraries, transpiled below
	var raw []string       // every library's ORIGINAL text, for FB signatures
	for _, f := range files {
		if hasProgram(f.src) {
			m.Programs = append(m.Programs, ProgramFile{File: f.name, Body: f.src, POU: POU(f.src)})
			continue
		}
		if strings.EqualFold(filepath.Ext(f.name), ".st") {
			if IsLibrary(f.src) {
				m.Libraries = append(m.Libraries, f.src)
				raw = append(raw, f.src)
			}
			continue
		}
		// A PROGRAM-less .ld/.fbd is a library of graphical blocks. (A
		// .sfc is a chart, never a library: it has no declaration form.)
		if IsGraphicalLibrary(f.name) {
			graphical = append(graphical, f)
			raw = append(raw, f.src)
		}
	}
	// ST libraries lead; the graphical ones follow, transpiled. A file that
	// won't transpile is skipped here and reports its own error when it is
	// opened or checked — the same posture a broken .st library gets.
	for _, f := range graphical {
		stSrc, err := LibraryST(f.name, f.src, raw...)
		if err != nil {
			continue
		}
		m.Libraries = append(m.Libraries, stSrc)
	}
	m.Prelude = Join(m.Libraries, "")
	return m, nil
}

// LibraryST is the ST text a project library file contributes to the
// prelude: an .st library verbatim, an .ld or .fbd library transpiled
// through its own front-end. libs are the project's other library sources
// (in their ORIGINAL languages), which a ladder file needs to place a rung's
// power on a block defined in another file.
//
// One definition, because both sides of `nautilus pull` must produce the
// same bytes: the workspace composes with ComposeAll and the controller with
// internal/project's libraries(), and a round-trip that disagreed by one
// character would report every program as dirty.
func LibraryST(name, src string, libs ...string) (string, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ld":
		fbdSrc, err := ld.Transpile(src, libs...)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		stSrc, err := fbd.Transpile(fbdSrc)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return stSrc, nil
	case ".fbd":
		stSrc, err := fbd.Transpile(src)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return stSrc, nil
	}
	return src, nil
}

// IsGraphicalLibrary reports whether a file name is one of the graphical
// languages that can supply a project library.
func IsGraphicalLibrary(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".ld" || ext == ".fbd"
}

// Compose reads a project directory and decomposes it: the .st files with no
// PROGRAM (sorted by name) are libraries, and the single file with a PROGRAM
// — .st or .fbd (the runtime accepts both) — is the program. Errors when
// there isn't exactly one program file — the single-program target must be
// unambiguous (multi-program projects use ComposeAll).
func Compose(dir string, override map[string]string) (Composition, error) {
	m, err := ComposeAll(dir, override)
	if err != nil {
		return Composition{}, err
	}
	if len(m.Programs) == 0 {
		return Composition{}, fmt.Errorf("no .st or .fbd file with a PROGRAM in %s", dir)
	}
	if len(m.Programs) > 1 {
		return Composition{}, fmt.Errorf("multiple PROGRAM files in %s — pull needs exactly one", dir)
	}
	p := m.Programs[0]
	return Composition{
		Composed:    Join(m.Libraries, p.Body),
		Prelude:     m.Prelude,
		ProgramFile: p.File,
		ProgramBody: p.Body,
		Libraries:   m.Libraries,
	}, nil
}

// hasProgram reports whether src declares a PROGRAM (parses to a PROGRAM top,
// with a text fallback so a program that doesn't parse mid-edit is still
// recognized as the program file rather than skipped).
func hasProgram(src string) bool {
	if prog, err := st.Parse(src); err == nil {
		return prog.TopKeyword == "PROGRAM"
	}
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "PROGRAM ") {
			return true
		}
	}
	return false
}

// Prelude gathers the library sources that should precede file when
// compiling it. override maps absolute paths to in-editor content (unsaved
// buffers win over disk). Returns the concatenated prelude (empty when the
// file has no library siblings) and its line count for diagnostic remapping.
//
// A sibling qualifies when it parses cleanly and contains no PROGRAM and no
// top-level statements; anything else — programs, broken files — is skipped
// and surfaces its own diagnostics when opened or checked itself. `.ld` and
// `.fbd` siblings with no PROGRAM join too, transpiled, in the tier order
// the package doc states: every .st first, then the graphical ones.
func Prelude(file string, override map[string]string) (string, int) {
	prelude, _, n := PreludeSources(file, override)
	return prelude, n
}

// PreludeSources is Prelude plus the library sources in their ORIGINAL
// languages — what a ladder file needs to resolve a block defined in a
// sibling, before that sibling has been transpiled.
func PreludeSources(file string, override map[string]string) (prelude string, sources []string, lines int) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", nil, 0
	}
	dir := filepath.Dir(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, 0
	}
	var stNames, gNames []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() || p == abs {
			continue
		}
		switch {
		case strings.EqualFold(filepath.Ext(e.Name()), ".st"):
			stNames = append(stNames, p)
		case IsGraphicalLibrary(e.Name()):
			gNames = append(gNames, p)
		}
	}
	sort.Strings(stNames)
	sort.Strings(gNames)

	read := func(p string) (string, bool) {
		if src, ok := override[p]; ok {
			return src, true
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return "", false
		}
		return string(raw), true
	}

	// Pass one: gather the library sources as written, so a ladder library
	// can resolve blocks the others define (in either direction).
	type libFile struct{ path, src string }
	var stLibs, gLibs []libFile
	for _, p := range stNames {
		src, ok := read(p)
		if !ok || !IsLibrary(src) {
			continue
		}
		stLibs = append(stLibs, libFile{p, src})
		sources = append(sources, src)
	}
	for _, p := range gNames {
		src, ok := read(p)
		if !ok || hasProgram(src) {
			continue
		}
		gLibs = append(gLibs, libFile{p, src})
		sources = append(sources, src)
	}

	var b strings.Builder
	write := func(src string) {
		b.WriteString(src)
		if !strings.HasSuffix(src, "\n") {
			b.WriteByte('\n')
		}
	}
	for _, l := range stLibs {
		write(l.src)
	}
	for _, l := range gLibs {
		stSrc, err := LibraryST(l.path, l.src, sources...)
		if err != nil {
			continue
		}
		write(stSrc)
	}
	prelude = b.String()
	return prelude, sources, strings.Count(prelude, "\n")
}

// IsLibrary reports whether src is a declarations-only ST source: it parses
// and has no PROGRAM and no top-level statements.
func IsLibrary(src string) bool {
	prog, err := st.Parse(src)
	if err != nil {
		return false
	}
	return prog.TopKeyword != "PROGRAM" && len(prog.Statements) == 0
}
