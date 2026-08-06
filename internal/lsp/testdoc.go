package lsp

// Acceptance suites are ST too.
//
// An expectation in a `*_test.yaml` may be an ST boolean expression —
// `ABS(TempC - TempSP) < 0.5` — compiled by the same compiler as the logic
// under test. Until now that only paid off when the test ran. This gives
// those expressions the treatment a .st file already gets: a typo squiggles
// as it is typed, hover says what a tag is, and completion offers the
// project's tags and its own library FUNCTIONs.
//
// The expressions are compiled through acceptance.CheckExpr, the same
// wrapper the runner builds, so the editor's verdict and `nautilus test`
// cannot disagree.
//
// Everything OUTSIDE an expression is YAML, and belongs to the YAML
// extension and the JSON schema. The one exception is a tag name in key
// position (`given: { TempC: 45.0 }`), which no schema can enumerate
// because only the project knows its tags.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/runtime"
)

// isTestDoc reports whether a URI names an acceptance suite.
func isTestDoc(uri string) bool {
	return strings.HasSuffix(strings.ToLower(uri), acceptance.SuffixTest)
}

// span is a piece of the YAML that means something in IEC terms — an ST
// expression, or a tag name in key position — in editor coordinates.
type span struct {
	line       int // 0-based
	start, end int // 0-based character span within that line
	text       string
}

// testDoc is the analysis of one suite file.
type testDoc struct {
	exprs []span        // ST expectation expressions
	keys  []span        // tag names in key position under given/expect/always
	tags  []ProjectTag  // what nautilus.yaml declares, for documentation
	build *projectBuild // nil when the project doesn't currently compile
}

func (t *testDoc) exprAt(p Position) *span { return at(t.exprs, p) }
func (t *testDoc) keyAt(p Position) *span  { return at(t.keys, p) }

func at(spans []span, p Position) *span {
	for i := range spans {
		if s := &spans[i]; s.line == p.Line && p.Character >= s.start && p.Character <= s.end {
			return s
		}
	}
	return nil
}

// diagRange spans the whole thing. For an expression that is the honest
// answer: the compiler positions an error at statement granularity and an
// expression is exactly one statement — the same reason a .st file
// squiggles from the start of the offending statement.
func (s span) diagRange() Range {
	return Range{
		Start: Position{Line: s.line, Character: s.start},
		End:   Position{Line: s.line, Character: s.end},
	}
}

// scanSuite finds the parts of a suite that are IEC rather than YAML.
//
// It mirrors the loader exactly. Under `expect:` and `always:`
// (Expect.UnmarshalYAML) a scalar is an ST expression, a mapping is tag
// matchers, and a sequence mixes the two; under `given:` a mapping's keys
// are tags. Only the FIRST level of keys counts — inside
// `TempC: {near: 65.0}` the inner keys are matcher operators, not tags.
//
// A file that does not parse yields nothing, and therefore no diagnostics.
// That is deliberate: YAML syntax belongs to the YAML extension and the
// schema, and a file half-typed should not be squiggled twice by two
// sources that word it differently.
func scanSuite(text string) (exprs, keys []span) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	spanOf := func(n *yaml.Node) (span, bool) {
		// A block scalar spanning lines has no single span to squiggle;
		// saying nothing beats pointing somewhere wrong.
		if n.Kind != yaml.ScalarNode || strings.Contains(n.Value, "\n") {
			return span{}, false
		}
		line := n.Line - 1
		if line < 0 || line >= len(lines) {
			return span{}, false
		}
		start := n.Column - 1
		if n.Style == yaml.SingleQuotedStyle || n.Style == yaml.DoubleQuotedStyle {
			start++ // Column points at the quote, the value starts after it
		}
		end := min(start+len(n.Value), len(lines[line]))
		if start < 0 || start >= end {
			return span{}, false
		}
		return span{line: line, start: start, end: end, text: n.Value}, true
	}
	addExpr := func(n *yaml.Node) {
		if s, ok := spanOf(n); ok {
			exprs = append(exprs, s)
		}
	}
	addKeys := func(m *yaml.Node) {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if s, ok := spanOf(m.Content[i]); ok {
				keys = append(keys, s)
			}
		}
	}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				switch k.Value {
				case "expect", "always":
					switch v.Kind {
					case yaml.ScalarNode:
						addExpr(v)
					case yaml.MappingNode:
						addKeys(v)
					case yaml.SequenceNode:
						for _, item := range v.Content {
							switch item.Kind {
							case yaml.ScalarNode:
								addExpr(item)
							case yaml.MappingNode:
								addKeys(item)
							}
						}
					}
				case "given":
					if v.Kind == yaml.MappingNode {
						addKeys(v)
					}
				default:
					walk(v)
				}
			}
		}
	}
	walk(&root)
	return exprs, keys
}

