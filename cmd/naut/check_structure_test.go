package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/lang/lint"
)

var structureWarnRe = regexp.MustCompile(`(?m)^(\S+):(\d+):(\d+): warning: .+ \[((?:LD|FBD|SFC)\d{3})\]$`)

// naut check reports the diagram structure rules (#561) as gcc-style
// warnings with the rule ID in brackets. The naive tank from the AGENTS.md
// investigation (#556) trips every rule; its rewrite trips none; neither
// fails the check (they are warnings).
func TestCheckReportsStructureWarnings(t *testing.T) {
	out, code := capture(t, func() int { return runCheck([]string{"testdata/structure/naive"}) })
	if code != 0 {
		t.Fatalf("structure warnings failed the check (exit %d):\n%s", code, out)
	}
	seen := map[string]bool{}
	for _, m := range structureWarnRe.FindAllStringSubmatch(out, -1) {
		seen[m[4]] = true
	}
	for _, r := range lint.Rules {
		if !seen[r.ID] {
			t.Errorf("the naive tank does not trip %s (%s):\n%s", r.ID, r.Summary, out)
		}
	}

	out, code = capture(t, func() int { return runCheck([]string{"testdata/structure/good"}) })
	if code != 0 || structureWarnRe.MatchString(out) {
		t.Errorf("the rewrite has structure warnings (exit %d):\n%s", code, out)
	}
}

// The regression guard: every ladder, FBD and SFC file under examples/, in
// every scaffold naut new writes, and in the AGENTS.md sample project (when
// it is in the tree) passes with no structure warning, under its own
// project's check: settings. A new example, or an edit to one, that draws
// badly fails here — fix the file (or, where a rule truly does not fit, say
// why in an (* @allow ID: reason *) pragma).
func TestExamplesAndScaffoldsHaveNoStructureWarnings(t *testing.T) {
	roots := []string{"../../examples", "testdata/structure/good", "testdata/agents-md"}
	for _, tc := range []struct{ name, template, language string }{
		{"minimal-st", tmplMinimal, "st"},
		{"minimal-fbd", tmplMinimal, "fbd"},
		{"minimal-ld", tmplMinimal, "ld"},
		{"minimal-sfc", tmplMinimal, "sfc"},
		{"tour", tmplDemo, ""},
	} {
		roots = append(roots, scaffoldInto(t, scaffold{Name: tc.name, Program: pascalCase(tc.name), Template: tc.template, Language: tc.language}))
	}
	linted := 0
	for _, root := range roots {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue // testdata/agents-md arrives with #556
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".ld", ".fbd", ".sfc":
			default:
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			linted++
			for _, f := range project.Structure(path, string(src), project.StructureFor(path, "")) {
				t.Errorf("%s:%d:%d: %s [%s]", path, f.Line, f.Col, f.Message, f.Rule)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if linted < 15 {
		t.Errorf("linted only %d diagram files: the walk lost the examples", linted)
	}
}

// The manifest's check: section moves the thresholds and switches rules
// off; an unknown rule ID there is a manifest error, not silence.
func TestCheckStructureManifestSettings(t *testing.T) {
	const rung = "PROGRAM Main\nLD\n  RUNG long\n    a b c d e f g ( x )\nEND_LD\nEND_PROGRAM\n"
	tags := "tags:\n" +
		"  - { name: a, role: input, type: BOOL, init: false }\n  - { name: b, role: input, type: BOOL, init: false }\n" +
		"  - { name: c, role: input, type: BOOL, init: false }\n  - { name: d, role: input, type: BOOL, init: false }\n" +
		"  - { name: e, role: input, type: BOOL, init: false }\n  - { name: f, role: input, type: BOOL, init: false }\n" +
		"  - { name: g, role: input, type: BOOL, init: false }\n  - { name: x, role: output, type: BOOL, init: false }\n"
	run := func(check string) (string, int) {
		dir := t.TempDir()
		writeProjectFiles(t, dir, map[string]string{
			"nautilus.yaml": "name: s\ntasks:\n  - program: main.ld\n" + tags + check,
			"main.ld":       rung,
		})
		return capture(t, func() int { return runCheck([]string{dir}) })
	}
	out, _ := run("")
	if !strings.Contains(out, "[LD003]") || !strings.Contains(out, "[LD005]") {
		t.Fatalf("defaults: want LD003 and LD005:\n%s", out)
	}
	out, code := run("check:\n  max-series: 7\n  disable: [LD005]\n")
	if code != 0 || structureWarnRe.MatchString(out) {
		t.Errorf("max-series 7, LD005 off: want no structure warning (exit %d):\n%s", code, out)
	}
	out, code = run("check:\n  disable: [LD050]\n")
	if code == 0 || !strings.Contains(out, `check.disable: "LD050" is not a structure rule`) {
		t.Errorf("an unknown rule ID: want a manifest error (exit %d):\n%s", code, out)
	}
}
