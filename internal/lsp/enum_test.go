package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestHashContext(t *testing.T) {
	cases := []struct {
		line string
		col  int
		want string
		ok   bool
	}{
		{"x := Mode#", 10, "Mode", true},
		{"x := Mode#Ru", 12, "Mode", true},
		{"x := 16#", 8, "16", true},
		{"x := Mode", 9, "", false},
		{"x := #", 6, "", false},
	}
	for _, c := range cases {
		got, ok := hashContext(c.line, c.col)
		if got != c.want || ok != c.ok {
			t.Errorf("hashContext(%q, %d) = %q, %v; want %q, %v", c.line, c.col, got, ok, c.want, c.ok)
		}
	}
}

// #238 / #176: after `Mode#` the completion offers Mode's members — an
// enumeration declared in a sibling library file — and the general list
// carries the project constants and members.
func TestEnumMemberCompletion(t *testing.T) {
	dir := t.TempDir()
	lib := "TYPE Mode : (Idle, Run, Fault); END_TYPE\nVAR_GLOBAL CONSTANT MAX_N : INT := 4; END_VAR\n"
	if err := os.WriteFile(filepath.Join(dir, "types.st"), []byte(lib), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := "PROGRAM P\nVAR m : Mode; END_VAR\nm := Mode#\nEND_PROGRAM\n"
	path := filepath.Join(dir, "program.st")
	if err := os.WriteFile(path, []byte(prog), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + path
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	s.recvResponse(id)
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: prog},
	}, false)
	s.recv() // diagnostics
	complete := func(line, char int) []CompletionItem {
		id := s.send("textDocument/completion", TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: line, Character: char},
		}, true)
		var items []CompletionItem
		if err := json.Unmarshal(s.recvResponse(id), &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	var got []string
	for _, it := range complete(2, len("m := Mode#")) {
		got = append(got, it.Label)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "Fault,Idle,Run" {
		t.Errorf("after Mode#: %v, want Fault, Idle, Run", got)
	}
	labels := map[string]bool{}
	for _, it := range complete(2, 0) {
		labels[it.Label] = true
	}
	for _, want := range []string{"MAX_N", "Run", "m"} {
		if !labels[want] {
			t.Errorf("general completion is missing %q", want)
		}
	}
}
