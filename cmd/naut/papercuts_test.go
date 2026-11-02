package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joyautomation/nautilus/internal/project"
)

// stderrOf runs fn with os.Stderr captured, for the commands whose usage and
// refusals go there.
func stderrOf(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	code := fn()
	w.Close()
	os.Stderr = old
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	r.Close()
	return sb.String(), code
}

// A flag the usage lists with no description leaves the reader guessing what
// it does. Go prints a flag's description on the line after its name, so an
// undescribed flag is a name line followed by a blank one.
func TestNewHelpDescribesEveryFlag(t *testing.T) {
	out, _ := stderrOf(t, func() int { return runNew([]string{"--help"}) })
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "  -") {
			continue
		}
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) == "" {
			t.Errorf("flag %q is listed with no description:\n%s", strings.TrimSpace(l), out)
		}
	}
}

// naut new runs init and makes no commit, so the build of that directory
// finds a repository with nothing in it. "not a repo" there told people to
// create one that already exists.
func TestBuildNoteSaysNoCommitsInAFreshRepo(t *testing.T) {
	dir := t.TempDir()
	if got := noHistoryNote(dir); !strings.Contains(got, "not a repo") {
		t.Errorf("a plain directory should say it is not a repo, got %q", got)
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init unavailable: %v: %s", err, out)
	}
	got := noHistoryNote(dir)
	if strings.Contains(got, "not a repo") || !strings.Contains(got, "no commits yet") {
		t.Errorf("a repository with no commits should say so, got %q", got)
	}
}

// check is the CI gate, so a tree with no programs and an unreadable manifest
// must fail it, with a message a person can act on. The decoder's own word
// for an empty file is a bare EOF.
func TestCheckRefusesAnEmptyManifest(t *testing.T) {
	var out string
	var code int
	errOut, _ := stderrOf(t, func() int {
		out, code = checkIn(t, map[string]string{"nautilus.yaml": ""})
		return code
	})
	if code != 1 {
		t.Fatalf("an empty manifest passed the check (code %d):\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "nautilus.yaml") || strings.Contains(errOut, ": EOF") {
		t.Errorf("the refusal should name the file and say more than EOF, got %q", errOut)
	}
}

func TestReadManifestSaysAnEmptyFileIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nautilus.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := project.ReadManifest(os.DirFS(dir), "")
	if err == nil {
		t.Fatal("an empty manifest was read without error")
	}
	if msg := err.Error(); !strings.Contains(msg, "empty") || strings.HasSuffix(msg, "EOF") {
		t.Errorf("message does not explain the empty file: %q", msg)
	}
}

// A built controller takes no arguments, so --help must answer instead of
// starting the scan loop.
func TestBuiltControllerAnswersHelp(t *testing.T) {
	for _, a := range []string{"--help", "-h", "-help", "help"} {
		if !wantsHelp([]string{a}) {
			t.Errorf("%s should print usage", a)
		}
	}
	if wantsHelp(nil) {
		t.Error("no arguments must start the controller")
	}
	u := builtUsage("pumps")
	for _, want := range []string{"usage: pumps", "NAUTILUS_ADDR", "NAUTILUS_TOKEN", "retain.json"} {
		if !strings.Contains(u, want) {
			t.Errorf("usage does not mention %s:\n%s", want, u)
		}
	}
}

// What naut build writes (a binary named for the project) and what a retain:
// block writes must not show up in git status of a project naut new made.
func TestScaffoldGitignoreCoversBuildOutput(t *testing.T) {
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	sc := scaffold{Name: "plant-proj", Program: "PlantProj", Template: tmplMinimal}
	if err := write(&sc); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plant-proj", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	has := func(want string) bool {
		for _, l := range lines {
			if l == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"/plant-proj", "retain.json"} {
		if !has(want) {
			t.Errorf(".gitignore lacks %q:\n%s", want, raw)
		}
	}
}

