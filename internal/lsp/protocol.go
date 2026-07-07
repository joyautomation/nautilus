// Package lsp implements a minimal Language Server Protocol (3.17) server
// for IEC 61131-3 Structured Text, reusing the nautilus lang/st compiler for
// diagnostics, definitions, hover, and completion. Pure stdlib: JSON-RPC 2.0
// framing over stdio is hand-rolled in jsonrpc.go.
//
// Only the protocol surface nautilus actually serves is typed here — this is
// not a general LSP library.
package lsp

// Position is a zero-based line/character offset. Characters are UTF-16
// code units per the LSP spec; ST source is ASCII in practice, so byte
// offsets are used directly.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open [start, end) span in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range inside a specific document.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Diagnostic severities (the subset used).
const (
	SeverityError   = 1
	SeverityWarning = 2
)

// Diagnostic is a compiler finding published to the editor.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// PublishDiagnosticsParams is the payload of textDocument/publishDiagnostics.
type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// TextDocumentItem is the full document sent in didOpen.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// TextDocumentIdentifier names a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// DidOpenTextDocumentParams / DidChangeTextDocumentParams /
// DidCloseTextDocumentParams are the document-sync payloads. Sync is
// full-text (TextDocumentSyncKind Full), so each change event carries the
// whole document.
type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   TextDocumentIdentifier      `json:"textDocument"`
	ContentChanges []TextDocumentContentChange `json:"contentChanges"`
}

type TextDocumentContentChange struct {
	Text string `json:"text"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// TextDocumentPositionParams is the shared request payload for definition,
// hover, and completion.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Hover is the response to textDocument/hover.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// MarkupContent is markdown (or plaintext) hover/completion documentation.
type MarkupContent struct {
	Kind  string `json:"kind"` // "markdown" | "plaintext"
	Value string `json:"value"`
}

// CompletionItem kinds (the subset used).
const (
	CompletionKindFunction = 3
	CompletionKindVariable = 6
	CompletionKindClass    = 7 // used for function blocks
	CompletionKindKeyword  = 14
	CompletionKindStruct   = 22 // used for elementary/user types
)

// CompletionItem is a single completion suggestion.
type CompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// InitializeResult advertises the server's capabilities.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerCapabilities struct {
	TextDocumentSync   int             `json:"textDocumentSync"` // 1 = Full
	HoverProvider      bool            `json:"hoverProvider"`
	DefinitionProvider bool            `json:"definitionProvider"`
	CompletionProvider *CompletionOpts `json:"completionProvider,omitempty"`
}

type CompletionOpts struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}
