package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joyautomation/nautilus/audit"
	"github.com/joyautomation/nautilus/internal/auditpg"
	"github.com/joyautomation/nautilus/internal/project"
)

const auditUsage = `naut audit — the runtime audit journal (#709)

Usage:
  naut audit verify [dir]                    Walk the journal's hash chain (default dir: audit)
  naut audit verify [dir] --anchor postgres  Also check it against the copy and anchors in Postgres
        [--dsn-env VAR] [--controller name] [--clock-skew 5m]
  naut audit pg-schema --role <role>         Print the SQL that gives <role> the copy, unable to rewrite it

verify checks that every line of every file is an audit entry whose prev is
the hash of the line before it, across files, and that a journal starting
mid-chain starts where an audit.prune entry recorded it. It prints the
entry count and the head hash, and exits 1 at the first break, naming the
file and line: a line edited, removed, inserted or moved, or a file
missing.

On its own it cannot see the last lines cut off, or the whole journal
rewritten with the chain recomputed, which anyone who can write the files
can do. --anchor postgres checks the journal against what audit.sink:
postgres keeps where the controller's service user cannot rewrite it:
every anchor (a seq and its hash), every copied line (the copy still holds
what the journal pruned), every gap in the copy, and every prune (by the
journalled retention, its time against the database's clock, and the age
of the files it removed). The controller and the connection string come
from nautilus.yaml in this directory, as naut run names them, unless given.

Its verdict:
  verified     every check passed and no anchor's writer could rewrite them (exit 0)
  BROKEN       a check failed (exit 1)
  UNPROTECTED  the checks passed, but an anchor's database role could have
               rewritten the copy or the anchors, so they prove nothing
               against someone holding it (exit 3)
A lowered retention is printed as a WARNING whatever the verdict: it is
the thing to look at, since what was pruned after it went sooner.
Exit 2 is a usage or I/O error, or no anchors for the controller.

pg-schema prints the tables and the grants a database administrator runs,
as the role that will own the tables: <role> (the controller's) gets
SELECT, and INSERT only on the columns that are not evidence.
`

func runAudit(args []string) int {
	return runAuditTo(args, os.Stdout, os.Stderr, os.Getenv)
}

func runAuditTo(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, auditUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "verify":
		fs := flag.NewFlagSet("naut audit verify", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprint(stderr, auditUsage) }
		anchor := fs.String("anchor", "", "check against anchors kept elsewhere: postgres")
		dsnEnv := fs.String("dsn-env", "", "the environment variable holding the copy's connection string")
		controller := fs.String("controller", "", "the controller's name in the copy (default: nautilus.yaml's, as naut run names it)")
		skew := fs.Duration("clock-skew", 5*time.Minute, "the tolerance between the controller's clock and the database's")
		dir, err := parseInterleaved(fs, args[1:])
		if err != nil {
			fmt.Fprintf(stderr, "naut audit verify: %v\n", err)
			return 2
		}
		if *anchor == "" {
			if *dsnEnv != "" || *controller != "" {
				fmt.Fprintln(stderr, "naut audit verify: --dsn-env and --controller go with --anchor postgres")
				return 2
			}
			if dir == "" {
				dir = "audit"
			}
			return auditVerify(dir, stdout, stderr)
		}
		if *anchor != "postgres" {
			fmt.Fprintf(stderr, "naut audit verify: --anchor %q: want postgres\n", *anchor)
			return 2
		}
		return auditVerifyAnchored(anchoredArgs{dir: dir, dsnEnv: *dsnEnv, controller: *controller, skew: *skew}, stdout, stderr, getenv)
	case "pg-schema":
		fs := flag.NewFlagSet("naut audit pg-schema", flag.ContinueOnError)
		fs.SetOutput(stderr)
		fs.Usage = func() { fmt.Fprint(stderr, auditUsage) }
		role := fs.String("role", "", "the controller's database role (the one in its connection string)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *role == "" || fs.NArg() > 0 {
			fmt.Fprintln(stderr, "naut audit pg-schema: --role <the controller's database role> is required, and nothing else")
			return 2
		}
		fmt.Fprint(stdout, auditpg.Schema(*role))
		return 0
	default:
		fmt.Fprintf(stderr, "naut audit: unknown subcommand %q\n\n%s", args[0], auditUsage)
		return 2
	}
}

// parseInterleaved parses flags before and after the one directory
// argument (flag stops at the first argument that is not a flag, so
// `verify audit --anchor postgres` would otherwise drop the flag).
func parseInterleaved(fs *flag.FlagSet, args []string) (dir string, err error) {
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			return dir, nil
		}
		if dir != "" {
			return "", fmt.Errorf("one journal directory, not %q and %q", dir, fs.Arg(0))
		}
		dir, args = fs.Arg(0), fs.Args()[1:]
	}
}

func auditVerify(dir string, stdout, stderr io.Writer) int {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "naut audit verify: %s is not a journal directory\n", dir)
		return 2
	}
	r, err := audit.Verify(dir)
	if err != nil {
		fmt.Fprintf(stderr, "naut audit verify: %v\n", err)
		return 2
	}
	if !r.OK() {
		fmt.Fprintf(stdout, "BROKEN at %v\n%d entries in %d files checked before the break\n", r.Break, r.Entries, r.Files)
		return 1
	}
	fmt.Fprintf(stdout, "ok: %d entries in %d files, chain intact\nhead seq %d %s\n", r.Entries, r.Files, r.HeadSeq, r.Head)
	printChainNotes(r, stdout)
	return 0
}

