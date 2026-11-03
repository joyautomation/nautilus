package lsp

// Manifest awareness.
//
// The analyzer knows what a PROGRAM declares; nautilus.yaml knows what the
// PROJECT declares — every tag's role, its engineering unit, and the
// sentence describing it. Those are different facts, and the second set is
// the one an author is usually missing: hovering TempC should say "°C,
// tank temperature, driven by the sim task", not just "REAL".
//
// It also answers a question the analyzer cannot: which tags exist at all.
// That is what a test file needs, since its expectations name tags rather
// than a program's locals.
//
// Reading is cheap (a YAML decode, no compile) and cached against the
// manifest's modtime, because this runs on a keystroke.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/ir"
	"github.com/joyautomation/nautilus/runtime"
	"gopkg.in/yaml.v3"
)

// ProjectTag is one tag as nautilus.yaml declares it.
type ProjectTag struct {
	Name string `json:"name"`
	Role string `json:"role"`
	Type string `json:"type"` // inferred from init: BOOL / REAL / STRING
	Unit string `json:"unit,omitempty"`
	Desc string `json:"desc,omitempty"`
}

// manifestCache remembers one parse per manifest path, invalidated by
// modtime so an edit to nautilus.yaml is picked up without a restart.
type manifestCache struct {
	mu      sync.Mutex
	entries map[string]*manifestEntry
	// failed remembers a manifest that does not read, against the same
	// modtimes, so a broken manifest is not re-read on every keystroke.
	failed map[string]*manifestEntry
}

type manifestEntry struct {
	mod   string   // modOf at the read
	files []string // the tag-files it composed, whose modtimes join mod
	tags  []ProjectTag
	defs  []runtime.TagDef // the same tags as the compiler scopes them
	// logix: the manifest declares `target: logix`, so the project's
	// programs are checked against the Logix writer's rules as they are
	// typed, the same rules `naut check` runs for such a project.
	logix bool
	// badTags are the tags whose declarations cannot be read (an unknown
	// role, a setpoint with neither init nor type): left out of defs, and
	// reported on the manifest instead of as undeclared in every program
	// that uses them (#518).
	badTags []badTag
	// readErr: the manifest itself does not read. The entry then serves
	// the last good read's tags, or none when there was none.
	readErr error
	// unread holds the tags the YAML declares (tags[].name, in the
	// manifest or a tag-file) that are not in defs, by ir.NameKey: a bad
	// tag, or every tag of a manifest that does not read. A program naming
	// one is not at fault; the manifest is, and says so.
	unread map[string]string
	// diags is what the manifest says is wrong, by file URI (#518),
	// computed once per read.
	diags map[string][]Diagnostic
}

// badTag is one tag declaration the compiler cannot scope, and why.
type badTag struct {
	name string
	err  error
}

// unreadTag reports whether name is a tag the manifest declares but whose
// declaration does not read — so a program naming it is not at fault.
func (e *manifestEntry) unreadTag(name string) bool {
	if e == nil || name == "" {
		return false
	}
	_, ok := e.unread[ir.NameKey(name)]
	return ok
}

