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
	"golang.org/x/term"
)

//go:embed templates
var templates embed.FS

// Project templates. The manifest form is the base architecture — it needs
// no toolchain to run, build, or test — so it holds the unqualified names.
// Go is an SDK you opt into for custom field buses and richer simulation,
// which is what the sdk* templates prepare a project for.
const (
	tmplMinimal = "minimal"  // manifest: one task, one program, one test
	tmplDemo    = "demo"     // manifest: the feature tour, runnable on arrival
	tmplSDK     = "sdk"      // Go: main.go + a driver seam to fill in
	tmplSDKDemo = "sdk-demo" // Go: the tour, with plant physics in Go
)

var templateNames = []string{tmplMinimal, tmplDemo, tmplSDK, tmplSDKDemo}

// scaffold is the answer set the prompts (or flags) fill in; every template
// renders against it.
type scaffold struct {
	Name     string // directory + program name, kebab-ish
	Module   string // Go module path
	Program  string // PROGRAM identifier derived from Name (PascalCase)
	Language string // program language: st, fbd, ld, or sfc
	Template string // one of the tmpl* constants

	CI     bool // GitHub Actions workflow
	VSCode bool // .vscode extension recommendation + runtime URL
	Git    bool // git init
	Deploy bool // Dockerfile + k8s manifests + CD workflow (manifest tier)

	// Replace, if set, adds a filesystem `replace` directive pointing the
	// nautilus dependency at a local checkout. For contributors testing
	// `nautilus new` before the module is published/tagged: the generated
	// project builds against the working copy without a network/VCS fetch.
	Replace string
}

// NoGo reports a manifest project: nautilus.yaml + IEC files, no toolchain.
// Templates read it, so it stays the name they already knew.
func (s scaffold) NoGo() bool { return s.Template == tmplMinimal || s.Template == tmplDemo }

// Plant reports the Go SDK's simulated-physics demo (plant.go).
func (s scaffold) Plant() bool { return s.Template == tmplSDKDemo }

// Demo reports the manifest feature tour.
func (s scaffold) Demo() bool { return s.Template == tmplDemo }

// Blank reports a template that starts from an empty program in the chosen
// language, rather than a worked example.
func (s scaffold) Blank() bool { return s.Template == tmplMinimal || s.Template == tmplSDK }

// settle resolves the fields the chosen template decides for itself.
// --language picks the language of the BLANK program, so it applies to
// minimal and sdk; the worked examples are fixed compositions (the demo is
// the multi-language tour, and the SDK demo's plant program is ST).
func (s *scaffold) settle() {
	switch s.Template {
	case tmplDemo:
		s.Language = "fbd" // control is FBD; sim is ST, interlocks are LD
	case tmplSDKDemo:
		s.Language = "st"
	case "":
		s.Template = tmplDemo
	}
	if s.Language == "" {
		s.Language = "st"
	}
	// Deploy is a manifest-tier feature: the Dockerfile wraps the binary
	// `nautilus build` emits, which the Go tier doesn't use.
	if !s.NoGo() {
		s.Deploy = false
	}
}

var identRE = regexp.MustCompile(`[^A-Za-z0-9]+`)

