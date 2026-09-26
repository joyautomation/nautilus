package main

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// buildIn runs runBuild against a temp project and returns its combined
// stdout+stderr and exit code — the same shape checkIn (check_manifest_test.go)
// uses for runCheck.
func buildIn(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	os.Stderr = w
	code := runBuild(append(args, dir))
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), code
}

func writeProjectFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const buildHMIManifest = "name: t\n" +
	"server:\n  hmi: hmi/build\n" +
	"tasks:\n  - program: p.st\n"

// A server.hmi directory that hasn't been built yet (the gitignored
// SvelteKit output, absent on a fresh clone) must warn, not fail — `naut
// check`/`naut run` already tolerate it, and `build` was the odd one out.
func TestRunBuildWarnsOnMissingHMIDir(t *testing.T) {
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		"nautilus.yaml": buildHMIManifest,
		"p.st":          "PROGRAM P\nEND_PROGRAM",
	})
	out := filepath.Join(dir, "out-bin")

	output, code := buildIn(t, dir, "-o", out)
	if code != 0 {
		t.Fatalf("runBuild code = %d, want 0 (a missing hmi/build warns, it does not fail); output:\n%s", code, output)
	}
	if !strings.Contains(output, "server.hmi: hmi/build is not built yet") ||
		!strings.Contains(output, "built-in dashboard") ||
		!strings.Contains(output, "npm run build in hmi/") {
		t.Fatalf("output missing the expected warning: %q", output)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("naut build must still produce a binary: stat(%s) = %v", out, err)
	}
}

// A server.hmi directory that HAS been built embeds normally, with no
// warning.
func TestRunBuildEmbedsHMIWhenPresent(t *testing.T) {
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		"nautilus.yaml":        buildHMIManifest,
		"p.st":                 "PROGRAM P\nEND_PROGRAM",
		"hmi/build/index.html": "<html>hmi shell</html>",
	})
	out := filepath.Join(dir, "out-bin")

	output, code := buildIn(t, dir, "-o", out)
	if code != 0 {
		t.Fatalf("runBuild code = %d, want 0; output:\n%s", code, output)
	}
	if strings.Contains(output, "not built yet") {
		t.Fatalf("a present hmi/build must not warn: %q", output)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, zf := range zr.File {
		if zf.Name == "hmi/build/index.html" {
			found = true
		}
	}
	if !found {
		t.Fatal("the built hmi/build must be embedded")
	}
}

// server.hmi naming a FILE instead of a directory is still a hard error —
// only "not there yet" gets the warning treatment.
func TestRunBuildErrorsWhenHMIPathIsAFile(t *testing.T) {
	dir := t.TempDir()
	writeProjectFiles(t, dir, map[string]string{
		"nautilus.yaml": buildHMIManifest,
		"p.st":          "PROGRAM P\nEND_PROGRAM",
		"hmi/build":     "not a directory, just a file",
	})
	out := filepath.Join(dir, "out-bin")

	output, code := buildIn(t, dir, "-o", out)
	if code == 0 {
		t.Fatalf("runBuild code = 0, want a hard error when server.hmi is a file, not a directory; output:\n%s", output)
	}
	if !strings.Contains(output, "not a directory") {
		t.Fatalf("output missing the not-a-directory error: %q", output)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("no binary should be produced on a hard error")
	}
}

// emitBinary must produce runner-bytes + zip + footer, be readable back via
// embeddedOffset, and — building FROM a built binary — replace the old
// archive rather than stacking a second one.
func TestBuildEmbedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	runnerBytes := []byte("FAKE-RUNNER-EXECUTABLE-BYTES")
	if err := os.WriteFile(runner, runnerBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: embedded-test\ntasks:\n  - program: program.st\n"
	os.WriteFile(filepath.Join(proj, "nautilus.yaml"), []byte(manifest), 0o644)
	os.WriteFile(filepath.Join(proj, "program.st"), []byte("PROGRAM P\nEND_PROGRAM"), 0o644)

	out := filepath.Join(dir, "out")
	if _, err := emitBinary(runner, proj, out, "", nil); err != nil {
		t.Fatal(err)
	}

	read := func(path string) (prefix []byte, files map[string]string) {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		st, _ := f.Stat()
		off, ok := embeddedOffset(f, st.Size())
		if !ok {
			t.Fatalf("%s: no embedded project", path)
		}
		prefix = make([]byte, off)
		if _, err := f.ReadAt(prefix, 0); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
		if err != nil {
			t.Fatal(err)
		}
		files = map[string]string{}
		for _, zf := range zr.File {
			r, _ := zf.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			files[zf.Name] = string(b)
		}
		return prefix, files
	}

	prefix, files := read(out)
	if !bytes.Equal(prefix, runnerBytes) {
		t.Fatal("runner bytes must be preserved verbatim")
	}
	if files["nautilus.yaml"] != manifest || files["program.st"] == "" {
		t.Fatalf("archive contents: %v", files)
	}

	// Rebuild from the BUILT binary with a changed project.
	os.WriteFile(filepath.Join(proj, "program.st"), []byte("PROGRAM P2\nEND_PROGRAM"), 0o644)
	out2 := filepath.Join(dir, "out2")
	if _, err := emitBinary(out, proj, out2, "", nil); err != nil {
		t.Fatal(err)
	}
	prefix2, files2 := read(out2)
	if !bytes.Equal(prefix2, runnerBytes) {
		t.Fatal("rebuilding from a built binary must strip the old archive")
	}
	if files2["program.st"] != "PROGRAM P2\nEND_PROGRAM" {
		t.Fatal("rebuild must carry the NEW project")
	}
}

