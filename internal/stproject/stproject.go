// Package stproject defines what "the project" means when compiling one
// Structured Text file: every other .st file in the same directory that is a
// pure library — TYPE / FUNCTION_BLOCK / FUNCTION declarations with no
// PROGRAM — is in scope. This mirrors how a runtime composes sources (e.g. a
// generated eip_types.st concatenated with program.st), so the LSP and
// `nautilus check` agree with what actually runs.
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
	"sort"
	"strings"

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

// Compose reads a project directory and decomposes it: the .st files with no
// PROGRAM (sorted by name) are libraries, the single .st file with a PROGRAM
// is the program. override maps a base file name to in-editor content so
// unsaved buffers win over disk. Errors when there isn't exactly one program
// file — the pull target must be unambiguous.
func Compose(dir string, override map[string]string) (Composition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Composition{}, err
	}
	type stFile struct {
		name string
		src  string
	}
	var files []stFile
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".st") {
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

	var libs []string
	var programFile, programBody string
	programs := 0
	for _, f := range files {
		if hasProgram(f.src) {
			programs++
			programFile, programBody = f.name, f.src
			continue
		}
		if IsLibrary(f.src) {
			libs = append(libs, f.src)
		}
	}
	if programs == 0 {
		return Composition{}, fmt.Errorf("no .st file with a PROGRAM in %s", dir)
	}
	if programs > 1 {
		return Composition{}, fmt.Errorf("multiple PROGRAM files in %s — pull needs exactly one", dir)
	}
	return Composition{
		Composed:    Join(libs, programBody),
		Prelude:     Join(libs, ""),
		ProgramFile: programFile,
		ProgramBody: programBody,
		Libraries:   libs,
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
// and surfaces its own diagnostics when opened or checked itself.
func Prelude(file string, override map[string]string) (string, int) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", 0
	}
	dir := filepath.Dir(abs)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".st") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p == abs {
			continue
		}
		names = append(names, p)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, p := range names {
		src, ok := override[p]
		if !ok {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			src = string(raw)
		}
		if !IsLibrary(src) {
			continue
		}
		b.WriteString(src)
		if !strings.HasSuffix(src, "\n") {
			b.WriteByte('\n')
		}
	}
	prelude := b.String()
	return prelude, strings.Count(prelude, "\n")
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
