package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
)

// Version is stamped into serverInfo so `naut lsp` and the extension
// can be correlated in logs. Release binaries inject the tag via -ldflags
// "-X .../internal/lsp.Version=X.Y.Z" (see .goreleaser.yaml); when nothing
// is stamped, init derives it from module build info so a
// `go install ...@vX.Y.Z` build reports the module version instead of a
// hand-maintained constant that drifts. Plain source builds report "dev".
var Version = ""

func init() {
	if Version != "" {
		return
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimPrefix(bi.Main.Version, "v"); v != "" && v != "(devel)" {
			Version = v
			return
		}
	}
	Version = "dev"
}

// Server hosts LSP sessions over a single connection (normally stdio).
// One Server serves one editor process; document state is per-connection.
type Server struct {
	w        *writer
	docs     map[string]*document
	// libDiags: library URI → reporting document URI → the errors that
	// document's compile found inside that library (publishLibDiags).
	libDiags map[string]map[string][]Diagnostic
	statics  []CompletionItem
	exited   bool
	shutdown bool
}

// document is the server's copy of an open editor buffer plus the analysis
// derived from its current text.
type document struct {
	text string
	an   analysis
	// test is set for a `*_test.yaml` acceptance suite: the document is
	// YAML with ST expressions embedded in it, not an IEC program, and
	// every handler answers from this instead of from an.
	test *testDoc
}

// Serve runs a session until the client disconnects or sends exit.
func Serve(r io.Reader, w io.Writer) error {
	s := &Server{
		w:       &writer{out: w},
		docs:    map[string]*document{},
		statics: staticCompletions(),
	}
	in := bufio.NewReader(r)
	for !s.exited {
		msg, err := readMessage(in)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		s.dispatch(msg)
	}
	return nil
}

func (s *Server) dispatch(m *message) {
	switch m.Method {
	case "initialize":
		s.w.respond(m.ID, InitializeResult{
			Capabilities: ServerCapabilities{
				TextDocumentSync:      1, // Full: client resends the whole doc per change
				HoverProvider:         true,
				DefinitionProvider:    true,
				ReferencesProvider:    true,
				CompletionProvider:    &CompletionOpts{TriggerCharacters: []string{"."}},
				SignatureHelpProvider: signatureHelpOptions(),
				RenameProvider:        &RenameOpts{PrepareProvider: true},

				DocumentSymbolProvider: true, // symbols.go
			},
			ServerInfo: ServerInfo{Name: "nautilus-st-lsp", Version: Version},
		})
	case "initialized", "$/cancelRequest", "$/setTrace":
		// Notifications requiring no action.
	case "shutdown":
		s.shutdown = true
		s.w.respond(m.ID, nil)
	case "exit":
		s.exited = true
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if unmarshal(m.Params, &p) {
			s.setDocument(p.TextDocument.URI, p.TextDocument.Text)
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if unmarshal(m.Params, &p) && len(p.ContentChanges) > 0 {
			// Full sync: the last change wins and carries the whole text.
			s.setDocument(p.TextDocument.URI, p.ContentChanges[len(p.ContentChanges)-1].Text)
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if unmarshal(m.Params, &p) {
			delete(s.docs, p.TextDocument.URI)
			// Clear stale squiggles for the closed buffer.
			s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
				URI: p.TextDocument.URI, Diagnostics: []Diagnostic{},
			})
		}
	case "textDocument/definition":
		s.handleDefinition(m)
	case "textDocument/references":
		s.handleReferences(m)
	case "textDocument/hover":
		s.handleHover(m)
	case "textDocument/completion":
		s.handleCompletion(m)
	case "textDocument/signatureHelp":
		s.handleSignatureHelp(m)
	case "textDocument/prepareRename":
		s.handlePrepareRename(m)
	case "textDocument/rename":
		s.handleRename(m)
	case "textDocument/documentSymbol":
		s.handleDocumentSymbol(m)
	case "nautilus/descriptions":
		s.handleDescriptions(m) // descriptions.go: tag descriptions for the diagram editors
	default:
		if m.ID != nil { // unknown request: must answer; unknown notification: ignore
			s.w.respondError(m.ID, codeMethodNotFound, fmt.Sprintf("method %q not supported", m.Method))
		}
	}
}

func unmarshal(raw json.RawMessage, v any) bool {
	return json.Unmarshal(raw, v) == nil
}

