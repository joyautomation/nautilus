package project

// server.hmi: names a built HMI's directory, relative to the project — the
// same "must resolve inside the project" rule as tag-files and
// driver.manifest (see projectPath), so what `nautilus build` embeds is
// exactly what a reviewer can see in the checkout.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
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

// fs.Sub only wraps a path prefix, so Load must not require the directory
// to exist yet — a manifest declaring server.hmi before `npm run build`
// has ever run (a fresh checkout, `nautilus check`, the language server)
// must still load cleanly. A request against the missing build 404s at
// serve time instead (see server.handleHMI).
func TestHMIDirNeedNotExistAtLoad(t *testing.T) {
	p, err := Load(hmiProject("hmi/build", nil), "")
	if err != nil {
		t.Fatalf("Load failed on a not-yet-built HMI dir: %v", err)
	}
	if p.Server.HMI == nil {
		t.Fatal("Server.HMI must still be set even though the directory is empty")
	}
	if _, err := p.Server.HMI.Open("index.html"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected a not-exist error opening an unbuilt HMI dir, got %v", err)
	}
}
