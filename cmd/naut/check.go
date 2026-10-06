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
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/logix/writer"
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
	target := fset.String("target", "", "also check the sources against a deploy target (experimental): \"logix\" runs the Allen-Bradley writer's rules, so a construct the L5X writer cannot express is a diagnostic here, not a download failure")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if *target != "" && *target != "logix" {
		fmt.Fprintf(os.Stderr, "naut check: unknown target %q (the targets are: logix)\n", *target)
		return 2
	}
	paths0 := fset.Args()
	if len(paths0) == 0 {
		paths0 = []string{"."}
	}
	// A project that declares a deploy target is checked against it
	// without being asked: the point of the target's rules is to fire on
	// the keystroke, not on the flag.
	if *target == "" {
		if dir, ok := manifestDir(paths0, *manifest); ok {
			if m, err := project.ReadManifest(os.DirFS(dir), *manifest); err == nil && m.Target != nil && m.Target.Logix != nil {
				*target = "logix"
			}
		}
	}
	paths := fset.Args()
	if len(paths) == 0 {
		paths = []string{"."}
	}

	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut check:", err)
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
			fmt.Fprintln(os.Stderr, "naut check:", err)
			return 2
		}
	}

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "naut check: no .st, .fbd, .ld, or .sfc files found")
		return 0
	}

	bad, blankWarns := 0, 0
	// #199: an error inside a library is reported once, on the library's
	// own line — by that file's own check when it is one of the files
	// checked, else from the first program that composes it. The programs
	// that compose a broken library say nothing more: their compile stops
	// at the library, and fixing it is the one thing to do.
	ownBad := map[string]bool{}  // abs path → its own check failed
	display := map[string]string{} // abs path → the name it is printed as
	var libErrs []libErr
	markBad := func(f string) {
		bad++
		if abs, err := filepath.Abs(f); err == nil {
			ownBad[abs] = true
		}
	}
	tagsFor := map[string][]runtime.TagDef{}
	projectTags := func(f string) []runtime.TagDef {
		root := stproject.ProjectRoot(f)
		if defs, ok := tagsFor[root]; ok {
			return defs
		}
		defs := project.TagDefsFor(f)
		tagsFor[root] = defs
		return defs
	}
	for _, f := range files {
		if abs, err := filepath.Abs(f); err == nil {
			display[abs] = f
		}
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut check:", err)
			return 2
		}
		source := string(src)
		// A new, still-empty diagram file (VS Code's New File, before the
		// diagram's "initialize" writes its POU) is no program yet, and no
		// task can name it without project.Load refusing it below — so it
		// is a warning that says what to do, not a parse error. (An empty
		// .st already compiles clean; an empty .ld/.fbd is read as a
		// library by the composition, which reports it.)
		if strings.EqualFold(filepath.Ext(f), ".sfc") && strings.TrimSpace(source) == "" {
			blankWarns++
			fmt.Printf("%s:1:1: warning: empty chart — no PROGRAM yet; open it as a diagram and click \"initialize\" (or delete it)\n", f)
			continue
		}
		original := source
		// lib/ holds libraries only. A PROGRAM there would be silently
		// dropped from every composition (it is neither a library nor a
		// task), so it is refused here, by its project-relative path.
		if inLibDir(f) && stproject.DeclaresProgram(source) {
			markBad(f)
			fmt.Printf("%s: declares a PROGRAM, but %s/ holds libraries only — "+
				"programs belong in the root and in `tasks:`\n", f, stproject.LibDir)
			continue
		}
		// Sibling library files (TYPE/FB/FUNCTION-only .st, and .ld/.fbd
		// files with no PROGRAM) are in scope, exactly as the LSP and a
		// runtime that composes sources see it. They are resolved BEFORE the
		// transpile hops below because a ladder rung needs a user block's
		// signature to know which pins its power uses.
		prelude, libSources, segs := stproject.PreludeParts(f, nil)
		tags := projectTags(f)
		// Graphical languages compile by transpiling toward ST — LD to the
		// FBD netlist, FBD to ST — then check exactly like an .st file; the
		// composed line maps project diagnostic positions back onto the
		// original source.
		var lineMap []int
		if strings.EqualFold(filepath.Ext(f), ".sfc") {
			// SFC transpiles directly to ST (a sibling of the LD/FBD hops,
			// not a stage in their chain — docs/design/sfc.md §3). The
			// structural checks of §5.1 run first, then the ST-level
			// hop (sfc.TranspileWithLines) compiles the chart like any
			// other program.
			prog, perr := sfc.Parse(source)
			if perr != nil {
				markBad(f)
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
				markBad(f)
				continue
			}
			stSrc, lm, terr := sfc.TranspileWithLines(source)
			if terr != nil {
				markBad(f)
				fmt.Printf("%s: %s\n", f, terr.Error())
				continue
			}
			source, lineMap = stSrc, lm
		}
		if strings.EqualFold(filepath.Ext(f), ".ld") {
			fbdSrc, lm, terr := ld.TranspileWithLines(source, libSources...)
			if terr != nil {
				markBad(f)
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
				markBad(f)
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
		mapLine := func(line int) int {
			if lineMap == nil {
				return line
			}
			if line >= 1 && line <= len(lineMap) {
				return lineMap[line-1]
			}
			return 1
		}
		res := compileFile(source, prelude, segs, tags)
		if res.failed {
			if res.inLib {
				libErrs = append(libErrs, res.lib)
				continue
			}
			markBad(f)
			pos := res.pos
			if lineMap != nil {
				pos = st.Pos{Line: mapLine(pos.Line), Col: 1}
			}
			fmt.Printf("%s:%d:%d: %s\n", f, pos.Line, pos.Col, res.msg)
			continue
		}
		// A program's own VAR of a tag's name hides the tag inside that
		// program — legal (IEC scoping), and almost never what was meant.
		for _, vd := range runtime.ShadowedTags(res.prog, tags) {
			blankWarns++
			line, col := mapLine(vd.Pos.Line), vd.Pos.Col
			if lineMap != nil {
				col = 1
			}
			fmt.Printf("%s:%d:%d: warning: local %s shadows the project tag %s — "+
				"this program reads and writes its own %s, not the tag; rename it, "+
				"or drop the declaration to use the tag\n", f, line, col, vd.Name, vd.Name, vd.Name)
		}
		// The target's rules run on a file that compiles: the same
		// lowering `naut logix write` uses, so what passes here writes.
		if *target == "logix" && checkLogixTarget(f, original, libSources) {
			markBad(f)
		}
	}

	// Library errors that surfaced only through a program that composes
	// the library: reported at the library's own line, once.
	seenLib, libCounted := map[string]bool{}, map[string]bool{}
	for _, le := range libErrs {
		if ownBad[le.path] {
			continue // its own check already reported it, on its own line
		}
		key := fmt.Sprintf("%s:%d:%d: %s", le.path, le.line, le.col, le.msg)
		if seenLib[key] {
			continue
		}
		seenLib[key] = true
		name, ok := display[le.path]
		if !ok {
			name = le.path
			if wd, err := os.Getwd(); err == nil {
				if rel, err := filepath.Rel(wd, le.path); err == nil {
					name = rel
				}
			}
		}
		fmt.Printf("%s:%d:%d: %s\n", name, le.line, le.col, le.msg)
		if !libCounted[le.path] {
			libCounted[le.path] = true
			bad++
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
	manifestWarns += blankWarns

	fmt.Printf("naut check: %d file(s), %d with errors", len(files), bad)
	if manifestWarns > 0 {
		fmt.Printf(", %d warning(s)", manifestWarns)
	}
	fmt.Println()
	if bad > 0 {
		return 1
	}
	return 0
}

// checkLogixTarget reports every construct in one source file that the
// Allen-Bradley writer (logix/writer) cannot express, in the same
// gcc-style lines as a compile error. Only ladder is in the v1 subset; a
// file in another language is one diagnostic naming the phase that adds
// it. Returns true when anything was reported.
func checkLogixTarget(f, source string, libs []string) bool {
	if writer.Language(f) == "" {
		fmt.Printf("%s: logix target: only ladder (.ld) and structured text (.st) programs are in the Logix subset; FBD and SFC come later\n", f)
		return true
	}
	// A library of TYPE declarations (and constants) is fine: the writer
	// checks the types a program actually uses. A library of blocks is
	// refused where a program uses one.
	if strings.EqualFold(filepath.Ext(f), ".st") && !stproject.DeclaresProgram(source) {
		return false
	}
	diags, err := writer.CheckProgram(f, source, libs...)
	if err != nil {
		fmt.Printf("%s: logix target: %s\n", f, err)
		return true
	}
	for _, d := range diags {
		fmt.Printf("%s:%d:1: logix target: %s [%s]\n", f, d.Line, d.Message, d.Rule)
	}
	return len(diags) > 0
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

	// A driver's own non-fatal findings: an unset credential variable, a
	// missing secret file, a weak SNMP auth. Warnings, never errors — this
	// runs on laptops that have none of the secrets a controller will.
	for _, w := range driverWarnings(proj.Runtime.Driver) {
		warns++
		fmt.Printf("%s: warning: %s\n", dir, w)
	}

	// The first task's name: key is a footgun, not a choice: Load (like
	// Sources, for a warm swap) assigns the first task runtime.MainTaskName
	// unconditionally, so a manifest author who names it — e.g. expecting
	// suspend: [that name] to work in an acceptance test — gets no
	// diagnostic here, just a task whose declared name silently does
	// nothing, and a confusing "no task \"that name\"" failure later at
	// `naut test` or `naut run`. Flagged here instead, while it's cheap to
	// fix — a warning, not an error: the key never changed behaviour, so a
	// manifest that carried it kept working and must keep passing check.
	if raw, rerr := project.ReadManifest(os.DirFS(dir), manifestName); rerr == nil &&
		len(raw.Tasks) > 0 && raw.Tasks[0].Name != "" {
		warns++
		fmt.Printf("%s: warning: %s's first task names itself %q, but the first task "+
			"is always %q — the name: key on it is ignored; drop it (or move the "+
			"program to a later task if it should be named %q)\n",
			dir, manifestLabel(manifestName), raw.Tasks[0].Name, runtime.MainTaskName, raw.Tasks[0].Name)
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		// The per-file pass already reported real compile errors; reaching
		// here means the composed resource failed for some other reason.
		fmt.Printf("%s: %s\n", dir, err)
		return 1, 0
	}

	// Keyed by ir.NameKey: tag names are case-insensitive identifiers, so a
	// program's `level` binds the manifest's Level.
	declared := make(map[string]bool, len(proj.Runtime.Tags))
	for _, d := range proj.Runtime.Tags {
		declared[ir.NameKey(d.Name)] = true
	}
	// A task's dt-tag is written by the runtime every scan, so a program may
	// read it without any tag entry. It is declared in the manifest — just
	// not under tags: — and reporting it would be a false alarm on the one
	// tag the manifest is most certain about.
	if proj.Runtime.DtTag != "" {
		declared[ir.NameKey(proj.Runtime.DtTag)] = true
	}
	for _, t := range proj.Runtime.Tasks {
		if t.DtTag != "" {
			declared[ir.NameKey(t.DtTag)] = true
		}
	}
	uses := rt.GlobalUses()

	for _, name := range sortedNames(rt.Globals()) {
		if declared[ir.NameKey(name)] {
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

	bound := map[string]bool{}
	for name := range rt.Globals() {
		bound[ir.NameKey(name)] = true
	}
	for _, d := range proj.Runtime.Tags {
		if bound[ir.NameKey(d.Name)] {
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
		if strings.Contains(key, ".") || declared[ir.NameKey(key)] {
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
		// of alarms": `naut alarms list` dumps what they became.
		fmt.Printf("%s: %d alarm definitions\n", dir, len(defs))
	}

	// Scenes: every *.scene.json at the root really binds struct tags the
	// manifest declares, and every kind really finds its members on them.
	// Same offline discipline as alarms; docs/design/spatial-hmi.md §3b.
	reports, serrs, swarns, err := proj.CheckScenes(os.DirFS(dir), rt)
	if err != nil {
		errs++
		fmt.Printf("%s: error: scenes: %s\n", dir, err)
		return errs, warns
	}
	for _, m := range serrs {
		errs++
		fmt.Printf("%s: error: %s\n", dir, m)
	}
	for _, m := range swarns {
		warns++
		fmt.Printf("%s: warning: %s\n", dir, m)
	}
	for _, r := range reports {
		fmt.Printf("%s: scene %s: %d nodes, %d pipes\n", dir, r.File, r.Nodes, r.Pipes)
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

// libErr is a compile error positioned in a library, by the library's
// own path and line.
type libErr struct {
	path      string
	line, col int
	msg       string
}

// checkResult is one file's compile verdict.
type checkResult struct {
	failed bool
	msg    string
	pos    st.Pos // in the checked file
	// inLib: the error lies in a library the file composes, at lib.
	inLib bool
	lib   libErr
	prog  *st.Program // the file's own parse, when it compiled
}

// compileFile runs the same parse+lower pipeline as the LSP: the prelude
// (the project's libraries, segs locating each) joins ahead of src, and
// the project's tags are in scope as the runtime puts them there
// (runtime.ResolveTagScope). Positions in src are reported in src's
// coordinates; an error inside the prelude is attributed to its library.
func compileFile(src, prelude string, segs []stproject.Segment, tags []runtime.TagDef) checkResult {
	prog, err := st.Parse(src)
	if err != nil {
		// Anchor on the parser-reported position (shared with the LSP via
		// st.ParseErrorPos) instead of always 1:1.
		pos := st.Pos{Line: 1, Col: 1}
		if p, ok := st.ParseErrorPos(err); ok {
			pos = p
		}
		return checkResult{failed: true, msg: err.Error(), pos: pos}
	}
	lowerProg, preludeLines := prog, 0
	if prelude != "" {
		if combined, cerr := st.Parse(prelude + src); cerr == nil {
			lowerProg, preludeLines = combined, strings.Count(prelude, "\n")
		}
	}
	var opts st.LowerOpts
	if len(tags) > 0 {
		types, _ := st.Types(lowerProg)
		scope, _ := runtime.ResolveTagScope(tags, types)
		opts = scope.LowerOpts()
	}
	if _, err := st.LowerWithOpts(lowerProg, opts); err != nil {
		pos := st.Pos{Line: 1, Col: 1}
		msg := err.Error()
		if le, ok := st.AsLowerError(err); ok && le.Pos.Line > 0 {
			// Print the unwrapped message: the position prefix that
			// LowerError.Error() adds is already in the path:line:col.
			pos, msg = le.Pos, le.Err.Error()
		}
		switch {
		case pos.Line > preludeLines:
			pos.Line -= preludeLines
		case preludeLines > 0:
			if sg, line, ok := stproject.Locate(segs, pos.Line); ok {
				col := pos.Col
				if sg.Transpiled {
					line, col = 1, 1
				}
				return checkResult{failed: true, inLib: true, msg: msg,
					lib: libErr{path: sg.Path, line: line, col: col, msg: msg}}
			}
			pos, msg = st.Pos{Line: 1, Col: 1}, "in project library files: "+msg
		}
		return checkResult{failed: true, msg: msg, pos: pos}
	}
	return checkResult{prog: prog}
}

// inLibDir reports whether f lies under the lib/ directory of a manifest
// project.
func inLibDir(f string) bool {
	abs, err := filepath.Abs(f)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(stproject.ProjectRoot(abs), abs)
	return err == nil && stproject.InLibDir(filepath.ToSlash(rel))
}

// driverWarnings collects Warnings() from a driver, recursing into a
// multi-driver set so each child's findings carry its name.
func driverWarnings(d nio.Driver) []string {
	switch drv := d.(type) {
	case nil:
		return nil
	case *nio.Multi:
		var out []string
		for _, c := range drv.Children() {
			for _, w := range driverWarnings(c.Driver) {
				out = append(out, c.Name+": "+w)
			}
		}
		return out
	case interface{ Warnings() []string }:
		return drv.Warnings()
	}
	return nil
}
