package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/runtime"
)

// runCheck compiles every .st file under the given paths (files or
// directories; default ".") and prints gcc-style diagnostics:
//
//	path/to/program.st:12:5: undeclared identifier "y" ...
//
// Exit code 0 = clean, 1 = diagnostics found, 2 = usage/IO error.
func runCheck(args []string) int {
	fset := flag.NewFlagSet("check", flag.ContinueOnError)
	manifest := fset.String("m", "", manifestFlagUsage)
	if err := fset.Parse(args); err != nil {
		return 2
	}
	paths := fset.Args()
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
			if ext := strings.ToLower(filepath.Ext(path)); ext == ".st" || ext == ".fbd" || ext == ".ld" || ext == ".sfc" {
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
		fmt.Fprintln(os.Stderr, "nautilus check: no .st, .fbd, .ld, or .sfc files found")
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
		// Sibling library files (TYPE/FB/FUNCTION-only .st, and .ld/.fbd
		// files with no PROGRAM) are in scope, exactly as the LSP and a
		// runtime that composes sources see it. They are resolved BEFORE the
		// transpile hops below because a ladder rung needs a user block's
		// signature to know which pins its power uses.
		prelude, libSources, preludeLines := stproject.PreludeSources(f, nil)
		// Graphical languages compile by transpiling toward ST — LD to the
		// FBD netlist, FBD to ST — then check exactly like an .st file; the
		// composed line maps project diagnostic positions back onto the
		// original source.
		var lineMap []int
		if strings.EqualFold(filepath.Ext(f), ".sfc") {
			// SFC transpiles directly to ST (a sibling of the LD/FBD hops,
			// not a stage in their chain — docs/design/sfc.md §3). The
			// structural checks of §5.1 run for real here; the ST-level
			// hop (sfc.TranspileWithLines) is a follow-on slice's
			// deliverable and errors cleanly until it lands.
			prog, perr := sfc.Parse(source)
			if perr != nil {
				bad++
				fmt.Printf("%s: %s\n", f, perr.Error())
				continue
			}
			hasErr := false
			for _, d := range sfc.Check(prog) {
				fmt.Printf("%s:%d:%d: %s: %s\n", f, d.Pos.Line, d.Pos.Col, d.Severity, d.Message)
				if d.Severity == sfc.SeverityError {
					hasErr = true
				}
			}
			if hasErr {
				bad++
				continue
			}
			stSrc, lm, terr := sfc.TranspileWithLines(source)
			if terr != nil {
				bad++
				fmt.Printf("%s: %s\n", f, terr.Error())
				continue
			}
			source, lineMap = stSrc, lm
		}
		if strings.EqualFold(filepath.Ext(f), ".ld") {
			fbdSrc, lm, terr := ld.TranspileWithLines(source, libSources...)
			if terr != nil {
				bad++
				fmt.Printf("%s: %s\n", f, terr.Error())
				continue
			}
			source, lineMap = fbdSrc, lm
		}
		// After the LD hop the source always carries an FBD block. SFC is
		// not in this chain — it transpiled directly to ST above.
		if strings.EqualFold(filepath.Ext(f), ".fbd") || strings.EqualFold(filepath.Ext(f), ".ld") {
			stSrc, lm, terr := fbd.TranspileWithLines(source)
			if terr != nil {
				bad++
				fmt.Printf("%s: %s\n", f, terr.Error())
				continue
			}
			// Compose: ST line → FBD line → (for .ld) LD line.
			if lineMap != nil {
				composed := make([]int, len(lm))
				for i, fbdLine := range lm {
					if fbdLine >= 1 && fbdLine <= len(lineMap) {
						composed[i] = lineMap[fbdLine-1]
					} else {
						composed[i] = 1
					}
				}
				lm = composed
			}
			source, lineMap = stSrc, lm
		}
		if msg, pos, failed := compileErr(source, prelude, preludeLines); failed {
			bad++
			if lineMap != nil {
				if pos.Line >= 1 && pos.Line <= len(lineMap) {
					pos = st.Pos{Line: lineMap[pos.Line-1], Col: 1}
				} else {
					pos = st.Pos{Line: 1, Col: 1}
				}
			}
			fmt.Printf("%s:%d:%d: %s\n", f, pos.Line, pos.Col, msg)
		}
	}

	// Compiling every file proves each one is well-formed. It says nothing
	// about whether the tag set and the logic agree — which is the thing a
	// generated manifest most needs checked, and the reason composition
	// (tag-files) and verification landed together.
	manifestErrs, manifestWarns := 0, 0
	if bad == 0 {
		manifestErrs, manifestWarns = checkManifest(paths, *manifest)
		bad += manifestErrs
	}

	fmt.Printf("nautilus check: %d file(s), %d with errors", len(files), bad)
	if manifestWarns > 0 {
		fmt.Printf(", %d warning(s)", manifestWarns)
	}
	fmt.Println()
	if bad > 0 {
		return 1
	}
	return 0
}

