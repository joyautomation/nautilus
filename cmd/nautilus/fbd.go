package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/joyautomation/nautilus/lang/fbd"
)

const fbdUsage = `nautilus fbd — Function Block Diagram tools

Usage:
  nautilus fbd graph <file>   Emit the diagram render model for a .fbd file as
                              JSON on stdout: nodes (input/block/fb/coil, with
                              pins and a left-to-right layer index) and edges
                              (output pin -> input pin, negation, feedback).
                              Used by the VS Code diagram preview. "-" reads
                              source from stdin. On a parse error, emits
                              {"error": "..."} and exits 1.
  nautilus fbd edit           Apply a structural edit op to .fbd source. Reads
                              {"source": "...", "op": {...}} JSON on stdin and
                              writes {"edits": [...]} — the minimal text edits
                              realizing the op (1-based, end-exclusive spans).
                              Ops address render-model node ids: setLiteral,
                              toggleNot, rewire, rename, deleteNode. On a
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
		fmt.Fprintf(os.Stderr, "nautilus fbd: unknown subcommand %q\n\n%s", args[0], fbdUsage)
		return 2
	}
}

func runFBDEdit() int {
	var req struct {
		Source string     `json:"source"`
		Op     fbd.EditOp `json:"op"`
	}
	enc := json.NewEncoder(os.Stdout)
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil || req.Source == "" {
		_ = enc.Encode(map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"})
		return 2
	}
	edits, err := fbd.ApplyEdit(req.Source, req.Op)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	if edits == nil {
		edits = []fbd.TextEdit{}
	}
	if err := enc.Encode(map[string]any{"edits": edits}); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus fbd edit:", err)
		return 2
	}
	return 0
}

func runFBDGraph(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, fbdUsage)
		return 2
	}
	var src []byte
	var err error
	if args[0] == "-" {
		src, err = io.ReadAll(os.Stdin)
	} else {
		src, err = os.ReadFile(args[0])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus fbd graph:", err)
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	model, gerr := fbd.Graph(string(src))
	if gerr != nil {
		// Machine-readable error on stdout so the preview panel can show it,
		// plus non-zero exit for scripted use.
		_ = enc.Encode(map[string]string{"error": gerr.Error()})
		return 1
	}
	if err := enc.Encode(model); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus fbd graph:", err)
		return 2
	}
	return 0
}
