package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// publishedUntilBarrier opens (or re-sends) a document and returns every
// diagnostics publication the server makes for it, by URI, the last one
// per URI winning.
func (s *lspSession) publishedUntilBarrier(uri, text string, version int) map[string][]Diagnostic {
	s.t.Helper()
	if version == 1 {
		s.send("textDocument/didOpen", DidOpenTextDocumentParams{
			TextDocument: TextDocumentItem{URI: uri, LanguageID: "iec-st", Version: 1, Text: text},
		}, false)
	} else {
		s.send("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": text}},
		}, false)
	}
	return s.untilBarrier(uri)
}

// watchedChanged reports files changed on disk, as the client's file
// watcher does, and returns what the server publishes for it.
func (s *lspSession) watchedChanged(typ int, paths ...string) map[string][]Diagnostic {
	s.t.Helper()
	var changes []FileEvent
	for _, p := range paths {
		changes = append(changes, FileEvent{URI: pathToURI(p), Type: typ})
	}
	return s.untilBarrierAfter(pathToURI(paths[0]), func() {
		s.send("workspace/didChangeWatchedFiles", DidChangeWatchedFilesParams{Changes: changes}, false)
	})
}

// untilBarrier collects publications until the response to a hover sent
// after them: the server handles messages in order, so everything
// published for what came before precedes the hover's response.
func (s *lspSession) untilBarrier(uri string) map[string][]Diagnostic {
	s.t.Helper()
	return s.untilBarrierAfter(uri, func() {})
}

// untilBarrierAfter is untilBarrier with first sent ahead of the barrier,
// in order, from the same goroutine: the pipes are unbuffered, and the
// server may be writing its publications while these are written.
func (s *lspSession) untilBarrierAfter(uri string, first func()) map[string][]Diagnostic {
	s.t.Helper()
	id := s.nextID + 1
	go func() {
		first()
		s.send("textDocument/hover", TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{Line: 0, Character: 0},
		}, true)
	}()
	out := map[string][]Diagnostic{}
	for {
		m := s.recv()
		if m.ID != nil {
			var got int
			if json.Unmarshal(*m.ID, &got) == nil && got == id {
				return out
			}
			continue
		}
		if m.Method != "textDocument/publishDiagnostics" {
			continue
		}
		var pub PublishDiagnosticsParams
		if err := json.Unmarshal(m.Params, &pub); err != nil {
			s.t.Fatal(err)
		}
		out[pub.URI] = pub.Diagnostics
	}
}

// writeFiles lays files out under dir, bumping each modtime past the
// last write so the manifest cache sees a change even within one tick.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	later := time.Now().Add(time.Duration(bump.Add(1)) * time.Second)
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, later, later); err != nil {
			t.Fatal(err)
		}
	}
}

var bump atomic.Int64

func startedSession(t *testing.T) *lspSession {
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)
	return s
}

// A manifest tag that does not read (#518) is reported on the manifest, on
// that tag's name, and the programs are not blamed for it: the tags that do
// read stay in scope, and a use of the bad one is not "undeclared". Before,
// the server dropped every tag and each program said its first tag was an
// undeclared identifier, with nothing on the manifest.
func TestManifestTagErrorIsReportedOnTheManifest(t *testing.T) {
	dir := t.TempDir()
	good := "name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n" +
		"  - { name: Level, role: input, type: REAL, init: 42.0 }\n" +
		"  - { name: PumpRun, role: output, type: BOOL, init: false }\n"
	mpath := filepath.Join(dir, "nautilus.yaml")
	if err := os.WriteFile(mpath, []byte(good+"  - { name: Horn, role: command, type: BOOL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "main.st")
	src := "PROGRAM Main\nPumpRun := Level > 50.0;\nEND_PROGRAM\n"
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	uri, muri := pathToURI(prog), pathToURI(mpath)

	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)

	got := s.publishedUntilBarrier(uri, src, 1)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the manifest's tag error: %+v", d)
	}
	md := got[muri]
	if len(md) != 1 || !strings.Contains(md[0].Message, "tag Horn") || !strings.Contains(md[0].Message, "role must be") {
		t.Fatalf("nautilus.yaml diagnostics = %+v, want one naming tag Horn's role", md)
	}
	if md[0].Range.Start.Line != 7 {
		t.Errorf("tag Horn's error is on line %d, want 7 (its tags[].name)", md[0].Range.Start.Line)
	}

	// A program using the bad tag is not told it is undeclared either.
	src2 := "PROGRAM Main\nPumpRun := Horn AND Level > 50.0;\nEND_PROGRAM\n"
	if d := s.publishedUntilBarrier(uri, src2, 2)[uri]; len(d) != 0 {
		t.Errorf("main.st's use of the unreadable tag Horn is reported: %+v", d)
	}
	// A real error in the program still is.
	src3 := "PROGRAM Main\nPumpRun := Bogus;\nEND_PROGRAM\n"
	if d := s.publishedUntilBarrier(uri, src3, 3)[uri]; len(d) != 1 || !strings.Contains(d[0].Message, "Bogus") {
		t.Errorf("main.st's undeclared Bogus = %+v, want it reported", d)
	}

	// Fixed: the next analysis clears the manifest's diagnostic.
	if err := os.WriteFile(mpath, []byte(good+"  - { name: Horn, role: output, type: BOOL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(mpath, later, later); err != nil {
		t.Fatal(err)
	}
	got = s.publishedUntilBarrier(uri, src2, 4)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st after the fix: %+v", d)
	}
	if md, ok := got[muri]; !ok || len(md) != 0 {
		t.Errorf("nautilus.yaml after the fix = %+v (published %v), want cleared", md, ok)
	}
}

