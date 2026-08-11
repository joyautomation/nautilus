// Package pumpstation holds no code — only this test, which is the thing
// that keeps the example honest.
//
// The example's whole claim is that a tag generator can be written in any
// language, because nautilus never runs it and only ever reads the committed
// tags/pumps.yaml. Five generators sit in tools/ to demonstrate that. A claim
// like "these all emit the identical file" rots the moment somebody edits one
// of them, so it is asserted rather than written down.
//
// Go and the CSV importer are required, since both are in this repo. Node,
// Deno, and Python are skipped when absent — CI should not fail because a
// runtime the project does not depend on is missing, which is precisely the
// independence being demonstrated.
package pumpstation

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tagLines strips the header comments — each generator names itself there, so
// the headers legitimately differ. Everything below them must not.
func tagLines(t *testing.T, raw []byte) string {
	t.Helper()
	var keep []string
	for line := range strings.SplitSeq(string(raw), "\n") {
		if !strings.HasPrefix(line, "#") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// run executes one generator in a copy of the example, and returns the
// tags/pumps.yaml it produced. Working on a copy means a failing test never
// leaves the committed file rewritten by whichever generator ran last.
func run(t *testing.T, name string, argv ...string) string {
	t.Helper()
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Skipf("%s not installed — skipping the %s generator", argv[0], name)
	}

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(".")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tags", "pumps.yaml"))
	if err != nil {
		t.Fatalf("%s wrote no tags/pumps.yaml: %v", name, err)
	}
	return tagLines(t, raw)
}

func TestEveryGeneratorEmitsTheCommittedFile(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("tags", "pumps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := tagLines(t, committed)

	// The Go generator is the primary one, so its output IS the committed
	// file. If this fails, somebody edited tags/pumps.yaml by hand — the one
	// thing its own header tells them not to do.
	if got := run(t, "go", "go", "run", "tools/tags.go"); got != want {
		t.Errorf("tags/pumps.yaml is not what tools/tags.go produces — re-run it\n%s", diff(want, got))
	}

	for _, g := range []struct {
		name string
		argv []string
	}{
		{"node", []string{"node", "tools/tags.ts"}},
		{"deno", []string{"deno", "run", "--allow-write", "tools/tags.deno.ts"}},
		{"python", []string{"python3", "tools/tags.py"}},
	} {
		t.Run(g.name, func(t *testing.T) {
			if got := run(t, g.name, g.argv...); got != want {
				t.Errorf("the %s generator disagrees with tools/tags.go\n%s", g.name, diff(want, got))
			}
		})
	}
}

// The CSV importer is a shipped tool rather than a script in tools/, so it is
// built from source here instead of being looked up on PATH.
func TestCSVImportEmitsTheCommittedFile(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("tags", "pumps.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := tagLines(t, committed)

	dir := t.TempDir()
	bin := filepath.Join(dir, "nautilus")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/nautilus")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building nautilus: %v\n%s", err, out)
	}

	out := filepath.Join(dir, "pumps.yaml")
	cmd := exec.Command(bin, "tags", "import-csv",
		"--name", "Tag", "--role", "Kind", "--type", "UDT",
		"--init", "Initial", "--unit", "EU", "--desc", "Service",
		"-o", out, "tools/pumps.csv")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("import-csv: %v\n%s", err, msg)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := tagLines(t, raw); got != want {
		t.Errorf("tools/pumps.csv no longer describes the same tags\n%s", diff(want, got))
	}
}

func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			b.WriteString("  want: " + wl + "\n   got: " + gl + "\n")
		}
	}
	return b.String()
}
