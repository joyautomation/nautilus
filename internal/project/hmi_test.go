package project

// server.hmi: names a built HMI's directory, relative to the project — the
// same "must resolve inside the project" rule as tag-files and
// driver.manifest (see projectPath), so what `naut build` embeds is
// exactly what a reviewer can see in the checkout.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
	"github.com/joyautomation/nautilus/server"
)

// hmiManifest is a minimal single-task manifest (program.fbd from fsys's
// fixtures) with server.hmi set to hmiPath.
const hmiManifest = `
name: test-plant
server:
  addr: "localhost:9911"
  hmi: %s
tasks:
  - program: program.fbd
tags:
  - { name: Sensor,   role: input }
  - { name: Setpoint, role: setpoint, init: 50.0 }
  - { name: Actuator, role: output }
driver:
  type: memory
`

func hmiProject(hmiPath string, files map[string]string) fstest.MapFS {
	m := fsys(fmt.Sprintf(hmiManifest, hmiPath))
	for name, body := range files {
		m[name] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func TestHMIDirLoadsIntoServerOptions(t *testing.T) {
	p, err := Load(hmiProject("hmi/build", map[string]string{
		"hmi/build/index.html": "<html>shell</html>",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.HMIDir != "hmi/build" {
		t.Fatalf("HMIDir = %q, want hmi/build", p.HMIDir)
	}
	if p.Server.HMI == nil {
		t.Fatal("Server.HMI must be set when the manifest declares server.hmi")
	}
	f, err := p.Server.HMI.Open("index.html")
	if err != nil {
		t.Fatalf("Server.HMI does not resolve inside the configured directory: %v", err)
	}
	defer f.Close()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "<html>shell</html>" {
		t.Fatalf("Server.HMI served %q, want the hmi/build/index.html contents", got)
	}
}

// With no server.hmi, nothing changes: Server.HMI stays nil, so server.New
// keeps the built-in dashboard at "/" exactly as before this feature.
func TestHMIUnsetLeavesServerOptionsNil(t *testing.T) {
	p, err := Load(fsys(manifest), "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Server.HMI != nil {
		t.Fatal("Server.HMI must be nil when the manifest has no server.hmi")
	}
	if p.HMIDir != "" {
		t.Fatalf("HMIDir = %q, want empty", p.HMIDir)
	}
}

// server.hmi must resolve inside the project, exactly like tag-files and
// driver.manifest — the deployable artifact is the directory (or the
// archive built from it), so a path that escapes it would work in
// development and vanish once built.
func TestHMIPathMustStayInsideProject(t *testing.T) {
	for _, bad := range []string{"../outside", "/etc/hmi", "hmi/../../escaped"} {
		_, err := Load(hmiProject(bad, nil), "")
		if err == nil {
			t.Errorf("server.hmi accepted %q, which is outside the project", bad)
			continue
		}
		if !strings.Contains(err.Error(), "server.hmi") {
			t.Errorf("error for %q = %q, want it to name server.hmi", bad, err)
		}
	}
}

// A manifest declaring server.hmi before `npm run build` has ever run (a
// fresh checkout, `naut check`, the language server) must still load
// cleanly — Load must not require the directory to exist. But it must not
// set Server.HMI to an fs.FS with nothing in it either: server.handleHMI
// has no way to tell "empty because unbuilt" from "empty because that's
// what the build produced," and would 404 every request, including "/",
// instead of falling back to the built-in dashboard. So Load leaves
// Server.HMI nil in this case (exactly as if server.hmi were unset) and
// flags HMIMissing so a caller that wants to say why can.
func TestHMIDirNeedNotExistAtLoad(t *testing.T) {
	p, err := Load(hmiProject("hmi/build", nil), "")
	if err != nil {
		t.Fatalf("Load failed on a not-yet-built HMI dir: %v", err)
	}
	if p.Server.HMI != nil {
		t.Fatal("Server.HMI must be nil when server.hmi's directory doesn't exist yet — the built-in dashboard keeps \"/\"")
	}
	if !p.HMIMissing {
		t.Fatal("HMIMissing must be true when server.hmi's directory doesn't exist yet")
	}
	if p.HMIDir != "hmi/build" {
		t.Fatalf("HMIDir = %q, want hmi/build — callers still need to name the configured (missing) directory", p.HMIDir)
	}
}

// A present-but-empty HMI directory (a build that produced zero files,
// which shouldn't happen but isn't Load's problem to detect) is NOT the
// same as a missing one: the directory exists, so Server.HMI is set as
// usual, HMIMissing stays false, and any 404 is a real "file not in this
// build" answer rather than "there is no build."
func TestHMIDirPresentButEmptyIsNotMissing(t *testing.T) {
	fsys := hmiProject("hmi/build", map[string]string{
		"hmi/build/.keep": "",
	})
	p, err := Load(fsys, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.HMIMissing {
		t.Fatal("HMIMissing must be false when the directory exists")
	}
	if p.Server.HMI == nil {
		t.Fatal("Server.HMI must be set when the directory exists, even without index.html")
	}
	if _, err := p.Server.HMI.Open("index.html"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected a not-exist error opening a missing file inside a real build, got %v", err)
	}
}

// server.hmi naming a FILE, not a directory, is still a hard load error —
// only "not there at all" gets the HMIMissing/fallback treatment.
func TestHMIPathIsAFileIsAnError(t *testing.T) {
	fsys := hmiProject("hmi/build", nil)
	fsys["hmi/build"] = &fstest.MapFile{Data: []byte("not a directory")}
	_, err := Load(fsys, "")
	if err == nil {
		t.Fatal("Load must reject server.hmi naming a file instead of a directory")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %q, want it to say server.hmi is not a directory", err)
	}
}

// serveOnce compiles a loaded project's runtime and hands its Server
// options straight to server.New, exactly as runProject (both `naut run`
// and a built binary's own entry point) does — the only way to prove the
// routing a real request sees, not just what Load recorded.
func serveOnce(t *testing.T, p *Project, path string) *httptest.ResponseRecorder {
	t.Helper()
	rt, err := runtime.New(p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(rt, p.Server).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

// The regression this PR fixes: a fresh clone of a project declaring
// server.hmi, before the HMI's own build has run, must show the built-in
// dashboard at "/" — not a 404. Load used to set Server.HMI to an fs.FS
// over a directory that didn't exist, so every request (including "/")
// 404ed once server.hmi was configured at all, missing build or not.
func TestServerFallsBackToDashboardWhenHMIDirMissing(t *testing.T) {
	p, err := Load(hmiProject("hmi/build", nil), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := serveOnce(t, p, "/")
	if rec.Code != 200 {
		t.Fatalf("GET / = %d, want 200 (the built-in dashboard)", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html (the built-in dashboard)", ct)
	}
	if strings.Contains(rec.Body.String(), "hmi shell") {
		t.Fatal("GET / served HMI content that was never built")
	}
}

// The other half of the same claim: once the HMI's own build exists,
// nothing about this fix changes behavior — "/" is the HMI's index.html,
// same as before.
func TestServerServesHMIIndexWhenDirPresent(t *testing.T) {
	p, err := Load(hmiProject("hmi/build", map[string]string{
		"hmi/build/index.html": "<html>hmi shell</html>",
	}), "")
	if err != nil {
		t.Fatal(err)
	}
	rec := serveOnce(t, p, "/")
	if rec.Code != 200 || rec.Body.String() != "<html>hmi shell</html>" {
		t.Fatalf("GET / = %d %q, want the built HMI's index.html", rec.Code, rec.Body.String())
	}
}
