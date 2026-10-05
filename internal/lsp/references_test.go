package lsp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// refsAt asks the session for the references of the identifier at
// (line, char) in uri.
func (s *lspSession) refsAt(uri string, line, char int, includeDecl bool) []Location {
	s.t.Helper()
	id := s.send("textDocument/references", ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: char},
		Context:      ReferenceContext{IncludeDeclaration: includeDecl},
	}, true)
	var locs []Location
	if err := json.Unmarshal(s.recvResponse(id), &locs); err != nil {
		s.t.Fatal(err)
	}
	return locs
}

// refOpen sends didOpen and swallows the diagnostics it triggers.
func (s *lspSession) refOpen(uri, text string) {
	s.t.Helper()
	s.send("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: text},
	}, false)
	s.recv()
}

// refLines renders locations as "base:line" (1-based), checking on the way
// that every range covers exactly the expected name in that file's text.
func refLines(t *testing.T, locs []Location, name string, texts map[string]string) []string {
	t.Helper()
	var out []string
	for _, l := range locs {
		text, ok := texts[l.URI]
		if !ok {
			t.Fatalf("unexpected file in references: %s", l.URI)
		}
		line := lineText(text, l.Range.Start.Line+1)
		if l.Range.Start.Line != l.Range.End.Line || l.Range.End.Character > len(line) ||
			!strings.EqualFold(line[l.Range.Start.Character:l.Range.End.Character], name) {
			t.Fatalf("%s %+v does not cover %q: line %q", l.URI, l.Range, name, line)
		}
		out = append(out, fmt.Sprintf("%s:%d", filepath.Base(l.URI), l.Range.Start.Line+1))
	}
	return out
}

func refSameLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if strings.Join(g, " ") != strings.Join(w, " ") {
		t.Fatalf("references\n got  %v\n want %v", got, want)
	}
}

func TestReferencesAdvertised(t *testing.T) {
	s := startSession(t)
	id := s.send("initialize", map[string]any{}, true)
	var init InitializeResult
	if err := json.Unmarshal(s.recvResponse(id), &init); err != nil {
		t.Fatal(err)
	}
	if !init.Capabilities.ReferencesProvider {
		t.Fatalf("referencesProvider not advertised: %+v", init.Capabilities)
	}
}

// A single file: a POU-local is found only inside its POU (renameSrc has
// two FBs, each with its own err), and includeDeclaration drops the VAR line.
func TestReferencesLocalSingleFile(t *testing.T) {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///blocks.st"
	s.refOpen(uri, renameSrc)
	texts := map[string]string{uri: renameSrc}

	// Alpha's err, asked from its use on line 5.
	refSameLines(t, refLines(t, s.refsAt(uri, 4, 1, true), "err", texts), "blocks.st:3", "blocks.st:5")
	refSameLines(t, refLines(t, s.refsAt(uri, 4, 1, false), "err", texts), "blocks.st:5")
	// Beta's err, asked from its declaration.
	refSameLines(t, refLines(t, s.refsAt(uri, 9, 5, true), "err", texts), "blocks.st:10", "blocks.st:12")
	// A keyword answers null, not an error.
	if got := s.refsAt(uri, 1, 1, true); got != nil {
		t.Fatalf("VAR: %+v", got)
	}
}

// A PROGRAM's local is not visible in an FB's body: a same-named but
// undeclared use inside the FB is not one of its references.
func TestReferencesProgramLocalNotInFBBody(t *testing.T) {
	const src = "FUNCTION_BLOCK F\nVAR\n  x : INT;\nEND_VAR\nx := y;\nEND_FUNCTION_BLOCK\n\n" +
		"PROGRAM P\nVAR\n  y : INT;\nEND_VAR\ny := 1;\nEND_PROGRAM\n"
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///scoped.st"
	s.refOpen(uri, src)
	refSameLines(t, refLines(t, s.refsAt(uri, 11, 0, true), "y", map[string]string{uri: src}), "scoped.st:10", "scoped.st:12")
}

// refProject lays out a project directory and returns its files' paths.
func refProject(t *testing.T, files map[string]string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
	}
	return dir, paths
}