// A manifest that does not read at all (an unknown field) is reported on
// the line YAML names, and with no tag known, a program's tags are not
// called undeclared.
func TestUnreadableManifestIsReportedOnTheManifest(t *testing.T) {
	dir := t.TempDir()
	mpath := filepath.Join(dir, "nautilus.yaml")
	if err := os.WriteFile(mpath, []byte("name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\ntags:\n"+
		"  - { name: Level, rol: input, type: REAL }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "main.st")
	src := "PROGRAM Main\nLevel := Level + 1.0;\nEND_PROGRAM\n"
	if err := os.WriteFile(prog, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	uri, muri := pathToURI(prog), pathToURI(mpath)
	s := startSession(t)
	s.recvResponse(s.send("initialize", map[string]any{}, true))
	s.send("initialized", map[string]any{}, false)
	got := s.publishedUntilBarrier(uri, src, 1)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the unreadable manifest: %+v", d)
	}
	md := got[muri]
	if len(md) != 1 || !strings.Contains(md[0].Message, "rol") || md[0].Range.Start.Line != 5 {
		t.Fatalf("nautilus.yaml diagnostics = %+v, want one on line 6 naming field rol", md)
	}
}

const liftHead = "name: lift-station\ntasks:\n  - program: main.st\n    scan: 100ms\n"

const liftTags = "tags:\n" +
	"  - { name: Level, role: input, type: REAL, init: 42.0 }\n" +
	"  - { name: PumpRun, role: output, type: BOOL, init: false }\n"

// A manifest that does not read and was never read well: only the names it
// declares are spared, so a name that is no tag is still reported (#518
// review).
func TestUnreadableManifestStillReportsANameThatIsNoTag(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"nautilus.yaml": liftHead + liftTags + "  - { name: Horn, rol: output, type: BOOL }\n",
	})
	uri := pathToURI(filepath.Join(dir, "main.st"))
	s := startedSession(t)
	if d := s.publishedUntilBarrier(uri, "PROGRAM Main\nPumpRun := Level > 50.0;\nEND_PROGRAM\n", 1)[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the declared tags of an unreadable manifest: %+v", d)
	}
	// (A declared tag ahead of it would stop the compile first: it reports
	// one error, and the tag's is the manifest's.)
	d := s.publishedUntilBarrier(uri, "PROGRAM Main\nVAR x : REAL; END_VAR\nx := Bogus;\nEND_PROGRAM\n", 2)[uri]
	if len(d) != 1 || !strings.Contains(d[0].Message, `"Bogus"`) {
		t.Errorf("an undeclared name that is no tag = %+v, want it reported", d)
	}
}

