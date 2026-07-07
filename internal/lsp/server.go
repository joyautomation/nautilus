package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Version is stamped into serverInfo so `nautilus lsp` and the extension
// can be correlated in logs.
const Version = "0.2.0"

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
				CompletionProvider: &CompletionOpts{},
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

// setDocument stores new text, re-analyzes, and pushes diagnostics.
func (s *Server) setDocument(uri, text string) {
	doc := &document{text: text, an: analyze(text)}
	s.docs[uri] = doc
	s.w.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI: uri, Diagnostics: nonNil(doc.an.Diags),
	})
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
	doc, _, word, wr, pos, ok := s.positional(m)
	if !ok {
		return
	}
	if word == "" {
		s.w.respond(m.ID, nil)
		return
	}
	sym := doc.an.lookup(word, pos.Line+1)
	if sym == nil {
		s.w.respond(m.ID, nil)
		return
	}
	var b strings.Builder
	switch sym.BlockKind {
	case "FUNCTION_BLOCK":
		fmt.Fprintf(&b, "```iec-st\nFUNCTION_BLOCK %s\n```", sym.Name)
	case "FUNCTION":
		fmt.Fprintf(&b, "```iec-st\nFUNCTION %s : %s\n```", sym.Name, sym.Datatype)
	case "TYPE":
		fmt.Fprintf(&b, "```iec-st\nTYPE %s : %s\n```", sym.Name, sym.Datatype)
	default:
		fmt.Fprintf(&b, "```iec-st\n%s : %s\n```\n\n%s", sym.Name, sym.Datatype, sym.BlockKind)
		if sym.Container != "" {
			fmt.Fprintf(&b, " — %s", sym.Container)
		}
	}
	s.w.respond(m.ID, Hover{
		Contents: MarkupContent{Kind: "markdown", Value: b.String()},
		Range:    &wr,
	})
}

func (s *Server) handleCompletion(m *message) {
	doc, _, _, _, pos, ok := s.positional(m)
	if !ok {
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
