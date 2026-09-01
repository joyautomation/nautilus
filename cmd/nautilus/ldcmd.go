package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/ld"
)

const ldUsage = `nautilus ld — Ladder Diagram tools

Usage:
  nautilus ld graph <file> [at]
                             Emit the ladder render model for a .ld file as
                             JSON on stdout: rungs of contacts, branches,
                             coils, and in-rung function blocks, in canonical
                             ladder order. Used by the VS Code ladder
                             preview. "-" reads source from stdin, and an
                             optional second argument names the path that
                             buffer belongs to, so the project's library
                             files (a PROGRAM-less .ld/.fbd/.st holding
                             FUNCTION_BLOCKs) are in scope for a user
                             block's power pins. On a parse error, emits
                             {"error": "..."} and exits 1.
  nautilus ld edit           Apply a structural edit op to .ld source. Reads
                             {"source": "...", "op": {...}, "file": "..."}
                             JSON on stdin ("file" optional, the path the
                             source belongs to — see graph above) and
                             writes {"edits": [...]} — the rung-level text
                             edits realizing the op (1-based, end-exclusive).
                             Ops address the render model: setRef, toggleNeg,
                             setCoilMode, setArgs, insert, delete, addLeg,
                             wrapBranch, addRung, renameRung, deleteRung,
                             move, setComment, addComment, setRungComment,
                             declareVar, deleteVar. On a rejected op, emits
                             {"error": "..."} and exits 1.
`

func runLD(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, ldUsage)
		return 2
	}
	switch args[0] {
	case "graph":
		return runLDGraph(args[1:])
	case "edit":
		return runLDEdit()
	default:
		fmt.Fprintf(os.Stderr, "nautilus ld: unknown subcommand %q\n\n%s", args[0], ldUsage)
		return 2
	}
}

func runLDEdit() int {
	var req struct {
		Source string    `json:"source"`
		Op     ld.EditOp `json:"op"`
		// File, when given, names the .ld on disk so the project's library
		// sources are in scope for a user block's power pins.
		File string `json:"file,omitempty"`
	}
	enc := json.NewEncoder(os.Stdout)
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil || req.Source == "" {
		_ = enc.Encode(map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"})
		return 2
	}
	var libs []string
	if req.File != "" {
		_, libs, _ = stproject.PreludeSources(req.File, nil)
	}
	edits, err := ld.ApplyEdit(req.Source, req.Op, libs...)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	_ = enc.Encode(map[string]any{"edits": edits})
	return 0
}

func runLDGraph(args []string) int {
	enc := json.NewEncoder(os.Stdout)
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprint(os.Stderr, ldUsage)
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
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 2
	}
	// The project's library sources are in scope, so an FB element shows
	// the pins the rung's power really uses (a user block's, not IN/Q).
	var libs []string
	if at != "" {
		_, libs, _ = stproject.PreludeSources(at, nil)
	}
	model, err := ld.Graph(string(src), libs...)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	_ = enc.Encode(model)
	return 0
}