// modOf fingerprints the manifest and its tag-files: each file's modtime
// and size, in order. A tags/*.yaml save must refresh hover and the
// diagrams' descriptions as surely as a nautilus.yaml one, and each file
// counts on its own — the newest modtime alone would miss a rewrite within
// one clock tick, or a timestamp-preserving restore of an older file. A
// file that cannot be stat'ed is marked so, and its return is a change.
func modOf(mpath string, files []string) string {
	var b strings.Builder
	for _, f := range append([]string{mpath}, files...) {
		if st, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%d:%d;", st.ModTime().UnixNano(), st.Size())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

var manifests = manifestCache{entries: map[string]*manifestEntry{}, failed: map[string]*manifestEntry{}}

// findManifest walks up from a file looking for nautilus.yaml, stopping at
// the filesystem root. A project is a directory with a manifest in it, so
// this is the same question `naut run` answers by being run there.
func findManifest(path string) (string, bool) {
	dir := filepath.Dir(path)
	for range 32 { // bounded: a symlink cycle must not hang the server
		candidate := filepath.Join(dir, project.ManifestName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	return "", false
}

// ProjectTags is projectTags for callers outside the server: `naut ld
// graph` hands the manifest's tags to the ladder editor, so a retag to a
// tag the file doesn't declare can offer the declaration.
func ProjectTags(path string) []ProjectTag { return projectTags(path) }

// projectTags returns the tags declared by the manifest governing path, or
// nil when there is no manifest (a bare .st file outside a project, which
// is a perfectly good thing to edit).
func projectTags(path string) []ProjectTag {
	if e := manifestFor(path); e != nil {
		return e.tags
	}
	return nil
}

// tagDecl is where the raw YAML declares a tag: its tags[].name scalar.
type tagDecl struct {
	name string
	file string
	r    Range
}

// scanTagDecls reads the tags[].name entries of the manifest and of its
// tag-files from the YAML trees alone — no validation, so it answers for a
// manifest that does not read too — keeping each name's first
// declaration, by ir.NameKey. files are the tag-files' paths.
func scanTagDecls(mpath string) (decls map[string]tagDecl, files []string) {
	decls = map[string]tagDecl{}
	root, err := parseYAMLFile(mpath)
	if err != nil {
		return decls, nil
	}
	for _, tf := range yamlStringSeq(mappingValue(root, "tag-files")) {
		files = append(files, filepath.Join(filepath.Dir(mpath), filepath.FromSlash(tf)))
	}
	scan := func(file string, doc *yaml.Node) {
		for _, item := range tagItems(doc) {
			n := mappingValue(item, "name")
			if n == nil || n.Kind != yaml.ScalarNode || n.Value == "" {
				continue
			}
			if _, dup := decls[ir.NameKey(n.Value)]; !dup {
				decls[ir.NameKey(n.Value)] = tagDecl{name: n.Value, file: file, r: scalarRange(n)}
			}
		}
	}
	for _, f := range files {
		if doc, err := parseYAMLFile(f); err == nil {
			scan(f, doc)
		}
	}
	scan(mpath, root)
	return decls, files
}

// unreadOf is the declared tags missing from defs.
func unreadOf(decls map[string]tagDecl, defs []runtime.TagDef) map[string]string {
	have := make(map[string]bool, len(defs))
	for _, d := range defs {
		have[ir.NameKey(d.Name)] = true
	}
	var out map[string]string
	for k, d := range decls {
		if !have[k] {
			if out == nil {
				out = map[string]string{}
			}
			out[k] = d.name
		}
	}
	return out
}

// manifestDiagnostics is what the manifest says is wrong with its tags,
// keyed by the file URI to show it on: each tag that does not read on its
// tags[].name (in nautilus.yaml or the tag-file declaring it), and a
// manifest that does not read at all on the file and line its error names.
// nil when there is nothing to say.
func manifestDiagnostics(mpath string, e *manifestEntry, decls map[string]tagDecl, files []string) map[string][]Diagnostic {
	if e.readErr == nil && len(e.badTags) == 0 {
		return nil
	}
	out := map[string][]Diagnostic{}
	add := func(file string, r Range, msg string) {
		uri := pathToURI(file)
		out[uri] = append(out[uri], Diagnostic{Range: r, Severity: SeverityError, Source: "nautilus", Message: msg})
	}
	if e.readErr != nil {
		file, r := readErrPlace(mpath, files, e.readErr)
		add(file, r, e.readErr.Error())
	}
	for _, b := range e.badTags {
		file, r := mpath, fileLineRange(mpath, 1)
		if d, ok := decls[ir.NameKey(b.name)]; ok && b.name != "" {
			file, r = d.file, d.r
		}
		add(file, r, b.err.Error())
	}
	return out
}

var yamlLineRe = regexp.MustCompile(`\bline (\d+)\b`)

// readErrPlace places an error reading the manifest: on the file it names
// (the manifest, or one of its tag-files, by project-relative path) at the
// line a YAML error gives, else on the manifest's first line.
func readErrPlace(mpath string, files []string, err error) (string, Range) {
	msg := err.Error()
	file := ""
	for _, f := range append([]string{mpath}, files...) {
		rel, rerr := filepath.Rel(filepath.Dir(mpath), f)
		if rerr == nil && strings.HasPrefix(msg, filepath.ToSlash(rel)+":") {
			file = f
			break
		}
	}
	if file == "" {
		return mpath, fileLineRange(mpath, 1)
	}
	line := 1
	if m := yamlLineRe.FindStringSubmatch(msg); m != nil {
		if n, cerr := strconv.Atoi(m[1]); cerr == nil && n >= 1 {
			line = n
		}
	}
	return file, fileLineRange(file, line)
}

// fileLineRange is lineRange on a file read from disk.
func fileLineRange(path string, line int) Range {
	text := ""
	if raw, err := os.ReadFile(path); err == nil {
		text = string(raw)
	}
	return lineRange(text, line)
}

// publishManifestDiags publishes the manifest's own diagnostics (#518) for
// the project governing path. It runs as the project's programs are
// analyzed, and when the editor reports a manifest or tag-file changed
// (manifestFilesChanged).
func (s *Server) publishManifestDiags(path string) {
	mpath, ok := findManifest(path)
	if !ok {
		return
	}
	s.publishManifestDiagsAt(mpath, manifestAt(mpath).diags)
}

// publishManifestDiagsAt publishes found for the manifest at mpath and
// clears what it published before that no longer holds. Only changes are
// sent: this runs on every keystroke in a program.
func (s *Server) publishManifestDiagsAt(mpath string, found map[string][]Diagnostic) {
	prev := s.manifestDiags[mpath]
	for uri, ds := range found {
		if !reflect.DeepEqual(prev[uri], ds) {
			s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: uri, Diagnostics: ds})
		}
	}
	for uri := range prev {
		if _, still := found[uri]; !still {
			s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: uri, Diagnostics: []Diagnostic{}})
		}
	}
	if len(found) == 0 {
		delete(s.manifestDiags, mpath)
		return
	}
	if s.manifestDiags == nil {
		s.manifestDiags = map[string]map[string][]Diagnostic{}
	}
	s.manifestDiags[mpath] = found
}

