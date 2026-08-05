// Command nautilus is the developer CLI for the nautilus SCADA framework.
//
//	nautilus lsp        run the IEC 61131-3 language server (stdio)
//	nautilus check      compile .st/.fbd files and report diagnostics (CI-friendly)
//	nautilus new        scaffold a new nautilus project (interactive)
//	nautilus fbd graph  emit a .fbd file's diagram render model as JSON
//
// Install: go install github.com/joyautomation/nautilus/cmd/nautilus@latest
package main

import (
	"fmt"
	"os"

	"github.com/joyautomation/nautilus/internal/lsp"
)

const usage = `nautilus — SCADA, built like software

Usage:
  nautilus lsp            Run the ST language server on stdio (used by the
                          VS Code extension; not meant to be run by hand).
  nautilus check [path]   Compile every .st file under path (default ".")
                          and print diagnostics. Exits 1 on any error.
  nautilus new [name]     Scaffold a new nautilus project. --template picks
                          the shape: demo (default) or minimal for a manifest
                          project — nautilus.yaml + IEC files, no toolchain —
                          or sdk / sdk-demo for a Go project when you need a
                          custom field bus or richer simulation.
                          --language st|fbd|ld|sfc picks the blank program's
                          language (minimal and sdk).
  nautilus run [dir]      Run a manifest project (nautilus.yaml + programs):
                          scan loop, dashboard, and tag API — no Go needed.
  nautilus test [dir]     Run a manifest project's acceptance tests
                          (*_test.yaml) in virtual time, so timers and loop
                          responses assert exactly (-run re, -v, -json).
  nautilus build [dir]    Emit a self-contained controller binary from a
                          manifest project (-o name). Ships like any compiled
                          program; no Go toolchain involved.
  nautilus eip <cmd>      EtherNet/IP tools: import (browse a Logix controller
                          and generate types + tag manifest) and browse.
  nautilus pull           Pull a controller's online edits back into the
                          program file (--host <controller>). Inverse of the
                          VS Code "Download Program to Controller" command.
  nautilus fbd graph <f>  Emit a .fbd file's diagram render model as JSON
                          (used by the VS Code FBD preview).
  nautilus sfc check <f>  Parse a .sfc file and run its structural checks
                          (early — see "nautilus sfc" for details).
  nautilus version        Print version.
`

func main() {
	// A binary produced by `nautilus build` IS the controller: the project
	// rides embedded on the executable's tail, and running it hosts the
	// scan loop directly (NAUTILUS_CLI=1 recovers the CLI).
	if fsys, ok := embeddedProject(); ok {
		os.Exit(runProject(fsys, "built"))
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "lsp":
		if err := lsp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus lsp:", err)
			os.Exit(1)
		}
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "run":
		os.Exit(runRun(os.Args[2:]))
	case "build":
		os.Exit(runBuild(os.Args[2:]))
	case "test":
		os.Exit(runTest(os.Args[2:]))
	case "new":
		os.Exit(runNew(os.Args[2:]))
	case "eip":
		os.Exit(runEIP(os.Args[2:]))
	case "pull":
		os.Exit(runPull(os.Args[2:]))
	case "fbd":
		os.Exit(runFBD(os.Args[2:]))
	case "ld":
		os.Exit(runLD(os.Args[2:]))
	case "sfc":
		os.Exit(runSFC(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println("nautilus", lsp.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "nautilus: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
