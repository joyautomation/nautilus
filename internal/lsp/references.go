package lsp

import (
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/sfc"
	"github.com/joyautomation/nautilus/lang/st"
)

// Find all references: the cross-reference every vendor IDE answers on day
// one ("where is this tag used?"). It is rename's machinery read-only —
// the same scan for identifiers, the same lookup() deciding whether each
// one resolves to the symbol under the cursor — so the two can never
// disagree about what "this symbol" means. The scope rules are rename's:
//
//   - a POU-local variable is found only inside its POU;
//   - a VAR_EXTERNAL name is a project tag: every program that binds it
//     (.st, and the .fbd/.ld/.sfc programs, reported on the diagram
//     file's own lines), plus where the manifest names it — tags[].name
//     (the declaration), tasks[].dt-tag, tag-meta and tag-classes, in
//     nautilus.yaml and its tag-files;
//   - a member (t1.Q, Tank.Level) is found as that member OF THAT
//     instance — t1.Q and t1(Q => …), never another TON's Q or a local Q.
//
// Two places the scan is stricter than rename's: an identifier after a dot
// is a member, not the name being searched for, and one before := / =>
// inside a call's parentheses is a formal parameter (IN, PT), not a
// variable. And an identifier the lexer did not produce as an identifier —
// the T of T#2s — is not one either.

// ReferenceParams is the textDocument/references request.
type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext       `json:"context"`
}

// ReferenceContext says whether the declaration itself is wanted.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// refTarget is what a references request is about: a declared symbol, or
// a member path below one (t1.Q is root t1, path [Q]).
type refTarget struct {
	root *Symbol
	path []string
}

// refHit is one occurrence; decl marks a declaration site (the VAR line,
// a program's VAR_EXTERNAL binding, the manifest's tags[].name).
type refHit struct {
	r    Range
	decl bool
}

func (s *Server) handleReferences(m *message) {
	var p ReferenceParams
	if !unmarshal(m.Params, &p) {
		s.w.respondError(m.ID, codeInvalidParams, "bad references params")
		return
	}
	doc, found := s.docs[p.TextDocument.URI]
	if !found || doc.test != nil {
		s.w.respond(m.ID, nil)
		return
	}
	word, wr := wordAt(doc.text, p.Position)
	if word == "" || isKeyword(word) {
		s.w.respond(m.ID, nil)
		return
	}
	uri := p.TextDocument.URI
	path, hasPath := uriToPath(uri)
	prelude := ""
	if hasPath {
		prelude, _ = stproject.Prelude(path, s.otherBuffers(uri))
	}
	self := newRefFile(uri, doc.text, &doc.an, prelude)
	target, ok := self.targetAt(word, wr)
	if !ok {
		s.w.respond(m.ID, nil)
		return
	}

	byURI := map[string][]refHit{uri: self.find(target)}
	if target.root.BlockKind == "VAR_EXTERNAL" && hasPath {
		s.tagReferences(path, target, byURI)
	}
	s.w.respond(m.ID, locations(uri, byURI, p.Context.IncludeDeclaration))
}

// locations flattens hits into the response: the requesting document
// first, then the rest by URI, each in source order.
func locations(first string, byURI map[string][]refHit, includeDecl bool) []Location {
	uris := make([]string, 0, len(byURI))
	for u := range byURI {
		if u != first {
			uris = append(uris, u)
		}
	}
	sort.Strings(uris)
	uris = append([]string{first}, uris...)
	out := []Location{}
	for _, u := range uris {
		hits := byURI[u]
		sort.Slice(hits, func(i, j int) bool { return posLess(hits[i].r.Start, hits[j].r.Start) })
		for i, h := range hits {
			if i > 0 && hits[i-1].r == h.r {
				continue
			}
			if h.decl && !includeDecl {
				continue
			}
			out = append(out, Location{URI: u, Range: h.r})
		}
	}
	return out
}

