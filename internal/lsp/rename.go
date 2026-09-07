package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/st"
)

// Rename is "every occurrence that resolves to this declaration", which is
// what makes it safe: a FUNCTION_BLOCK local that happens to share a tag's
// name is a different symbol and is left alone. The symbol index knows
// declarations, not references, so references are found by scanning the
// text for identifiers — skipping comments and string literals — and
// asking lookup() whether each one resolves to the symbol being renamed.
//
// A VAR_EXTERNAL name is a project tag, and a tag's identity is its name
// everywhere: every program that binds it, the manifest that declares it,
// the task that names it as dt-tag. So renaming one is a project-wide
// edit — one WorkspaceEdit, one refactor preview, one undo. Deliberately
// NOT covered (they fail loudly on their own, at `nautilus test` / HMI
// load): *_test.yaml suites, mimic bindings, Sparkplug metric lists.

const codeInvalidRequest = -32600

func (s *Server) handlePrepareRename(m *message) {
	doc, _, word, wr, pos, ok := s.positional(m)
	if !ok {
		return
	}
	if doc.test != nil || word == "" {
		s.w.respond(m.ID, nil)
		return
	}
	if isKeyword(word) {
		s.w.respondError(m.ID, codeInvalidRequest, fmt.Sprintf("%s is a keyword", word))
		return
	}
	sym := doc.an.lookup(word, pos.Line+1)
	if sym == nil || sym.Pos.Line == 0 {
		s.w.respondError(m.ID, codeInvalidRequest, fmt.Sprintf("no declaration of %s in this file to rename", word))
		return
	}
	s.w.respond(m.ID, PrepareRenameResult{Range: wr, Placeholder: word})
}

func (s *Server) handleRename(m *message) {
	var p RenameParams
	if !unmarshal(m.Params, &p) {
		s.w.respondError(m.ID, codeInvalidParams, "bad rename params")
		return
	}
	doc, found := s.docs[p.TextDocument.URI]
	if !found || doc.test != nil {
		s.w.respond(m.ID, nil)
		return
	}
	word, _ := wordAt(doc.text, p.Position)
	if word == "" {
		s.w.respond(m.ID, nil)
		return
	}
	newName := strings.TrimSpace(p.NewName)
	if !isIdentifier(newName) {
		s.w.respondError(m.ID, codeInvalidRequest, fmt.Sprintf("%q is not a valid identifier", newName))
		return
	}
	if isKeyword(newName) {
		s.w.respondError(m.ID, codeInvalidRequest, fmt.Sprintf("%s is a keyword", newName))
		return
	}
	sym := doc.an.lookup(word, p.Position.Line+1)
	if sym == nil || sym.Pos.Line == 0 {
		s.w.respondError(m.ID, codeInvalidRequest, fmt.Sprintf("no declaration of %s in this file to rename", word))
		return
	}
	if strings.EqualFold(word, newName) && word == newName {
		s.w.respond(m.ID, WorkspaceEdit{Changes: map[string][]TextEdit{}})
		return
	}
	// Refuse a collision in the same scope: the compiler would flag the
	// duplicate declaration, but the edit has already landed by then.
	if other := doc.an.lookup(newName, sym.Pos.Line); other != nil && other.Container == sym.Container {
		s.w.respondError(m.ID, codeInvalidRequest,
			fmt.Sprintf("%s is already declared here (%s, line %d)", other.Name, other.BlockKind, other.Pos.Line))
		return
	}

	edits := WorkspaceEdit{Changes: map[string][]TextEdit{}}
	edits.Changes[p.TextDocument.URI] = renameInDocument(doc.text, &doc.an, sym, newName)

	if sym.BlockKind == "VAR_EXTERNAL" {
		if err := s.renameTagAcrossProject(p.TextDocument.URI, sym.Name, newName, &edits); err != nil {
			s.w.respondError(m.ID, codeInvalidRequest, err.Error())
			return
		}
	}
	s.w.respond(m.ID, edits)
}

