package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
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
			if ext := strings.ToLower(filepath.Ext(path)); ext == ".st" || ext == ".fbd" {
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
		fmt.Fprintln(os.Stderr, "nautilus check: no .st or .fbd files found")
		return 0
	}

	bad := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus check:", err)
			return 2
		}
		source := string(src)
		// FBD compiles by transpiling to ST, then it's checked exactly like an
		// .st file (positions are approximate against the transpiled form for
		// now).
		if strings.EqualFold(filepath.Ext(f), ".fbd") {
			stSrc, terr := fbd.Transpile(source)
			if terr != nil {
				bad++
				fmt.Printf("%s: %s\n", f, terr.Error())
				continue
			}
			source = stSrc
		}
		// Sibling library files (TYPE/FB/FUNCTION-only .st in the same
		// directory) are in scope, exactly as the LSP and a runtime that
		// concatenates sources see it.
		prelude, preludeLines := stproject.Prelude(f, nil)
		if msg, pos, failed := compileErr(source, prelude, preludeLines); failed {
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
// attach one (e.g. some parse errors). The prelude participates in lowering
// only; positions are remapped back into the checked file.
func compileErr(src, prelude string, preludeLines int) (string, st.Pos, bool) {
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
	lowerProg := prog
	if prelude != "" {
		if combined, cerr := st.Parse(prelude + src); cerr == nil {
			lowerProg = combined
		} else {
			preludeLines = 0
		}
	} else {
		preludeLines = 0
	}
	if _, err := st.Lower(lowerProg); err != nil {
		pos := st.Pos{Line: 1, Col: 1}
		msg := err.Error()
		if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
			// Print the unwrapped message: the position prefix that
			// LowerError.Error() adds is already in the path:line:col.
			pos, msg = le.Pos, le.Err.Error()
		}
		if pos.Line > preludeLines {
			pos.Line -= preludeLines
		} else if preludeLines > 0 {
			pos = st.Pos{Line: 1, Col: 1}
			msg = "in project library files: " + msg
		}
		return msg, pos, true
	}
	return "", st.Pos{}, false
}