// manifestFilesChanged answers workspace/didChangeWatchedFiles. The editor
// does not send nautilus.yaml or a tag-file to the server as a document, so
// this is how a save, a fix or a delete of one reaches the squiggles: the
// open documents are analyzed again (their tags may have changed), and
// every manifest with diagnostics out — or named by the change — publishes
// afresh, which clears a deleted manifest's.
func (s *Server) manifestFilesChanged(changes []FileEvent) {
	watched := manifestFilesWatched()
	var named []string
	reanalyze := false
	for _, c := range changes {
		p, ok := uriToPath(c.URI)
		if !ok {
			continue
		}
		switch {
		case filepath.Base(p) == project.ManifestName:
			// Its own diagnostics are always published (one cached read),
			// so a manifest saved with no program open still shows its
			// errors; the open documents are analyzed again only when it
			// is theirs.
			named = append(named, p)
			if s.governsProject(p, watched) {
				reanalyze = true
			}
		case watched[p]:
			reanalyze = true
		}
	}
	if reanalyze {
		uris := make([]string, 0, len(s.docs))
		for uri := range s.docs {
			uris = append(uris, uri)
		}
		sort.Strings(uris)
		for _, uri := range uris {
			s.setDocument(uri, s.docs[uri].text)
		}
	} else if len(named) == 0 {
		return
	}
	mpaths := append([]string(nil), named...)
	for mpath := range s.manifestDiags {
		mpaths = append(mpaths, mpath)
	}
	sort.Strings(mpaths)
	for _, mpath := range mpaths {
		if st, err := os.Stat(mpath); err != nil || st.IsDir() {
			s.publishManifestDiagsAt(mpath, nil) // gone: nothing left to say
			continue
		}
		s.publishManifestDiagsAt(mpath, manifestAt(mpath).diags)
	}
}

// governsProject reports whether a changed nautilus.yaml is the open
// documents' business: it is cached, has diagnostics out, or governs an
// open document (a manifest just created above one). An unrelated
// project's manifest elsewhere in the workspace analyzes nothing again.
func (s *Server) governsProject(mpath string, watched map[string]bool) bool {
	if watched[mpath] {
		return true
	}
	if _, ok := s.manifestDiags[mpath]; ok {
		return true
	}
	for uri := range s.docs {
		if p, ok := uriToPath(uri); ok {
			if m, found := findManifest(p); found && m == mpath {
				return true
			}
		}
	}
	return false
}

// manifestFilesWatched is every cached manifest and every tag-file one
// composes.
func manifestFilesWatched() map[string]bool {
	manifests.mu.Lock()
	defer manifests.mu.Unlock()
	out := map[string]bool{}
	for _, m := range []map[string]*manifestEntry{manifests.entries, manifests.failed} {
		for mpath, e := range m {
			out[mpath] = true
			for _, f := range e.files {
				out[f] = true
			}
		}
	}
	return out
}