// checkManifest cross-checks a manifest project's declared tags against the
// tags its programs actually bind, in both directions. Returns (errors,
// warnings).
//
// The asymmetry is deliberate:
//
//   - a program READS a tag the manifest never declares → error. Undeclared
//     means unseeded and not driver-fed, so the first read faults the scan.
//     This is the failure that used to wait until commissioning.
//   - a program only WRITES an undeclared tag → warning. It runs, but it
//     reaches an HMI with no unit and no description.
//   - the manifest declares a tag no program binds → warning. Often correct
//     (driver- or HMI-only tags are real), so it cannot be an error — but it
//     is also what a stale generated tag file looks like.
func checkManifest(paths []string, manifestName string) (errs, warns int) {
	dir, ok := manifestDir(paths, manifestName)
	if !ok {
		return 0, 0 // not a manifest project; compiling the files was the whole job
	}
	proj, err := project.Load(os.DirFS(dir), manifestName)
	if err != nil {
		fmt.Printf("%s: %s\n", dir, err)
		return 1, 0
	}
	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		// The per-file pass already reported real compile errors; reaching
		// here means the composed resource failed for some other reason.
		fmt.Printf("%s: %s\n", dir, err)
		return 1, 0
	}

	declared := make(map[string]bool, len(proj.Runtime.Tags))
	for _, d := range proj.Runtime.Tags {
		declared[d.Name] = true
	}
	// A task's dt-tag is written by the runtime every scan, so a program may
	// read it without any tag entry. It is declared in the manifest — just
	// not under tags: — and reporting it would be a false alarm on the one
	// tag the manifest is most certain about.
	if proj.Runtime.DtTag != "" {
		declared[proj.Runtime.DtTag] = true
	}
	for _, t := range proj.Runtime.Tasks {
		if t.DtTag != "" {
			declared[t.DtTag] = true
		}
	}
	uses := rt.GlobalUses()

	for _, name := range sortedNames(rt.Globals()) {
		if declared[name] {
			continue
		}
		switch {
		case uses.Read[name]:
			errs++
			fmt.Printf("%s: error: the programs read %q, which %s declares no tag for — "+
				"an undeclared tag is never seeded and never driver-fed, so the first "+
				"read faults the scan\n", dir, name, manifestLabel(manifestName))
		default:
			warns++
			fmt.Printf("%s: warning: the programs write %q, which %s declares no tag for — "+
				"it will reach an HMI with no unit and no description\n",
				dir, name, manifestLabel(manifestName))
		}
	}

	bound := rt.Globals()
	for _, d := range proj.Runtime.Tags {
		if _, ok := bound[d.Name]; ok {
			continue
		}
		// An INPUT no program binds is not a defect: the driver fills it and
		// an HMI or Sparkplug republishes it, which is what most of an
		// imported tag list is for. client60 is the proof — every one of its
		// unbound tags is telemetry, and warning on all of them would teach
		// people to ignore this warning before it ever caught anything.
		//
		// The other roles have no such excuse. A setpoint or state exists to
		// be read by logic, and an output that nothing writes ships its seed
		// to the field forever.
		if d.Role == runtime.RoleInput {
			continue
		}
		warns++
		fmt.Printf("%s: warning: %s declares %s %q, which no program binds — "+
			"dead, or a stale generated entry\n",
			dir, manifestLabel(manifestName), roleName(d.Role), d.Name)
	}

	// A tag-meta key that matched a tag was folded into that tag's own
	// documentation by Load; what survives in Meta matched nothing. A dotted
	// key is a field path and legitimate, so only bare names are reported —
	// which is where a typo in a hand-written documentation block shows up.
	for _, key := range sortedNames(proj.Runtime.Meta) {
		if strings.Contains(key, ".") || declared[key] {
			continue
		}
		warns++
		fmt.Printf("%s: warning: tag-meta documents %q, which is not a declared tag — "+
			"documentation for a tag that does not exist reaches nothing\n", dir, key)
	}

	// Alarms: the rules really expand, the templates really interpolate,
	// and every condition path really names a BOOL. Offline — no engine is
	// constructed, nothing is opened.
	defs, aerrs, awarns, err := proj.CheckAlarms(rt)
	if err != nil {
		errs++
		fmt.Printf("%s: error: alarms: %s\n", dir, err)
		return errs, warns
	}
	for _, m := range aerrs {
		errs++
		fmt.Printf("%s: error: %s\n", dir, m)
	}
	for _, m := range awarns {
		warns++
		fmt.Printf("%s: warning: %s\n", dir, m)
	}
	if proj.Alarms != nil {
		// The count is the auditable half of "a few rules cover thousands
		// of alarms": `nautilus alarms list` dumps what they became.
		fmt.Printf("%s: %d alarm definitions\n", dir, len(defs))
	}
	return errs, warns
}

func roleName(r runtime.TagRole) string {
	switch r {
	case runtime.RoleInput:
		return "input"
	case runtime.RoleOutput:
		return "output"
	case runtime.RoleSetpoint:
		return "setpoint"
	case runtime.RoleState:
		return "state"
	}
	return "tag"
}

// manifestDir finds the single directory to cross-check: the first checked
// path that is a directory holding the manifest. Checking a bare file, or a
// tree with no manifest, skips this pass entirely.
func manifestDir(paths []string, manifestName string) (string, bool) {
	name := manifestName
	if name == "" {
		name = project.ManifestName
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			continue
		}
		if st, err := os.Stat(filepath.Join(p, filepath.FromSlash(name))); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func manifestLabel(manifestName string) string {
	if manifestName == "" {
		return project.ManifestName
	}
	return manifestName
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