func printChainNotes(r audit.Report, stdout io.Writer) {
	for _, d := range r.TailDrops {
		fmt.Fprintf(stdout, "note: %d bytes of a partial last line were cut from %s on reopen (recorded at %s): a crash mid-write, or a removed newline\n", d.Bytes, d.File, d.At)
	}
	if r.Start != "" {
		fmt.Fprintf(stdout, "starts after a pruned file: %s\n", r.Start)
	}
	if r.Torn {
		fmt.Fprintln(stdout, "the newest file ends in a partial line (a crash mid-write); the next open drops it")
	}
}

type anchoredArgs struct {
	dir, dsnEnv, controller string
	skew                    time.Duration
}

// auditVerifyAnchored is verify --anchor postgres: the journal against its
// copy and anchors (audit.VerifyAnchored).
func auditVerifyAnchored(a anchoredArgs, stdout, stderr io.Writer, getenv func(string) string) int {
	fail := func(format string, args ...any) int {
		fmt.Fprintf(stderr, "naut audit verify: "+format+"\n", args...)
		return 2
	}
	proj, err := project.Load(os.DirFS("."), "")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail("%v", err)
	}
	if err != nil {
		proj = nil
	}
	if a.dir == "" {
		a.dir = "audit"
		if proj != nil {
			a.dir, _ = proj.AuditJournal()
		}
	}
	if st, err := os.Stat(a.dir); err != nil || !st.IsDir() {
		return fail("%s is not a journal directory", a.dir)
	}
	if a.controller == "" {
		if proj == nil {
			return fail("no nautilus.yaml here to name the controller: give --controller")
		}
		a.controller = auditController(proj, getenv)
	}
	dsn, env := "", a.dsnEnv
	switch {
	case env != "":
		dsn = getenv(env)
	case proj != nil && proj.Audit != nil && proj.Audit.Sink == "postgres":
		if dsn, err = proj.AuditCopyDSN(getenv); err != nil {
			return fail("%v", err)
		}
	default:
		env = project.DefaultAuditDSNEnv + " (or " + project.FallbackAuditDSNEnv + ")"
		if dsn = getenv(project.DefaultAuditDSNEnv); dsn == "" {
			dsn = getenv(project.FallbackAuditDSNEnv)
		}
	}
	if dsn == "" {
		return fail("no connection string: %s is empty", env)
	}
	store, err := auditpg.Open(dsn, a.controller)
	if err != nil {
		return fail("%v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	anchors, err := store.Anchors(ctx)
	if err != nil {
		return fail("reading the anchors: %v", err)
	}
	if len(anchors) == 0 {
		names, _ := store.Controllers(ctx)
		msg := "none"
		if len(names) > 0 {
			msg = strings.Join(names, ", ")
		}
		return fail("no anchors for controller %q in this database (controllers with anchors: %s); give --controller, or wait for the copy's first anchor", a.controller, msg)
	}
	writers := map[string]audit.WriterCheck{}
	for _, an := range anchors {
		if _, done := writers[an.Writer]; done {
			continue
		}
		c, err := store.Protection(ctx, an.Writer)
		if err != nil {
			return fail("checking role %q: %v", an.Writer, err)
		}
		writers[an.Writer] = c
	}
	gaps, err := store.Gaps(ctx)
	if err != nil {
		return fail("reading the gaps: %v", err)
	}
	next, done, err := store.Rows(ctx)
	if err != nil {
		return fail("reading the copy: %v", err)
	}
	defer done()
	r, err := audit.VerifyAnchored(audit.AnchorInput{Dir: a.dir, Copy: next, Gaps: gaps, Anchors: anchors, Writers: writers, ClockSkew: a.skew})
	if err != nil {
		return fail("%v", err)
	}
	return printAnchored(r, a.controller, stdout)
}

func printAnchored(r audit.AnchorReport, controller string, stdout io.Writer) int {
	fmt.Fprintf(stdout, "%s: controller %q, %d journal entries in %d files, head seq %d; %d copied rows (%d compared line for line), %d anchors (newest seq %d)\n",
		r.Verdict, controller, r.Chain.Entries, r.Chain.Files, r.Chain.HeadSeq, r.CopyRows, r.Compared, r.Anchors, r.NewestAnchor)
	for _, w := range r.Warnings {
		fmt.Fprintf(stdout, "WARNING: %s\n", w)
	}
	for _, p := range r.Problems {
		fmt.Fprintf(stdout, "BROKEN: %s\n", p)
	}
	unp := append([]string(nil), r.Unprotected...)
	sort.Strings(unp)
	for _, u := range unp {
		fmt.Fprintf(stdout, "UNPROTECTED: %s\n", u)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(stdout, "note: %s\n", n)
	}
	printChainNotes(r.Chain, stdout)
	switch r.Verdict {
	case audit.Broken:
		return 1
	case audit.Unprotected:
		return 3
	}
	return 0
}
