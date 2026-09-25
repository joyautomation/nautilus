// Command nautilus is the developer CLI for the nautilus SCADA framework.
//
//	naut lsp        run the IEC 61131-3 language server (stdio)
//	naut check      compile .st/.fbd files and report diagnostics (CI-friendly)
//	naut new        scaffold a new nautilus project (interactive)
//	naut fbd graph  emit a .fbd file's diagram render model as JSON
//
// Install: go install github.com/joyautomation/nautilus/cmd/naut@latest
package main

import (
	"fmt"
	"os"

	"github.com/joyautomation/nautilus/internal/lsp"
)

const usage = `nautilus — SCADA, built like software

Usage:
  naut lsp            Run the ST language server on stdio (used by the
                          VS Code extension; not meant to be run by hand).
  naut check [path]   Compile every .st file under path (default ".")
                          and print diagnostics. Exits 1 on any error.
  naut new [name]     Scaffold a new nautilus project. --template picks
                          the shape: demo (default) or minimal for a manifest
                          project — nautilus.yaml + IEC files, no toolchain —
                          or sdk / sdk-demo for a Go project when you need a
                          custom field bus or richer simulation.
                          --language st|fbd|ld|sfc picks the blank program's
                          language (minimal and sdk).
  naut run [dir]      Run a manifest project (nautilus.yaml + programs):
                          scan loop, dashboard, and tag API — no Go needed.
  naut test [dir]     Run a manifest project's acceptance tests
                          (*_test.yaml) in virtual time, so timers and loop
                          responses assert exactly (-run re, -v, -json).
  naut build [dir]    Emit a self-contained controller binary from a
                          manifest project (-o name). Ships like any compiled
                          program; no Go toolchain involved.

                          run, test, and build take -m <manifest> to load one
                          other than nautilus.yaml. That is how one project
                          serves several sites: shared programs, a manifest
                          per site choosing which tag-files it composes and
                          which controller it points at. Every one of them is
                          committed, so what deploys is always readable.
  naut eip <cmd>      EtherNet/IP tools: import (browse a Logix controller
                          and generate types + tag manifest) and browse.
  naut logix <cmd>    Allen-Bradley Logix tools, all offline: import an
                          L5X export's UDTs and tags (descriptions included,
                          which a live browse cannot recover), graph an RLL
                          routine for the ladder preview, normalize an export
                          so drift is detectable, info.
  naut sparkplug <cmd> Sparkplug B host tools: import (listen to a group
                          and generate types + manifest + tag file), browse,
                          and tags (re-derive the tag file, no broker).
  naut modbus <cmd>   Modbus TCP tools: import (device map → manifest +
                          tag file, offline), browse (read a live register
                          range raw + decoded), serve (bench slave for a
                          manifest), tags (re-derive the tag file).
  naut tags <cmd>     Generate a tag file from a spreadsheet export
                          (import-csv). Commit the output and compose it
                          with tag-files:.
  naut alarms list    Expand a manifest project's alarm rules and files
                          and print every definition (-o yaml, -count,
                          -site, -priority). The auditable half of "a few
                          rules cover thousands of alarms".
  naut pull           Pull a controller's online edits back into the
                          program file (--host <controller>). Inverse of the
                          VS Code "Download Program to Controller" command.
  naut compose <f>    Print the source the controller runs for a program:
                          the library prelude (lib/ and root, .ld/.fbd
                          transpiled) + the program (--json for the parts).
                          What "Download Program to Controller" sends.
  naut historian      Archive a controller's tags into Postgres and
                          serve downsampled history (-source, -db).
  naut fbd graph <f>  Emit a .fbd file's diagram render model as JSON
                          (used by the VS Code FBD preview).
  naut sfc check <f>  Parse a .sfc file and run its structural checks
                          (early — see "naut sfc" for details).
  naut version        Print version.
`

func main() {
	// A binary produced by `naut build` IS the controller: the project
	// rides embedded on the executable's tail, and running it hosts the
	// scan loop directly (NAUTILUS_CLI=1 recovers the CLI).
	if fsys, ok := embeddedProject(); ok {
		os.Exit(runProject(fsys, embeddedManifest(fsys), "built", ""))
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "lsp":
		if err := lsp.Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "naut lsp:", err)
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
	case "logix":
		os.Exit(runLogix(os.Args[2:]))
	case "sparkplug":
		os.Exit(runSparkplug(os.Args[2:]))
	case "modbus":
		os.Exit(runModbus(os.Args[2:]))
	case "tags":
		os.Exit(runTags(os.Args[2:]))
	case "alarms":
		os.Exit(runAlarms(os.Args[2:]))
	case "pull":
		os.Exit(runPull(os.Args[2:]))
	case "compose":
		os.Exit(runCompose(os.Args[2:]))
	case "historian":
		os.Exit(runHistorian(os.Args[2:]))
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
