package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/joyautomation/nautilus/internal/diagram"
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
	enc := json.NewEncoder(os.Stdout)
	req, err := io.ReadAll(os.Stdin)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"})
		return 2
	}
	out, code := diagram.Edit("fbd", req)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "naut fbd edit:", err)
		return 2
	}
	return code
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
	model, gerr := diagram.Graph("fbd", string(src), at)
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
