package main

import (
	"embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/charmbracelet/huh"
)

//go:embed templates
var templates embed.FS

// scaffold is the answer set the prompts (or flags) fill in; every template
// renders against it.
type scaffold struct {
	Name    string // directory + program name, kebab-ish
	Module  string // Go module path
	Program string // PROGRAM identifier derived from Name (PascalCase)

	Plant  bool // simulated plant driver + richer example program
	CI     bool // GitHub Actions workflow
	VSCode bool // .vscode extension recommendation + runtime URL
	Git    bool // git init

	// Replace, if set, adds a filesystem `replace` directive pointing the
	// nautilus dependency at a local checkout. For contributors testing
	// `nautilus new` before the module is published/tagged: the generated
	// project builds against the working copy without a network/VCS fetch.
	Replace string
}

var identRE = regexp.MustCompile(`[^A-Za-z0-9]+`)

// runNew scaffolds a nautilus controller project, sv-create style: prompt
// for whatever wasn't given on the command line, then write the tree.
// --no-input skips the prompts (CI/tests) and takes the defaults.
func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	module := fs.String("module", "", "Go module path (default: name)")
	noInput := fs.Bool("no-input", false, "accept defaults instead of prompting")
	replace := fs.String("replace", "", "path to a local nautilus checkout; adds a filesystem replace directive so the project builds against it (for contributors, pre-publish)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	name := ""
	if fs.NArg() > 0 {
		// Go's flag package stops at the first positional argument, so
		// `nautilus new water-plant --no-input` leaves the flags unparsed.
		// Take the name, then parse the remainder as flags.
		name = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return 2
		}
	}

	sc := scaffold{
		Name:   name,
		Module: *module,
		Plant:  true, CI: true, VSCode: true, Git: true,
	}
	if *replace != "" {
		// Absolute, so the directive resolves from the generated project dir
		// regardless of where the user later runs go from.
		abs, err := filepath.Abs(*replace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "nautilus new: --replace:", err)
			return 2
		}
		sc.Replace = abs
	}

	if *noInput {
		if sc.Name == "" {
			fmt.Fprintln(os.Stderr, "nautilus new: a name is required with --no-input")
			return 2
		}
	} else {
		if err := prompt(&sc); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus new:", err)
			return 1
		}
	}
	if sc.Module == "" {
		sc.Module = sc.Name
	}
	sc.Program = pascalCase(sc.Name)

	if err := write(&sc); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus new:", err)
		return 1
	}

	dep := "pulls github.com/joyautomation/nautilus"
	if sc.Replace != "" {
		dep = "resolves against " + sc.Replace
	}
	fmt.Printf(`
  created %s/

  next steps:
    cd %s
    go mod tidy      # %s
    go run .         # scan loop + tag API on http://localhost:8080
    go test ./...    # the program's acceptance test

  open the folder in VS Code with the "nautilus IEC 61131-3" extension for
  compile diagnostics and live tag values in program.st while it runs.
`, sc.Name, sc.Name, dep)
	return 0
}

// prompt runs the interactive form, pre-filled with current values.
func prompt(sc *scaffold) error {
	nameInput := huh.NewInput().
		Title("Project name").
		Description("Directory and program name for the new controller.").
		Placeholder("water-plant").
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("a name is required")
			}
			if strings.ContainsAny(s, " /\\") {
				return fmt.Errorf("no spaces or slashes")
			}
			return nil
		}).
		Value(&sc.Name)

	moduleInput := huh.NewInput().
		Title("Go module path").
		Description("Leave empty to use the project name (fine for local-only builds).").
		Placeholder("github.com/you/water-plant").
		Value(&sc.Module)

	features := []string{"plant", "ci", "vscode", "git"}
	featureSelect := huh.NewMultiSelect[string]().
		Title("Features").
		Description("Space to toggle, enter to confirm.").
		Options(
			huh.NewOption("Simulated plant (runnable demo physics)", "plant").Selected(true),
			huh.NewOption("GitHub Actions CI (vet, test, nautilus check)", "ci").Selected(true),
			huh.NewOption("VS Code setup (extension recommendation, live values)", "vscode").Selected(true),
			huh.NewOption("git init", "git").Selected(true),
		).
		Value(&features)

	groups := []*huh.Group{}
	if sc.Name == "" {
		groups = append(groups, huh.NewGroup(nameInput, moduleInput))
	} else if sc.Module == "" {
		groups = append(groups, huh.NewGroup(moduleInput))
	}
	groups = append(groups, huh.NewGroup(featureSelect))

	if err := huh.NewForm(groups...).Run(); err != nil {
		return err
	}
	has := func(f string) bool {
		for _, x := range features {
			if x == f {
				return true
			}
		}
		return false
	}
	sc.Plant, sc.CI, sc.VSCode, sc.Git = has("plant"), has("ci"), has("vscode"), has("git")
	return nil
}

// write renders the template tree into ./<name>.
func write(sc *scaffold) error {
	if _, err := os.Stat(sc.Name); err == nil {
		return fmt.Errorf("%s already exists", sc.Name)
	}

	// Template file → destination. Empty destination = skip unless the
	// guarding feature is on.
	files := []struct {
		src, dst string
		on       bool
	}{
		{"go.mod.tmpl", "go.mod", true},
		{"main.go.tmpl", "main.go", true},
		{"program_test.go.tmpl", "program_test.go", true},
		{"gitignore.tmpl", ".gitignore", true},
		{"README.md.tmpl", "README.md", true},
		{"program_plant.st.tmpl", "program.st", sc.Plant},
		{"plant.go.tmpl", "plant.go", sc.Plant},
		{"program_blank.st.tmpl", "program.st", !sc.Plant},
		{"driver.go.tmpl", "driver.go", !sc.Plant},
		{"ci.yml.tmpl", ".github/workflows/ci.yml", sc.CI},
		{"extensions.json.tmpl", ".vscode/extensions.json", sc.VSCode},
		{"settings.json.tmpl", ".vscode/settings.json", sc.VSCode},
	}

	for _, f := range files {
		if !f.on {
			continue
		}
		raw, err := templates.ReadFile("templates/" + f.src)
		if err != nil {
			return err
		}
		tmpl, err := template.New(f.src).Parse(string(raw))
		if err != nil {
			return err
		}
		dst := filepath.Join(sc.Name, f.dst)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if err := tmpl.Execute(out, sc); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
	}

	if sc.Git {
		// Best-effort: a missing git binary shouldn't fail the scaffold.
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = sc.Name
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus new: git init skipped:", err)
		}
	}
	return nil
}

// pascalCase turns "water-plant" into "WaterPlant" for the PROGRAM name.
func pascalCase(s string) string {
	parts := identRE.Split(s, -1)
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	if b.Len() == 0 {
		return "Main"
	}
	out := b.String()
	if out[0] >= '0' && out[0] <= '9' {
		out = "P" + out
	}
	return out
}