// refTexts is the files' text keyed the way references report them.
func refTexts(files, paths map[string]string) map[string]string {
	out := map[string]string{}
	for name, body := range files {
		out[pathToURI(paths[name])] = body
	}
	return out
}

// A tag across the project: every program that binds it, plus the
// manifest's declaration and its tag-meta key — not other.st's own local
// that happens to share the name.
func TestReferencesTagAcrossProject(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": renameManifest,
		"program.st":    renameProgram,
		"sim.st":        renameSim,
		"other.st":      renameOther,
	}
	_, paths := refProject(t, files)
	texts := refTexts(files, paths)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	progURI := pathToURI(paths["program.st"])
	s.refOpen(progURI, renameProgram)

	got := refLines(t, s.refsAt(progURI, 8, 15, true), "TempC", texts)
	refSameLines(t, got,
		"program.st:3", "program.st:9", // VAR_EXTERNAL binding, use
		"sim.st:3", "sim.st:8", "sim.st:8", // binding, TempC := TempC + …
		"nautilus.yaml:12", "nautilus.yaml:18", // tags[].name, tag-meta key
	)
	if got[0] != "program.st:3" {
		t.Errorf("the requesting file should come first: %v", got)
	}
	// Without declarations: the uses, and tag-meta (a mention, not a declaration).
	refSameLines(t, refLines(t, s.refsAt(progURI, 8, 15, false), "TempC", texts),
		"program.st:9", "sim.st:8", "sim.st:8", "nautilus.yaml:18")

	// An unsaved buffer wins over disk: sim.st open with an extra use.
	simEdited := strings.Replace(renameSim, "END_PROGRAM", "heat := TempC;\nEND_PROGRAM", 1)
	s.refOpen(pathToURI(paths["sim.st"]), simEdited)
	texts[pathToURI(paths["sim.st"])] = simEdited
	refSameLines(t, refLines(t, s.refsAt(progURI, 8, 15, false), "TempC", texts),
		"program.st:9", "sim.st:8", "sim.st:8", "sim.st:9", "nautilus.yaml:18")

	// A dt-tag mention counts as a reference of that tag.
	refSameLines(t, refLines(t, s.refsAt(progURI, 3, 5, true), "ScanDtS", texts),
		"program.st:4", "nautilus.yaml:7", "nautilus.yaml:13")
}

// Shadowing: an FB in the same file declares its own TempC. The program's
// TempC is the tag (project-wide); the FB's is a local (the FB only).
const shadowProgram = `FUNCTION_BLOCK Heater
VAR
    TempC : REAL;
END_VAR
TempC := TempC + 1.0;
END_FUNCTION_BLOCK

PROGRAM Main
VAR_EXTERNAL
    TempC : REAL;
END_VAR
VAR
    h : Heater;
END_VAR
h();
TempC := 2.0;
END_PROGRAM
`

func TestReferencesLocalShadowsTag(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": renameManifest,
		"program.st":    shadowProgram,
		"sim.st":        renameSim,
	}
	_, paths := refProject(t, files)
	texts := refTexts(files, paths)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(paths["program.st"])
	s.refOpen(uri, shadowProgram)

	// The FB's local: its declaration and its two uses, nothing else.
	refSameLines(t, refLines(t, s.refsAt(uri, 4, 0, true), "TempC", texts),
		"program.st:3", "program.st:5", "program.st:5")
	// The program's tag: its binding and use, sim.st, the manifest — never
	// the FB's lines.
	refSameLines(t, refLines(t, s.refsAt(uri, 15, 0, true), "TempC", texts),
		"program.st:10", "program.st:16",
		"sim.st:3", "sim.st:8", "sim.st:8",
		"nautilus.yaml:12", "nautilus.yaml:18")
}

// Members: t1.Q finds t1.Q and t1's own Q => binding, not t2.Q, not the
// local Q; the local Q finds neither member. T#1s is a literal, not a
// reference to a variable named T.
const memberSrc = `PROGRAM P
VAR
    t1 : TON;
    t2 : TON;
    Q : BOOL;
    T : BOOL;
    z : BOOL;
END_VAR
t1(IN := Q, PT := T#1s, Q => z);
t2(IN := T, PT := T#2s);
Q := t1.Q AND t2.Q;
IF t1.Q THEN
    T := NOT T;
END_IF;
END_PROGRAM
`