// runNew scaffolds a nautilus controller project, sv-create style: prompt
// for whatever wasn't given on the command line, then write the tree.
// --no-input — or having no terminal to prompt on — skips the prompts and
// takes the defaults.
func runNew(args []string) int {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	module := fs.String("module", "", "Go module path (sdk templates only; default: name)")
	language := fs.String("language", "st", "program language for minimal/sdk: st, fbd, ld, or sfc")
	tmpl := fs.String("template", "", "project template: minimal, demo, sdk, or sdk-demo (default: demo)")
	// Superseded by --template; kept working so documented one-liners and
	// existing scripts don't break. Deliberately undocumented.
	noGo := fs.Bool("no-go", false, "")
	noInput := fs.Bool("no-input", false, "accept defaults instead of prompting")
	deploy := fs.Bool("deploy", false, "add Dockerfile, k8s manifests, and a CD workflow (manifest templates)")
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
		CI:     true, VSCode: true, Git: true, Deploy: *deploy,
		Language: strings.ToLower(strings.TrimSpace(*language)),
		Template: strings.ToLower(strings.TrimSpace(*tmpl)),
	}
	switch sc.Template {
	case "":
		sc.Template = tmplDemo // a new project should run and move
		if *noGo {
			sc.Template = tmplMinimal // the flag --template replaced
		}
	case tmplMinimal, tmplDemo, tmplSDK, tmplSDKDemo:
	default:
		fmt.Fprintf(os.Stderr, "nautilus new: --template must be one of %s\n", strings.Join(templateNames, ", "))
		return 2
	}
	switch sc.Language {
	case "", "st":
		sc.Language = "st"
	case "fbd", "ld", "sfc":
	default:
		fmt.Fprintln(os.Stderr, "nautilus new: --language must be st, fbd, ld, or sfc")
		return 2
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

	if *noInput || !stdinIsTTY() {
		// No terminal to prompt on (CI, docker, piped input) behaves like
		// --no-input, so the documented one-liners work headlessly.
		if sc.Name == "" {
			fmt.Fprintln(os.Stderr, "nautilus new: a name is required when not prompting (--no-input or no terminal)")
			return 2
		}
	} else {
		if err := prompt(&sc); err != nil {
			fmt.Fprintln(os.Stderr, "nautilus new:", err)
			return 1
		}
	}
	sc.settle()
	if sc.Module == "" {
		sc.Module = sc.Name
	}
	sc.Program = pascalCase(sc.Name)

	if err := write(&sc); err != nil {
		fmt.Fprintln(os.Stderr, "nautilus new:", err)
		return 1
	}

	if sc.NoGo() {
		what := "a manifest project, no Go required"
		if sc.Demo() {
			what = "3 tasks, 3 IEC languages, simulated plant — no Go required"
		}
		fmt.Printf(`
  created %s/ — %s

  next steps:
    cd %s
    nautilus run     # scan loop + dashboard + tag API on http://localhost:8080
    nautilus test    # acceptance tests, in virtual time
    nautilus build   # emit ./%s — a self-contained controller binary

  open the folder in VS Code with the "nautilus IEC 61131-3" extension for
  diagnostics, the graphical editors, and live values in program.%s.
`, sc.Name, what, sc.Name, sc.Name, sc.Language)
		return 0
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
  compile diagnostics and live tag values in program.%s while it runs.
`, sc.Name, sc.Name, dep, sc.Language)
	return 0
}

// stdinIsTTY reports whether there's a terminal to prompt on — without one,
// huh's form would die trying to open /dev/tty. (A real ioctl check, not a
// char-device stat: /dev/null is a char device too.)
func stdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
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

	features := []string{"ci", "vscode", "git"}
	if sc.Deploy {
		features = append(features, "deploy")
	}
	featureSelect := huh.NewMultiSelect[string]().
		Title("Features").
		Description("deploy applies to manifest templates (the Dockerfile wraps `nautilus build`)").
		Description("Space to toggle, enter to confirm.").
		Options(
			huh.NewOption("GitHub Actions CI (check, test, build)", "ci").Selected(true),
			huh.NewOption("VS Code setup (extension recommendation, live values)", "vscode").Selected(true),
			huh.NewOption("git init", "git").Selected(true),
			huh.NewOption("Kubernetes deploy (Dockerfile, manifests, CD workflow)", "deploy"),
		).
		Value(&features)

	templateSelect := huh.NewSelect[string]().
		Title("Template").
		Description("A manifest project needs no toolchain to run, test, or build. The SDK templates add Go for custom field buses and richer simulation.").
		Options(
			huh.NewOption("Demo — runnable tour: 3 tasks, 3 languages, simulated plant", tmplDemo),
			huh.NewOption("Minimal — one task, one program, one test", tmplMinimal),
			huh.NewOption("SDK — Go project with a driver seam to fill in", tmplSDK),
			huh.NewOption("SDK demo — Go project with plant physics in Go", tmplSDKDemo),
		).
		Value(&sc.Template)

	languageSelect := huh.NewSelect[string]().
		Title("Program language").
		Description("All four share the same runtime, tags, and tooling.").
		Options(
			huh.NewOption("Structured Text (.st)", "st"),
			huh.NewOption("Function Block Diagram (.fbd)", "fbd"),
			huh.NewOption("Ladder Diagram (.ld)", "ld"),
			huh.NewOption("Sequential Function Chart (.sfc)", "sfc"),
		).
		Value(&sc.Language)

	groups := []*huh.Group{}
	if sc.Name == "" {
		groups = append(groups, huh.NewGroup(nameInput))
	}
	groups = append(groups, huh.NewGroup(templateSelect))
	// The worked examples are fixed compositions, and only the SDK
	// templates have a Go module — so ask each question where it applies.
	groups = append(groups,
		huh.NewGroup(languageSelect).WithHideFunc(func() bool { return !sc.Blank() }),
		huh.NewGroup(moduleInput).WithHideFunc(func() bool { return sc.NoGo() || sc.Module != "" }),
		huh.NewGroup(featureSelect),
	)

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
	sc.CI, sc.VSCode, sc.Git, sc.Deploy = has("ci"), has("vscode"), has("git"), has("deploy")
	return nil
}

// write renders the template tree into ./<name>.
func write(sc *scaffold) error {
	sc.settle() // zero-value scaffolds (tests) get the defaults
	if _, err := os.Stat(sc.Name); err == nil {
		return fmt.Errorf("%s already exists", sc.Name)
	}

	// Template file → destination. Empty destination = skip unless the
	// guarding feature is on.
	files := []struct {
		src, dst string
		on       bool
	}{
		// Go scaffolding — the SDK templates only.
		{"go.mod.tmpl", "go.mod", !sc.NoGo()},
		{"main.go.tmpl", "main.go", !sc.NoGo()},
		{"program_test.go.tmpl", "program_test.go", !sc.NoGo()},
		{"driver.go.tmpl", "driver.go", sc.Template == tmplSDK},
		{"plant.go.tmpl", "plant.go", sc.Plant()},
		{"program_plant.st.tmpl", "program.st", sc.Plant()},
		{"blocks.st.tmpl", "blocks.st", sc.Plant()},

		// Manifest scaffolding — the base architecture, no toolchain.
		{"nautilus.yaml.tmpl", "nautilus.yaml", sc.Template == tmplMinimal},
		{"acceptance_test.yaml.tmpl", sc.Name + "_test.yaml", sc.Template == tmplMinimal},
		{"nautilus_demo.yaml.tmpl", "nautilus.yaml", sc.Demo()},
		{"acceptance_demo_test.yaml.tmpl", sc.Name + "_test.yaml", sc.Demo()},
		{"demo_program.fbd.tmpl", "program.fbd", sc.Demo()},
		{"demo_sim.st.tmpl", "sim.st", sc.Demo()},
		{"demo_interlocks.ld.tmpl", "interlocks.ld", sc.Demo()},
		{"demo_blocks.st.tmpl", "blocks.st", sc.Demo()},

		// The blank program, in the language that was asked for.
		{"program_blank.st.tmpl", "program.st", sc.Blank() && sc.Language == "st"},
		{"program_blank.fbd.tmpl", "program.fbd", sc.Blank() && sc.Language == "fbd"},
		{"program_blank.ld.tmpl", "program.ld", sc.Blank() && sc.Language == "ld"},
		{"program_blank.sfc.tmpl", "program.sfc", sc.Blank() && sc.Language == "sfc"},

		// Deploy scaffolding — commit-to-running-controller for the
		// manifest tier: the Dockerfile wraps the binary `nautilus build`
		// emits, the k8s manifests carry the RBAC the retain store and
		// elector need, and the workflow pushes an image on every main.
		{"Dockerfile.tmpl", "Dockerfile", sc.Deploy && sc.NoGo()},
		{"deploy_k8s.yaml.tmpl", "deploy/k8s.yaml", sc.Deploy && sc.NoGo()},
		{"deploy.yml.tmpl", ".github/workflows/deploy.yml", sc.Deploy && sc.NoGo()},

		{"gitignore.tmpl", ".gitignore", true},
		{"README.md.tmpl", "README.md", !sc.NoGo()},
		{"README_nogo.md.tmpl", "README.md", sc.NoGo()},
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
