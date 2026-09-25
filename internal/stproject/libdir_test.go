package stproject

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestLibraryPathsRootAndLib(t *testing.T) {
	fsys := fstest.MapFS{
		"nautilus.yaml":         {Data: []byte("name: x\n")},
		"main.st":               {Data: []byte("PROGRAM P\nEND_PROGRAM\n")},
		"types.st":              {Data: []byte("")},
		"lib/z.st":              {Data: []byte("")},
		"lib/a/b.st":            {Data: []byte("")},
		"lib/motor.ld":          {Data: []byte("")},
		"lib/net.fbd":           {Data: []byte("")},
		"lib/chart.sfc":         {Data: []byte("")}, // never a library
		"lib/.hidden/h.st":      {Data: []byte("")},
		"lib/node_modules/n.st": {Data: []byte("")},
		"hmi/ignored.st":        {Data: []byte("")},
		"tags/ignored.ld":       {Data: []byte("")},
	}
	st, g, err := LibraryPaths(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib/a/b.st", "lib/z.st", "main.st", "types.st"}; !reflect.DeepEqual(st, want) {
		t.Errorf("st tier = %q, want %q", st, want)
	}
	if want := []string{"lib/motor.ld", "lib/net.fbd"}; !reflect.DeepEqual(g, want) {
		t.Errorf("graphical tier = %q, want %q", g, want)
	}
}

// A project with no lib/ lists exactly what the root-only rule did.
func TestLibraryPathsWithoutLib(t *testing.T) {
	fsys := fstest.MapFS{
		"b.st":     {Data: []byte("")},
		"a.st":     {Data: []byte("")},
		"x.ld":     {Data: []byte("")},
		"sub/c.st": {Data: []byte("")},
	}
	st, g, err := LibraryPaths(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, []string{"a.st", "b.st"}) || !reflect.DeepEqual(g, []string{"x.ld"}) {
		t.Errorf("got %q / %q", st, g)
	}
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	rootTypes = "TYPE Mode :\nSTRUCT\n    Auto : BOOL;\nEND_STRUCT;\nEND_TYPE\n"
	libBlock  = "FUNCTION_BLOCK Pump\nVAR_INPUT m : Mode; END_VAR\nEND_FUNCTION_BLOCK\n"
	deepBlock = "FUNCTION_BLOCK Tank\nVAR p : Pump; END_VAR\nEND_FUNCTION_BLOCK\n"
	rootProg  = "PROGRAM Main\nVAR t : Tank; END_VAR\nEND_PROGRAM\n"
)

func TestProjectRootAndPrelude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"nautilus.yaml":    "name: x\n",
		"types.st":         rootTypes,
		"main.st":          rootProg,
		"lib/pump.st":      libBlock,
		"lib/deep/tank.st": deepBlock,
		"hmi/lib/nope.st":  "FUNCTION_BLOCK Nope\nEND_FUNCTION_BLOCK\n",
	})
	for file, want := range map[string]string{
		"main.st":          dir,
		"lib/pump.st":      dir,
		"lib/deep/tank.st": dir,
		// hmi/lib is inside the project, but it is not the project's lib/.
		"hmi/lib/nope.st": filepath.Join(dir, "hmi", "lib"),
	} {
		if got := ProjectRoot(filepath.Join(dir, file)); got != want {
			t.Errorf("ProjectRoot(%s) = %s, want %s", file, got, want)
		}
	}

	// A root program sees lib/ at any depth...
	pre, _ := Prelude(filepath.Join(dir, "main.st"), nil)
	for _, w := range []string{"TYPE Mode", "FUNCTION_BLOCK Pump", "FUNCTION_BLOCK Tank"} {
		if !strings.Contains(pre, w) {
			t.Errorf("main.st prelude lacks %s:\n%s", w, pre)
		}
	}
	if strings.Contains(pre, "PROGRAM") || strings.Contains(pre, "Nope") {
		t.Errorf("main.st prelude holds a program or an hmi/ file:\n%s", pre)
	}
	// ...and a lib/ file sees the root and the rest of lib/, not itself.
	pre, _ = Prelude(filepath.Join(dir, "lib", "deep", "tank.st"), nil)
	if !strings.Contains(pre, "TYPE Mode") || !strings.Contains(pre, "FUNCTION_BLOCK Pump") || strings.Contains(pre, "FUNCTION_BLOCK Tank") {
		t.Errorf("lib/deep/tank.st prelude:\n%s", pre)
	}
	// An unsaved buffer of a lib/ file wins over disk.
	over := map[string]string{filepath.Join(dir, "lib", "pump.st"): "FUNCTION_BLOCK PumpV2\nEND_FUNCTION_BLOCK\n"}
	pre, _ = Prelude(filepath.Join(dir, "main.st"), over)
	if !strings.Contains(pre, "PumpV2") {
		t.Errorf("override not applied:\n%s", pre)
	}

	m, err := ComposeAll(dir, map[string]string{"lib/deep/tank.st": deepBlock + "(* edited *)\n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Programs) != 1 || m.Programs[0].File != "main.st" {
		t.Errorf("programs = %+v", m.Programs)
	}
	if len(m.Libraries) != 3 || !strings.Contains(m.Prelude, "(* edited *)") {
		t.Errorf("libraries = %q", m.Libraries)
	}
}

// Without a manifest, a file under a lib/ directory compiles against its
// own directory, as before: lib/ has meaning only inside a project.
func TestProjectRootWithoutManifest(t *testing.T) {
	dir := writeTree(t, map[string]string{"lib/pump.st": libBlock})
	if got, want := ProjectRoot(filepath.Join(dir, "lib", "pump.st")), filepath.Join(dir, "lib"); got != want {
		t.Errorf("ProjectRoot = %s, want %s", got, want)
	}
}

// A block comment that wraps onto a line starting with PROGRAM (the lift
// station's motor.ld header did) is not a PROGRAM: the ladder library must
// stay a library, and the POU name must not become "is".
func TestProgramInACommentIsNotAProgram(t *testing.T) {
	ldLib := "(* Motor starter library, written as rungs; a .ld file with no\n" +
		"   PROGRAM is a project library. *)\n" +
		"FUNCTION_BLOCK M\nVAR_INPUT a : BOOL; END_VAR\nVAR_OUTPUT q : BOOL; END_VAR\n" +
		"LD\n  RUNG r a ( q )\nEND_LD\nEND_FUNCTION_BLOCK\n"
	if DeclaresProgram(ldLib) || hasProgram(ldLib) {
		t.Error("a PROGRAM inside a comment was taken for a declaration")
	}
	if got := POU(ldLib); got != "" {
		t.Errorf("POU = %q, want none", got)
	}
	if got := POU(ldLib + "PROGRAM Main\nVAR x : M; END_VAR\nEND_PROGRAM\n"); got != "Main" {
		t.Errorf("POU = %q, want Main", got)
	}
}