func TestReferencesMemberAccess(t *testing.T) {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///member.st"
	s.refOpen(uri, memberSrc)
	texts := map[string]string{uri: memberSrc}

	// The Q of "t1.Q" on line 11 ("Q := t1.Q AND t2.Q;", Q at column 8).
	tQ := s.refsAt(uri, 10, 8, true)
	refSameLines(t, refLines(t, tQ, "Q", texts), "member.st:9", "member.st:11", "member.st:12")
	if tQ[0].Range.Start.Character != 24 { // "Q => z" inside t1's call
		t.Errorf("t1's Q => binding: %+v", tQ[0].Range)
	}
	// The same set asked from the formal: Q in t1(… Q => z).
	refSameLines(t, refLines(t, s.refsAt(uri, 8, 24, true), "Q", texts), "member.st:9", "member.st:11", "member.st:12")
	// t2.Q is only t2's.
	refSameLines(t, refLines(t, s.refsAt(uri, 10, 17, true), "Q", texts), "member.st:11")
	// The local Q: declaration, IN := Q, Q := … — no member, no formal.
	refSameLines(t, refLines(t, s.refsAt(uri, 10, 0, true), "Q", texts), "member.st:5", "member.st:9", "member.st:11")
	// The local T: never the T of T#1s / T#2s.
	refSameLines(t, refLines(t, s.refsAt(uri, 5, 4, true), "T", texts),
		"member.st:6", "member.st:10", "member.st:13", "member.st:13")
	// t1 itself: declaration, call, the two t1.Q bases.
	refSameLines(t, refLines(t, s.refsAt(uri, 2, 5, true), "t1", texts),
		"member.st:3", "member.st:9", "member.st:11", "member.st:12")
	// IN in t1's call is t1.IN — t2's IN is another instance's.
	refSameLines(t, refLines(t, s.refsAt(uri, 8, 3, true), "IN", texts), "member.st:9")
}

// A buffer that does not parse (mid-keystroke) still answers from the
// recovered declarations, including on the broken line itself.
func TestReferencesInNonParsingBuffer(t *testing.T) {
	src := typingSrc("Full := settle.")
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	const uri = "file:///typing.st"
	s.refOpen(uri, src)
	texts := map[string]string{uri: src}
	refSameLines(t, refLines(t, s.refsAt(uri, 9, 3, true), "settle", texts),
		"typing.st:10", "typing.st:15", "typing.st:16")
	refSameLines(t, refLines(t, s.refsAt(uri, 15, 0, true), "Full", texts),
		"typing.st:11", "typing.st:16")
}

// Diagrams: a tag bound by an .fbd and an .ld program is reported on the
// diagram files' own lines — including inside a multi-line FBD call — and
// a diagram-local instance resolves from inside the diagram.
const diagManifest = `name: diag
tasks:
  - program: main.st
    scan: 100ms
  - name: level
    program: level.fbd
    scan: 100ms
  - name: perms
    program: perms.ld
    scan: 100ms
tags:
  - { name: Level, role: state, init: 0.0 }
  - { name: Speed, role: state, init: 0.0 }
  - { name: Run,   role: state, init: false }
  - { name: Stop,  role: state, init: false }
`

const diagMain = `PROGRAM Main
VAR_EXTERNAL
    Level : REAL;
END_VAR
Level := Level + 0.1;
END_PROGRAM
`

const diagFBD = `PROGRAM LevelControl
VAR_EXTERNAL
    Level : REAL;
    Speed : REAL;
    Run   : BOOL;
END_VAR
FBD
  // Level here is in a comment and does not count.
  lic : PID(AUTO := Run, PV := Level,
            SP := 50.0, KP := 1.0)
  Speed := lic.CV
  hi = GT(Level, 90.0)
END_FBD
END_PROGRAM
`

const diagLD = `PROGRAM Perms
VAR_EXTERNAL
    Level : REAL;
    Run   : BOOL;
    Stop  : BOOL;
END_VAR
LD
  RUNG r1
    /Stop GT(Level, 10.0) ( Run )
END_LD
END_PROGRAM
`

