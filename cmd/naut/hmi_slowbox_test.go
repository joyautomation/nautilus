package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
)

// A small ARM board building the screens at `naut run` takes minutes of every core
// before it serves anything (measured on a two-core Cortex-A53 board): naut run says
// so, and where the answer is (build off the board), before it starts; a desktop
// is told nothing.
func TestNautRunSaysAScreenBuildIsSlowOnASmallARMBoard(t *testing.T) {
	for _, arch := range []string{"arm", "arm64"} {
		n := slowBoxNotice(arch)
		for _, want := range []string{"minutes", "naut build", "naut deploy"} {
			if !strings.Contains(n, want) {
				t.Errorf("%s: the notice lacks %q: %q", arch, want, n)
			}
		}
	}
	for _, arch := range []string{"amd64", "386", "riscv64"} {
		if n := slowBoxNotice(arch); n != "" {
			t.Errorf("%s was told %q", arch, n)
		}
	}
}

func TestNautRunPrintsTheNoticeOnlyWhenItWillBuildScreens(t *testing.T) {
	dir := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("nautilus.yaml", "name: grain\ntasks:\n  - program: main.st\n    scan: 100ms\n")
	put("main.st", "PROGRAM main\nVAR x : INT; END_VAR\nx := 1;\nEND_PROGRAM\n")
	// no screens: nothing to build, so nothing to warn about, even on an ARM board
	old := hmiArch
	hmiArch = "arm64"
	t.Cleanup(func() { hmiArch = old })
	proj, err := project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buildHMIForRun(proj, dir, &out)
	if strings.Contains(out.String(), "building the screens") {
		t.Errorf("a project with no screens was told: %s", out.String())
	}
	// a screen: the notice comes first, then the build
	put("screens/main.mimic.json", `{"name":"Main","equipment":[{"id":"T1","component":"Tank","x":10,"y":10}]}`)
	proj, err = project.Load(os.DirFS(dir), "")
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	buildHMIForRun(proj, dir, &out)
	if !strings.HasPrefix(out.String(), "naut run: building the screens first") {
		t.Errorf("an ARM board with a screen was not told first: %q", out.String())
	}
	hmiArch = "amd64"
	out.Reset()
	buildHMIForRun(proj, dir, &out)
	if strings.Contains(out.String(), "building the screens") {
		t.Errorf("a desktop was told: %s", out.String())
	}
}
