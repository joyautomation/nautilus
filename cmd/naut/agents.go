package main

// naut agents: the guidance for AI coding assistants (AGENTS.md, and the
// CLAUDE.md that imports it for Claude Code) is part of the binary, not a
// one-time copy. `naut new` writes it through agentsWrite; `naut agents`
// adds it to a project that predates it and refreshes it when this naut's
// text is newer; `naut check` warns when a project's copy is stale or
// broken; the VS Code extension asks `naut agents -check -json` and offers
// to run the rest.
//
// The nautilus text sits between two HTML comment markers. A refresh
// replaces only what is between them, so a project's own notes above or
// below survive. A file with no markers is adopted when it is a text some
// naut wrote (agentsPrevious), alone or followed by the project's own
// notes. A file that holds nautilus guidance naut cannot place (broken
// markers, or an older copy edited in the middle) is a conflict for a
// person to settle. Anything else is somebody's own file, left alone unless
// told -append or -force.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
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
	// agentsSignature is a line every text of the guidance has carried. An
	// unmarked file that has it but is no text naut wrote is an older copy
	// somebody edited: appending would leave two sets of rules in it.
	agentsSignature = "For AI coding agents in this nautilus"
)

// agentsPrevious holds the SHA-256 of every earlier text of
// templates/AGENTS.md.tmpl that `naut new` wrote without markers (LF line
// endings, one trailing newline), so a project made by an older naut is
// recognised as ours and refreshed rather than refused. When the template
// changes, add the hash of the text it replaces:
// TestAgentsPreviousCoversHistory checks this list against git.
var agentsPrevious = map[string]string{
	"4f88a56f4a1d73731abe610253d3d42e80949fed498a74be02b580a20273f90b": "#556, 2026-11-04",
	"988ff1b0879bc462564de93cb79b4f8fce489509210af48709c9499e1f2c3e61": "#630, 2026-11-04",
	"48b0f7363093b8aad723de24d03827c83e438431dc5f2584921d175f809a3f07": "#671, 2026-11-13",
}

// agentsState is what a project's AGENTS.md is, as far as naut is concerned.
type agentsState string