func posLess(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// tagReferences adds a tag's occurrences beyond the requesting file: every
// other program that binds it in a program-level VAR_EXTERNAL (unsaved
// buffers win over disk), and the manifest plus its tag-files.
func (s *Server) tagReferences(path string, t refTarget, byURI map[string][]refHit) {
	mpath, ok := findManifest(path)
	if !ok {
		return
	}
	dir := filepath.Dir(mpath)
	overrides := map[string]string{}
	for otherURI, otherDoc := range s.docs {
		p, ok := uriToPath(otherURI)
		if !ok || otherDoc.test != nil {
			continue
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			rel = filepath.ToSlash(rel)
			if !strings.Contains(rel, "/") || stproject.InLibDir(rel) {
				overrides[rel] = otherDoc.text
			}
		}
	}
	if comp, err := stproject.ComposeAll(dir, overrides); err == nil {
		preludeLines := strings.Count(comp.Prelude, "\n")
		for _, prog := range comp.Programs {
			progPath := filepath.Join(dir, prog.File)
			if progPath == path {
				continue
			}
			an := analyzerFor(prog.File)(prog.Body, comp.Prelude, preludeLines)
			var bound *Symbol
			for i := range an.Symbols {
				sy := &an.Symbols[i]
				if sy.BlockKind == "VAR_EXTERNAL" && sy.Container == "" && strings.EqualFold(sy.Name, t.root.Name) {
					bound = sy
					break
				}
			}
			if bound == nil {
				continue // a same-named local elsewhere is a different name
			}
			f := newRefFile(prog.File, prog.Body, &an, comp.Prelude)
			if hits := f.find(refTarget{root: bound, path: t.path}); len(hits) > 0 {
				u := pathToURI(progPath)
				byURI[u] = append(byURI[u], hits...)
			}
		}
	}
	if len(t.path) > 0 {
		return // the manifest names tags, not their members
	}
	name := t.root.Name
	for _, tag := range projectTags(path) {
		if strings.EqualFold(tag.Name, name) {
			name = tag.Name // the manifest's spelling: YAML keys are case-sensitive
			break
		}
	}
	refs, err := renameTagInManifest(mpath, name, name)
	if err != nil {
		return
	}
	decls := manifestTagDecls(mpath, name)
	for file, edits := range refs {
		u := pathToURI(file)
		for _, e := range edits {
			byURI[u] = append(byURI[u], refHit{r: e.Range, decl: decls[file][e.Range]})
		}
	}
}

// manifestTagDecls marks which of the manifest's mentions of a tag are its
// declaration: the tags[].name entry, in nautilus.yaml or a tag-file.
func manifestTagDecls(mpath, name string) map[string]map[Range]bool {
	out := map[string]map[Range]bool{}
	root, err := parseYAMLFile(mpath)
	if err != nil {
		return out
	}
	files := []string{mpath}
	for _, tf := range yamlStringSeq(mappingValue(root, "tag-files")) {
		files = append(files, filepath.Join(filepath.Dir(mpath), tf))
	}
	for _, f := range files {
		doc := root
		if f != mpath {
			if doc, err = parseYAMLFile(f); err != nil {
				continue
			}
		}
		for _, item := range seqItems(mappingValue(doc, "tags")) {
			if n := mappingValue(item, "name"); n != nil && n.Kind == yaml.ScalarNode && n.Value == name {
				if out[f] == nil {
					out[f] = map[Range]bool{}
				}
				out[f][scalarRange(n)] = true
			}
		}
	}
	return out
}

// otherBuffers maps every other open document's path to its text — the
// override shape stproject.Prelude expects, as setDocument builds it.
func (s *Server) otherBuffers(uri string) map[string]string {
	out := map[string]string{}
	for otherURI, otherDoc := range s.docs {
		if otherURI == uri {
			continue
		}
		if p, ok := uriToPath(otherURI); ok {
			out[p] = otherDoc.text
		}
	}
	return out
}

// ─── One file ───────────────────────────────────────────────────────────────

// refFile is one source file prepared for a reference scan: its text as
// the user wrote it, its analysis with POU scopes taken from that text,
// the lexical context of every identifier, and — for a diagram, whose
// analysis is of its ST view — the ST-view-line → source-line map that
// diagnostics use, to put declarations back on the diagram's own lines.
type refFile struct {
	text    string
	an      analysis
	ctx     map[Position]identCtx
	lineMap []int // index = ST-view line-1, value = 1-based source line; nil = identity
}

func newRefFile(name, text string, an *analysis, prelude string) *refFile {
	f := &refFile{text: text, an: *an, ctx: identContexts(text)}
	// The analysis of a diagram is of its transpiled ST, so its scopes are
	// in those coordinates; the scan runs over the diagram itself. POU
	// boundaries are keywords in both, so scan them from the source.
	f.an.scopes = scanScopes(text)
	f.lineMap = stViewLineMap(name, text, prelude)
	return f
}

// stViewLineMap is the transpiler's line map for a diagram — composed
// through the FBD hop for ladder, as analyzeLD composes its diagnostics —
// or nil for ST (identity) and for a diagram that does not transpile.
func stViewLineMap(name, text, prelude string) []int {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".fbd":
		if _, m, err := fbd.TranspileWithLines(text); err == nil {
			return m
		}
	case ".sfc":
		if _, m, err := sfc.TranspileWithLines(text); err == nil {
			return m
		}
	case ".ld":
		fbdText, ldMap, err := ld.TranspileWithLines(text, prelude)
		if err != nil {
			return nil
		}
		_, fbdMap, err := fbd.TranspileWithLines(fbdText)
		if err != nil {
			return nil
		}
		out := make([]int, len(fbdMap))
		for i, l := range fbdMap {
			out[i] = 1
			if l >= 1 && l <= len(ldMap) {
				out[i] = ldMap[l-1]
			}
		}
		return out
	}
	return nil
}

// sourceLine maps an analysis (ST-view) line to the user's file.
func (f *refFile) sourceLine(line int) int {
	if f.lineMap == nil || line < 1 || line > len(f.lineMap) {
		return line
	}
	return f.lineMap[line-1]
}

