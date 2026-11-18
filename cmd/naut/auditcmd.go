package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/joyautomation/nautilus/audit"
)

const auditUsage = `naut audit — the runtime audit journal (#709)

Usage:
  naut audit verify [dir]   Walk the journal's hash chain (default dir: audit)

verify checks that every line of every file is an audit entry whose prev is
the hash of the line before it, across files, and that a journal starting
mid-chain starts where an audit.prune entry recorded it. It prints the
entry count and the head hash, and exits 1 at the first break, naming the
file and line: a line edited, removed, inserted or moved, or a file
missing.

It cannot see lines removed from the very end. Compare the head it prints
with one kept elsewhere (the Postgres copy, the peer) for that.
`

func runAudit(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, auditUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "verify":
		fs := flag.NewFlagSet("naut audit verify", flag.ContinueOnError)
		fs.Usage = func() { fmt.Fprint(os.Stderr, auditUsage) }
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		dir := "audit"
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		return auditVerify(dir)
	default:
		fmt.Fprintf(os.Stderr, "naut audit: unknown subcommand %q\n\n%s", args[0], auditUsage)
		return 2
	}
}

func auditVerify(dir string) int {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(os.Stderr, "naut audit verify: %s is not a journal directory\n", dir)
		return 2
	}
	r, err := audit.Verify(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "naut audit verify: %v\n", err)
		return 2
	}
	if !r.OK() {
		fmt.Printf("BROKEN at %v\n%d entries in %d files checked before the break\n", r.Break, r.Entries, r.Files)
		return 1
	}
	fmt.Printf("ok: %d entries in %d files, chain intact\nhead %s\n", r.Entries, r.Files, r.Head)
	if r.Start != "" {
		fmt.Printf("starts after a pruned file: %s\n", r.Start)
	}
	if r.Torn {
		fmt.Println("the newest file ends in a partial line (a crash mid-write); the next open drops it")
	}
	return 0
}
