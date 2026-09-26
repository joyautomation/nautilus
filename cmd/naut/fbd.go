package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
)

const fbdUsage = `naut fbd — Function Block Diagram tools

Usage:
  naut fbd graph <file> [at]
                          Emit the diagram render model for a .fbd file as
                              JSON on stdout: nodes (input/block/fb/coil, with
                              pins and a left-to-right layer index), edges
                              (output pin -> input pin, negation, feedback)
                              and fbTypes, the palette's block catalog (the
                              standard blocks + every user FUNCTION_BLOCK in
                              scope, with pins). Used by the VS Code diagram
                              preview. "-" reads source from stdin; [at] then
                              names the file it belongs to, so the project's
                              library blocks are in scope. On a parse error,
                              emits {"error": "..."} and exits 1.
  naut fbd edit           Apply a structural edit op to .fbd source. Reads
                              {"source": "...", "op": {...}} JSON on stdin and
                              writes {"edits": [...]} — the minimal text edits
                              realizing the op (1-based, end-exclusive spans).
                              An optional "file" puts that file's project
                              libraries in scope (a library block's pins).
                              Ops address render-model node ids: setLiteral,
                              toggleNot, rewire, rename, deleteNode (one id,
                              or "nodes": a whole selection), init. Blank
                              source seeds a PROGRAM skeleton (named by the
                              op's "pou") and applies the op to it. On a
                              rejected op, emits {"error": "..."} and exits 1.
`

func runFBD(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, fbdUsage)
		return 2
	}
	switch args[0] {
	case "graph":
		return runFBDGraph(args[1:])
	case "edit":
		return runFBDEdit()
	default:
		fmt.Fprintf(os.Stderr, "naut fbd: unknown subcommand %q\n\n%s", args[0], fbdUsage)
		return 2
	}
}

func runFBDEdit() int {
	var req struct {
		Source string     `json:"source"`
		Op     fbd.EditOp `json:"op"`
		// File, when given, names the .fbd on disk so the project's library
		// sources are in scope for a user block's pins.
		File string `json:"file,omitempty"`
	}
	enc := json.NewEncoder(os.Stdout)
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		_ = enc.Encode(map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"})
		return 2
	}
	var libs []string
	if req.File != "" {
		_, libs, _ = stproject.PreludeSources(req.File, nil)
	}
	edits, err := fbd.ApplyEdit(req.Source, req.Op, libs...)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	if edits == nil {
		edits = []fbd.TextEdit{}
	}
	if err := enc.Encode(map[string]any{"edits": edits}); err != nil {
		fmt.Fprintln(os.Stderr, "naut fbd edit:", err)
		return 2
	}
	return 0
}

func runFBDGraph(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprint(os.Stderr, fbdUsage)
		return 2
	}
	var src []byte
	var err error
	// The file the source BELONGS to, for library resolution: the path
	// itself, or — reading an unsaved buffer from stdin — the optional
	// second argument naming where that buffer lives.
	at := args[0]
	if at == "-" {
		src, err = io.ReadAll(os.Stdin)
		at = ""
		if len(args) == 2 {
			at = args[1]
		}
	} else {
		src, err = os.ReadFile(args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut fbd graph:", err)
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	var libs []string
	if at != "" {
		_, libs, _ = stproject.PreludeSources(at, nil)
	}
	model, gerr := fbd.GraphWithLibs(string(src), libs)
	if gerr != nil {
		// Machine-readable error on stdout so the preview panel can show it,
		// plus non-zero exit for scripted use.
		_ = enc.Encode(map[string]string{"error": gerr.Error()})
		return 1
	}
	if err := enc.Encode(model); err != nil {
		fmt.Fprintln(os.Stderr, "naut fbd graph:", err)
		return 2
	}
	return 0
}
