package project

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/joyautomation/nautilus/runtime"
)

// testdata/libdir keeps its shared code in lib/: lib/blocks.st,
// lib/rungs.ld (a ladder FUNCTION_BLOCK) and the nested
// lib/physics/tank.st, all consumed by the root program main.st. hmi/
// holds a .st that redeclares a lib/ block — it must stay ignored.

func TestLibDirComposes(t *testing.T) {
	fsys := os.DirFS("testdata/libdir")
	libs, err := libraries(fsys)
	if err != nil {
		t.Fatalf("libraries: %v", err)
	}
	// Tier order, path order within a tier: lib/blocks.st,
	// lib/physics/tank.st, then lib/rungs.ld transpiled.
	want := []string{"FUNCTION_BLOCK HiAlarm", "FUNCTION_BLOCK TankModel", "FUNCTION_BLOCK PumpSeq"}
	if len(libs) != len(want) {
		t.Fatalf("want %d libraries, got %d: %q", len(want), len(libs), libs)
	}
	for i, w := range want {
		if !strings.Contains(libs[i], w) {
			t.Errorf("library %d should hold %s:\n%s", i, w, libs[i])
		}
	}

	p, err := Load(fsys, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := runtime.New(p.Runtime); err != nil {
		t.Fatalf("runtime.New: %v", err)
	}

	src, err := Sources(fsys, "")
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if main := src[runtime.MainTaskName]; !strings.Contains(main, "FUNCTION_BLOCK TankModel") ||
		!strings.Contains(main, "FUNCTION_BLOCK PumpSeq") {
		t.Errorf("Sources must compose lib/ ahead of the program:\n%s", main)
	}
}

// libDirFS is the fixture as a MapFS, for variations on it.
func libDirFS(t *testing.T) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	for _, p := range []string{"nautilus.yaml", "main.st", "lib/blocks.st", "lib/rungs.ld", "lib/physics/tank.st"} {
		raw, err := os.ReadFile("testdata/libdir/" + p)
		if err != nil {
			t.Fatal(err)
		}
		m[p] = &fstest.MapFile{Data: raw}
	}
	return m
}

func TestLibDirRefusesAProgram(t *testing.T) {
	fsys := libDirFS(t)
	fsys["lib/deep/extra.st"] = &fstest.MapFile{Data: []byte("PROGRAM Extra\nVAR x : BOOL; END_VAR\nx := TRUE;\nEND_PROGRAM\n")}
	_, err := Load(fsys, "")
	if err == nil {
		t.Fatal("a PROGRAM under lib/ must fail Load")
	}
	for _, w := range []string{"lib/deep/extra.st", "programs belong in the root and in `tasks:`"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q should mention %q", err, w)
		}
	}
	if _, err := Sources(fsys, ""); err == nil {
		t.Error("Sources must refuse the same project")
	}
}

// A lib/ ladder library that will not transpile names itself by its
// project-relative path, not a bare base name.
func TestLibDirErrorsUseProjectPaths(t *testing.T) {
	fsys := libDirFS(t)
	fsys["lib/rungs.ld"] = &fstest.MapFile{Data: []byte("FUNCTION_BLOCK Broken\nLD\n  RUNG r ( \nEND_LD\nEND_FUNCTION_BLOCK\n")}
	_, err := Load(fsys, "")
	if err == nil || !strings.Contains(err.Error(), "lib/rungs.ld") {
		t.Fatalf("want an error naming lib/rungs.ld, got %v", err)
	}
}

// A block declared in both the root and lib/ is the same error two root
// files declaring it would be.
func TestLibDirDuplicateAcrossRootAndLib(t *testing.T) {
	fsys := libDirFS(t)
	dup, _ := os.ReadFile("testdata/libdir/lib/blocks.st")
	fsys["blocks.st"] = &fstest.MapFile{Data: dup}
	p, err := Load(fsys, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err1 := runtime.New(p.Runtime)
	if err1 == nil || !strings.Contains(err1.Error(), "HiAlarm") {
		t.Fatalf("want a duplicate-HiAlarm error, got %v", err1)
	}

	// ...and matches what two root files produce.
	root := libDirFS(t)
	root["a.st"] = &fstest.MapFile{Data: dup}
	root["b.st"] = &fstest.MapFile{Data: dup}
	p2, err := Load(root, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_, err2 := runtime.New(p2.Runtime)
	if err2 == nil || err2.Error() != err1.Error() {
		t.Fatalf("two root files declaring HiAlarm: got %v, want the same error as root+lib/ (%v)", err2, err1)
	}
}

// Subdirectories other than lib/ stay out of the composition, whatever
// they hold — including a PROGRAM.
func TestOtherSubdirsStayIgnored(t *testing.T) {
	fsys := libDirFS(t)
	fsys["hmi/x.st"] = &fstest.MapFile{Data: []byte("PROGRAM X\nEND_PROGRAM\n")}
	fsys["tags/y.st"] = &fstest.MapFile{Data: []byte("FUNCTION_BLOCK HiAlarm\nEND_FUNCTION_BLOCK\n")}
	p, err := Load(fsys, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := runtime.New(p.Runtime); err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
}
