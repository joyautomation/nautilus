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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
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
}

type manifestEntry struct {
	mod   int64
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
	// readErr: the manifest itself does not read. The entry is then the
	// last good one (stale is false) or empty (stale is true: no tag is
	// known at all).
	readErr error
	stale   bool
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
	if e.stale {
		return true // nothing is known: any undeclared name may be a tag
	}
	for _, b := range e.badTags {
		if b.name != "" && strings.EqualFold(b.name, name) {
			return true
		}
	}
	return false
}

// modOf is the newest modtime among the manifest and its tag-files: a
// tags/*.yaml save must refresh hover and the diagrams' descriptions as
// surely as a nautilus.yaml one. A file that cannot be stat'ed counts as
// changed (-1 never matches a stored modtime).
func modOf(mpath string, files []string) int64 {
	st, err := os.Stat(mpath)
	if err != nil {
		return -1
	}
	mod := st.ModTime().UnixNano()
	for _, f := range files {
		fst, err := os.Stat(f)
		if err != nil {
			return -1
		}
		if m := fst.ModTime().UnixNano(); m > mod {
			mod = m
		}
	}
	return mod
}

var manifests = manifestCache{entries: map[string]*manifestEntry{}}

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

// manifestDiagnostics is what the manifest governing a program says is
// wrong with its tags, keyed by the file URI to show it on: each tag that
// does not read on its tags[].name (in nautilus.yaml or the tag-file
// declaring it), and a manifest that does not read at all on the line its
// error names. nil when there is nothing to say.
func manifestDiagnostics(mpath string, e *manifestEntry) map[string][]Diagnostic {
	if e == nil || (e.readErr == nil && len(e.badTags) == 0) {
		return nil
	}
	out := map[string][]Diagnostic{}
	add := func(file string, r Range, msg string) {
		uri := pathToURI(file)
		out[uri] = append(out[uri], Diagnostic{Range: r, Severity: SeverityError, Source: "nautilus", Message: msg})
	}
	if e.readErr != nil {
		add(mpath, manifestErrRange(mpath, e.readErr), e.readErr.Error())
	}
	for _, b := range e.badTags {
		file, r := mpath, manifestErrRange(mpath, nil)
		if b.name != "" {
			if f, rr, ok := firstDecl(manifestTagDecls(mpath, b.name)); ok {
				file, r = f, rr
			}
		}
		add(file, r, b.err.Error())
	}
	return out
}

// firstDecl is the first declaration in a manifestTagDecls result, in
// file order.
func firstDecl(decls map[string]map[Range]bool) (string, Range, bool) {
	files := make([]string, 0, len(decls))
	for f := range decls {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		var best Range
		found := false
		for r := range decls[f] {
			if !found || r.Start.Line < best.Start.Line {
				best, found = r, true
			}
		}
		if found {
			return f, best, true
		}
	}
	return "", Range{}, false
}

var yamlLineRe = regexp.MustCompile(`\bline (\d+)\b`)

// manifestErrRange places a manifest-level error: on the line a YAML
// error in the manifest names, else on the manifest's first line.
func manifestErrRange(mpath string, err error) Range {
	text := ""
	if raw, rerr := os.ReadFile(mpath); rerr == nil {
		text = string(raw)
	}
	line := 1
	if err != nil && strings.HasPrefix(err.Error(), filepath.Base(mpath)+":") {
		if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
			if n, cerr := strconv.Atoi(m[1]); cerr == nil && n >= 1 {
				line = n
			}
		}
	}
	return lineRange(text, line)
}

// publishManifestDiags publishes the manifest's own diagnostics (#518) for
// the project governing path, and clears the ones it published before that
// no longer hold. The editor does not send nautilus.yaml to the server, so
// this runs as the project's programs are analyzed: a save of the manifest
// shows on the next keystroke in any of them.
func (s *Server) publishManifestDiags(path string) {
	mpath, ok := findManifest(path)
	if !ok {
		return
	}
	found := manifestDiagnostics(mpath, manifestFor(path))
	if s.manifestDiags == nil {
		s.manifestDiags = map[string]map[string][]Diagnostic{}
	}
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
	s.manifestDiags[mpath] = found
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
	manifests.mu.Lock()
	defer manifests.mu.Unlock()
	prev, hadPrev := manifests.entries[mpath]
	if hadPrev && prev.mod == modOf(mpath, prev.files) {
		return prev
	}
	tags, defs, bad, files, logix, err := readTags(mpath)
	if err != nil {
		// A manifest being edited is invalid for most of the keystrokes it
		// takes to add a tag. Serving the last good answer beats completion
		// blinking out mid-edit — and the failure is deliberately NOT
		// cached, so the next keystroke retries. The error rides along, for
		// the diagnostic on the manifest.
		if hadPrev {
			e := *prev
			e.readErr = err
			return &e
		}
		return &manifestEntry{readErr: err, stale: true}
	}
	e := &manifestEntry{mod: modOf(mpath, files), files: files, tags: tags, defs: defs, badTags: bad, logix: logix}
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
