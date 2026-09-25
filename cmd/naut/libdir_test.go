package main

// A project whose shared code lives in lib/ (internal/project/testdata/
// libdir: lib/blocks.st, lib/rungs.ld, lib/physics/tank.st, consumed by the
// root main.st) through every CLI path that composes libraries: check, test,
// and a built binary's embedded archive.

import (
	"archive/zip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
)

const libDirFixture = "../../internal/project/testdata/libdir"

// libDirFiles reads the fixture into a name → body map (checkIn's shape).
func libDirFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(libDirFixture, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(libDirFixture, p)
		files[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestCheckLibDirClean(t *testing.T) {
	out, code := checkIn(t, libDirFiles(t))
	if code != 0 {
		t.Fatalf("naut check = %d, want clean:\n%s", code, out)
	}
}

func TestCheckLibDirRefusesAProgram(t *testing.T) {
	files := libDirFiles(t)
	files["lib/extra.st"] = "PROGRAM Extra\nVAR x : BOOL; END_VAR\nx := TRUE;\nEND_PROGRAM\n"
	out, code := checkIn(t, files)
	if code == 0 {
		t.Fatalf("a PROGRAM under lib/ must fail check:\n%s", out)
	}
	if !strings.Contains(out, "extra.st: declares a PROGRAM, but lib/ holds libraries only — programs belong in the root and in `tasks:`") {
		t.Errorf("check output should name the file and the rule:\n%s", out)
	}
}

func TestTestLibDir(t *testing.T) {
	if code := runTest([]string{libDirFixture}); code != 0 {
		t.Fatalf("naut test = %d, want green", code)
	}
}

// `naut build` ships lib/, and the embedded archive loads and passes the
// fixture's acceptance suite exactly as the checkout does.
func TestBuildShipsLibDir(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "runner")
	if err := os.WriteFile(runner, []byte("FAKE-RUNNER"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if _, err := emitBinary(runner, libDirFixture, out, "", nil); err != nil {
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
	for _, want := range []string{"lib/blocks.st", "lib/rungs.ld", "lib/physics/tank.st"} {
		if _, err := fs.Stat(zr, want); err != nil {
			t.Errorf("archive lacks %s: %v", want, err)
		}
	}

	p, err := project.Load(zr, "")
	if err != nil {
		t.Fatalf("project.Load over the embedded archive: %v", err)
	}
	// The suites never ride in the archive; run the checkout's against
	// the archive's project.
	suite, err := acceptance.LoadSuite(os.DirFS(libDirFixture), "libdir_test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	results, err := acceptance.RunSuite(suite, p.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("%s: %+v", r.Name, r.Failure)
		}
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
}
