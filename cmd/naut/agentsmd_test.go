package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/runtime"
)

// fencedBlocks returns the body of every ``` fenced block in a Markdown
// document, in order, without the fence lines.
func fencedBlocks(md string) []string {
	var out []string
	var cur []string
	in := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "```") {
			if in {
				out = append(out, strings.Join(cur, "\n"))
				cur = nil
			}
			in = !in
			continue
		}
		if in {
			cur = append(cur, line)
		}
	}
	return out
}

// The AGENTS.md that `naut new` writes is read by an AI on every turn, so
// an example in it that does not compile, or does not do what its comment
// says, is copied into every project that follows it. Every program, tag
// line and test it shows must therefore be the files of
// testdata/agents-md, verbatim, and that project must compile and pass its
// acceptance tests (agents_test.yaml is the one AGENTS.md shows;
// more_test.yaml covers the rest of what the examples claim).
func TestAgentsMdSnippetsRun(t *testing.T) {
	raw, err := templates.ReadFile("templates/AGENTS.md.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	md := string(raw)
	// Rendered through text/template like every other file: it must not
	// carry template actions, so what is in the repo is what users get.
	if strings.Contains(md, "{{") {
		t.Fatal("AGENTS.md.tmpl contains template syntax; it is meant to be copied verbatim")
	}

	dir := filepath.Join("testdata", "agents-md")
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	manifest := read("nautilus.yaml")
	programs := map[string]string{}
	for _, f := range []string{"interlocks.ld", "control.fbd", "sequence.sfc"} {
		programs[f] = read(f)
	}

	seen := map[string]bool{}
	for _, b := range fencedBlocks(md) {
		body := strings.TrimSpace(b)
		switch {
		case strings.HasPrefix(body, "PROGRAM "):
			found := ""
			for f, src := range programs {
				if src == body {
					found = f
				}
			}
			if found == "" {
				t.Errorf("AGENTS.md shows a program that is not a file of testdata/agents-md verbatim:\n%s", firstLine(body))
			}
			seen[found] = true
		case strings.HasPrefix(body, "tags:"):
			for _, line := range strings.Split(body, "\n")[1:] {
				if l := strings.TrimSpace(line); l != "" && !strings.Contains(manifest, l) {
					t.Errorf("AGENTS.md tag line is not in testdata/agents-md/nautilus.yaml: %s", l)
				}
			}
			seen["tags"] = true
		case strings.HasPrefix(body, "tests:"):
			if body != read("agents_test.yaml") {
				t.Error("AGENTS.md's test example differs from testdata/agents-md/agents_test.yaml")
			}
			seen["tests"] = true
		}
	}
	for _, want := range []string{"interlocks.ld", "control.fbd", "sequence.sfc", "tags", "tests"} {
		if !seen[want] {
			t.Errorf("AGENTS.md no longer shows %s; update this test with it", want)
		}
	}

	// AGENTS.md tells the agent to treat every naut check warning as a bug,
	// so its own examples must check with none, not merely compile.
	out, code := capture(t, func() int { return runCheck([]string{dir}) })
	if code != 0 || strings.Contains(out, "warning") {
		t.Errorf("naut check on testdata/agents-md (exit %d) must report no errors and no warnings:\n%s", code, out)
	}

	fsys := os.DirFS(dir)
	proj, err := project.Load(fsys, "")
	if err != nil {
		t.Fatalf("testdata/agents-md does not load: %v", err)
	}
	if _, err := runtime.New(proj.Runtime); err != nil {
		t.Fatalf("testdata/agents-md does not compile: %v", err)
	}
	results, err := acceptance.RunDir(fsys, proj.Runtime)
	if err != nil {
		t.Fatalf("testdata/agents-md tests do not run: %v", err)
	}
	if len(results) < 9 {
		t.Fatalf("testdata/agents-md ran %d tests; want the 9 of both test files", len(results))
	}
	for _, r := range results {
		if !r.Passed {
			t.Errorf("AGENTS.md example test %q fails:\n%s", r.Name, acceptance.FormatFailure(r))
		}
	}
}