// projectTagDefs is projectTags as the compiler sees them: the tags every
// program in the project has in scope without declaring them (#177/#210),
// for runtime.ResolveTagScope.
func projectTagDefs(path string) []runtime.TagDef {
	if e := manifestFor(path); e != nil {
		return e.defs
	}
	return nil
}

// logixTarget reports whether the manifest governing path declares a
// Logix deploy target.
func logixTarget(path string) bool {
	e := manifestFor(path)
	return e != nil && e.logix
}

func manifestFor(path string) *manifestEntry {
	mpath, ok := findManifest(path)
	if !ok {
		return nil
	}
	return manifestAt(mpath)
}

// manifestAt is the cached read of the manifest at mpath, refreshed when
// it or a tag-file changes. Never nil.
func manifestAt(mpath string) *manifestEntry {
	manifests.mu.Lock()
	defer manifests.mu.Unlock()
	prev, hadPrev := manifests.entries[mpath]
	if hadPrev && prev.mod == modOf(mpath, prev.files) {
		return prev
	}
	if f, ok := manifests.failed[mpath]; ok && f.mod == modOf(mpath, f.files) {
		return f
	}
	decls, rawFiles := scanTagDecls(mpath)
	tags, defs, bad, files, logix, err := readTags(mpath)
	if err != nil {
		// A manifest being edited is invalid for many of the saves it
		// takes to add a tag. Serving the last good answer beats completion
		// blinking out mid-edit; the error rides along, for the diagnostic
		// on the manifest. The failure is cached against the files'
		// modtimes, like a good read, and the next good read replaces it.
		var e manifestEntry
		if hadPrev {
			e = *prev
		}
		e.badTags = nil
		e.readErr = err
		e.files = rawFiles
		e.mod = modOf(mpath, rawFiles)
		e.unread = unreadOf(decls, e.defs)
		e.diags = manifestDiagnostics(mpath, &e, decls, rawFiles)
		manifests.failed[mpath] = &e
		return &e
	}
	delete(manifests.failed, mpath)
	e := &manifestEntry{mod: modOf(mpath, files), files: files, tags: tags, defs: defs, badTags: bad, logix: logix}
	e.unread = unreadOf(decls, defs)
	e.diags = manifestDiagnostics(mpath, e, decls, files)
	manifests.entries[mpath] = e
	return e
}

// manifestTagLocation is where the manifest (or a tag-file) declares the
// tag name — its tags[].name entry — for go-to-definition on a tag the
// program names without declaring.
func manifestTagLocation(uri, name string) (Location, bool) {
	path, ok := uriToPath(uri)
	if !ok {
		return Location{}, false
	}
	mpath, ok := findManifest(path)
	if !ok {
		return Location{}, false
	}
	for _, t := range projectTags(path) {
		if strings.EqualFold(t.Name, name) {
			name = t.Name
			break
		}
	}
	decls := manifestTagDecls(mpath, name)
	files := make([]string, 0, len(decls))
	for f := range decls {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		for r := range decls[f] {
			return Location{URI: pathToURI(f), Range: r}, true
		}
	}
	return Location{}, false
}

// readTags returns the manifest's composed tags and the tag-files they came
// from (as paths, for the cache's modtime check).
//
// A tag whose declaration does not read is left out of defs and returned
// in bad; the rest are still in scope. `naut check` refuses the whole
// project instead, but an editor is answering hover and completion on a
// keystroke: one bad tag must not take every other tag's type with it.
func readTags(mpath string) ([]ProjectTag, []runtime.TagDef, []badTag, []string, bool, error) {
	dir := filepath.Dir(mpath)
	m, err := project.ReadManifest(os.DirFS(dir), "")
	if err != nil {
		return nil, nil, nil, nil, false, err
	}
	defs, err := project.TagDefs(m)
	var bad []badTag
	if err != nil {
		good := make([]project.TagConfig, 0, len(m.Tags))
		for _, t := range m.Tags {
			if _, terr := project.TagDefs(&project.Manifest{Tags: []project.TagConfig{t}}); terr != nil {
				bad = append(bad, badTag{name: t.Name, err: terr})
				continue
			}
			good = append(good, t)
		}
		if len(bad) == 0 {
			bad = []badTag{{err: err}} // not one tag's fault; still said, on the manifest
		}
		defs, _ = project.TagDefs(&project.Manifest{Tags: good, Tasks: m.Tasks})
	}
	files := make([]string, 0, len(m.TagFiles))
	for _, f := range m.TagFiles {
		files = append(files, filepath.Join(dir, filepath.FromSlash(f)))
	}
	out := make([]ProjectTag, 0, len(m.Tags))
	for _, t := range m.Tags {
		if t.Name == "" {
			continue
		}
		// A declared type is the truth; inferring from the seed is the
		// fallback that existed only because there was nothing better. This
		// also answers the type of a tag with no seed at all, which
		// inference never could.
		typ := t.Type
		if typ == "" {
			typ = iecTypeOf(t.Init)
		}
		out = append(out, ProjectTag{
			Name: t.Name,
			Role: strings.ToLower(t.Role),
			Type: typ,
			Unit: t.Unit,
			Desc: t.Desc,
		})
	}
	return out, defs, bad, files, m.Target != nil && m.Target.Logix != nil, nil
}