// ─── the project an expression compiles against ────────────────────────

// projectBuild is what an expectation expression needs to compile: the tags
// in scope with their real types, and the project's library sources.
type projectBuild struct {
	ext  acceptance.Externals
	libs []string
	lib  analysis // the libraries analyzed, for hover and completion
}

// buildEntry caches one project's build plus the verdict on every
// expression checked against it.
type buildEntry struct {
	fp    string
	build *projectBuild
	// exprs maps expression → compile verdict; a nil VALUE is a clean
	// compile, and absence is "not checked yet".
	exprs map[string]error
}

type buildCache struct {
	mu      sync.Mutex
	entries map[string]*buildEntry
}

var builds = buildCache{entries: map[string]*buildEntry{}}

// projectBuildFor compiles the project governing path, cached against the
// modtimes of its sources. A keystroke in a test file must not recompile
// four programs; a save in a program file must.
func projectBuildFor(path string) *buildEntry {
	mpath, ok := findManifest(path)
	if !ok {
		return nil
	}
	dir := filepath.Dir(mpath)
	fp := fingerprintProject(dir)

	builds.mu.Lock()
	defer builds.mu.Unlock()
	prev := builds.entries[dir]
	if prev != nil && prev.fp == fp {
		return prev
	}
	b, err := buildProject(dir)
	if err != nil {
		// A program being edited does not compile, and its own editor
		// already says so — a test file must not fill with errors about a
		// neighbour. Serve the last good build; the failure is not cached,
		// so the next save retries.
		return prev
	}
	e := &buildEntry{fp: fp, build: b, exprs: map[string]error{}}
	builds.entries[dir] = e
	return e
}

// check returns the compiler's verdict on one expression, computing it once
// per (project build, expression).
func (e *buildEntry) check(expr string) error {
	builds.mu.Lock()
	defer builds.mu.Unlock()
	if verdict, done := e.exprs[expr]; done {
		return verdict
	}
	verdict := acceptance.CheckExpr(e.build.ext, e.build.libs, expr)
	e.exprs[expr] = verdict
	return verdict
}

// fingerprintProject summarizes the inputs a build depends on: the manifest
// and the program/library files. Everything else in the directory — the
// README, the test files themselves — must not invalidate it, or every
// keystroke would recompile the project.
//
// On-disk content deliberately, not open buffers: `nautilus test` reads
// disk, so an unsaved edit to a program legitimately isn't in effect yet.
func fingerprintProject(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if name != project.ManifestName && !isProgramFile(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s:%d:%d\n", name, info.ModTime().UnixNano(), info.Size())
	}
	return b.String()
}

func isProgramFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".st", ".fbd", ".ld", ".sfc":
		return true
	}
	return false
}

// buildProject loads and compiles a project without running it. New()
// compiles every task and seeds the tag store; it starts no goroutine and
// touches no driver, so this is safe to do on a save.
func buildProject(dir string) (*projectBuild, error) {
	p, err := project.Load(os.DirFS(dir))
	if err != nil {
		return nil, err
	}
	o := p.Runtime
	// Nothing here scans, but a driver the manifest configured for real
	// hardware has no business being held by an editor.
	o.Driver = nil
	rt, err := runtime.New(o)
	if err != nil {
		return nil, err
	}
	b := &projectBuild{ext: acceptance.ExternalsOf(rt), libs: o.Libraries}
	if len(o.Libraries) > 0 {
		b.lib = analyze(stproject.Join(o.Libraries, ""), "", 0)
	}
	return b, nil
}

// ─── analysis, hover, completion ───────────────────────────────────────