// renameInDocument returns the edits for every identifier in text that
// resolves to sym. Occurrences are matched case-insensitively (IEC
// identifiers are), and every one is rewritten to newName as typed.
func renameInDocument(text string, an *analysis, sym *Symbol, newName string) []TextEdit {
	var edits []TextEdit
	for _, r := range identOccurrences(text, sym.Name) {
		got := an.lookup(sym.Name, r.Start.Line+1)
		if got == nil || !sameSymbol(got, sym) {
			continue
		}
		edits = append(edits, TextEdit{Range: r, NewText: newName})
	}
	if edits == nil {
		edits = []TextEdit{}
	}
	return edits
}

func sameSymbol(a, b *Symbol) bool {
	return a.Pos == b.Pos && a.Container == b.Container && strings.EqualFold(a.Name, b.Name)
}

// renameTagAcrossProject adds the edits that keep a tag rename coherent
// beyond the current file: every other program that binds the tag in its
// VAR_EXTERNAL, and the manifest (plus any tag-files it includes).
func (s *Server) renameTagAcrossProject(uri, name, newName string, edits *WorkspaceEdit) error {
	path, ok := uriToPath(uri)
	if !ok {
		return nil // an untitled buffer has no project to walk
	}
	mpath, ok := findManifest(path)
	if !ok {
		return nil
	}
	for _, t := range projectTags(path) {
		if strings.EqualFold(t.Name, newName) {
			return fmt.Errorf("the manifest already declares a tag named %s", t.Name)
		}
	}
	dir := filepath.Dir(mpath)

	// Open buffers win over disk, keyed by base name as ComposeAll expects.
	overrides := map[string]string{}
	for otherURI, otherDoc := range s.docs {
		if p, ok := uriToPath(otherURI); ok && filepath.Dir(p) == dir {
			overrides[filepath.Base(p)] = otherDoc.text
		}
	}
	comp, err := stproject.ComposeAll(dir, overrides)
	if err != nil {
		return err
	}
	preludeLines := strings.Count(comp.Prelude, "\n")
	self := filepath.Base(path)
	for _, prog := range comp.Programs {
		if prog.File == self {
			continue
		}
		an := analyzerFor(prog.File)(prog.Body, comp.Prelude, preludeLines)
		// The tag is bound in this program only if its VAR_EXTERNAL declares
		// it; a same-named local elsewhere in the file is a different name.
		var bound *Symbol
		for i := range an.Symbols {
			sy := &an.Symbols[i]
			if sy.BlockKind == "VAR_EXTERNAL" && sy.Container == "" && strings.EqualFold(sy.Name, name) {
				bound = sy
				break
			}
		}
		if bound == nil {
			continue
		}
		progURI := pathToURI(filepath.Join(dir, prog.File))
		if e := renameInDocument(prog.Body, &an, bound, newName); len(e) > 0 {
			edits.Changes[progURI] = e
		}
	}

	manifestEdits, err := renameTagInManifest(mpath, name, newName)
	if err != nil {
		return err
	}
	for p, e := range manifestEdits {
		if len(e) > 0 {
			edits.Changes[pathToURI(p)] = e
		}
	}
	return nil
}

// analyzerFor picks the per-language analysis, as setDocument does.
func analyzerFor(file string) func(text, prelude string, preludeLines int) analysis {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".fbd":
		return analyzeFBD
	case ".ld":
		return analyzeLD
	case ".sfc":
		return analyzeSFC
	}
	return analyze
}

// renameTagInManifest edits, by YAML node position, every place the
// manifest refers to the tag by name: tags[].name, tasks[].dt-tag, the
// tag-meta key, and tag-classes members — in nautilus.yaml and in each
// tag-files entry (imported tag manifests declare tags the same way).
// Returns edits keyed by file path.
func renameTagInManifest(mpath, name, newName string) (map[string][]TextEdit, error) {
	out := map[string][]TextEdit{}
	files := []string{mpath}
	root, err := parseYAMLFile(mpath)
	if err != nil {
		return nil, fmt.Errorf("cannot rename in %s: %v", filepath.Base(mpath), err)
	}
	for _, tf := range yamlStringSeq(mappingValue(root, "tag-files")) {
		files = append(files, filepath.Join(filepath.Dir(mpath), tf))
	}
	for _, f := range files {
		doc := root
		if f != mpath {
			if doc, err = parseYAMLFile(f); err != nil {
				continue // a missing tag-file is `nautilus check`'s finding, not rename's
			}
		}
		var edits []TextEdit
		hit := func(n *yaml.Node) {
			if n != nil && n.Kind == yaml.ScalarNode && n.Value == name {
				edits = append(edits, TextEdit{Range: scalarRange(n), NewText: newName})
			}
		}
		for _, item := range seqItems(mappingValue(doc, "tags")) {
			hit(mappingValue(item, "name"))
		}
		for _, item := range seqItems(mappingValue(doc, "tasks")) {
			hit(mappingValue(item, "dt-tag"))
		}
		if meta := mappingValue(doc, "tag-meta"); meta != nil && meta.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(meta.Content); i += 2 {
				hit(meta.Content[i])
			}
		}
		if classes := mappingValue(doc, "tag-classes"); classes != nil && classes.Kind == yaml.MappingNode {
			for i := 1; i < len(classes.Content); i += 2 {
				for _, member := range seqItems(classes.Content[i]) {
					hit(member)
				}
			}
		}
		if len(edits) > 0 {
			out[f] = edits
		}
	}
	return out, nil
}

