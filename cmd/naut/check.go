package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joyautomation/nautilus/internal/hmibuild"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	nio "github.com/joyautomation/nautilus/io"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/lint"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/logix/writer"
	opcuaserver "github.com/joyautomation/nautilus/opcua/server"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/sparkplug"
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
		// A manifest that cannot be read is the project's whole problem, and
		// check is the CI gate: a tree with nothing to compile must not pass
		// just because the file that names the programs is empty or malformed.
		if dir, ok := manifestDir(paths, *manifest); ok {
			if _, err := project.ReadManifest(os.DirFS(dir), *manifest); err != nil {
				fmt.Fprintln(os.Stderr, "naut check:", err)
				return 1
			}
		}
		fmt.Fprintln(os.Stderr, "naut check: no .st, .fbd, .ld, or .sfc files found")
		return 0
	}

	bad, blankWarns := 0, 0
	// #199: an error inside a library is reported once, on the library's
	// own line — by that file's own check when it is one of the files
	// checked, else from the first program that composes it. The programs
	// that compose a broken library say nothing more: their compile stops
	// at the library, and fixing it is the one thing to do.
	ownBad := map[string]bool{}    // abs path → its own check failed
	display := map[string]string{} // abs path → the name it is printed as
	var libErrs []libErr
	markBad := func(f string) {
		bad++
		if abs, err := filepath.Abs(f); err == nil {
			ownBad[abs] = true
		}
	}
	tagsFor := map[string][]runtime.TagDef{}
	tagsErr := map[string]error{} // root → why its manifest's tags can't be read
	tagsSaid := map[string]bool{} // manifest dir → its error is printed (a directory reads one manifest)
	projectTags := func(f string) []runtime.TagDef {
		root := stproject.ProjectRoot(f)
		if defs, ok := tagsFor[root]; ok {
			return defs
		}
		defs, mdir, mname, err := project.TagDefsForErr(f, *manifest)
		tagsFor[root] = defs
		if err != nil {
			// Reported once per project, on the manifest. Its programs are
			// not compiled against an empty tag set: that would only say
			// each tag they use is undeclared, and hide this.
			tagsErr[root] = err
			if !tagsSaid[mdir] { // once per manifest, however many directories its programs are in
				tagsSaid[mdir] = true
				shown := mdir
				if wd, werr := os.Getwd(); werr == nil && wd != string(filepath.Separator) {
					if rel, rerr := filepath.Rel(wd, mdir); rerr == nil && !strings.HasPrefix(rel, "..") {
						shown = rel
					}
				}
				fmt.Println(newManifestLoc(shown, mname).say(err))
			}
		}
		return defs
	}
	structureCfg := map[string]lint.Config{} // project root → its check: settings
	structureFor := func(f string) lint.Config {
		root := stproject.ProjectRoot(f)
		cfg, ok := structureCfg[root]
		if !ok {
			cfg = project.StructureFor(f, *manifest)
			structureCfg[root] = cfg
		}
		return cfg
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
		if tagsErr[stproject.ProjectRoot(f)] != nil {
			markBad(f)
			continue
		}
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
			for _, d := range sfc.CheckWith(prog, tagNames(projectTags(f))) {
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
		res := compileFile(source, prelude, segs, tags, project.PeersFor(f, nil, libSources)...)
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
		for _, w := range res.warns {
			blankWarns++
			line, col := mapLine(w.Pos.Line), w.Pos.Col
			if lineMap != nil {
				col = 1
			}
			fmt.Printf("%s:%d:%d: warning: %s\n", f, line, col, w.Msg)
		}
		// An FB instance named like a FUNCTION hides it in that POU (#336).
		for _, sh := range st.InstancesShadowingFunctions(res.prog, res.composed) {
			blankWarns++
			line, col := mapLine(sh.Decl.Pos.Line), sh.Decl.Pos.Col
			if lineMap != nil {
				col = 1
			}
			fmt.Printf("%s:%d:%d: warning: %s\n", f, line, col, sh.Message())
		}
		// Diagram structure (#561): a ladder, FBD or SFC file that compiles
		// can still draw as a knot. The rules, their IDs and thresholds:
		// lang/lint and docs/structure-warnings.md.
		for _, sf := range project.Structure(f, original, structureFor(f)) {
			blankWarns++
			fmt.Printf("%s:%d:%d: warning: %s [%s]\n", f, sf.Line, sf.Col, sf.Message, sf.Rule)
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
// gcc-style lines as a compile error. Ladder and ST programs are in the
// subset; a program in another language is one diagnostic naming the
// phase that adds it. Returns true when anything was reported. The editor
// runs the same check (internal/lsp/logix.go); the two must agree.
func checkLogixTarget(f, source string, libs []string) bool {
	// A library, in any language, is checked where a program uses it: the
	// writer checks the types and blocks a program actually reaches.
	if !stproject.DeclaresProgram(source) {
		return false
	}
	if writer.Language(f) == "" {
		fmt.Printf("%s: logix target: only ladder (.ld) and structured text (.st) programs are in the Logix subset; FBD and SFC come later\n", f)
		return true
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
	loc := newManifestLoc(dir, manifestName)
	proj, err := project.Load(os.DirFS(dir), manifestName)
	if err != nil {
		fmt.Println(loc.say(err))
		return 1, 0
	}

	// A driver's own non-fatal findings: an unset credential variable, a
	// missing secret file, a weak SNMP auth. Warnings, never errors — this
	// runs on laptops that have none of the secrets a controller will.
	for _, w := range driverWarnings(proj.Runtime.Driver) {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, w)
	}

	// The project's guidance for AI assistants is older than this naut's,
	// conflicted, or not imported by CLAUDE.md (agents.go; a missing or
	// home-grown AGENTS.md is deliberately not a warning).
	for _, w := range agentsWarnings(dir) {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, w)
	}

	// The served HMI predates an edit it compiles in (a mimic, a scene,
	// its own code): what the operator sees is not what the files say.
	if !proj.HMIMissing && proj.HMIApp == "" {
		if src := project.StaleHMISource(os.DirFS(dir), proj.HMIDir); src != "" {
			warns++
			fmt.Printf("%s: warning: server.hmi: %s is older than %s — run npm run build in %s\n",
				loc.file, proj.HMIDir, src, project.HMIBuildHint(proj.HMIDir))
		}
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
			loc.file, manifestLabel(manifestName), raw.Tasks[0].Name, runtime.MainTaskName, raw.Tasks[0].Name)
	}

	rt, err := runtime.New(proj.Runtime)
	if err != nil {
		// The per-file pass already reported real compile errors; reaching
		// here means the composed resource failed for some other reason.
		fmt.Printf("%s: %s\n", loc.tag(tagNamedInError(err)), err)
		return 1, 0
	}

	// A protocol input reads as its init: (or the zero of its type) until
	// its device first answers — but only when some type is knowable for
	// it. One with none stays absent until then, which faults nothing (no
	// program reads it, or the program would have typed it) but leaves an
	// HMI or a Sparkplug birth with no value for it. Say so, with the fix.
	for _, name := range rt.UntypedCommsInputs() {
		warns++
		fmt.Printf("%s: warning: input %q comes from a communications driver but has no "+
			"knowable type (no type:, no program declares it, the driver cannot say), "+
			"so it has no value until the device first delivers one — give it a type: "+
			"to have it read as zero until then\n", loc.tag(name), name)
	}

	// A declared tag and a driver binding that differ only in case. Program
	// identifiers are case-insensitive, but the driver delivers (and accepts
	// writes for) its binding's exact spelling and the tag store matches
	// exactly, so `m1_kw` never receives what the driver delivers as
	// `M1_KW`, and nothing else says so (#544).
	for _, m := range caseOnlyBindings(proj.Runtime.Tags, proj.Runtime.Driver) {
		errs++
		fmt.Printf("%s: error: tag %q is spelled %q in the driver's bindings — the driver "+
			"matches names exactly, so this tag would never %s; spell both the same\n",
			loc.tag(m.tag), m.tag, m.binding, m.never)
	}

	// sparkplug.writable against the names a command can carry: a pattern
	// that matches none is a typo, a renamed tag, or an old character
	// class ("Pump[12]") that is an array index now.
	for _, w := range sparkplug.WritableWarnings(rt, proj.SparkplugWritable()) {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, w)
	}

	// opcua.writable against the tags it is meant to open: a pattern that
	// opens nothing, or only inputs (never writable), is a list that does
	// not say what its author thinks; an output a program writes is open to
	// a client only until the next commit.
	if proj.OPCUA != nil {
		for _, w := range opcuaserver.WritableWarnings(rt, proj.OPCUA.Writable) {
			warns++
			fmt.Printf("%s: warning: %s\n", loc.file, w)
		}
		if w := opcuaserver.AnonymousWritesWarning(proj.OPCUA.AnonymousWrites, proj.OPCUA.Writable, proj.OPCUA.Endpoint); w != "" {
			warns++
			fmt.Printf("%s: warning: %s\n", loc.file, w)
		}
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
				"read faults the scan\n", loc.file, name, manifestLabel(manifestName))
		default:
			warns++
			fmt.Printf("%s: warning: the programs write %q, which %s declares no tag for — "+
				"it will reach an HMI with no unit and no description\n",
				loc.file, name, manifestLabel(manifestName))
		}
	}

	// An output with no init: is never seeded: it holds nothing until a
	// program writes it, and a read before that faults the scan with
	// "undefined tag" (nautilus#560). A seal-in reads its own coil first,
	// so it faults on scan one and — the faulting rung being the one that
	// writes the tag — on every scan after. Only a program's own earlier
	// write counts: tasks start concurrently. An error when some path the
	// scan can take reaches the read unwritten; a warning when it is
	// unwritten only if a loop runs zero times.
	readFirst := map[string][]runtime.EarlyRead{}
	for name, reads := range rt.ReadBeforeWrite() {
		readFirst[ir.NameKey(name)] = append(readFirst[ir.NameKey(name)], reads...)
	}
	writtenAnywhere := map[string]bool{}
	for name := range uses.Written {
		writtenAnywhere[ir.NameKey(name)] = true
	}
	for _, d := range proj.Runtime.Tags {
		if d.Role != runtime.RoleOutput || d.Init != nil {
			continue
		}
		var sure, maybe []runtime.EarlyRead
		for _, r := range readFirst[ir.NameKey(d.Name)] {
			if r.Sure {
				sure = append(sure, r)
			} else {
				maybe = append(maybe, r)
			}
		}
		switch {
		case len(sure) > 0:
			errs++
			fmt.Printf("%s: error: output %q has no init:, and %s — an output with no init: holds "+
				"nothing until it is written, so that read faults the scan with \"undefined tag %s\"; "+
				"give it an init:%s\n",
				loc.tag(d.Name), d.Name, earlyReads(sure, writtenAnywhere[ir.NameKey(d.Name)]), d.Name, initHint(d, rt))
		case len(maybe) > 0:
			warns++
			fmt.Printf("%s: warning: output %q has no init:, and %s — if a read comes first it "+
				"faults the scan with \"undefined tag %s\"; give it an init:%s\n",
				loc.tag(d.Name), d.Name, earlyReads(maybe, true), d.Name, initHint(d, rt))
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
			loc.tag(d.Name), manifestLabel(manifestName), roleName(d.Role), d.Name)
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
			"documentation for a tag that does not exist reaches nothing\n", loc.file, key)
	}

	// Alarms: the rules really expand, the templates really interpolate,
	// and every condition path really names a BOOL. Offline — no engine is
	// constructed, nothing is opened.
	defs, aerrs, awarns, err := proj.CheckAlarms(rt)
	if err != nil {
		errs++
		fmt.Printf("%s: error: alarms: %s\n", loc.file, err)
		return errs, warns
	}
	for _, m := range aerrs {
		errs++
		fmt.Printf("%s: error: %s\n", loc.file, m)
	}
	for _, m := range awarns {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, m)
	}
	if proj.Alarms != nil {
		// The count is the auditable half of "a few rules cover thousands
		// of alarms": `naut alarms list` dumps what they became.
		fmt.Printf("%s: %d alarm definitions\n", loc.file, len(defs))
	}

	// Scenes: every *.scene.json at the root really binds struct tags the
	// manifest declares, and every kind really finds its members on them.
	// Same offline discipline as alarms; docs/design/spatial-hmi.md §3b.
	reports, serrs, swarns, err := proj.CheckScenes(os.DirFS(dir), rt)
	if err != nil {
		errs++
		fmt.Printf("%s: error: scenes: %s\n", loc.file, err)
		return errs, warns
	}
	for _, m := range serrs {
		errs++
		fmt.Printf("%s: error: %s\n", loc.file, m)
	}
	for _, m := range swarns {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, m)
	}
	for _, r := range reports {
		fmt.Printf("%s: scene %s: %d nodes, %d pipes\n", loc.file, r.File, r.Nodes, r.Pipes)
	}

	// Screens: every *.mimic.json binds tags the project declares, its
	// controls write tags the API lets them, its pipes end on ports that
	// exist; then the screen is built, as naut run would serve it, so a
	// component that won't compile (or would throw when it mounts) is an
	// error here and not a blank box on the operator's screen.
	mreports, merrs, mwarns := proj.CheckMimics(os.DirFS(dir), manifestName == project.ManifestName)
	for _, m := range merrs {
		errs++
		fmt.Printf("%s: error: %s\n", loc.file, m)
	}
	for _, m := range mwarns {
		warns++
		fmt.Printf("%s: warning: %s\n", loc.file, m)
	}
	for _, r := range mreports {
		fmt.Printf("%s: screen %s: %d equipment, %d pipes\n", loc.file, r.File, r.Equipment, r.Pipes)
	}
	if src := hmiSource(proj); src != "" {
		res, err := buildHMI(proj, dir)
		var be *hmibuild.Error
		switch {
		case errors.As(err, &be):
			for _, m := range be.Messages {
				errs++
				fmt.Printf("%s: error: hmi: %s\n", loc.file, m)
			}
		case err != nil:
			errs++
			fmt.Printf("%s: error: hmi: %s\n", loc.file, err)
		default:
			for _, w := range res.Warnings {
				warns++
				fmt.Printf("%s: warning: hmi: %s\n", loc.file, w)
			}
			fmt.Printf("%s: hmi: %s builds: %s (%s)\n", loc.file, src, plural(len(res.Pages), "page"), strings.Join(res.Pages, " "))
		}
	}
	return errs, warns
}

