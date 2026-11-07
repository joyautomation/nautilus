package main

import (
	"io"
	"os"

	"github.com/joyautomation/nautilus/internal/composecmd"
)

// naut compose lives in internal/composecmd, shared with the browser
// playground (cmd/naut-wasm).

func runCompose(args []string) int {
	return composeTo(args, os.Stdin, os.Stdout, os.Stderr)
}

func composeTo(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return composecmd.Run(args, stdin, stdout, stderr)
}

// composeResult is `naut compose --json`'s output.
type composeResult = composecmd.Result