// declStart is where sym's declaration starts in the user's file.
func (f *refFile) declStart(sym *Symbol) (Position, bool) {
	if sym.Pos.Line == 0 {
		return Position{}, false
	}
	at := *sym
	if sym.BlockKind != "SFC step" { // steps are positioned on the .sfc already
		at.Pos.Line = f.sourceLine(sym.Pos.Line)
	}
	return declRange(f.text, &at).Start, true
}

// targetAt resolves the identifier under the cursor into what to search for.
func (f *refFile) targetAt(word string, wr Range) (refTarget, bool) {
	line := wr.Start.Line + 1
	c, isIdent := f.ctx[wr.Start]
	if !isIdent {
		return refTarget{}, false // the T of T#2s
	}
	switch {
	case c.member:
		base, path, ok := memberContext(lineText(f.text, line), wr.End.Character)
		if !ok {
			return refTarget{}, false
		}
		root := f.an.lookup(base, line)
		if root == nil {
			return refTarget{}, false
		}
		return refTarget{root: root, path: append(path, word)}, true
	case c.formal:
		// IN in t1(IN := …) is t1.IN; in LIMIT(IN := …) it is nothing
		// declared — a function's parameter, not a variable.
		root := f.an.lookup(c.callee, line)
		if root == nil || isPOUSymbol(root) {
			return refTarget{}, false
		}
		return refTarget{root: root, path: []string{word}}, true
	}
	sym := f.an.lookup(word, line)
	if sym == nil {
		return refTarget{}, false
	}
	return refTarget{root: sym}, true
}

func isPOUSymbol(s *Symbol) bool {
	switch s.BlockKind {
	case "FUNCTION_BLOCK", "FUNCTION", "TYPE":
		return true
	}
	return false
}

// find returns every occurrence of t in the file.
func (f *refFile) find(t refTarget) []refHit {
	name := t.root.Name
	if len(t.path) > 0 {
		name = t.path[len(t.path)-1]
	}
	decl, hasDecl := Position{}, false
	if len(t.path) == 0 {
		decl, hasDecl = f.declStart(t.root)
	}
	var hits []refHit
	for _, r := range identOccurrences(f.text, name) {
		c, isIdent := f.ctx[r.Start]
		if !isIdent {
			continue // the lexer saw no identifier here (T#2s, 16#FF)
		}
		line := r.Start.Line + 1
		if len(t.path) == 0 {
			if c.member || c.formal {
				continue
			}
			if got := f.an.lookup(t.root.Name, line); got != nil && sameSymbol(got, t.root) {
				hits = append(hits, refHit{r: r, decl: hasDecl && r.Start == decl})
			}
			continue
		}
		if c.formal {
			if len(t.path) == 1 {
				if got := f.an.lookup(c.callee, line); got != nil && sameSymbol(got, t.root) {
					hits = append(hits, refHit{r: r})
				}
			}
			continue
		}
		if !c.member {
			continue
		}
		base, path, ok := memberContext(lineText(f.text, line), r.End.Character)
		if !ok || len(path) != len(t.path)-1 {
			continue
		}
		same := true
		for i := range path {
			if !strings.EqualFold(path[i], t.path[i]) {
				same = false
				break
			}
		}
		if !same {
			continue
		}
		if got := f.an.lookup(base, line); got != nil && sameSymbol(got, t.root) {
			hits = append(hits, refHit{r: r})
		}
	}
	return hits
}

// identCtx is the lexical context of one identifier token.
type identCtx struct {
	member bool   // right after a '.': a member name, not a variable
	formal bool   // a parameter name: before := / => inside a call's (…)
	callee string // for a formal, the name the parentheses call
}

// identContexts lexes text with the real lexer and records, keyed by the
// 0-based start of each identifier token, whether it is a member or a
// formal parameter. Positions that are not in the map are not identifiers.
func identContexts(text string) map[Position]identCtx {
	toks := st.Lex(text)
	out := make(map[Position]identCtx, len(toks)/2)
	var callees []string
	for i, t := range toks {
		switch t.Type {
		case st.TokenLParen:
			callee := ""
			if i > 0 && toks[i-1].Type == st.TokenIdent {
				callee = toks[i-1].Literal
				// FBD / LD declare-and-call an instance in one line:
				// "lic : PID(…)", "m101:MotorStarter(…)" — the call is lic's.
				if i > 2 && toks[i-2].Type == st.TokenColon && toks[i-3].Type == st.TokenIdent {
					callee = toks[i-3].Literal
				}
			}
			callees = append(callees, callee)
		case st.TokenRParen:
			if len(callees) > 0 {
				callees = callees[:len(callees)-1]
			}
		case st.TokenIdent:
			var c identCtx
			c.member = i > 0 && toks[i-1].Type == st.TokenDot
			if len(callees) > 0 && i+1 < len(toks) &&
				(toks[i+1].Type == st.TokenAssign || toks[i+1].Type == st.TokenOutputAssign) {
				c.formal = true
				c.callee = callees[len(callees)-1]
			}
			out[Position{Line: t.Line - 1, Character: t.Col - 1}] = c
		}
	}
	return out
}