func TestReferencesInDiagramsReportDiagramLines(t *testing.T) {
	files := map[string]string{
		"nautilus.yaml": diagManifest,
		"main.st":       diagMain,
		"level.fbd":     diagFBD,
		"perms.ld":      diagLD,
	}
	_, paths := refProject(t, files)
	texts := refTexts(files, paths)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	mainURI := pathToURI(paths["main.st"])
	s.refOpen(mainURI, diagMain)

	refSameLines(t, refLines(t, s.refsAt(mainURI, 4, 0, true), "Level", texts),
		"main.st:3", "main.st:5", "main.st:5",
		"level.fbd:3", "level.fbd:9", "level.fbd:12", // binding, PV := Level (multi-line call), GT(Level…)
		"perms.ld:3", "perms.ld:9",
		"nautilus.yaml:12")

	// From inside the diagram: the instance lic is declared by its
	// declare-and-call line and used as lic.CV.
	fbdURI := pathToURI(paths["level.fbd"])
	s.refOpen(fbdURI, diagFBD)
	refSameLines(t, refLines(t, s.refsAt(fbdURI, 10, 12, true), "lic", texts), "level.fbd:9", "level.fbd:11")
	refSameLines(t, refLines(t, s.refsAt(fbdURI, 10, 12, false), "lic", texts), "level.fbd:11")
	// Speed, asked from the diagram, crosses back to the manifest.
	refSameLines(t, refLines(t, s.refsAt(fbdURI, 10, 3, true), "Speed", texts),
		"level.fbd:4", "level.fbd:11", "nautilus.yaml:13")

	// A ladder file: Stop is a contact on rung r1's line.
	ldURI := pathToURI(paths["perms.ld"])
	s.refOpen(ldURI, diagLD)
	refSameLines(t, refLines(t, s.refsAt(ldURI, 8, 6, true), "Stop", texts),
		"perms.ld:5", "perms.ld:9", "nautilus.yaml:15")
	refSameLines(t, refLines(t, s.refsAt(ldURI, 8, 6, false), "Stop", texts), "perms.ld:9")
}

// A member of a tag crosses files with the tag: Tank.Level in every
// program that binds Tank, never Tank.High or a local Level; an indexed
// base (Plt[i].Valid) is the same member whatever the index.
func TestReferencesTagMemberAcrossFiles(t *testing.T) {
	const types = "TYPE\n  Tank_Type : STRUCT\n    Level : REAL;\n    High : BOOL;\n  END_STRUCT;\n  Hdr : STRUCT\n    Valid : BOOL;\n  END_STRUCT;\nEND_TYPE\n"
	const a = "PROGRAM A\nVAR_EXTERNAL\n    Tank : Tank_Type;\nEND_VAR\nVAR\n    Level : REAL;\n    Plt : ARRAY[1..3] OF Hdr;\n    i : INT;\nEND_VAR\n" +
		"Level := Tank.Level;\nTank.High := Tank.Level > 90.0;\nPlt[2].Valid := Plt[i].Valid;\nEND_PROGRAM\n"
	const b = "PROGRAM B\nVAR_EXTERNAL\n    Tank : Tank_Type;\nEND_VAR\nTank.Level := Tank.Level + 1.0;\nEND_PROGRAM\n"
	files := map[string]string{
		"nautilus.yaml": "name: m\ntasks:\n  - program: a.st\n    scan: 100ms\n  - name: b\n    program: b.st\n    scan: 100ms\n",
		"types.st":      types,
		"a.st":          a,
		"b.st":          b,
	}
	_, paths := refProject(t, files)
	texts := refTexts(files, paths)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	uri := pathToURI(paths["a.st"])
	s.refOpen(uri, a)

	// "Level := Tank.Level;" — the member, at column 14.
	refSameLines(t, refLines(t, s.refsAt(uri, 9, 15, true), "Level", texts),
		"a.st:10", "a.st:11", "b.st:5", "b.st:5")
	// The local Level: declaration and the one plain use.
	refSameLines(t, refLines(t, s.refsAt(uri, 9, 0, true), "Level", texts), "a.st:6", "a.st:10")
	// Plt[2].Valid and Plt[i].Valid are the same member.
	refSameLines(t, refLines(t, s.refsAt(uri, 11, 8, true), "Valid", texts), "a.st:12", "a.st:12")
}
