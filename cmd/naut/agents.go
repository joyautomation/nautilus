package main

// naut agents: the guidance for AI coding assistants (AGENTS.md, and the
// CLAUDE.md that imports it for Claude Code) is part of the binary, not a
// one-time copy. `naut new` writes it through agentsWrite; `naut agents`
// adds it to a project that predates it and refreshes it when this naut's
// text is newer; `naut check` warns when it is missing or stale; the VS Code
// extension asks `naut agents -check -json` and offers to run the rest.
//
// The nautilus text sits between two HTML comment markers. A refresh
// replaces only what is between them, so a project's own notes above or
// below survive. A file with no markers is adopted only when it is a copy
// of a text some naut wrote (agentsPrevious); anything else is somebody's
// own file, and naut agents leaves it alone unless told -append or -force.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joyautomation/nautilus/internal/lsp"
)

const (
	agentsFileName  = "AGENTS.md"
	claudeFileName  = "CLAUDE.md"
	agentsBeginMark = "<!-- nautilus:begin"
	agentsEndMark   = "<!-- nautilus:end -->"
)

// agentsPrevious holds the SHA-256 of every earlier text of
// templates/AGENTS.md.tmpl that `naut new` wrote without markers, so a
// project made by an older naut is recognised as ours and refreshed rather
// than refused. When the template changes, add the hash of the text it
// replaces: TestAgentsPreviousCoversHistory checks this list against git.
var agentsPrevious = map[string]string{
	"4f88a56f4a1d73731abe610253d3d42e80949fed498a74be02b580a20273f90b": "#556, 2026-11-04",
	"988ff1b0879bc462564de93cb79b4f8fce489509210af48709c9499e1f2c3e61": "#630, 2026-11-04",
}

// agentsState is what a project's AGENTS.md is, as far as naut is concerned.
type agentsState string

const (
	agentsMissing   agentsState = "missing"   // no AGENTS.md
	agentsCurrent   agentsState = "current"   // the nautilus block holds this naut's text
	agentsUnstamped agentsState = "unstamped" // no markers, but exactly this naut's text: current, stamped on the next write
	agentsStale     agentsState = "stale"     // the block (or an unmarked copy) holds an older naut's text
	agentsUnmanaged agentsState = "unmanaged" // somebody's own AGENTS.md: no nautilus block
)

// ok reports whether the state needs nothing done.
func (s agentsState) ok() bool { return s == agentsCurrent || s == agentsUnstamped }

// public is the state as `naut agents -check -json` reports it: unstamped
// is current to anyone asking.
func (s agentsState) public() string {
	if s == agentsUnstamped {
		return string(agentsCurrent)
	}
	return string(s)
}

// claudeState is what a project's CLAUDE.md is.
type claudeState string

const (
	claudeMissing  claudeState = "missing"  // no CLAUDE.md
	claudeImports  claudeState = "current"  // it imports AGENTS.md
	claudeNoImport claudeState = "noimport" // it exists but does not import AGENTS.md
)

// agentsText is this naut's guidance, with LF line endings and one
// trailing newline.
func agentsText() string {
	raw, err := templates.ReadFile("templates/AGENTS.md.tmpl")
	if err != nil {
		panic(err) // embedded; cannot happen in a built binary
	}
	return strings.TrimRight(lf(string(raw)), "\n") + "\n"
}

// claudeText is the CLAUDE.md a project gets when it has none.
func claudeText() string {
	raw, err := templates.ReadFile("templates/CLAUDE.md.tmpl")
	if err != nil {
		panic(err)
	}
	return strings.TrimRight(lf(string(raw)), "\n") + "\n"
}

func lf(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// agentsBlock is the nautilus text between its markers. The version on the
// begin line is for people; staleness is decided by the text alone.
func agentsBlock() string {
	return agentsBeginMark + " (naut " + lsp.Version + "): `naut agents` keeps the text between these markers current. Write this project's own notes above or below them. -->\n" +
		agentsText() + agentsEndMark + "\n"
}

// agentsSpan finds the nautilus block in an LF document: the byte offsets
// of the start of its begin line and the end of its end line (after the
// newline, if any), and the text between the two marker lines. found is
// false with no begin marker; err is set when the markers are broken.
func agentsSpan(doc string) (start, end int, body string, found bool, err error) {
	// Every marker in the file is counted: exactly one begin, then exactly
	// one end, or the block is not ours to rewrite.
	lines := strings.SplitAfter(doc, "\n")
	off, begin, endLine, begins, ends := 0, -1, -1, 0, 0
	for i, line := range lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, agentsBeginMark):
			begins++
			begin, start = i, off
		case t == agentsEndMark:
			ends++
			if begin < 0 {
				return 0, 0, "", true, errors.New("its nautilus:end marker comes before any nautilus:begin")
			}
			endLine, end = i, off+len(line)
		}
		off += len(line)
	}
	switch {
	case begins == 0 && ends == 0:
		return 0, 0, "", false, nil
	case begins > 1 || ends > 1:
		return 0, 0, "", true, errors.New("it has more than one nautilus block")
	case ends == 0:
		return 0, 0, "", true, errors.New("its nautilus:begin marker has no nautilus:end")
	}
	return start, end, strings.Join(lines[begin+1:endLine], ""), true, nil
}

