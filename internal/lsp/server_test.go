package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// lspSession drives a Server end-to-end through the real JSON-RPC framing,
// the same way the VS Code client will.
type lspSession struct {
	t      *testing.T
	toSrv  *io.PipeWriter
	out    *bufio.Reader
	done   chan error
	nextID int
}

func startSession(t *testing.T) *lspSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(inR, outW) }()
	s := &lspSession{t: t, toSrv: inW, out: bufio.NewReader(outR), done: done}
	t.Cleanup(func() { inW.Close() })
	return s
}

func (s *lspSession) send(method string, params any, isRequest bool) int {
	s.t.Helper()
	m := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	id := 0
	if isRequest {
		s.nextID++
		id = s.nextID
		m["id"] = id
	}
	body, err := json.Marshal(m)
	if err != nil {
		s.t.Fatal(err)
	}
	fmt.Fprintf(s.toSrv, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return id
}

// recv reads one server->client message.
func (s *lspSession) recv() *message {
	s.t.Helper()
	m, err := readMessage(s.out)
	if err != nil {
		s.t.Fatalf("readMessage: %v", err)
	}
	return m
}

// recvResponse skips notifications until the response with the given id.
func (s *lspSession) recvResponse(id int) json.RawMessage {
	s.t.Helper()
	for i := 0; i < 10; i++ {
		m := s.recv()
		if m.ID == nil {
			continue
		}
		var got int
		if err := json.Unmarshal(*m.ID, &got); err != nil || got != id {
			continue
		}
		if len(m.Error) > 0 {
			s.t.Fatalf("error response: %s", m.Error)
		}
		return m.Result
	}
	s.t.Fatal("response never arrived")
	return nil
}

const sessionSrc = "PROGRAM P\nVAR\n  Level : REAL;\nEND_VAR\nLevel := bogus;\nEND_PROGRAM\n"

func TestServerSession(t *testing.T) {
	s := startSession(t)

	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	if !init.Capabilities.DefinitionProvider || init.Capabilities.TextDocumentSync != 1 {
		t.Fatalf("capabilities = %+v", init.Capabilities)
	}
	s.send("initialized", map[string]any{}, false)

	// didOpen on a broken program → publishDiagnostics with the lower error.
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: "file:///p.st", LanguageID: "iec-st", Version: 1, Text: sessionSrc},
	}, false)
	diagMsg := s.recv()
	if diagMsg.Method != "textDocument/publishDiagnostics" {
		t.Fatalf("expected diagnostics push, got %q", diagMsg.Method)
	}
	var pub PublishDiagnosticsParams
	if err := json.Unmarshal(diagMsg.Params, &pub); err != nil {
		t.Fatal(err)
	}
	if len(pub.Diagnostics) != 1 || pub.Diagnostics[0].Range.Start.Line != 4 {
		t.Fatalf("diagnostics = %+v", pub.Diagnostics)
	}

	// didChange fixing the program → empty diagnostics.
	fixed := "PROGRAM P\nVAR\n  Level : REAL;\nEND_VAR\nLevel := 1.0;\nEND_PROGRAM\n"
	s.send("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument:   TextDocumentIdentifier{URI: "file:///p.st"},
		ContentChanges: []TextDocumentContentChange{{Text: fixed}},
	}, false)
	if err := json.Unmarshal(s.recv().Params, &pub); err != nil {
		t.Fatal(err)
	}
	if len(pub.Diagnostics) != 0 {
		t.Fatalf("expected clean diagnostics, got %+v", pub.Diagnostics)
	}

	// definition on `Level` in the assignment (line 4, col 1) → its VAR decl.
	id = s.send("textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///p.st"},
		Position:     Position{Line: 4, Character: 2},
	}, true)
	var loc Location
	if err := json.Unmarshal(s.recvResponse(id), &loc); err != nil {
		t.Fatal(err)
	}
	if loc.Range.Start.Line != 2 {
		t.Fatalf("definition = %+v", loc)
	}

	// hover shows the declared type.
	id = s.send("textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///p.st"},
		Position:     Position{Line: 4, Character: 2},
	}, true)
	var hov Hover
	if err := json.Unmarshal(s.recvResponse(id), &hov); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(hov.Contents.Value), []byte("Level : REAL")) {
		t.Fatalf("hover = %q", hov.Contents.Value)
	}

	// completion includes the local variable and keywords.
	id = s.send("textDocument/completion", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: "file:///p.st"},
		Position:     Position{Line: 4, Character: 2},
	}, true)
	var items []CompletionItem
	if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
		t.Fatal(err)
	}
	var hasLevel, hasIf bool
	for _, it := range items {
		if it.Label == "Level" {
			hasLevel = true
		}
		if it.Label == "IF" {
			hasIf = true
		}
	}
	if !hasLevel || !hasIf {
		t.Fatalf("completion missing Level/IF (got %d items)", len(items))
	}

	// shutdown/exit terminates Serve cleanly.
	id = s.send("shutdown", nil, true)
	s.recvResponse(id)
	s.send("exit", nil, false)
	if err := <-s.done; err != nil {
		t.Fatalf("Serve returned %v", err)
	}
}
