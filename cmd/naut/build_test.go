package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		// `naut run` from the project directory makes the OPC UA
		// certificate store there: its private key must never ship.
		"pki/own/cert.der":          "CERT",
		"pki/own/private/key.pem":   "-----BEGIN PRIVATE KEY-----",
		"pki/trusted/certs/a.der":   "CERT",
		"certs/own/private/key.pem": "-----BEGIN PRIVATE KEY-----", // a store under another name
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
		if strings.Contains(name, "node_modules") || strings.HasPrefix(name, "vendor/") || strings.HasPrefix(name, "pki/") || strings.HasPrefix(name, "certs/") {
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

// The e2e project for TestBuiltControllerActivatesWarm: a library FB, and
// two tasks that each run a TON and an instance of it. Each program writes
// a revision marker, so the test can tell which commit every task runs.
const (
	e2eManifest = `name: warm-e2e
server:
  online-edits: true
tasks:
  - program: main.st
    scan: 20ms
  - name: totals
    program: totals.st
    scan: 20ms
tags:
  - { name: MainET,  role: output, type: TIME }
  - { name: MainSum, role: output, type: REAL }
  - { name: MainRev, role: output, type: INT }
  - { name: AuxET,   role: output, type: TIME }
  - { name: AuxSum,  role: output, type: REAL }
  - { name: AuxRev,  role: output, type: INT }
driver:
  type: memory
`
	e2eBlocks = `FUNCTION_BLOCK Totalizer
VAR_INPUT X : REAL; END_VAR
VAR_OUTPUT Y : REAL; END_VAR
VAR Sum : REAL; END_VAR
Sum := Sum + X;
Y := Sum;
END_FUNCTION_BLOCK
`
	e2eMain = `PROGRAM Main
VAR Dwell : TON; Acc : Totalizer; END_VAR
Dwell(IN := TRUE, PT := T#1H);
MainET := Dwell.ET;
Acc(X := 1.0);
MainSum := Acc.Y;
MainRev := REV;
END_PROGRAM
`
	e2eAux = `PROGRAM Totals
VAR Hold : TON; Acc : Totalizer; END_VAR
Hold(IN := TRUE, PT := T#1H);
AuxET := Hold.ET;
Acc(X := 1.0);
AuxSum := Acc.Y;
AuxRev := REV;
END_PROGRAM
`
)

// TestBuiltControllerActivatesWarm runs a REAL built controller: it builds
// the naut CLI, has that binary `naut build` a two-commit, two-task git
// project into a controller, runs the controller with no Go toolchain or
// git on PATH, lets both tasks' timers and FB instances run, then
// activates the older commit over HTTP. The activation must rebuild every
// task's source from the embedded history (library composed ahead), swap
// both tasks, and carry each one's TON and FB instance state.
func TestBuiltControllerActivatesWarm(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a controller binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()

	// 1. The real CLI, built from this package.
	naut := filepath.Join(dir, "naut")
	if out, err := exec.Command(goBin, "build", "-o", naut, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut: %v\n%s", err, out)
	}

	// 2. A git project with two commits: rev 1 (the one activated) and rev
	// 2 (HEAD, the one built).
	proj := filepath.Join(dir, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", proj}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeRev := func(rev string) {
		t.Helper()
		for name, body := range map[string]string{
			"nautilus.yaml": e2eManifest,
			"blocks.st":     e2eBlocks,
			"main.st":       strings.Replace(e2eMain, "REV", rev, 1),
			"totals.st":     strings.Replace(e2eAux, "REV", rev, 1),
		} {
			if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q", "-b", "main")
	writeRev("1")
	git("add", "-A")
	git("commit", "-q", "-m", "rev 1")
	rev1 := git("rev-parse", "HEAD")
	writeRev("2")
	git("add", "-A")
	git("commit", "-q", "-m", "rev 2")

	// 3. naut build, by the real CLI: the controller is that binary with
	// the project and its history appended.
	ctl := filepath.Join(dir, "ctl")
	if out, err := exec.Command(naut, "build", "-o", ctl, proj).CombinedOutput(); err != nil {
		t.Fatalf("naut build: %v\n%s", err, out)
	}

	// 4. Run it: an empty PATH proves it needs neither Go nor git, and
	// NAUTILUS_TOKEN is left unset so online edits need no token.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	var logs bytes.Buffer
	cmd := exec.Command(ctl)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=", "HOME=" + dir, "NAUTILUS_ADDR=" + addr}
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("controller output:\n%s", logs.String())
		}
	})
	base := "http://" + addr
	client := &http.Client{Timeout: 5 * time.Second}
	state := func() map[string]float64 {
		t.Helper()
		resp, err := client.Get(base + "/api/state")
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		var f struct {
			Tags map[string]any `json:"tags"`
		}
		if json.NewDecoder(resp.Body).Decode(&f) != nil {
			return nil
		}
		out := map[string]float64{}
		for k, v := range f.Tags {
			if n, ok := v.(float64); ok {
				out[k] = n
			}
		}
		return out
	}

	// 5. Let both tasks run a while: each TON past 1 s, each integrator
	// well into the tens.
	var before map[string]float64
	deadline := time.Now().Add(20 * time.Second)
	for {
		before = state()
		if before != nil && before["MainET"] >= 1000 && before["AuxET"] >= 1000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("controller never ran both tasks for 1 s: state %v", before)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if before["MainRev"] != 2 || before["AuxRev"] != 2 {
		t.Fatalf("the built controller should boot HEAD (rev 2): %v", before)
	}

	// 6. Activate rev 1 through the API, by short sha.
	body, _ := json.Marshal(map[string]string{"sha": rev1[:7]})
	resp, err := client.Post(base+"/api/program/activate", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var act struct {
		Programs []struct {
			Task   string   `json:"task"`
			Resets []string `json:"resets"`
		} `json:"programs"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&act)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate = %d: %s", resp.StatusCode, act.Error)
	}
	swapped := map[string]bool{}
	for _, p := range act.Programs {
		swapped[p.Task] = true
		if len(p.Resets) != 0 {
			t.Errorf("task %s reset %v: nothing in it changed but a constant", p.Task, p.Resets)
		}
	}
	if len(act.Programs) != 2 || !swapped["main"] || !swapped["totals"] {
		t.Fatalf("activation should swap both tasks: %+v", act.Programs)
	}

	// 7. Both tasks now run rev 1, and neither restarted its timer or its
	// FB instance: a cold swap would read a few scans' worth, under the
	// pre-activation values.
	var after map[string]float64
	deadline = time.Now().Add(10 * time.Second)
	for {
		after = state()
		if after != nil && after["MainRev"] == 1 && after["AuxRev"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("both tasks should run rev 1 after activation: %v", after)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, name := range []string{"MainET", "MainSum", "AuxET", "AuxSum"} {
		if after[name] < before[name] {
			t.Errorf("%s went %v → %v across the activation: state restarted instead of carrying",
				name, before[name], after[name])
		}
	}
}