// agentsClassify says what an existing AGENTS.md is.
func agentsClassify(doc string) (agentsState, error) {
	doc = lf(doc)
	_, _, body, found, err := agentsSpan(doc)
	if err != nil {
		return agentsUnmanaged, err
	}
	want := agentsText()
	if found {
		if strings.TrimRight(body, "\n")+"\n" == want {
			return agentsCurrent, nil
		}
		return agentsStale, nil
	}
	norm := strings.TrimRight(doc, "\n") + "\n"
	if norm == want {
		return agentsUnstamped, nil
	}
	if _, ok := agentsPrevious[sha(norm)]; ok {
		return agentsStale, nil
	}
	if _, ok := agentsPrevious[sha(doc)]; ok {
		return agentsStale, nil
	}
	return agentsUnmanaged, nil
}

// agentsMode is how naut agents treats somebody's own AGENTS.md.
type agentsMode int

const (
	agentsRefuse agentsMode = iota // leave it alone and say how to proceed
	agentsAppend                   // add the nautilus block below it
	agentsForce                    // replace it
)

// agentsRender returns the AGENTS.md to write, given the one that exists
// (exists false: none), and a word for what changed. changed is false when
// the file is already current and stamped, so nothing need be written.
func agentsRender(existing string, exists bool, mode agentsMode) (out, did string, changed bool, err error) {
	if !exists {
		return agentsBlock(), "wrote", true, nil
	}
	crlf := strings.Contains(existing, "\r\n")
	doc := lf(existing)
	restore := func(s string) string {
		if crlf {
			return strings.ReplaceAll(s, "\n", "\r\n")
		}
		return s
	}
	state, cerr := agentsClassify(doc)
	switch {
	case state == agentsCurrent:
		return existing, "already current", false, nil
	case state == agentsStale:
		if start, end, _, found, _ := agentsSpan(doc); found {
			return restore(doc[:start] + agentsBlock() + doc[end:]), "updated", true, nil
		}
		return restore(agentsBlock()), "updated", true, nil
	case state == agentsUnstamped:
		return restore(agentsBlock()), "stamped", true, nil
	}
	// Somebody's own file (or one whose markers are broken).
	switch mode {
	case agentsForce:
		return restore(agentsBlock()), "replaced", true, nil
	case agentsAppend:
		if cerr != nil {
			return "", "", false, fmt.Errorf("%s: %v; fix the markers or use -force to replace the file", agentsFileName, cerr)
		}
		sep := "\n"
		if !strings.HasSuffix(doc, "\n") {
			sep = "\n\n"
		}
		return restore(doc + sep + agentsBlock()), "added the nautilus guidance below your text", true, nil
	}
	if cerr != nil {
		return "", "", false, fmt.Errorf("%s: %v; fix the markers, or use -force to replace the file", agentsFileName, cerr)
	}
	return "", "", false, fmt.Errorf("%s is not one naut wrote (it has no nautilus:begin marker), so naut agents leaves it alone: -append adds the nautilus guidance below your text, -force replaces the file", agentsFileName)
}

// claudeClassify says whether a CLAUDE.md imports AGENTS.md.
func claudeClassify(doc string) claudeState {
	for _, line := range strings.Split(lf(doc), "\n") {
		switch strings.TrimSpace(line) {
		case "@AGENTS.md", "@./AGENTS.md":
			return claudeImports
		}
	}
	return claudeNoImport
}

// claudeRender returns the CLAUDE.md to write: the template when there is
// none, the existing one with the import added when it lacks it.
func claudeRender(existing string, exists bool) (out, did string, changed bool) {
	if !exists {
		return claudeText(), "wrote", true
	}
	if claudeClassify(existing) == claudeImports {
		return existing, "already imports AGENTS.md", false
	}
	nl := "\n"
	if strings.Contains(existing, "\r\n") {
		nl = "\r\n"
	}
	sep := nl
	if !strings.HasSuffix(existing, "\n") {
		sep = nl + nl
	}
	return existing + sep + "@AGENTS.md" + nl, "added @AGENTS.md", true
}

// agentsInspect reads a project's two files.
func agentsInspect(dir string) (agentsState, error, claudeState) {
	var as agentsState
	var aerr error
	if b, err := os.ReadFile(filepath.Join(dir, agentsFileName)); err != nil {
		as = agentsMissing
	} else {
		as, aerr = agentsClassify(string(b))
	}
	cs := claudeMissing
	if b, err := os.ReadFile(filepath.Join(dir, claudeFileName)); err == nil {
		cs = claudeClassify(string(b))
	}
	return as, aerr, cs
}