const (
	agentsMissing   agentsState = "missing"   // no AGENTS.md
	agentsCurrent   agentsState = "current"   // the nautilus block holds this naut's text
	agentsUnstamped agentsState = "unstamped" // no markers, but this naut's text (maybe with notes after it): stamped on the next write
	agentsStale     agentsState = "stale"     // the block, or an unmarked copy, holds an older naut's text
	agentsConflict  agentsState = "conflict"  // nautilus guidance naut cannot place: broken markers, or an older copy edited inside
	agentsUnmanaged agentsState = "unmanaged" // somebody's own AGENTS.md, with none of the guidance
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

// embeddedText reads an embedded template as LF text with one trailing newline.
func embeddedText(name string) string {
	raw, err := templates.ReadFile("templates/" + name)
	if err != nil {
		panic(err) // embedded; cannot happen in a built binary
	}
	return oneNewline(lf(string(raw)))
}

// agentsText is this naut's guidance.
func agentsText() string { return embeddedText("AGENTS.md.tmpl") }

// claudeText is the CLAUDE.md a project gets when it has none.
func claudeText() string { return embeddedText("CLAUDE.md.tmpl") }

func lf(s string) string         { return strings.ReplaceAll(s, "\r\n", "\n") }
func oneNewline(s string) string { return strings.TrimRight(s, "\n") + "\n" }

// eol gives text back in the line endings of the file it came from.
func eol(text, like string) string {
	if strings.Contains(like, "\r\n") {
		return strings.ReplaceAll(lf(text), "\n", "\r\n")
	}
	return text
}

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
// false with no marker at all; err is set when the markers are broken.
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

// knownPrefix finds a text some naut wrote at the start of an unmarked LF
// document: today's (current true) or an earlier one. It returns the
// length of that text in doc, so what follows it is the project's own.
func knownPrefix(doc string) (n int, current, ok bool) {
	if cur := agentsText(); strings.HasPrefix(oneNewline(doc), cur) {
		return min(len(cur), len(doc)), true, true
	}
	// An earlier text is known only by its hash: try every line boundary.
	for i := 0; i < len(doc); {
		j := strings.IndexByte(doc[i:], '\n')
		if j < 0 {
			j = len(doc)
		} else {
			j += i + 1
		}
		if _, hit := agentsPrevious[sha(oneNewline(doc[:j]))]; hit {
			// The text ends at its own last newline; blank lines after it
			// (which oneNewline folded into the match) belong to what follows.
			// Keep going: the longest match wins.
			n, ok = min(len(strings.TrimRight(doc[:j], "\n"))+1, len(doc)), true
		}
		i = j
	}
	return n, false, ok
}

// agentsClassify says what an existing AGENTS.md is; err explains a
// conflict.
func agentsClassify(doc string) (agentsState, error) {
	doc = lf(doc)
	_, _, body, found, err := agentsSpan(doc)
	if err != nil {
		return agentsConflict, err
	}
	if found {
		if oneNewline(body) == agentsText() {
			return agentsCurrent, nil
		}
		return agentsStale, nil
	}
	if _, current, ok := knownPrefix(doc); ok {
		if current {
			return agentsUnstamped, nil
		}
		return agentsStale, nil
	}
	if strings.Contains(doc, agentsSignature) {
		return agentsConflict, errors.New("it is an older naut's guidance with edits inside it, so naut cannot tell its text from yours")
	}
	return agentsUnmanaged, nil
}

// agentsMode is how naut agents treats an AGENTS.md it cannot update.
type agentsMode int

const (
	agentsRefuse agentsMode = iota // leave it alone and say how to proceed
	agentsAppend                   // add the nautilus block below somebody's own file
	agentsForce                    // replace the file
)

// agentsRender returns the AGENTS.md to write, given the one that exists
// (exists false: none), and a word for what changed. changed is false when
// the file is already current and stamped, so nothing need be written.
func agentsRender(existing string, exists bool, mode agentsMode) (out, did string, changed bool, err error) {
	if !exists {
		return agentsBlock(), "wrote", true, nil
	}
	doc := lf(existing)
	state, cerr := agentsClassify(doc)
	switch state {
	case agentsCurrent:
		return existing, "already current", false, nil
	case agentsStale, agentsUnstamped:
		did := "updated"
		if state == agentsUnstamped {
			did = "stamped"
		}
		if start, end, _, found, _ := agentsSpan(doc); found {
			return eol(doc[:start]+agentsBlock()+doc[end:], existing), did, true, nil
		}
		n, _, _ := knownPrefix(doc)
		return eol(agentsBlock()+doc[n:], existing), did, true, nil
	}
	if mode == agentsForce {
		return eol(agentsBlock(), existing), "replaced", true, nil
	}
	if state == agentsConflict {
		return "", "", false, fmt.Errorf("%s: %v; move your own notes out of the nautilus text (above or below it), then run naut agents -force to replace the file", agentsFileName, cerr)
	}
	if mode == agentsAppend {
		sep := "\n"
		if !strings.HasSuffix(doc, "\n") {
			sep = "\n\n"
		}
		return eol(doc+sep+agentsBlock(), existing), "added the nautilus guidance below your text", true, nil
	}
	return "", "", false, fmt.Errorf("%s is not one naut wrote, so naut agents leaves it alone: -append adds the nautilus guidance below your text, -force replaces the file", agentsFileName)
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
	sep := "\n"
	if !strings.HasSuffix(existing, "\n") {
		sep = "\n\n"
	}
	return existing + eol(sep+"@AGENTS.md\n", existing), "added @AGENTS.md", true
}

// readIfThere reads a file; a file that is not there is not an error.
func readIfThere(path string) (string, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// agentsInspect reads a project's two files. why explains a conflict; err
// is a file that is there but cannot be read.
func agentsInspect(dir string) (as agentsState, why error, cs claudeState, err error) {
	doc, ok, err := readIfThere(filepath.Join(dir, agentsFileName))
	if err != nil {
		return "", nil, "", err
	}
	as = agentsMissing
	if ok {
		as, why = agentsClassify(doc)
	}
	cdoc, ok, err := readIfThere(filepath.Join(dir, claudeFileName))
	if err != nil {
		return "", nil, "", err
	}
	cs = claudeMissing
	if ok {
		cs = claudeClassify(cdoc)
	}
	return as, why, cs, nil
}

// agentsNeedsWork is -check's verdict: something naut agents (or a person)
// should do. Somebody's own AGENTS.md and a missing CLAUDE.md are choices,
// not faults.
func agentsNeedsWork(as agentsState, cs claudeState) bool {
	switch as {
	case agentsMissing, agentsStale, agentsConflict:
		return true
	}
	return cs == claudeNoImport && as.ok()
}

// agentsWarnings are naut check's findings for a manifest project. Only a
// project that carries the guidance hears about it: a stale or conflicted
// copy, or a CLAUDE.md that does not import a current one. A project
// without AGENTS.md, or with only its own, gets nothing here. Plenty of
// projects (an import, a test fixture, a team that writes its own) have no
// use for it, and a warning that never goes away teaches people to ignore
// warnings. The VS Code extension offers the missing file instead, and
// `naut agents -check` answers a script.
func agentsWarnings(dir string) []string {
	as, why, cs, err := agentsInspect(dir)
	if err != nil {
		return []string{"cannot read the AI assistant guidance: " + err.Error()}
	}
	var w []string
	switch as {
	case agentsStale:
		w = append(w, "AGENTS.md's nautilus guidance is older than this naut's — run naut agents to update it (notes outside the nautilus text are kept)")
	case agentsConflict:
		w = append(w, "AGENTS.md: "+why.Error()+" — move your own notes above or below the nautilus text, then run naut agents -force")
	}
	if cs == claudeNoImport && (as.ok() || as == agentsStale) {
		w = append(w, "CLAUDE.md does not import AGENTS.md, so Claude Code does not read the nautilus guidance — run naut agents to add @AGENTS.md")
	}
	return w
}

// agentsWrite brings a project's AGENTS.md and CLAUDE.md up to date and
// returns a line per file saying what it did.
func agentsWrite(dir string, mode agentsMode) ([]string, error) {
	ap := filepath.Join(dir, agentsFileName)
	old, exists, err := readIfThere(ap)
	if err != nil {
		return nil, err
	}
	out, did, changed, err := agentsRender(old, exists, mode)
	if err != nil {
		return nil, err
	}
	cp := filepath.Join(dir, claudeFileName)
	cold, cexists, err := readIfThere(cp)
	if err != nil {
		return nil, err
	}
	if changed {
		if err := os.WriteFile(ap, []byte(out), 0o644); err != nil {
			return nil, err
		}
	}
	cout, cdid, cchanged := claudeRender(cold, cexists)
	if cchanged {
		if err := os.WriteFile(cp, []byte(cout), 0o644); err != nil {
			return nil, err
		}
	}
	return []string{agentsFileName + ": " + did, claudeFileName + ": " + cdid}, nil
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

  -check   say what the files are; write nothing. Exits 1 when AGENTS.md is
           missing, stale or conflicted, or CLAUDE.md does not import it;
           0 otherwise (your own AGENTS.md, or no CLAUDE.md, is a choice).
  -json    with -check: {"agentsMd": "missing|current|stale|conflict|unmanaged",
           "claudeMd": "missing|current|noimport", "version": "..."}
  -append  an AGENTS.md naut did not write: add the guidance below it
  -force   replace AGENTS.md whatever it holds
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
		as, why, cs, err := agentsInspect(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "naut agents:", err)
			return 2
		}
		if *asJSON {
			b, _ := json.Marshal(map[string]string{"agentsMd": as.public(), "claudeMd": string(cs), "version": lsp.Version})
			fmt.Println(string(b))
		} else {
			line := agentsFileName + ": " + as.public()
			if why != nil {
				line += " (" + why.Error() + ")"
			}
			fmt.Println(line)
			fmt.Println(claudeFileName + ": " + string(cs))
		}
		if agentsNeedsWork(as, cs) {
			return 1
		}
		return 0
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
