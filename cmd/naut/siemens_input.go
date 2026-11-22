package main

// The Siemens writer's input, read from a manifest project. The context
// map keeps siemens/writer away from internal/project, so the CLI hands
// the writer what it needs (docs/design/siemens-ide.md §6). `naut check
// --target siemens` and the deploy use this one function, so what check
// passes is what a deploy writes.

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/runtime"
	swriter "github.com/joyautomation/nautilus/siemens/writer"
)

// siemensDefaultScan is the scan runtime.New gives a task with none.
const siemensDefaultScan = 100 * time.Millisecond

// siemensWriterInput reads the project in dir into the writer's Input:
//
//   - each task's program file, with the library sources a ladder needs;
//   - one Task per manifest task, named as the runtime names it (the
//     first is always runtime.MainTaskName) and scanned at its scan:, or
//     the runtime's default when it has none;
//   - the manifest's tags, typed by the runtime's rule (type:, else what
//     init: implies; a tag with neither is left out, since the compiler
//     already refuses a program that names it), retained exactly where
//     the runtime keeps them: with a retain: section, every setpoint and
//     every tag marked retain: true; without one, none.
//
// Program paths are dir joined with the manifest's path, so a diagnostic
// names the file the way check names it.
func siemensWriterInput(dir, manifestName string) (swriter.Input, error) {
	m, err := project.ReadManifest(os.DirFS(dir), manifestName)
	if err != nil {
		return swriter.Input{}, err
	}
	var in swriter.Input

	defs, err := project.TagDefs(m)
	if err != nil {
		return swriter.Input{}, err
	}
	scope, _ := runtime.ResolveTagScope(defs, nil)
	for _, d := range defs {
		typ := d.Type
		if typ == "" {
			if t := scope.Implicit[d.Name]; t != nil {
				typ = t.String()
			}
		}
		if typ == "" {
			continue
		}
		g := swriter.Global{
			Name:   d.Name,
			Type:   typ,
			Retain: m.Retain != nil && (d.Retain || d.Role == runtime.RoleSetpoint),
		}
		if d.Init != nil {
			lit, err := siemensInitLiteral(d.Init, typ)
			if err != nil {
				return swriter.Input{}, fmt.Errorf("tag %s: %w", d.Name, err)
			}
			g.Init = lit
		}
		in.Globals = append(in.Globals, g)
	}

	if len(m.Tasks) == 0 {
		return swriter.Input{}, errors.New("the project declares no task")
	}
	written := map[string]string{} // program file → PROGRAM name
	for i, t := range m.Tasks {
		if t.Program == "" {
			return swriter.Input{}, fmt.Errorf("task %d names no program file", i+1)
		}
		file := filepath.Join(dir, filepath.FromSlash(path.Clean(t.Program)))
		name, ok := written[file]
		if !ok {
			src, err := os.ReadFile(file)
			if err != nil {
				return swriter.Input{}, err
			}
			_, libs, _ := stproject.PreludeSources(file, nil)
			p := swriter.Program{Path: file, Source: string(src), Libs: libs}
			if name, err = swriter.ProgramName(p); err != nil {
				return swriter.Input{}, err
			}
			written[file] = name
			in.Programs = append(in.Programs, p)
		}
		task := swriter.Task{Name: t.Name, Program: name, Interval: time.Duration(t.Scan)}
		if i == 0 {
			task.Name = runtime.MainTaskName
		} else if task.Name == "" {
			task.Name = strings.TrimSuffix(path.Base(t.Program), path.Ext(t.Program))
		}
		if task.Interval <= 0 {
			task.Interval = siemensDefaultScan
		}
		in.Tasks = append(in.Tasks, task)
	}
	return in, nil
}

// siemensInitLiteral prints a manifest init: as the ST literal the writer
// parses. The loader hands every number over as a float64
// (project.TagDefs), so the tag's type decides the spelling: a whole
// number is an integer literal for an integer type and keeps a decimal
// point for REAL and LREAL, and a fraction on an integer type is refused.
func siemensInitLiteral(v any, typ string) (string, error) {
	u := strings.ToUpper(typ)
	real := u == "REAL" || u == "LREAL"
	integer := false
	switch u {
	case "SINT", "INT", "DINT", "LINT", "USINT", "UINT", "UDINT", "ULINT", "BYTE", "WORD", "DWORD", "LWORD":
		integer = true
	}
	switch x := v.(type) {
	case bool:
		if x {
			return "TRUE", nil
		}
		return "FALSE", nil
	case int:
		return siemensNumberLiteral(float64(x), integer, real)
	case int64:
		return siemensNumberLiteral(float64(x), integer, real)
	case float64:
		return siemensNumberLiteral(x, integer, real)
	case string:
		t := strings.ToUpper(strings.TrimSpace(x))
		if strings.HasPrefix(t, "T#") || strings.HasPrefix(t, "TIME#") {
			return strings.TrimSpace(x), nil
		}
		return "'" + strings.ReplaceAll(x, "'", "$'") + "'", nil
	}
	return "", fmt.Errorf("init %v is not a value the Siemens target can seed", v)
}

func siemensNumberLiteral(x float64, integer, real bool) (string, error) {
	whole := x == math.Trunc(x) && math.Abs(x) < 1<<53
	if integer {
		if !whole {
			return "", fmt.Errorf("init %v is not a whole number", x)
		}
		return strconv.FormatInt(int64(x), 10), nil
	}
	s := strconv.FormatFloat(x, 'g', -1, 64)
	if real && !strings.ContainsAny(s, ".eEn") { // n: NaN and Inf stay as they are, for the writer to refuse
		s += ".0"
	}
	return s, nil
}