// programList names programs for a diagnostic: "Interlocks", "Interlocks
// and Alarms", "A, B and C".
// earlyReads says which programs read an output before writing it, and
// why, one clause per program in task order. writtenAnywhere is whether
// any program writes it.
func earlyReads(reads []runtime.EarlyRead, writtenAnywhere bool) string {
	var parts []string
	for _, r := range reads {
		var part string
		switch {
		case !r.Sure:
			part = r.Program + " may read it before writing it (it is written first only inside a loop that may run zero times)"
		case !r.Writes && writtenAnywhere:
			part = r.Program + " reads it, and only another task writes it (tasks start together, so the read can come first)"
		case !r.Writes:
			part = r.Program + " reads it, and no program writes it"
		default:
			part = r.Program + " reads it before writing it (" + readCause(r) + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// readCause is why a program's read of a tag comes before its own write.
func readCause(r runtime.EarlyRead) string {
	switch r.Cause {
	case ir.CauseSelfAssign:
		if r.Lang == "ld" {
			return "a seal-in or set/reset coil reads its own coil"
		}
		return "the assignment that writes it reads it first"
	case ir.CauseCompound:
		return "setting a member, element or bit of it reads the whole tag first"
	case ir.CauseSomePaths:
		return "it is written only on some paths before this read"
	}
	return "the read comes before the first write"
}

// initHint is " (init: <the zero of the tag's type>)" for a diagnostic
// that asks for an init:, "" when the type has no one-word zero.
func initHint(d runtime.TagDef, rt *runtime.Runtime) string {
	var t *ir.Type
	for name, gt := range rt.Globals() {
		if ir.NameKey(name) == ir.NameKey(d.Name) {
			t = gt
			break
		}
	}
	if t == nil || t.Enum != nil {
		return ""
	}
	zero := map[ir.TypeKind]string{
		ir.TypeBool: "false", ir.TypeInt: "0", ir.TypeReal: "0.0",
		ir.TypeTime: "T#0s", ir.TypeString: "''",
	}[t.Kind]
	if zero == "" {
		return ""
	}
	return " (init: " + zero + ")"
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

// tagNames lists the tags' names.
func tagNames(tags []runtime.TagDef) []string {
	out := make([]string, 0, len(tags))
	for _, d := range tags {
		out = append(out, d.Name)
	}
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
	// composed is the library prelude and the file parsed together (prog
	// when there is no prelude).
	composed *st.Program
	// warns are what the file's lowering accepted but flagged (an integer
	// where a TIME is expected), positioned in the checked file.
	warns []st.LowerWarning
}

// compileFile runs the same parse+lower pipeline as the LSP: the prelude
// (the project's libraries, segs locating each) joins ahead of src, and
// the project's tags are in scope as the runtime puts them there
// (runtime.ResolveTagScope). Positions in src are reported in src's
// coordinates; an error inside the prelude is attributed to its library.
//
// peers are the project's programs (project.PeersFor): a reference to
// another task's variable is then explained, as runtime.New explains it.
func compileFile(src, prelude string, segs []stproject.Segment, tags []runtime.TagDef, peers ...st.PeerProgram) checkResult {
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
	opts.Peers = peers
	var warns []st.LowerWarning
	opts.Warn = func(w st.LowerWarning) {
		// A warning inside the library prelude belongs to the library's
		// own check, where it is reported in the library's coordinates.
		if w.Pos.Line > preludeLines {
			w.Pos.Line -= preludeLines
			warns = append(warns, w)
		}
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
	return checkResult{prog: prog, composed: lowerProg, warns: warns}
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

// caseMismatch is a declared tag whose driver binding is spelled differently.
type caseMismatch struct {
	tag, binding string
	never        string // what the tag would never do: receive values / reach the device
}

// caseOnlyBindings finds the declared inputs and outputs that no driver
// binding names exactly but one names in another case. A tag the driver names exactly is
// fine even if another binding differs only in case from it.
func caseOnlyBindings(tags []runtime.TagDef, d nio.Driver) []caseMismatch {
	type namer interface {
		InputNames() []string
		OutputNames() []string
	}
	n, ok := d.(namer)
	if !ok {
		return nil
	}
	exact := map[string]bool{}
	byKey := map[string]string{} // NameKey → a binding's spelling
	for _, name := range append(n.InputNames(), n.OutputNames()...) {
		exact[name] = true
		if _, seen := byKey[ir.NameKey(name)]; !seen {
			byKey[ir.NameKey(name)] = name
		}
	}
	var out []caseMismatch
	for _, t := range tags {
		// Only inputs and outputs meet the driver; a state or setpoint tag
		// beside an undeclared binding of the same name is not wired to it.
		if t.Role != runtime.RoleInput && t.Role != runtime.RoleOutput {
			continue
		}
		if exact[t.Name] {
			continue
		}
		b, ok := byKey[ir.NameKey(t.Name)]
		if !ok {
			continue
		}
		never := "receive the driver's values"
		if t.Role == runtime.RoleOutput {
			never = "reach the device"
		}
		out = append(out, caseMismatch{tag: t.Name, binding: b, never: never})
	}
	return out
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