func parseYAMLFile(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return nil, err
	}
	// Unmarshal into a Node yields a DocumentNode wrapping the root mapping.
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0], nil
	}
	return &n, nil
}

// mappingValue returns the value node for key in a mapping node, or nil.
func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func seqItems(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func yamlStringSeq(n *yaml.Node) []string {
	var out []string
	for _, item := range seqItems(n) {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

// scalarRange is the LSP range of a scalar's text. yaml.v3 positions are
// 1-based and point at the first character of the scalar — for a quoted
// scalar that is the quote, so the edit keeps the quoting style by
// replacing only the value inside it.
func scalarRange(n *yaml.Node) Range {
	start := Position{Line: n.Line - 1, Character: n.Column - 1}
	if n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 {
		start.Character++
	}
	return Range{Start: start, End: Position{Line: start.Line, Character: start.Character + len(n.Value)}}
}

// identOccurrences finds every identifier in text equal (case-insensitively)
// to name, skipping (* block *) and // line comments and 'string' / "string"
// literals. Block comments nest per IEC 61131-3 3rd edition. Ranges are
// 0-based LSP ranges.
func identOccurrences(text, name string) []Range {
	var out []Range
	line, col := 0, 0
	i := 0
	advance := func(n int) {
		for k := 0; k < n && i < len(text); k++ {
			if text[i] == '\n' {
				line++
				col = 0
			} else {
				col++
			}
			i++
		}
	}
	for i < len(text) {
		c := text[i]
		switch {
		case c == '(' && i+1 < len(text) && text[i+1] == '*':
			// Skip nested block comment: (* ... (*  ... *) ... *)
			depth := 1
			advance(2) // consume the opening (*
			for i < len(text) && depth > 0 {
				switch {
				case text[i] == '(' && i+1 < len(text) && text[i+1] == '*':
					depth++
					advance(2)
				case text[i] == '*' && i+1 < len(text) && text[i+1] == ')':
					depth--
					advance(2)
				case text[i] == '\n':
					line++
					col = 0
					i++
				default:
					advance(1)
				}
			}
		case c == '/' && i+1 < len(text) && text[i+1] == '/':
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				return out
			}
			advance(end)
		case c == '\'' || c == '"':
			quote := c
			advance(1)
			for i < len(text) && text[i] != quote && text[i] != '\n' {
				if text[i] == '$' { // IEC escape: $' $" $$ $L $N …
					advance(1)
				}
				advance(1)
			}
			advance(1)
		case isIdentByte(c):
			startCol, startLine, startIdx := col, line, i
			for i < len(text) && isIdentByte(text[i]) {
				advance(1)
			}
			word := text[startIdx:i]
			if (word[0] < '0' || word[0] > '9') && strings.EqualFold(word, name) {
				out = append(out, Range{
					Start: Position{Line: startLine, Character: startCol},
					End:   Position{Line: startLine, Character: startCol + len(word)},
				})
			}
		default:
			advance(1)
		}
	}
	return out
}

func isIdentifier(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

func isKeyword(s string) bool {
	for _, k := range st.KeywordNames() {
		if strings.EqualFold(k, s) {
			return true
		}
	}
	return false
}

// pathToURI is the inverse of uriToPath.
func pathToURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") { // Windows drive paths get the leading slash
		p = "/" + p
	}
	return "file://" + p
}