// analyzeTest builds a suite's analysis and its diagnostics.
func (s *Server) analyzeTest(uri, text string) (*testDoc, []Diagnostic) {
	td := &testDoc{}
	td.exprs, td.keys = scanSuite(text)
	path, ok := uriToPath(uri)
	if !ok {
		return td, nil
	}
	td.tags = projectTags(path)
	entry := projectBuildFor(path)
	if entry == nil {
		// No project, or none that has ever compiled. There is nothing to
		// check a name against, and guessing would mean squiggling names
		// that are perfectly real.
		return td, nil
	}
	td.build = entry.build

	var diags []Diagnostic
	for _, e := range td.exprs {
		verdict := entry.check(e.text)
		if verdict == nil {
			continue
		}
		diags = append(diags, Diagnostic{
			Range:    e.diagRange(),
			Severity: SeverityError,
			Source:   "nautilus-test",
			Message:  verdict.Error(),
		})
	}
	diags = append(diags, td.unknownTagDiags()...)
	return td, diags
}

// unknownTagDiags reports a tag name in key position that the project does
// not have. `nautilus test` already refuses these — "no tag %q in this
// project" — so this is the same verdict, moved to where the mistake was
// made rather than where it is discovered.
func (t *testDoc) unknownTagDiags() []Diagnostic {
	known := t.knownTags()
	if len(known) == 0 {
		return nil
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)

	var out []Diagnostic
	for _, k := range t.keys {
		// `expect: { main.integral: ... }` names a program's retained local,
		// which nothing outside a run can enumerate.
		if strings.Contains(k.text, ".") || known[k.text] {
			continue
		}
		msg := fmt.Sprintf("no tag %q in this project", k.text)
		if did := nearest(k.text, names); did != "" {
			msg += fmt.Sprintf(" — did you mean %q?", did)
		}
		out = append(out, Diagnostic{
			Range:    k.diagRange(),
			Severity: SeverityError,
			Source:   "nautilus-test",
			Message:  msg,
		})
	}
	return out
}

// knownTags is every name a test may use, as the union of what the programs
// bind and what the manifest declares.
//
// The union is deliberately wider than either authority the runner
// consults — `given` checks the manifest's declarations, `expect` reads the
// live store — because the cost of the two errors is not symmetric. Missing
// a typo means the test still fails loudly when run; squiggling a real tag
// means the editor is lying about working code.
func (t *testDoc) knownTags() map[string]bool {
	known := map[string]bool{}
	if t.build != nil {
		for n := range t.build.ext {
			known[n] = true
		}
	}
	for _, tag := range t.tags {
		known[tag.Name] = true
	}
	return known
}

// nearest returns the closest known name within a two-edit distance, or ""
// — a typo is nearly always a transposition or a doubled key, and anything
// looser starts suggesting unrelated tags with confidence it hasn't earned.
func nearest(name string, known []string) string {
	best, bestDist := "", 3
	for _, candidate := range known {
		// A case-only difference is always the answer; IEC identifiers are
		// case-insensitive but tag names are not.
		if strings.EqualFold(candidate, name) {
			return candidate
		}
		if d := editDistance(name, candidate); d < bestDist {
			best, bestDist = candidate, d
		}
	}
	return best
}

// editDistance is Levenshtein over two short identifiers, one row at a time.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func (s *Server) hoverTest(m *message, doc *document, uri, word string, wr Range, pos Position) {
	if word == "" || (doc.test.exprAt(pos) == nil && doc.test.keyAt(pos) == nil) {
		s.w.respond(m.ID, nil)
		return
	}
	if tag, found := s.manifestTag(uri, word); found {
		s.w.respond(m.ID, Hover{
			Contents: MarkupContent{Kind: "markdown", Value: tag.hoverDoc()},
			Range:    &wr,
		})
		return
	}
	// A library FUNCTION is only callable from an expression; in key
	// position the name can only ever be a tag.
	if b := doc.test.build; b != nil && doc.test.exprAt(pos) != nil {
		if sym := fileScopeSymbol(&b.lib, word); sym != nil {
			s.w.respond(m.ID, Hover{
				Contents: MarkupContent{Kind: "markdown", Value: symbolHover(&b.lib, sym)},
				Range:    &wr,
			})
			return
		}
	}
	s.w.respond(m.ID, nil)
}

