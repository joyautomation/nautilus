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
`

func runFBD(args []string) int {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, fbdUsage)
		return 2
	}
	switch args[0] {
	case "graph":
		return runFBDGraph(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "nautilus fbd: unknown subcommand %q\n\n%s", args[0], fbdUsage)
		return 2
	}
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