// Diagnostics are file:line:col so a terminal or editor can jump to them. A
// manifest problem names nautilus.yaml, and the tag's line when the manifest
// declares the tag.
func TestCheckLocatesManifestDiagnosticsInTheFile(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": `tasks:
  - program: program.st
tags:
  - { name: Sensor, role: input }
  - { name: Actuator, role: output }
  - { name: Spare, role: setpoint, type: REAL }
`,
		"program.st": programBinding("", ""),
	})
	if code != 0 {
		t.Fatalf("code %d:\n%s", code, out)
	}
	if !strings.Contains(out, "/nautilus.yaml:6:") {
		t.Errorf("the unbound-setpoint warning should name nautilus.yaml:6, got:\n%s", out)
	}
}

func TestCheckLocatesATagInitErrorAtItsLine(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"lib/types.st": "TYPE\n    Mode : (Auto, Manual) := Auto;\nEND_TYPE\n",
		"nautilus.yaml": `tasks:
  - program: program.st
tags:
  - { name: Sensor, role: input }
  - { name: Actuator, role: output }
  - { name: Sel, role: state, type: Mode, init: Bogus }
`,
		"program.st": programBinding("", ""),
	})
	if code == 0 {
		t.Fatalf("a bad enum init passed:\n%s", out)
	}
	if !strings.Contains(out, "/nautilus.yaml:6:") {
		t.Errorf("the init error should name nautilus.yaml:6, got:\n%s", out)
	}
}

// A manifest the decoder refuses (an unknown key) is located at the file,
// once: the loader's error already begins "nautilus.yaml:", and check used
// to print the project directory in front of it (".: nautilus.yaml: …").
func TestCheckLocatesAManifestDecodeError(t *testing.T) {
	out, code := checkIn(t, map[string]string{
		"nautilus.yaml": "tasks:\n  - program: program.st\nbogus: 1\n",
		"program.st":    "PROGRAM P\nEND_PROGRAM\n",
	})
	if code == 0 {
		t.Fatalf("an unknown manifest key passed:\n%s", out)
	}
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "unmarshal errors") {
			line = l
			break
		}
	}
	if !strings.Contains(line, "/nautilus.yaml:") || strings.Count(line, "nautilus.yaml") != 1 {
		t.Errorf("the decode error should name nautilus.yaml once, got:\n%s", out)
	}
}

// The built controller itself answers --help: main's hook, not just the
// helpers above. Builds naut, builds a project with it, runs the result.
func TestBuiltBinaryAnswersHelp(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs a controller binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	dir := t.TempDir()
	naut := filepath.Join(dir, "naut")
	if out, err := exec.Command(goBin, "build", "-o", naut, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/naut: %v\n%s", err, out)
	}
	proj := filepath.Join(dir, "pumps")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"nautilus.yaml": "name: pumps\ntasks:\n  - program: main.st\n",
		"main.st":       "PROGRAM Main\nEND_PROGRAM\n",
	} {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command(naut, "build")
	build.Dir = proj
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("naut build: %v\n%s", err, out)
	}
	bin := filepath.Join(proj, "pumps")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := exec.CommandContext(ctx, bin, "--help")
	run.Dir = dir
	out, err := run.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("--help started the controller (still running after 10 s):\n%s", out)
	}
	if err != nil || !strings.Contains(string(out), "usage: pumps") {
		t.Fatalf("--help: %v\n%s", err, out)
	}
}

// Under -m the loader's errors begin with the manifest's own relative name
// ("sites/bad.yaml:"); say puts the project directory in front of that name
// once, not the whole path twice.
func TestManifestLocSaysAnAlternateManifestOnce(t *testing.T) {
	l := newManifestLoc("proj", "sites/bad.yaml")
	got := l.say(errors.New("sites/bad.yaml: yaml: unmarshal errors"))
	want := filepath.Join("proj", "sites", "bad.yaml") + ": yaml: unmarshal errors"
	if got != want {
		t.Errorf("say = %q, want %q", got, want)
	}
	if got := l.say(errors.New("no tasks")); got != filepath.Join("proj", "sites", "bad.yaml")+": no tasks" {
		t.Errorf("an error without the name: %q", got)
	}
}
