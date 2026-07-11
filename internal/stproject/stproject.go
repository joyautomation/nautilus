// Package stproject defines what "the project" means when compiling one
// Structured Text file: every other .st file in the same directory that is a
// pure library — TYPE / FUNCTION_BLOCK / FUNCTION declarations with no
// PROGRAM — is in scope. This mirrors how a runtime composes sources (e.g. a
// generated eip_types.st concatenated with program.st), so the LSP and
// `nautilus check` agree with what actually runs.
package stproject

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

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