// setDocument stores new text, re-analyzes, and pushes diagnostics. Sibling
// library files (TYPE / FB / FUNCTION-only .st in the same directory) join
// the compile as a prelude so cross-file types resolve — unsaved buffers of
// those siblings win over their on-disk content.
func (s *Server) setDocument(uri, text string) {
	if isTestDoc(uri) {
		td, diags := s.analyzeTest(uri, text)
		s.docs[uri] = &document{text: text, test: td}
		s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
			URI: uri, Diagnostics: nonNil(diags),
		})
		return
	}
	var e env
	if path, ok := uriToPath(uri); ok {
		e = s.envFor(uri, path)
	}
	doc := &document{text: text, an: analyzerFor(uri)(text, e)}
	if prev, ok := s.docs[uri]; ok && prev.test == nil {
		// Not even the declarations parse (a VAR block mid-edit): answer
		// hover and completion from the last version that did.
		doc.an.carryDeclarations(&prev.an)
	}
	s.docs[uri] = doc
	s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI: uri, Diagnostics: nonNil(doc.an.Diags),
	})
	s.publishLibDiags(uri, doc.an.libDiags)
}

// envFor is the project a document at path compiles in: its libraries
// (unsaved buffers win over disk) and the manifest's tags.
func (s *Server) envFor(uri, path string) env {
	prelude, _, segs := stproject.PreludeParts(path, s.otherBuffers(uri))
	return env{
		prelude:      prelude,
		preludeLines: strings.Count(prelude, "\n"),
		segs:         segs,
		tags:         projectTagDefs(path),
	}
}

// publishLibDiags reports errors that lie in a library on the library's
// own file (#199), once, instead of on every document that composes it.
// A library open in the editor reports its own errors from its own
// analysis, so it is left alone here. Several documents may compose the
// same broken library: each one's findings are kept per document and the
// library's list is their union, so one clean document does not clear
// what another still sees.
func (s *Server) publishLibDiags(from string, found []libDiag) {
	if s.libDiags == nil {
		s.libDiags = map[string]map[string][]Diagnostic{}
	}
	touched := map[string]bool{}
	for lib, by := range s.libDiags {
		if _, had := by[from]; had {
			delete(by, from)
			touched[lib] = true
		}
	}
	for _, d := range found {
		lib := pathToURI(d.path)
		if lib == from {
			continue
		}
		text := ""
		if raw, err := os.ReadFile(d.path); err == nil {
			text = string(raw)
		}
		r := spanRange(text, d.pos, d.end)
		if d.transpile {
			r = lineRange(text, 1)
		}
		if s.libDiags[lib] == nil {
			s.libDiags[lib] = map[string][]Diagnostic{}
		}
		s.libDiags[lib][from] = append(s.libDiags[lib][from], Diagnostic{
			Range: r, Severity: SeverityError, Source: "nautilus-st", Message: d.msg,
		})
		touched[lib] = true
	}
	for lib := range touched {
		if _, open := s.docs[lib]; open {
			continue
		}
		var union []Diagnostic
		seen := map[string]bool{}
		for _, ds := range s.libDiags[lib] {
			for _, d := range ds {
				k := fmt.Sprintf("%d:%d:%s", d.Range.Start.Line, d.Range.Start.Character, d.Message)
				if !seen[k] {
					seen[k] = true
					union = append(union, d)
				}
			}
		}
		if len(union) == 0 {
			delete(s.libDiags, lib)
		}
		s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: lib, Diagnostics: nonNil(union)})
	}
}

// uriToPath converts a file:// URI to a filesystem path. Non-file schemes
// (untitled:, vscode-vfs:, ...) report ok=false — those documents compile
// without a project prelude.
func uriToPath(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "file://") {
		return "", false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", false
	}
	p := u.Path
	// Windows URIs arrive as file:///C:/dir/file.st.
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p), true
}

// nonNil keeps empty diagnostic lists serializing as [] not null.
func nonNil(d []Diagnostic) []Diagnostic {
	if d == nil {
		return []Diagnostic{}
	}
	return d
}