// A tag-file is a bare YAML list: a bad tag in one is reported on the
// tag-file, at its name, and so is a tag-file that does not read.
func TestTagFileErrorsAreReportedOnTheTagFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"nautilus.yaml": liftHead + "tag-files: [tags/io.yaml]\n" + liftTags,
		"tags/io.yaml":  "- { name: Spare, role: input, type: BOOL }\n- { name: Horn, role: command, type: BOOL }\n",
	})
	uri := pathToURI(filepath.Join(dir, "main.st"))
	turi := pathToURI(filepath.Join(dir, "tags", "io.yaml"))
	s := startedSession(t)
	got := s.publishedUntilBarrier(uri, "PROGRAM Main\nPumpRun := Horn AND Level > 50.0;\nEND_PROGRAM\n", 1)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st blamed for the tag-file's bad tag: %+v", d)
	}
	td := got[turi]
	if len(td) != 1 || !strings.Contains(td[0].Message, "tag Horn") || td[0].Range.Start.Line != 1 || td[0].Range.Start.Character != 10 {
		t.Fatalf("tags/io.yaml diagnostics = %+v, want tag Horn's error on line 2 col 11", td)
	}
	if d := got[pathToURI(filepath.Join(dir, "nautilus.yaml"))]; len(d) != 0 {
		t.Errorf("the tag-file's error also landed on nautilus.yaml: %+v", d)
	}

	// The tag-file stops reading: the YAML error is on its line there.
	writeFiles(t, dir, map[string]string{
		"tags/io.yaml": "- { name: Spare, role: input, type: BOOL }\n- { name: Horn, rol: output }\n",
	})
	got = s.watchedChanged(2, filepath.Join(dir, "tags", "io.yaml"))
	td = got[turi]
	if len(td) != 1 || !strings.Contains(td[0].Message, "rol") || td[0].Range.Start.Line != 1 {
		t.Fatalf("tags/io.yaml diagnostics = %+v, want the YAML error on line 2", td)
	}
}

// Rename and references find a tag declared in a tag-file (a bare list).
func TestRenameAndReferencesFindATagFileTag(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"nautilus.yaml": liftHead + "tag-files: [tags/io.yaml]\n" + liftTags,
		"tags/io.yaml":  "- { name: Spare, role: input, type: BOOL }\n",
	})
	mpath, tpath := filepath.Join(dir, "nautilus.yaml"), filepath.Join(dir, "tags", "io.yaml")
	edits, err := renameTagInManifest(mpath, "Spare", "Reserve")
	if err != nil {
		t.Fatal(err)
	}
	if e := edits[tpath]; len(e) != 1 || e[0].Range.Start.Line != 0 || e[0].NewText != "Reserve" {
		t.Errorf("rename edits in the tag-file = %+v, want its name: on line 1", edits)
	}
	if d := manifestTagDecls(mpath, "Spare")[tpath]; len(d) != 1 {
		t.Errorf("the tag-file's declaration of Spare is not found: %+v", d)
	}
}

// A good read, then a manifest that does not read: the last good tags keep
// serving the programs, and the manifest still shows its error.
func TestManifestBrokenAfterAGoodReadIsReported(t *testing.T) {
	dir := t.TempDir()
	mpath := filepath.Join(dir, "nautilus.yaml")
	writeFiles(t, dir, map[string]string{"nautilus.yaml": liftHead + liftTags})
	uri, muri := pathToURI(filepath.Join(dir, "main.st")), pathToURI(mpath)
	src := "PROGRAM Main\nPumpRun := Level > 50.0;\nEND_PROGRAM\n"
	s := startedSession(t)
	if got := s.publishedUntilBarrier(uri, src, 1); len(got[uri]) != 0 || len(got[muri]) != 0 {
		t.Fatalf("a good project has diagnostics: %+v", got)
	}
	writeFiles(t, dir, map[string]string{"nautilus.yaml": liftHead + liftTags + "  - { name: Horn, rol: output }\n"})
	got := s.publishedUntilBarrier(uri, src, 2)
	if d := got[uri]; len(d) != 0 {
		t.Errorf("main.st lost the last good tags: %+v", d)
	}
	if d := got[muri]; len(d) != 1 || !strings.Contains(d[0].Message, "rol") {
		t.Errorf("nautilus.yaml diagnostics = %+v, want the read error", d)
	}
}

