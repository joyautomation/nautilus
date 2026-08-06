package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/joyautomation/nautilus/internal/stproject"
)

// Version is stamped into serverInfo so `nautilus lsp` and the extension
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
				TextDocumentSync:   1, // Full: client resends the whole doc per change
				HoverProvider:      true,
				DefinitionProvider: true,
				CompletionProvider: &CompletionOpts{TriggerCharacters: []string{"."}},
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
	case "textDocument/hover":
		s.handleHover(m)
	case "textDocument/completion":
		s.handleCompletion(m)
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
	var prelude string
	var preludeLines int
	if path, ok := uriToPath(uri); ok {
		overrides := map[string]string{}
		for otherURI, otherDoc := range s.docs {
			if otherURI == uri {
				continue
			}
			if p, ok := uriToPath(otherURI); ok {
				overrides[p] = otherDoc.text
			}
		}
		prelude, preludeLines = stproject.Prelude(path, overrides)
	}
	an := analyze
	if strings.HasSuffix(strings.ToLower(uri), ".fbd") {
		an = analyzeFBD
	}
	if strings.HasSuffix(strings.ToLower(uri), ".ld") {
		an = analyzeLD
	}
	if strings.HasSuffix(strings.ToLower(uri), ".sfc") {
		an = analyzeSFC
	}
	doc := &document{text: text, an: an(text, prelude, preludeLines)}
	s.docs[uri] = doc
	s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI: uri, Diagnostics: nonNil(doc.an.Diags),
	})
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
	// want to bind?", and only nautilus.yaml can answer it. Elsewhere in the
	// program the answer would be actively wrong: an undeclared tag doesn't
	// compile, so offering one would suggest a name that cannot be used.
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
		var items []CompletionItem
		if t, ok := doc.an.resolveChain(base, path, pos.Line+1); ok {
			items = doc.an.memberCompletions(t)
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
		kind := CompletionKindVariable
		switch sym.BlockKind {
		case "FUNCTION_BLOCK":
			kind = CompletionKindClass
		case "FUNCTION":
			kind = CompletionKindFunction
		case "TYPE":
			kind = CompletionKindStruct
		}
		items = append(items, CompletionItem{
			Label:  sym.Name,
			Kind:   kind,
			Detail: strings.TrimSpace(sym.Datatype + " " + strings.ToLower(sym.BlockKind)),
		})
	}
	items = append(items, s.statics...)
	s.w.respond(m.ID, items)
}
