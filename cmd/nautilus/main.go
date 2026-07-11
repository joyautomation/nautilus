// Command nautilus is the developer CLI for the nautilus SCADA framework.
//
//	nautilus lsp     run the IEC 61131-3 Structured Text language server (stdio)
//	nautilus check   compile .st files and report diagnostics (CI-friendly)
//	nautilus new     scaffold a new nautilus project (interactive)
//
// Install: go install github.com/joyautomation/nautilus/cmd/nautilus@latest
package main

import (
	"fmt"
	"os"

	"github.com/joyautomation/nautilus/internal/lsp"
)

const usage = `nautilus — SCADA as software

Usage:
  nautilus lsp            Run the ST language server on stdio (used by the
                          VS Code extension; not meant to be run by hand).
  nautilus check [path]   Compile every .st file under path (default ".")
                          and print diagnostics. Exits 1 on any error.
  nautilus new [name]     Scaffold a new nautilus project.
  nautilus eip <cmd>      EtherNet/IP tools: import (browse a Logix controller
                          and generate types + tag manifest) and browse.
  nautilus pull           Pull a controller's online edits back into the
                          program file (--host <controller>). Inverse of the
                          VS Code "Download Program to Controller" command.
  nautilus version        Print version.
`

func main() {
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
	case "new":
		os.Exit(runNew(os.Args[2:]))
	case "eip":
		os.Exit(runEIP(os.Args[2:]))
	case "pull":
		os.Exit(runPull(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Println("nautilus", lsp.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "nautilus: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}
