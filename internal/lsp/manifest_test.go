package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testManifest = `name: tank
tasks:
  - program: program.st
    scan: 100ms
tags:
  - { name: TempC, role: state, init: 60.0, unit: "°C", desc: "Tank temperature" }
  - { name: PumpRun, role: output, init: false, desc: "Pump run command" }
  - { name: Status, role: state, init: "", desc: "Operator status line" }
  - { name: LevelPct, role: input }
driver:
  type: memory
`

// writeProject lays out a manifest project in a temp dir and returns the
// path of a program file inside it.
func writeProject(t *testing.T, sub string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nautilus.yaml"), []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := root
	if sub != "" {
		dir = filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prog := filepath.Join(dir, "program.st")
	if err := os.WriteFile(prog, []byte("PROGRAM Main\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return prog
}

func TestProjectTagsReadsTheManifest(t *testing.T) {
	tags := projectTags(writeProject(t, ""))
	if len(tags) != 4 {
		t.Fatalf("got %d tags, want 4: %+v", len(tags), tags)
	}
	byName := map[string]ProjectTag{}
	for _, tag := range tags {
		byName[tag.Name] = tag
	}
	for _, tc := range []struct{ name, typ, role, unit string }{
		{"TempC", "REAL", "state", "°C"},
		{"PumpRun", "BOOL", "output", ""},
		{"Status", "STRING", "state", ""},
		{"LevelPct", "", "input", ""}, // no init — type unknown, and saying so beats guessing
	} {
		got := byName[tc.name]
		if got.Type != tc.typ || got.Role != tc.role || got.Unit != tc.unit {
			t.Errorf("%s = {type:%q role:%q unit:%q}, want {%q %q %q}",
				tc.name, got.Type, got.Role, got.Unit, tc.typ, tc.role, tc.unit)
		}
	}
}

// A program in a subdirectory still belongs to the project above it.
func TestProjectTagsWalksUp(t *testing.T) {
	if got := projectTags(writeProject(t, "logic/sub")); len(got) != 4 {
		t.Fatalf("got %d tags from a nested file, want 4", len(got))
	}
}

// A .st file with no project is an ordinary thing to edit; it must not
// error, hang, or invent tags.
func TestProjectTagsWithoutAManifest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "loose.st")
	if err := os.WriteFile(p, []byte("PROGRAM P\nEND_PROGRAM\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectTags(p); got != nil {
		t.Fatalf("want no tags outside a project, got %+v", got)
	}
}

// Editing nautilus.yaml must show up without restarting the server — the
// cache is keyed on modtime for exactly this.
func TestProjectTagsCacheInvalidates(t *testing.T) {
	prog := writeProject(t, "")
	manifest := filepath.Join(filepath.Dir(prog), "nautilus.yaml")
	if got := projectTags(prog); len(got) != 4 {
		t.Fatalf("got %d tags, want 4", len(got))
	}
	updated := strings.Replace(testManifest, "driver:",
		"  - { name: Extra, role: setpoint, init: 1.0 }\ndriver:", 1)
	if err := os.WriteFile(manifest, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force a distinct modtime: a rewrite inside the same filesystem
	// timestamp tick would legitimately look unchanged.
	ahead := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(manifest, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	if got := projectTags(prog); len(got) != 5 {
		t.Fatalf("after editing the manifest got %d tags, want 5 — cache did not invalidate", len(got))
	}
}

func TestInVarExternal(t *testing.T) {
	src := `PROGRAM Main
VAR_EXTERNAL
    TempC : REAL;
END_VAR
VAR
    local : REAL;
END_VAR
TempC := 1.0;
END_PROGRAM`
	for _, tc := range []struct {
		line int
		want bool
		why  string
	}{
		{1, false, "the PROGRAM line"},
		{2, true, "the VAR_EXTERNAL line itself"},
		{3, true, "a declaration inside it"},
		{4, false, "END_VAR closes it"},
		{6, false, "a plain VAR block is not VAR_EXTERNAL"},
		{8, false, "the body"},
	} {
		if got := inVarExternal(src, tc.line); got != tc.want {
			t.Errorf("line %d (%s) = %v, want %v", tc.line, tc.why, got, tc.want)
		}
	}
}

// A manifest is invalid for most of the keystrokes it takes to add a tag.
// Completion must not blink out while you type — the last good answer is
// served until the file parses again.
func TestProjectTagsSurviveABrokenManifest(t *testing.T) {
	prog := writeProject(t, "")
	manifest := filepath.Join(filepath.Dir(prog), "nautilus.yaml")
	if got := projectTags(prog); len(got) != 4 {
		t.Fatalf("got %d tags, want 4", len(got))
	}
	if err := os.WriteFile(manifest, []byte("tags:\n  - { name: broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	ahead := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(manifest, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	if got := projectTags(prog); len(got) != 4 {
		t.Fatalf("mid-edit manifest gave %d tags, want the last good 4", len(got))
	}
	// And the failure must not be cached: fixing the file restores service
	// without a restart.
	if err := os.WriteFile(manifest, []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	ahead = ahead.Add(2 * time.Second)
	if err := os.Chtimes(manifest, ahead, ahead); err != nil {
		t.Fatal(err)
	}
	if got := projectTags(prog); len(got) != 4 {
		t.Fatalf("after repair got %d tags, want 4", len(got))
	}
}
