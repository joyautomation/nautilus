package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/lang/st"
)

// runCheck compiles every .st file under the given paths (files or
// directories; default ".") and prints gcc-style diagnostics:
//
//	path/to/program.st:12:5: undeclared identifier "y" ...
//
// Exit code 0 = clean, 1 = diagnostics found, 2 = usage/IO error.
func runCheck(args []string) int {
	paths := args
	if len(paths) == 0 {
		paths = []string{"."}
	}

	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus check:", err)
			return 2
		}
		if !info.IsDir() {
			files = append(files, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Skip the usual dependency/VCS trees.
				switch d.Name() {
				case ".git", "node_modules", "vendor":
					return filepath.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".st") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus check:", err)
			return 2
		}
	}

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "nautilus check: no .st files found")
		return 0
	}

	bad := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus check:", err)
			return 2
		}
		if msg, pos, failed := compileErr(string(src)); failed {
			bad++
			fmt.Printf("%s:%d:%d: %s\n", f, pos.Line, pos.Col, msg)
		}
	}

	fmt.Printf("nautilus check: %d file(s), %d with errors\n", len(files), bad)
	if bad > 0 {
		return 1
	}
	return 0
}

// compileErr runs the same parse+lower pipeline as the LSP and returns the
// first diagnostic. Positions default to 1:1 when the compiler couldn't
// attach one (e.g. some parse errors).
func compileErr(src string) (string, st.Pos, bool) {
	prog, err := st.Parse(src)
	if err != nil {
		// Anchor on the parser-reported position (shared with the LSP via
		// st.ParseErrorPos) instead of always 1:1.
		pos := st.Pos{Line: 1, Col: 1}
		if p, ok := st.ParseErrorPos(err); ok {
			pos = p
		}
		return err.Error(), pos, true
	}
	if _, err := st.Lower(prog); err != nil {
		pos := st.Pos{Line: 1, Col: 1}
		if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
			// Print the unwrapped message: the position prefix that
			// LowerError.Error() adds is already in the path:line:col.
			return le.Err.Error(), le.Pos, true
		}
		return err.Error(), pos, true
	}
	return "", st.Pos{}, false
}
