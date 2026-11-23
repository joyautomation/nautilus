package main

// The Siemens writer's input, read from a manifest project. The context
// map keeps siemens/writer away from internal/project, so the CLI hands
// the writer what it needs (docs/design/siemens-ide.md §6). `naut check
// --target siemens` and the deploy use this one function, so what check
// passes is what a deploy writes.

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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
//     the runtime's default when it has none. What runtime.New refuses is
//     refused here too: a later task named like the main task, and two
//     tasks whose programs share a PROGRAM name;
//   - the manifest's tags, typed by the runtime's rule (type:, else the
//     type a program's VAR_EXTERNAL binds it as, else what init: implies;
//     a tag with none of them is left out, since the compiler already
//     refuses a program that names it), retained exactly where
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
	var st *project.SiemensTarget
	if m.Target != nil {
		st = m.Target.Siemens
	}
	// An invalid prefix is check's problem (Problems reports it); the
	// writer still gets the value as written.
	in.BlockPrefix = st.Prefix()

	defs, err := project.TagDefs(m)
	if err != nil {
		return swriter.Input{}, err
	}
	if len(m.Tasks) == 0 {
		return swriter.Input{}, errors.New("the project declares no task")
	}
	pous := map[string]string{} // PROGRAM name, lower-cased → task
	for i, t := range m.Tasks {
		if t.Program == "" {
			return swriter.Input{}, fmt.Errorf("task %d names no program file", i+1)
		}
		file := filepath.Join(dir, filepath.FromSlash(path.Clean(t.Program)))
		src, err := os.ReadFile(file)
		if err != nil {
			return swriter.Input{}, err
		}
		_, libs, _ := stproject.PreludeSources(file, nil)
		p := swriter.Program{Path: file, Source: string(src), Libs: libs}
		name, err := swriter.ProgramName(p)
		if err != nil {
			return swriter.Input{}, err
		}
		task := swriter.Task{Name: t.Name, Program: name, Interval: time.Duration(t.Scan)}
		if i == 0 {
			task.Name = runtime.MainTaskName
		} else {
			if task.Name == "" {
				task.Name = strings.TrimSuffix(path.Base(t.Program), path.Ext(t.Program))
			}
			if task.Name == runtime.MainTaskName {
				return swriter.Input{}, fmt.Errorf("task name %q is reserved for the main task", runtime.MainTaskName)
			}
		}
		if other, ok := pous[strings.ToLower(name)]; ok {
			return swriter.Input{}, fmt.Errorf("task %s: PROGRAM %s collides with task %s — POU names identify programs and must be unique", task.Name, name, other)
		}
		pous[strings.ToLower(name)] = task.Name
		in.Programs = append(in.Programs, p)
		if task.Interval <= 0 {
			task.Interval = siemensDefaultScan
		}
		in.Tasks = append(in.Tasks, task)
	}
	scope, _ := runtime.ResolveTagScope(defs, nil)
	// A tag with no type: is the type the programs bind it as, as the
	// runtime has it; only a tag no program declares takes its init's.
	ext := swriter.ExternalTypes(in.Programs)
	for _, d := range defs {
		typ := d.Type
		if typ == "" {
			typ = ext[strings.ToLower(d.Name)]
		}
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
		switch d.Init.(type) {
		case nil:
		case map[string]any, []any:
			// A struct's or an array's start value: the writer types each
			// member from the type it resolves.
			g.Start = d.Init
		default:
			lit, err := swriter.InitLiteral(d.Init, typ)
			if err != nil {
				return swriter.Input{}, fmt.Errorf("tag %s: %w", d.Name, err)
			}
			g.Init = lit
		}
		in.Globals = append(in.Globals, g)
	}

	return in, nil
}