// iecTypeOf infers a tag's IEC type from its declared initial value. The
// manifest does not state types — the seed's shape is the declaration, the
// same way the runtime's tag store derives a kind from what it is given.
// A role:input tag may have no init, in which case the type is unknown and
// saying nothing beats guessing.
func iecTypeOf(init any) string {
	switch init.(type) {
	case bool:
		return "BOOL"
	case string:
		if x := strings.ToUpper(strings.TrimSpace(init.(string))); strings.HasPrefix(x, "T#") || strings.HasPrefix(x, "TIME#") {
			return "TIME"
		}
		return "STRING"
	case int, int64, float64:
		return "REAL"
	}
	return ""
}

// hoverDoc renders a tag's manifest facts as markdown, for hover and for a
// completion item's documentation.
func (t ProjectTag) hoverDoc() string {
	var b strings.Builder
	b.WriteString("```iec-st\n")
	b.WriteString(t.Name)
	if t.Type != "" {
		b.WriteString(" : " + t.Type)
	}
	b.WriteString("\n```")
	if t.Desc != "" {
		b.WriteString("\n\n" + t.Desc)
	}
	var facts []string
	if t.Role != "" {
		facts = append(facts, roleBlurb(t.Role))
	}
	if t.Unit != "" {
		facts = append(facts, "measured in "+t.Unit)
	}
	if len(facts) > 0 {
		b.WriteString("\n\n*" + strings.Join(facts, " — ") + "*")
	}
	return b.String()
}

// roleBlurb says what a role MEANS in the scan, which is the part that
// actually bites: writing to an input is pointless because the driver
// overwrites it before every scan.
func roleBlurb(role string) string {
	switch role {
	case "input":
		return "input — the driver writes it before each scan"
	case "output":
		return "output — the logic writes it, the driver reads it after each scan"
	case "setpoint":
		return "setpoint — seeded, operator-writable"
	case "state":
		return "state — logic-owned, retained across scans"
	}
	return role
}

// detail is the one-line summary shown beside a completion item.
func (t ProjectTag) detail() string {
	parts := []string{}
	if t.Type != "" {
		parts = append(parts, t.Type)
	}
	if t.Role != "" {
		parts = append(parts, t.Role)
	}
	if t.Unit != "" {
		parts = append(parts, t.Unit)
	}
	return strings.Join(parts, " · ")
}

// manifestTag looks up one tag by name for the project governing uri.
func (s *Server) manifestTag(uri, name string) (ProjectTag, bool) {
	for _, t := range s.projectTagsFor(uri) {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return ProjectTag{}, false
}

// projectTagsFor resolves a document URI to its project's tags. Documents
// that are not files (untitled:, vscode-vfs:) have no project.
func (s *Server) projectTagsFor(uri string) []ProjectTag {
	path, ok := uriToPath(uri)
	if !ok {
		return nil
	}
	return projectTags(path)
}

// inVarExternal reports whether line (1-based) falls inside a VAR_EXTERNAL
// block. A text scan rather than an analysis lookup, because completion is
// asked mid-keystroke when the buffer usually does not parse.
func inVarExternal(text string, line int) bool {
	inBlock := false
	for i, l := range strings.Split(text, "\n") {
		if i+1 > line {
			break
		}
		switch upper := strings.ToUpper(strings.TrimSpace(l)); {
		case strings.HasPrefix(upper, "VAR_EXTERNAL"):
			inBlock = true
		case strings.HasPrefix(upper, "END_VAR"):
			inBlock = false
		case strings.HasPrefix(upper, "VAR"):
			// VAR / VAR_INPUT / VAR_OUTPUT / VAR_TEMP open a different block.
			inBlock = false
		}
	}
	return inBlock
}