func (s *Server) completeTest(m *message, doc *document, pos Position) {
	td := doc.test
	switch {
	case td.exprAt(pos) != nil:
		items := td.tagItems()
		if b := td.build; b != nil {
			for i := range b.lib.Symbols {
				sym := &b.lib.Symbols[i]
				// Only FUNCTIONs are callable from an expression: a function
				// block needs an instance, and a type needs a declaration.
				if sym.Container == "" && sym.BlockKind == "FUNCTION" {
					items = append(items, CompletionItem{
						Label:  sym.Name,
						Kind:   CompletionKindFunction,
						Detail: sym.Datatype + " — project library",
					})
				}
			}
		}
		items = append(items, s.exprStatics()...)
		s.w.respond(m.ID, items)
	case namesTags(doc.text, pos):
		s.w.respond(m.ID, td.tagItems())
	default:
		// Plain YAML. The schema owns the keys here.
		s.w.respond(m.ID, []CompletionItem{})
	}
}

// tagItems lists every tag a test may name — what the programs bind unioned
// with what the manifest declares — carrying the manifest's unit and
// description wherever it has them.
func (t *testDoc) tagItems() []CompletionItem {
	types := map[string]string{}
	if t.build != nil {
		for name, ty := range t.build.ext {
			types[name] = ty
		}
	}
	meta := make(map[string]ProjectTag, len(t.tags))
	for _, tag := range t.tags {
		meta[tag.Name] = tag
		if _, known := types[tag.Name]; !known {
			types[tag.Name] = tag.Type
		}
	}
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]CompletionItem, 0, len(names))
	for _, name := range names {
		tag, documented := meta[name]
		if !documented {
			tag = ProjectTag{Name: name}
		}
		// The compiled type is what an expression actually sees; the
		// manifest's init could only ever have been an inference.
		if ty := types[name]; ty != "" {
			tag.Type = ty
		}
		items = append(items, CompletionItem{
			Label:         name,
			Kind:          CompletionKindVariable,
			Detail:        tag.detail(),
			Documentation: &MarkupContent{Kind: "markdown", Value: tag.hoverDoc()},
		})
	}
	return items
}

// exprKeywords are the keywords that mean something inside an expression.
// The rest of ST's vocabulary is statements, and offering END_PROGRAM in a
// boolean expectation is noise.
var exprKeywords = map[string]bool{
	"TRUE": true, "FALSE": true, "AND": true, "OR": true,
	"XOR": true, "NOT": true, "MOD": true,
}

func (s *Server) exprStatics() []CompletionItem {
	out := make([]CompletionItem, 0, len(s.statics))
	for _, item := range s.statics {
		if item.Kind == CompletionKindFunction || exprKeywords[item.Label] {
			out = append(out, item)
		}
	}
	return out
}

// fileScopeSymbol finds a file-scope declaration by name — the FUNCTIONs,
// FUNCTION_BLOCKs, and TYPEs a library file publishes.
func fileScopeSymbol(an *analysis, name string) *Symbol {
	for i := range an.Symbols {
		if sym := &an.Symbols[i]; sym.Container == "" && strings.EqualFold(sym.Name, name) {
			return sym
		}
	}
	return nil
}

// ─── tag names in key position ─────────────────────────────────────────

// A step is a sequence item, so the key that opens a tag block is as often
// `- given:` as `given:`.
var (
	tagKeyRe    = regexp.MustCompile(`^\s*(?:-\s+)?(given|expect|always)\s*:`)
	tagIndentRe = regexp.MustCompile(`^[ \t]*`)
)

// namesTags reports whether a position sits where a TAG NAME belongs: the
// flow mapping on a given/expect/always line, or the block beneath one.
//
// A bounded walk back rather than a YAML parse, because completion is asked
// mid-keystroke when the buffer usually does not parse — and this must work
// precisely then.
func namesTags(text string, pos Position) bool {
	lines := strings.Split(text, "\n")
	if pos.Line < 0 || pos.Line >= len(lines) {
		return false
	}
	here := lines[pos.Line]
	if tagKeyRe.MatchString(here) {
		// On the key's own line only a flow mapping names tags; a bare
		// scalar there is an ST expression, handled as a region.
		return strings.Contains(here[:min(pos.Character, len(here))], "{")
	}
	indent := len(tagIndentRe.FindString(here))
	for line := pos.Line - 1; line >= 0 && pos.Line-line <= 40; line-- {
		text := lines[line]
		if strings.TrimSpace(text) == "" {
			continue
		}
		// A line at or right of ours is a sibling or a child, not the key
		// that opened the block we are in.
		if len(tagIndentRe.FindString(text)) >= indent {
			continue
		}
		return tagKeyRe.MatchString(text)
	}
	return false
}