// Tag names are case-insensitive: a program writing horn for the manifest's
// Horn is not blamed for Horn's bad role.
func TestUnreadTagMatchesAnyCase(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"nautilus.yaml": liftHead + liftTags + "  - { name: Horn, role: command, type: BOOL }\n"})
	uri := pathToURI(filepath.Join(dir, "main.st"))
	s := startedSession(t)
	if d := s.publishedUntilBarrier(uri, "PROGRAM Main\nPumpRun := horn;\nEND_PROGRAM\n", 1)[uri]; len(d) != 0 {
		t.Errorf("main.st's use of horn (Horn) is reported: %+v", d)
	}
}

// An SFC action association to a tag that does not read is not "neither an
// ACTION nor a declared variable": the tag is declared, if badly.
func TestSFCAssociationToAnUnreadTag(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"nautilus.yaml": liftHead + liftTags + "  - { name: Horn, role: command, type: BOOL }\n"})
	uri := pathToURI(filepath.Join(dir, "chart.sfc"))
	s := startedSession(t)
	src := "PROGRAM P\nSFC\n  INITIAL_STEP Idle:\n    N Horn;\n  END_STEP\nEND_SFC\nEND_PROGRAM\n"
	for _, d := range s.publishedUntilBarrier(uri, src, 1)[uri] {
		if d.Severity == SeverityError {
			t.Errorf("chart.sfc blamed for the manifest's tag Horn: %s", d.Message)
		}
	}
}

// The editor reports a manifest saved or deleted (the client watches YAML):
// the squiggles follow without a keystroke in any program.
func TestWatchedManifestChangesRepublish(t *testing.T) {
	dir := t.TempDir()
	mpath := filepath.Join(dir, "nautilus.yaml")
	bad := liftHead + liftTags + "  - { name: Horn, role: command, type: BOOL }\n"
	writeFiles(t, dir, map[string]string{"nautilus.yaml": bad})
	muri := pathToURI(mpath)
	s := startedSession(t)

	// No program open: a saved manifest is still reported.
	if d := s.watchedChanged(2, mpath)[muri]; len(d) != 1 || !strings.Contains(d[0].Message, "tag Horn") {
		t.Fatalf("nautilus.yaml after a watched save = %+v, want tag Horn's error", d)
	}

	// Fixed and saved: cleared, and the open program is analyzed again.
	uri := pathToURI(filepath.Join(dir, "main.st"))
	s.publishedUntilBarrier(uri, "PROGRAM Main\nPumpRun := Horn;\nEND_PROGRAM\n", 1)
	writeFiles(t, dir, map[string]string{"nautilus.yaml": liftHead + liftTags + "  - { name: Horn, role: input, type: BOOL }\n"})
	got := s.watchedChanged(2, mpath)
	if d, ok := got[muri]; !ok || len(d) != 0 {
		t.Errorf("nautilus.yaml after the fix = %+v (published %v), want cleared", d, ok)
	}
	if d, ok := got[uri]; !ok || len(d) != 0 {
		t.Errorf("main.st after the fix = %+v (published %v), want analyzed again, clean", d, ok)
	}

	// Broken again, then deleted: the squiggle goes with the file.
	writeFiles(t, dir, map[string]string{"nautilus.yaml": bad})
	if d := s.watchedChanged(2, mpath)[muri]; len(d) != 1 {
		t.Fatalf("nautilus.yaml broken again = %+v", d)
	}
	if err := os.Remove(mpath); err != nil {
		t.Fatal(err)
	}
	if d, ok := s.watchedChanged(3, mpath)[muri]; !ok || len(d) != 0 {
		t.Errorf("nautilus.yaml deleted = %+v (published %v), want cleared", d, ok)
	}
}

// A manifest is read once per change, not once per keystroke: a broken one
// and its diagnostics are cached against the files' modtimes too.
func TestManifestReadIsCached(t *testing.T) {
	for name, body := range map[string]string{
		"bad tag":    liftHead + liftTags + "  - { name: Horn, role: command, type: BOOL }\n",
		"unreadable": liftHead + liftTags + "  - { name: Horn, rol: output }\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"nautilus.yaml": body})
			mpath := filepath.Join(dir, project.ManifestName)
			a, b := manifestAt(mpath), manifestAt(mpath)
			if a != b {
				t.Errorf("the manifest was read twice without a change")
			}
			if len(a.diags) == 0 {
				t.Errorf("no diagnostics on the cached entry")
			}
		})
	}
}
