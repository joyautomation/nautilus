package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joyautomation/nautilus/lang/st"
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
			name: "plant with everything",
			sc:   scaffold{Name: "plant-proj", Module: "example.com/plant-proj", Program: "PlantProj", Plant: true, CI: true, VSCode: true},
			want: []string{
				"go.mod", "main.go", "plant.go", "program.st", "program_test.go",
				".github/workflows/ci.yml", ".vscode/extensions.json", ".vscode/settings.json",
				"README.md", ".gitignore",
			},
			absent: []string{"driver.go"},
		},
		{
			name: "blank minimal",
			sc:   scaffold{Name: "blank-proj", Module: "blank-proj", Program: "BlankProj", Plant: false, CI: false, VSCode: false},
			want: []string{"go.mod", "main.go", "driver.go", "program.st", "program_test.go"},
			absent: []string{
				"plant.go", ".github/workflows/ci.yml", ".vscode/extensions.json",
			},
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

			// The generated control program must compile.
			src, err := os.ReadFile(filepath.Join(dir, tc.sc.Name, "program.st"))
			if err != nil {
				t.Fatal(err)
			}
			prog, err := st.Parse(string(src))
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
