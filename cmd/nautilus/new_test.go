package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/acceptance"
	"github.com/joyautomation/nautilus/internal/project"
	"github.com/joyautomation/nautilus/internal/stproject"
	"github.com/joyautomation/nautilus/lang/fbd"
	"github.com/joyautomation/nautilus/lang/ld"
	"github.com/joyautomation/nautilus/lang/st"
	"github.com/joyautomation/nautilus/runtime"
)

// Scaffold both variants into a temp dir and sanity-check the tree: the
// right files for the chosen features, rendered templates (no stray
// {{...}}), and control programs that actually compile.
func TestScaffoldVariants(t *testing.T) {
	cases := []struct {
		name   string
		sc     scaffold
		want   []string
		absent []string
	}{
		{
			name: "sdk-demo with everything",
			sc:   scaffold{Name: "plant-proj", Module: "example.com/plant-proj", Program: "PlantProj", Template: tmplSDKDemo, CI: true, VSCode: true},
			want: []string{
				"go.mod", "main.go", "plant.go", "program.st", "blocks.st", "program_test.go",
				".github/workflows/ci.yml", ".vscode/extensions.json", ".vscode/settings.json",
				"README.md", ".gitignore",
			},
			absent: []string{"driver.go", "nautilus.yaml"},
		},
		{
			name: "sdk, blank program",
			sc:   scaffold{Name: "blank-proj", Module: "blank-proj", Program: "BlankProj", Template: tmplSDK},
			want: []string{"go.mod", "main.go", "driver.go", "program.st", "program_test.go"},
			absent: []string{
				"plant.go", "blocks.st", "nautilus.yaml", ".github/workflows/ci.yml", ".vscode/extensions.json",
			},
		},
		{
			name:   "sdk, ladder program",
			sc:     scaffold{Name: "ld-proj", Module: "ld-proj", Program: "LdProj", Template: tmplSDK, Language: "ld"},
			want:   []string{"go.mod", "main.go", "driver.go", "program.ld", "program_test.go"},
			absent: []string{"program.st", "program.fbd", "plant.go", "blocks.st"},
		},
		{
			name:   "minimal manifest project",
			sc:     scaffold{Name: "min-proj", Program: "MinProj", Template: tmplMinimal, Language: "fbd"},
			want:   []string{"nautilus.yaml", "program.fbd", "min-proj_test.yaml", "README.md", ".gitignore"},
			absent: []string{"go.mod", "main.go", "driver.go", "plant.go", "program_test.go", "sim.st"},
		},
		{
			// The manifest tour: three tasks in three languages, plus the
			// library FB and the plant simulated in IEC.
			name: "demo manifest project",
			sc:   scaffold{Name: "demo-proj", Program: "DemoProj", Template: tmplDemo, CI: true},
			want: []string{
				"nautilus.yaml", "program.fbd", "sim.st", "interlocks.ld", "blocks.st",
				"demo-proj_test.yaml", "README.md", ".github/workflows/ci.yml",
			},
			absent: []string{"go.mod", "main.go", "plant.go", "program.st", "program_test.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cwd, _ := os.Getwd()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(cwd)

			if err := write(&tc.sc); err != nil {
				t.Fatal(err)
			}
			for _, f := range tc.want {
				p := filepath.Join(dir, tc.sc.Name, f)
				raw, err := os.ReadFile(p)
				if err != nil {
					t.Fatalf("missing %s: %v", f, err)
				}
				if strings.Contains(string(raw), "{{") {
					t.Errorf("%s contains unrendered template syntax", f)
				}
			}
			for _, f := range tc.absent {
				if _, err := os.Stat(filepath.Join(dir, tc.sc.Name, f)); err == nil {
					t.Errorf("%s should not exist in this variant", f)
				}
			}

			// The generated control program must compile — composed with
			// its library file, exactly as main.go composes it. Graphical
			// languages take their transpile hops first (ld → fbd → st).
			lang := tc.sc.Language
			if lang == "" {
				lang = "st"
			}
			src, err := os.ReadFile(filepath.Join(dir, tc.sc.Name, "program."+lang))
			if err != nil {
				t.Fatal(err)
			}
			composed := string(src)
			if lang == "ld" {
				f, err := ld.Transpile(composed)
				if err != nil {
					t.Fatalf("generated program.ld doesn't transpile: %v", err)
				}
				composed = f
				lang = "fbd"
			}
			if lang == "fbd" {
				s, err := fbd.Transpile(composed)
				if err != nil {
					t.Fatalf("generated program doesn't transpile from fbd: %v", err)
				}
				composed = s
			}
			if blocks, err := os.ReadFile(filepath.Join(dir, tc.sc.Name, "blocks.st")); err == nil {
				composed = stproject.Join([]string{string(blocks)}, composed)
			}
			prog, err := st.Parse(composed)
			if err != nil {
				t.Fatalf("generated program.st doesn't parse: %v", err)
			}
			if prog.Name != tc.sc.Program {
				t.Errorf("PROGRAM name = %q, want %q", prog.Name, tc.sc.Program)
			}
			if _, err := st.Lower(prog); err != nil {
				t.Fatalf("generated program.st doesn't lower: %v", err)
			}
		})
	}
}

func TestPascalCase(t *testing.T) {
	for in, want := range map[string]string{
		"water-plant": "WaterPlant",
		"tank":        "Tank",
		"my_plc_2":    "MyPlc2",
		"3rd-line":    "P3rdLine",
	} {
		if got := pascalCase(in); got != want {
			t.Errorf("pascalCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// The scaffolded manifest projects must load, compile every task, and pass
// their own acceptance tests — the promise `nautilus new` makes. This is
// the check that catches a broken template the moment it lands, rather
// than in someone's first five minutes with the tool.
func TestScaffoldedManifestProjectsPass(t *testing.T) {
	for _, tc := range []struct{ name, template, language string }{
		{"minimal-st", tmplMinimal, "st"},
		{"minimal-fbd", tmplMinimal, "fbd"},
		{"minimal-ld", tmplMinimal, "ld"},
		{"demo", tmplDemo, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cwd, _ := os.Getwd()
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			defer os.Chdir(cwd)

			sc := scaffold{Name: tc.name, Program: pascalCase(tc.name), Template: tc.template, Language: tc.language}
			if err := write(&sc); err != nil {
				t.Fatal(err)
			}
			fsys := os.DirFS(filepath.Join(dir, tc.name))
			proj, err := project.Load(fsys, "")
			if err != nil {
				t.Fatalf("scaffolded project does not load: %v", err)
			}
			if _, err := runtime.New(proj.Runtime); err != nil {
				t.Fatalf("scaffolded project does not compile: %v", err)
			}
			results, err := acceptance.RunDir(fsys, proj.Runtime)
			if err != nil {
				t.Fatalf("scaffolded tests do not run: %v", err)
			}
			if len(results) == 0 {
				t.Fatal("scaffolded project has no acceptance tests")
			}
			for _, r := range results {
				if !r.Passed {
					t.Errorf("scaffolded test %q fails:\n%s", r.Name, acceptance.FormatFailure(r))
				}
			}
		})
	}
}