// A build's captured history rides the archive as .history, and loadHistory
// decodes it back — the built-binary side of GET /api/program/history.
func TestBuildEmbedsHistory(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, []byte("FAKE-RUNNER"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(proj, "nautilus.yaml"), []byte("name: t\ntasks:\n  - program: p.st\n"), 0o644)
	os.WriteFile(filepath.Join(proj, "p.st"), []byte("PROGRAM P\nEND_PROGRAM"), 0o644)

	hist := []byte(`{"built":"abc123","commits":[{"sha":"abc123","short":"abc123x","subject":"s"}]}`)
	out := filepath.Join(dir, "out")
	if _, err := emitBinary(runner, proj, out, "", hist); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		t.Fatal(err)
	}
	h := loadHistory(zr, "")()
	if h == nil || h.Built != "abc123" || len(h.Commits) != 1 || h.Commits[0].Short != "abc123x" {
		t.Fatalf("embedded history decode: %+v", h)
	}
}

// A dependency tree (node_modules, vendor) never rides in the archive, even
// when it sits right inside the project directory — which it now can,
// since server.hmi points at an hmi/ subdirectory's *build output* while
// the hmi/ project itself (node_modules included) is free to live beside
// it. Without this exclusion, `naut build` would embed the whole
// SvelteKit toolchain into the CLI binary instead of a few static files.
func TestEmitBinarySkipsDependencyTrees(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, []byte("FAKE-RUNNER"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	files := map[string]string{
		"nautilus.yaml":                        "name: t\ntasks:\n  - program: p.st\n",
		"p.st":                                 "PROGRAM P\nEND_PROGRAM",
		"hmi/build/index.html":                 "<html>shell</html>",
		"hmi/node_modules/pkg/index.js":        "module.exports = {}",
		"hmi/node_modules/pkg/big-dep-tree.js": strings.Repeat("x", 10_000),
		"vendor/some/pkg/pkg.go":               "package pkg",
	}
	for name, body := range files {
		p := filepath.Join(proj, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(dir, "out")
	archiveBytes, err := emitBinary(runner, proj, out, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if archiveBytes > 1000 {
		t.Fatalf("archiveBytes = %d, want the node_modules/vendor bulk excluded", archiveBytes)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, zf := range zr.File {
		names[zf.Name] = true
	}
	if !names["hmi/build/index.html"] {
		t.Error("the hmi build output must still be embedded")
	}
	for name := range names {
		if strings.Contains(name, "node_modules") || strings.HasPrefix(name, "vendor/") {
			t.Errorf("archive contains %q, which should have been skipped", name)
		}
	}
}

// End to end: a project declaring server.hmi builds, and the SAME loader
// `naut run` uses (project.Load, over the embedded archive's fs.FS)
// resolves Server.HMI to the embedded files — which server.New then serves
// at "/" with SPA fallback, and the tag API keeps answering at "/api/*".
// This is the built-binary side of the HMI feature: `naut build`
// embeds hmi/build, and a plain `./out` run (no separate web server) is
// enough to serve it.
func TestBuildEmbedsHMIAndBuiltBinaryServesIt(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, []byte("FAKE-RUNNER"), 0o755); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(dir, "proj")
	files := map[string]string{
		"nautilus.yaml": "name: t\n" +
			"server:\n  hmi: hmi/build\n" +
			"tasks:\n  - program: p.st\n" +
			"tags:\n  - { name: SP, role: setpoint, init: 1.0 }\n",
		"p.st":                      "PROGRAM P VAR_EXTERNAL SP : REAL; END_VAR END_PROGRAM",
		"hmi/build/index.html":      "<html>hmi shell</html>",
		"hmi/build/assets/app.js":   "console.log('hi')",
		"hmi/node_modules/pkg/x.js": "module.exports = {}",
	}
	for name, body := range files {
		p := filepath.Join(proj, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(dir, "out")
	if _, err := emitBinary(runner, proj, out, "", nil); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off, ok := embeddedOffset(f, st.Size())
	if !ok {
		t.Fatal("no embedded project")
	}
	zr, err := zip.NewReader(io.NewSectionReader(f, off, st.Size()-16-off), st.Size()-16-off)
	if err != nil {
		t.Fatal(err)
	}

	p, err := project.Load(zr, "")
	if err != nil {
		t.Fatalf("project.Load over the embedded archive: %v", err)
	}
	if p.Server.HMI == nil {
		t.Fatal("Server.HMI must resolve from the embedded archive")
	}
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(rt, p.Server).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Body.String() != "<html>hmi shell</html>" {
		t.Fatalf("GET / = %d %q, want the embedded HMI's index.html", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/assets/app.js", nil))
	if rec.Code != 200 || rec.Body.String() != "console.log('hi')" {
		t.Fatalf("GET /assets/app.js = %d %q", rec.Code, rec.Body.String())
	}

	// A client-side route with no matching file falls back to index.html.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/some/spa/route", nil))
	if rec.Code != 200 || rec.Body.String() != "<html>hmi shell</html>" {
		t.Fatalf("GET /some/spa/route = %d %q, want SPA fallback", rec.Code, rec.Body.String())
	}

	// /api/* must still work — the HMI owns "/", not the API.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/state = %d, want 200", rec.Code)
	}

	// The built-in dashboard is still there, moved to /_nautilus/.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/_nautilus/", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /_nautilus/ = %d, want 200 (the built-in dashboard)", rec.Code)
	}
}
