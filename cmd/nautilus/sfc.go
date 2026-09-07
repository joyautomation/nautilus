package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/joyautomation/nautilus/lang/sfc"
)

const sfcUsage = `nautilus sfc — Sequential Function Chart tools

Usage:
  nautilus sfc check <file>   Parse a .sfc file and run its structural checks
                              (docs/design/sfc.md §5.1): step/
                              transition/action name and reference
                              resolution, unreachable/dead-end steps,
                              qualifier support, alt-priority ambiguity,
                              and more. Prints gcc-style diagnostics.
                              Chart-shape checks only; "nautilus check"
                              runs these and then the ST-level hop
                              (transpile + lower) as well.
  nautilus sfc graph <file>   Emit the diagram render model for a .sfc file
                              as JSON on stdout: steps (with action
                              associations), transitions (with derived
                              kind: normal/alt/simDiverge/simConverge),
                              action blocks, header vars, comments, and any
                              pinned (* @layout *) positions. Used by the
                              VS Code diagram preview. "-" reads source from
                              stdin. On a parse error, emits {"error": "..."}
                              and exits 1.
  nautilus sfc edit           Apply a structural edit op to .sfc source.
                              Reads {"source": "...", "op": {...}} JSON on
                              stdin and writes {"edits": [...]} — the
                              minimal text edits realizing the op (1-based,
                              end-exclusive spans). Ops address render-model
                              ids (st:/tr:/ac:): addStep, deleteStep,
                              renameStep, addTransition, deleteTransition,
                              setCondition, setTransitionEnds, addAssoc,
                              setAssoc, deleteAssoc, setActionBody,
                              insertAlternativeBranch,
                              insertSimultaneousBranch, setLayout,
                              clearLayout, setComment. On a rejected op,
                              emits {"error": "..."} and exits 1.
`

func runSFC(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, sfcUsage)
		return 2
	}
	switch args[0] {
	case "check":
		return runSFCCheck(args[1:])
	case "graph":
		return runSFCGraph(args[1:])
	case "edit":
		return runSFCEdit()
	default:
		fmt.Fprintf(os.Stderr, "nautilus sfc: unknown subcommand %q\n\n%s", args[0], sfcUsage)
		return 2
	}
}

func runSFCGraph(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, sfcUsage)
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
		fmt.Fprintln(os.Stderr, "nautilus sfc graph:", err)
		return 2
	}
	enc := json.NewEncoder(os.Stdout)
	model, gerr := sfc.Graph(string(src))
	if gerr != nil {
		// Machine-readable error on stdout so the preview panel can show it,
		// plus non-zero exit for scripted use — same contract as fbd/ld graph.
		_ = enc.Encode(map[string]string{"error": gerr.Error()})
		return 1
	}
	if err := enc.Encode(model); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sfc graph:", err)
		return 2
	}
	return 0
}

func runSFCEdit() int {
	var req struct {
		Source string     `json:"source"`
		Op     sfc.EditOp `json:"op"`
	}
	enc := json.NewEncoder(os.Stdout)
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil || req.Source == "" {
		_ = enc.Encode(map[string]string{"error": "expected {\"source\": ..., \"op\": {...}} on stdin"})
		return 2
	}
	edits, err := sfc.ApplyEdit(req.Source, req.Op)
	if err != nil {
		_ = enc.Encode(map[string]string{"error": err.Error()})
		return 1
	}
	if edits == nil {
		edits = []sfc.TextEdit{}
	}
	if err := enc.Encode(map[string]any{"edits": edits}); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sfc edit:", err)
		return 2
	}
	return 0
}

// runSFCCheck parses+checks a single .sfc file and prints gcc-style
// diagnostics — the same shape as `nautilus check`'s per-file output, but
// standalone (no directory walk) and SFC-only.
func runSFCCheck(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, sfcUsage)
		return 2
	}
	src, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "nautilus sfc check:", err)
		return 2
	}
	prog, err := sfc.Parse(string(src))
	if err != nil {
		fmt.Printf("%s: %s\n", args[0], err.Error())
		fmt.Println("nautilus sfc check: 1 file(s), 1 with errors")
		return 1
	}
	bad := false
	for _, d := range sfc.Check(prog) {
		fmt.Printf("%s:%d:%d: %s: %s\n", args[0], d.Pos.Line, d.Pos.Col, d.Severity, d.Message)
		if d.Severity == sfc.SeverityError {
			bad = true
		}
	}
	if bad {
		fmt.Println("nautilus sfc check: 1 file(s), 1 with errors")
		return 1
	}
	fmt.Println("nautilus sfc check: 1 file(s), 0 with errors")
	return 0
}
