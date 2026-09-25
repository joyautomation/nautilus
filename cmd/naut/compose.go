package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/runtime"
)

const composeUsage = `naut compose — print the source the controller runs for a program

Composes a program exactly as the runtime does (and as "Download Program to
Controller" sends it): the project's library prelude — every PROGRAM-less
.st file in the root and under lib/, verbatim and in path order, then every
PROGRAM-less .ld/.fbd file, transpiled to ST, in path order — followed by
the program file as written. A graphical program (.ld/.fbd/.sfc) stays in
its own language: the controller transpiles it on the way in and reports
the original, which is what diff and pull compare against.

A library that fails to transpile, or a PROGRAM under lib/, is an error
(exit 1), reported as naut check reports it.

Usage:
  naut compose [--json] [--overrides <file|->] <program file | library file | project dir>

  <program file>   print its composed source
  <project dir>    print the one program's composed source (an error when
                   the project has several; name one)

Flags:
  --json        Print {root, prelude, libraries, programs[]} instead; when a
                program file is named, also {file, pou, language, program,
                source} for it. libraries are project-relative paths in
                prelude order. A library file or a project directory is
                accepted too (no program fields then).
  --overrides   A JSON object mapping file paths (project-relative with
                slashes, or absolute) to content that wins over the file on
                disk (unsaved editor buffers); "-" reads it from stdin.
                Absolute paths outside the project are ignored.
`

// composeProgram is one program file in the JSON output.
type composeProgram struct {
	File     string `json:"file"`     // project-relative (a root file)
	POU      string `json:"pou"`      // PROGRAM <Name>, "" if unnamed
	Language string `json:"language"` // st | fbd | ld | sfc
	Program  string `json:"program"`  // the file's source, as written
}

// composeResult is `naut compose --json`'s output.
type composeResult struct {
	Root      string           `json:"root"`
	Prelude   string           `json:"prelude"`
	Libraries []string         `json:"libraries"`
	Programs  []composeProgram `json:"programs"`

	// Set when a program file was named.
	File     string `json:"file,omitempty"`
	POU      string `json:"pou,omitempty"`
	Language string `json:"language,omitempty"`
	Program  string `json:"program,omitempty"`
	Source   string `json:"source,omitempty"` // prelude + program: what a download sends
}

func runCompose(args []string) int {
	return composeTo(args, os.Stdin, os.Stdout, os.Stderr)
}

func composeTo(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("compose", flag.ContinueOnError)
	fset.SetOutput(stderr)
	asJSON := fset.Bool("json", false, "print JSON")
	overridesPath := fset.String("overrides", "", "JSON {path: content} of unsaved buffers; - for stdin")
	fset.Usage = func() { fmt.Fprint(stderr, composeUsage) }
	// Flags may follow the path too (naut compose main.st --json).
	var pos []string
	for rest := args; ; {
		if err := fset.Parse(rest); err != nil {
			return 2
		}
		if fset.NArg() == 0 {
			break
		}
		pos = append(pos, fset.Arg(0))
		rest = fset.Args()[1:]
	}
	if len(pos) != 1 {
		fmt.Fprint(stderr, composeUsage)
		return 2
	}

	var override map[string]string
	if *overridesPath != "" {
		var raw []byte
		var err error
		if *overridesPath == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(*overridesPath)
		}
		if err == nil {
			err = json.Unmarshal(raw, &override)
		}
		if err != nil {
			fmt.Fprintln(stderr, "naut compose: --overrides:", err)
			return 2
		}
	}

	target, err := filepath.Abs(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "naut compose:", err)
		return 2
	}
	info, err := os.Stat(target)
	if err != nil {
		fmt.Fprintln(stderr, "naut compose:", err)
		return 2
	}
	root, named := target, ""
	if !info.IsDir() {
		root = stproject.ProjectRoot(target)
		rel, err := filepath.Rel(root, target)
		if err != nil {
			fmt.Fprintln(stderr, "naut compose:", err)
			return 2
		}
		named = filepath.ToSlash(rel)
	}

	override = relOverrides(root, override)
	libs, err := stproject.Libraries(os.DirFS(root), override)
	if err != nil {
		fmt.Fprintln(stderr, "naut compose:", err)
		return 1
	}
	progs, err := stproject.Programs(root, override)
	if err != nil {
		fmt.Fprintln(stderr, "naut compose:", err)
		return 2
	}

	res := composeResult{Root: root, Libraries: []string{}, Programs: []composeProgram{}}
	var sts []string
	for _, l := range libs {
		res.Libraries = append(res.Libraries, l.Path)
		sts = append(sts, l.ST)
	}
	res.Prelude = stproject.Join(sts, "")
	var program *composeProgram
	for _, p := range progs {
		res.Programs = append(res.Programs, composeProgram{
			File: p.File, POU: p.POU, Language: runtime.Language(p.Body), Program: p.Body,
		})
		if p.File == named {
			program = &res.Programs[len(res.Programs)-1]
		}
	}
	if named == "" && len(res.Programs) == 1 && !*asJSON {
		program = &res.Programs[0]
	}
	if program != nil {
		res.File, res.POU, res.Language, res.Program = program.File, program.POU, program.Language, program.Program
		res.Source = stproject.Join(sts, program.Program)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(stderr, "naut compose:", err)
			return 2
		}
		return 0
	}
	if program == nil {
		switch {
		case named != "":
			fmt.Fprintf(stderr, "naut compose: %s declares no PROGRAM (it is a library) — name a program file\n", named)
		case len(res.Programs) == 0:
			fmt.Fprintf(stderr, "naut compose: no file with a PROGRAM in %s\n", root)
		default:
			var files []string
			for _, p := range res.Programs {
				files = append(files, p.File)
			}
			fmt.Fprintf(stderr, "naut compose: %s has several programs (%s) — name one\n", root, strings.Join(files, ", "))
		}
		return 2
	}
	fmt.Fprint(stdout, res.Source)
	return 0
}

// relOverrides re-keys --overrides by project-relative slash path. A key may
// be absolute (an editor knows its buffers' paths, not which project root
// the CLI will resolve); one outside root is dropped.
func relOverrides(root string, in map[string]string) map[string]string {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if filepath.IsAbs(k) {
			rel, err := filepath.Rel(root, k)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			k = rel
		}
		out[filepath.ToSlash(filepath.Clean(k))] = v
	}
	return out
}
