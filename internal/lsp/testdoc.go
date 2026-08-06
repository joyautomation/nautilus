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

// exprRegion is one ST expression's span inside the YAML, in editor
// coordinates.
type exprRegion struct {
	line       int // 0-based
	start, end int // 0-based character span within that line
	text       string
}

// testDoc is the analysis of one suite file.
type testDoc struct {
	regions []exprRegion
	tags    []ProjectTag  // what nautilus.yaml declares, for documentation
	build   *projectBuild // nil when the project doesn't currently compile
}

func (t *testDoc) regionAt(p Position) *exprRegion {
	for i := range t.regions {
		r := &t.regions[i]
		if r.line == p.Line && p.Character >= r.start && p.Character <= r.end {
			return r
		}
	}
	return nil
}

// diagRange spans the whole expression. The compiler positions an error at
// statement granularity and the expression is exactly one statement, so
// there is no finer span to be honest about — the same reason a .st file
// squiggles from the start of the offending statement.
func (r exprRegion) diagRange() Range {
	return Range{
		Start: Position{Line: r.line, Character: r.start},
		End:   Position{Line: r.line, Character: r.end},
	}
}

// testRegions finds the ST expressions in a suite. It mirrors
// Expect.UnmarshalYAML exactly: under `expect:` and `always:`, a scalar is
// an expression, a mapping is tag matchers, and a sequence mixes the two.
//
// A file that does not parse yields no regions and therefore no
// diagnostics. That is deliberate: YAML syntax belongs to the YAML
// extension and the schema, and a file half-typed should not be squiggled
// twice by two sources that word it differently.
func testRegions(text string) []exprRegion {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(text), &root); err != nil {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []exprRegion
	add := func(n *yaml.Node) {
		// A block scalar spanning lines has no single span to squiggle;
		// saying nothing beats pointing somewhere wrong.
		if n.Kind != yaml.ScalarNode || strings.Contains(n.Value, "\n") {
			return
		}
		line := n.Line - 1
		if line < 0 || line >= len(lines) {
			return
		}
		start := n.Column - 1
		if n.Style == yaml.SingleQuotedStyle || n.Style == yaml.DoubleQuotedStyle {
			start++ // Column points at the quote, the value starts after it
		}
		end := min(start+len(n.Value), len(lines[line]))
		if start < 0 || start >= end {
			return
		}
		out = append(out, exprRegion{line: line, start: start, end: end, text: n.Value})
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
				if k.Value == "expect" || k.Value == "always" {
					switch v.Kind {
					case yaml.ScalarNode:
						add(v)
					case yaml.SequenceNode:
						for _, item := range v.Content {
							add(item)
						}
					}
					continue
				}
				walk(v)
			}
		}
	}
	walk(&root)
	return out
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
	td := &testDoc{regions: testRegions(text)}
	path, ok := uriToPath(uri)
	if !ok {
		return td, nil
	}
	td.tags = projectTags(path)
	entry := projectBuildFor(path)
	if entry == nil {
		// No project, or none that has ever compiled. There is nothing to
		// check an expression against, and guessing would mean squiggling
		// names that are perfectly real.
		return td, nil
	}
	td.build = entry.build

	var diags []Diagnostic
	for _, r := range td.regions {
		verdict := entry.check(r.text)
		if verdict == nil {
			continue
		}
		diags = append(diags, Diagnostic{
			Range:    r.diagRange(),
			Severity: SeverityError,
			Source:   "nautilus-test",
			Message:  verdict.Error(),
		})
	}
	return td, diags
}

func (s *Server) hoverTest(m *message, doc *document, uri, word string, wr Range, pos Position) {
	if word == "" || doc.test.regionAt(pos) == nil {
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
	if b := doc.test.build; b != nil {
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
	case td.regionAt(pos) != nil:
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