// positional decodes the shared TextDocumentPositionParams payload and
// resolves the document plus the identifier under the cursor.
func (s *Server) positional(m *message) (doc *document, uri, word string, wr Range, pos Position, ok bool) {
	var p TextDocumentPositionParams
	if !unmarshal(m.Params, &p) {
		s.w.respondError(m.ID, codeInvalidParams, "bad position params")
		return nil, "", "", Range{}, Position{}, false
	}
	doc, found := s.docs[p.TextDocument.URI]
	if !found {
		s.w.respond(m.ID, nil)
		return nil, "", "", Range{}, Position{}, false
	}
	word, wr = wordAt(doc.text, p.Position)
	return doc, p.TextDocument.URI, word, wr, p.Position, true
}

func (s *Server) handleDefinition(m *message) {
	doc, uri, word, _, pos, ok := s.positional(m)
	if !ok {
		return
	}
	// A test file's names live in the project's programs and libraries, not
	// in the suite; there is nothing in this document to jump to.
	if doc.test != nil {
		s.w.respond(m.ID, nil)
		return
	}
	sym := (*Symbol)(nil)
	if word != "" {
		sym = doc.an.lookup(word, pos.Line+1)
	}
	if sym != nil && sym.Implicit {
		// A tag in scope without a declaration here: its declaration is
		// the manifest's tags[].name entry.
		if loc, ok := manifestTagLocation(uri, sym.Name); ok {
			s.w.respond(m.ID, loc)
			return
		}
	}
	if sym == nil || sym.Pos.Line == 0 {
		s.w.respond(m.ID, nil)
		return
	}
	s.w.respond(m.ID, Location{URI: uri, Range: declRange(doc.text, sym)})
}

// declRange spans the symbol's name at its declaration site.
func declRange(text string, sym *Symbol) Range {
	l := lineText(text, sym.Pos.Line)
	col := sym.Pos.Col - 1
	// The parser anchors VarDecl.Pos at the declaration; make sure the range
	// covers the name itself even if the position points at the line start.
	if idx := strings.Index(l, sym.Name); idx >= 0 && (col < 0 || col >= len(l) || !strings.HasPrefix(l[col:], sym.Name)) {
		col = idx
	}
	if col < 0 {
		col = 0
	}
	return Range{
		Start: Position{Line: sym.Pos.Line - 1, Character: col},
		End:   Position{Line: sym.Pos.Line - 1, Character: col + len(sym.Name)},
	}
}

func (s *Server) handleHover(m *message) {
	doc, uri, word, wr, pos, ok := s.positional(m)
	if !ok {
		return
	}
	if doc.test != nil {
		s.hoverTest(m, doc, uri, word, wr, pos)
		return
	}
	if word == "" {
		s.w.respond(m.ID, nil)
		return
	}
	sym := doc.an.lookup(word, pos.Line+1)
	// A VAR_EXTERNAL name is a project tag, and what the author usually
	// wants to know is what the manifest says about it — its unit, what it
	// is, and how it moves through the scan — not that it is a REAL.
	if tag, found := s.manifestTag(uri, word); found &&
		(sym == nil || strings.EqualFold(sym.BlockKind, "VAR_EXTERNAL")) {
		s.w.respond(m.ID, Hover{
			Contents: MarkupContent{Kind: "markdown", Value: tag.hoverDoc()},
			Range:    &wr,
		})
		return
	}
	if sym == nil {
		// Not a declared symbol — but it may be a type name from a project
		// library file (e.g. hovering "Analog_Input" in a declaration).
		if def, ok := doc.an.typeExpansion(word); ok {
			s.w.respond(m.ID, Hover{
				Contents: MarkupContent{Kind: "markdown", Value: "```iec-st\nTYPE " + def + "\n```"},
				Range:    &wr,
			})
			return
		}
		s.w.respond(m.ID, nil)
		return
	}
	s.w.respond(m.ID, Hover{
		Contents: MarkupContent{Kind: "markdown", Value: symbolHover(&doc.an, sym)},
		Range:    &wr,
	})
}