// agentsWarnings are naut check's findings for a manifest project. Only a
// project that carries the guidance hears about it: a stale block, broken
// markers, or a CLAUDE.md that does not import it. A project without
// AGENTS.md, or with only its own, gets nothing here. Plenty of projects
// (an import, a test fixture, a team that writes its own) have no use for
// it, and a warning that never goes away teaches people to ignore
// warnings. The VS Code extension offers the missing file instead, and
// `naut agents -check` answers a script.
func agentsWarnings(dir string) []string {
	as, aerr, cs := agentsInspect(dir)
	var w []string
	switch {
	case as == agentsStale:
		w = append(w, "AGENTS.md's nautilus guidance is older than this naut's — run naut agents to update it (notes outside the nautilus block are kept)")
	case as == agentsUnmanaged && aerr != nil:
		w = append(w, "AGENTS.md: "+aerr.Error()+" — fix the markers, or run naut agents -force to replace the file")
	}
	if cs == claudeNoImport && (as.ok() || as == agentsStale) {
		w = append(w, "CLAUDE.md does not import AGENTS.md, so Claude Code does not read the nautilus guidance — run naut agents to add @AGENTS.md")
	}
	return w
}

// agentsWrite brings a project's AGENTS.md and CLAUDE.md up to date and
// returns a line per file saying what it did.
func agentsWrite(dir string, mode agentsMode) ([]string, error) {
	var report []string
	ap := filepath.Join(dir, agentsFileName)
	old, rerr := os.ReadFile(ap)
	out, did, changed, err := agentsRender(string(old), rerr == nil, mode)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := os.WriteFile(ap, []byte(out), 0o644); err != nil {
			return nil, err
		}
	}
	report = append(report, agentsFileName+": "+did)

	cp := filepath.Join(dir, claudeFileName)
	cold, crerr := os.ReadFile(cp)
	cout, cdid, cchanged := claudeRender(string(cold), crerr == nil)
	if cchanged {
		if err := os.WriteFile(cp, []byte(cout), 0o644); err != nil {
			return nil, err
		}
	}
	report = append(report, claudeFileName+": "+cdid)
	return report, nil
}

const agentsUsage = `naut agents [-check [-json]] [-append | -force] [-m manifest] [dir]

Adds or updates the guidance for AI coding assistants in a nautilus project:
AGENTS.md (read by Codex, Cursor, Copilot and others) and a CLAUDE.md that
imports it for Claude Code. The text comes from this naut, so it matches
the checks this naut runs. naut new writes it; this adds it to a project
that predates it, and refreshes it when naut has newer guidance.

Only the text between AGENTS.md's nautilus:begin and nautilus:end markers
is naut's; notes above or below them are kept. An AGENTS.md naut did not
write is left alone unless -append or -force says otherwise.

  -check   say whether the files are missing or stale; write nothing. Exits
           0 when nothing needs doing, 1 when something does.
  -json    with -check: {"agentsMd": "missing|current|stale|unmanaged",
           "claudeMd": "missing|current|noimport", "version": "..."}
  -append  an AGENTS.md naut did not write: add the guidance below it
  -force   an AGENTS.md naut did not write: replace it
`

func runAgents(args []string) int {
	fset := flag.NewFlagSet("agents", flag.ContinueOnError)
	fset.Usage = func() { fmt.Fprint(os.Stderr, agentsUsage) }
	check := fset.Bool("check", false, "")
	asJSON := fset.Bool("json", false, "")
	appendF := fset.Bool("append", false, "")
	force := fset.Bool("force", false, "")
	manifest := fset.String("m", "", manifestFlagUsage)
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if *appendF && *force {
		fmt.Fprintln(os.Stderr, "naut agents: -append and -force are two answers to one question; pick one")
		return 2
	}
	if *asJSON && !*check {
		fmt.Fprintln(os.Stderr, "naut agents: -json goes with -check")
		return 2
	}
	if fset.NArg() > 1 {
		fmt.Fprint(os.Stderr, agentsUsage)
		return 2
	}
	dir := "."
	if fset.NArg() == 1 {
		dir = fset.Arg(0)
	}
	if _, ok := manifestDir([]string{dir}, *manifest); !ok {
		fmt.Fprintf(os.Stderr, "naut agents: %s has no %s; AGENTS.md describes a nautilus manifest project, so it goes beside the manifest\n", dir, manifestLabel(*manifest))
		return 2
	}

	if *check {
		as, aerr, cs := agentsInspect(dir)
		if *asJSON {
			b, _ := json.Marshal(map[string]string{"agentsMd": as.public(), "claudeMd": string(cs), "version": lsp.Version})
			fmt.Println(string(b))
		} else {
			line := agentsFileName + ": " + as.public()
			if aerr != nil {
				line += " (" + aerr.Error() + ")"
			}
			fmt.Println(line)
			fmt.Println(claudeFileName + ": " + string(cs))
		}
		if as.ok() && cs == claudeImports {
			return 0
		}
		return 1
	}

	mode := agentsRefuse
	switch {
	case *appendF:
		mode = agentsAppend
	case *force:
		mode = agentsForce
	}
	report, err := agentsWrite(dir, mode)
	if err != nil {
		fmt.Fprintln(os.Stderr, "naut agents:", err)
		return 1
	}
	for _, r := range report {
		fmt.Println(r)
	}
	return 0
}