// symbolHover renders a declared symbol as markdown. Shared so a name means
// the same thing wherever it is hovered — in a program, or inside an ST
// expectation expression in a test file.
func symbolHover(an *analysis, sym *Symbol) string {
	var b strings.Builder
	switch sym.BlockKind {
	case "FUNCTION_BLOCK":
		fmt.Fprintf(&b, "```iec-st\nFUNCTION_BLOCK %s\n```", sym.Name)
	case "FUNCTION":
		fmt.Fprintf(&b, "```iec-st\nFUNCTION %s : %s\n```", sym.Name, sym.Datatype)
	case "TYPE":
		// Show the full definition, not just the name.
		if def, ok := an.typeExpansion(sym.Name); ok {
			fmt.Fprintf(&b, "```iec-st\nTYPE %s\n```", def)
		} else {
			fmt.Fprintf(&b, "```iec-st\nTYPE %s : %s\n```", sym.Name, sym.Datatype)
		}
	default:
		fmt.Fprintf(&b, "```iec-st\n%s : %s\n```\n\n%s", sym.Name, sym.Datatype, sym.BlockKind)
		if sym.Container != "" {
			fmt.Fprintf(&b, " — %s", sym.Container)
		}
		// A variable of a UDT type gets the type's structure expanded
		// beneath, TypeScript-style — including types declared in sibling
		// library files.
		if def, ok := an.typeExpansion(sym.Datatype); ok {
			fmt.Fprintf(&b, "\n\n```iec-st\nTYPE %s\n```", def)
		}
	}
	return b.String()
}

func (s *Server) handleCompletion(m *message) {
	doc, uri, _, _, pos, ok := s.positional(m)
	if !ok {
		return
	}
	if doc.test != nil {
		s.completeTest(m, doc, pos)
		return
	}
	// Inside VAR_EXTERNAL the question is "which of the project's tags do I
	// want to bind?", and only nautilus.yaml can answer it. Elsewhere in a
	// program the tags are already in scope (#177/#210) — they come from
	// the analysis's implicit symbols below, like any declared name.
	if inVarExternal(doc.text, pos.Line+1) {
		declared := map[string]bool{}
		for i := range doc.an.Symbols {
			declared[strings.ToLower(doc.an.Symbols[i].Name)] = true
		}
		var items []CompletionItem
		for _, t := range s.projectTagsFor(uri) {
			if declared[strings.ToLower(t.Name)] {
				continue // already bound in this POU
			}
			text := t.Name
			if t.Type != "" {
				text = t.Name + " : " + t.Type + ";"
			}
			items = append(items, CompletionItem{
				Label:         t.Name,
				Kind:          CompletionKindVariable,
				Detail:        t.detail(),
				Documentation: &MarkupContent{Kind: "markdown", Value: t.hoverDoc()},
				InsertText:    text,
			})
		}
		if len(items) > 0 {
			s.w.respond(m.ID, items)
			return
		}
	}
	// After a dot, offer the members of the base expression's type —
	// "PIT_001.| " lists Analog_Input's members, chains and array indexing
	// included ("Plt[3].Header.|"). Nothing else is meaningful there.
	line := lineText(doc.text, pos.Line+1)
	if base, path, isMember := memberContext(line, pos.Character); isMember {
		// An empty list, not null: the dot is answered, there is just
		// nothing to offer on an unknown base.
		items := []CompletionItem{}
		if t, ok := doc.an.resolveChain(base, path, pos.Line+1); ok {
			items = append(items, doc.an.memberCompletions(t)...)
		}
		s.w.respond(m.ID, items)
		return
	}
	container := doc.an.containerAt(pos.Line + 1)
	items := make([]CompletionItem, 0, len(s.statics)+len(doc.an.Symbols))
	for i := range doc.an.Symbols {
		sym := &doc.an.Symbols[i]
		// Offer locals of the current POU plus file-scope names; hide other
		// POUs' locals, which aren't referencable here.
		if sym.Container != "" && sym.Container != container {
			continue
		}
		// A block body does not see tags implicitly (#177/#210).
		if sym.Implicit && container != "" {
			continue
		}
		kind := CompletionKindVariable
		switch sym.BlockKind {
		case "FUNCTION_BLOCK":
			kind = CompletionKindClass
		case "FUNCTION":
			kind = CompletionKindFunction
		case "TYPE":
			kind = CompletionKindStruct
		}
		item := CompletionItem{
			Label:  sym.Name,
			Kind:   kind,
			Detail: strings.TrimSpace(sym.Datatype + " " + strings.ToLower(sym.BlockKind)),
		}
		if sym.Implicit {
			// A tag in scope without a declaration: what the manifest says
			// about it is the useful part.
			if t, ok := s.manifestTag(uri, sym.Name); ok {
				item.Detail = t.detail()
				item.Documentation = &MarkupContent{Kind: "markdown", Value: t.hoverDoc()}
			}
		}
		items = append(items, item)
	}
	items = append(items, s.statics...)
	s.w.respond(m.ID, items)
}
