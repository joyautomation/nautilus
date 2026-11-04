package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Diag is one `naut check` diagnostic line.
type Diag struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"` // "error" | "warning"
	ID       string `json:"id"`
	Message  string `json:"message"`
}

// CheckResult is what `naut check` said about the project.
type CheckResult struct {
	ExitCode    int            `json:"exitCode"`
	Errors      int            `json:"errors"`
	Warnings    int            `json:"warnings"`
	WarningIDs  map[string]int `json:"warningIds,omitempty"`
	Diagnostics []Diag         `json:"diagnostics,omitempty"`
}

// TestResult is one `naut test -json` run.
type TestResult struct {
	Total    int      `json:"total"`
	Passed   int      `json:"passed"`
	Failures []string `json:"failures,omitempty"` // failing test names
	Error    string   `json:"error,omitempty"`    // compile/load failure, no tests ran
}

// Score is everything the scorer measured about one finished project.
type Score struct {
	Task             string          `json:"task"`
	Compiles         bool            `json:"compiles"`
	Check            CheckResult     `json:"check"`
	Hidden           TestResult      `json:"hidden"`
	AgentTests       TestResult      `json:"agentTests"`
	AgentTestFiles   int             `json:"agentTestFiles"` // *_test.yaml the agent added
	TaskLanguages    []string        `json:"taskLanguages"`  // languages of the manifest's task programs
	MissingLanguages []string        `json:"missingLanguages,omitempty"`
	Metrics          *ProjectMetrics `json:"metrics"`
	Violations       []string        `json:"violations,omitempty"`
	Verdict          Verdict         `json:"verdict"`
}

// Scorer runs the deterministic checks against a project with one naut
// binary.
type Scorer struct {
	Naut string // path to the naut binary
	// TemplateTests are test files the scaffold itself wrote, not counted
	// as tests the agent added.
	TemplateTests map[string]bool
	Timeout       time.Duration // per naut invocation
}

// ScoreProject scores dir against task. It never modifies dir: the hidden
// tests run in a scratch copy.
func (s *Scorer) ScoreProject(dir string, task *Task) (*Score, error) {
	sc := &Score{Task: task.ID}

	out, code, err := s.naut(dir, "check")
	if err != nil {
		return nil, err
	}
	sc.Check = ParseCheck(out, code)
	sc.Compiles = code == 0 && sc.Check.Errors == 0

	langs, err := manifestLanguages(dir)
	if err != nil {
		sc.Check.Errors++
		sc.Check.Diagnostics = append(sc.Check.Diagnostics, Diag{File: "nautilus.yaml", Severity: "error", ID: "manifest", Message: err.Error()})
		sc.Compiles = false
	}
	sc.TaskLanguages = langs
	have := map[string]bool{}
	for _, l := range langs {
		have[l] = true
	}
	for _, want := range task.Languages {
		if !have[want] {
			sc.MissingLanguages = append(sc.MissingLanguages, want)
		}
	}

	if sc.AgentTestFiles, err = s.countAgentTests(dir); err != nil {
		return nil, err
	}
	if sc.AgentTestFiles > 0 || hasTests(dir) {
		sc.AgentTests = s.runTests(dir)
	}

	scratch, err := os.MkdirTemp("", "agents-eval-score-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if err := copyProject(dir, scratch); err != nil {
		return nil, err
	}
	// The agent's own tests stay out of the hidden run: only the hidden
	// suite decides correctness.
	_ = filepath.WalkDir(scratch, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), "_test.yaml") {
			os.Remove(p)
		}
		return nil
	})
	if err := os.WriteFile(filepath.Join(scratch, "zz_hidden_eval_test.yaml"), task.HiddenTest, 0o644); err != nil {
		return nil, err
	}
	sc.Hidden = s.runTests(scratch)

	if sc.Metrics, err = MeasureProject(dir); err != nil {
		return nil, err
	}
	sc.Violations = sc.Metrics.Violations()
	sc.Verdict = Judge(sc)
	return sc, nil
}

func (s *Scorer) naut(dir string, args ...string) (string, int, error) {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Naut, args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return buf.String(), 0, nil
	case ctx.Err() != nil:
		return buf.String() + "\n(timed out)", -1, nil
	case errors.As(err, &ee):
		return buf.String(), ee.ExitCode(), nil
	default:
		return "", 0, fmt.Errorf("naut %s: %w", strings.Join(args, " "), err)
	}
}

func (s *Scorer) runTests(dir string) TestResult {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.Naut, "test", "-json")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()
	r := ParseTestJSON(stdout.String())
	if r.Total == 0 {
		r.Error = strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			r.Error = "timed out"
		}
		if r.Error == "" {
			r.Error = "no test results"
		}
	}
	return r
}

func (s *Scorer) countAgentTests(dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.yaml"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range files {
		if !s.TemplateTests[filepath.Base(f)] {
			n++
		}
	}
	return n, nil
}

func hasTests(dir string) bool {
	files, _ := filepath.Glob(filepath.Join(dir, "*_test.yaml"))
	return len(files) > 0
}

// diagLine matches `file:line:col: [warning: ]message` and
// `file: warning: message` (manifest diagnostics have no position).
var diagLine = regexp.MustCompile(`^(\S+?):(?:(\d+):(\d+):)? (warning: )?(.+)$`)

// ParseCheck turns `naut check` output into counts and diagnostics. naut
// check has no JSON mode, so this reads its gcc-style lines.
func ParseCheck(out string, exitCode int) CheckResult {
	r := CheckResult{ExitCode: exitCode, WarningIDs: map[string]int{}}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \r")
		if strings.HasPrefix(line, "naut check:") || line == "" {
			continue
		}
		m := diagLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		file := m[1]
		if !strings.Contains(file, ".") { // "foo: bar" prose, not a file
			continue
		}
		d := Diag{File: file, Message: m[5], Severity: "error"}
		fmt.Sscan(m[2], &d.Line)
		if m[4] != "" {
			d.Severity = "warning"
			d.ID = WarningID(d.Message)
			r.Warnings++
			r.WarningIDs[d.ID]++
		} else {
			d.ID = "error"
			r.Errors++
		}
		r.Diagnostics = append(r.Diagnostics, d)
	}
	if exitCode != 0 && r.Errors == 0 {
		// A failure naut reported without a positioned line (an
		// unreadable manifest, a crash) is still a failure.
		r.Errors = 1
		r.Diagnostics = append(r.Diagnostics, Diag{Severity: "error", ID: "error", Message: lastLine(out)})
	}
	if len(r.WarningIDs) == 0 {
		r.WarningIDs = nil
	}
	return r
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// warningIDs names the warnings naut check prints today. A warning that
// carries its own id (`[ld-multi-coil]` at the end, or `ld-multi-coil:` at
// the start, as the #561 structure rules may) keeps it; anything else falls
// back to a slug of its first words.
var warningIDs = []struct{ substr, id string }{
	{"shadows the project tag", "shadowed-tag"},
	{"which no program binds", "unbound-tag"},
	{"declares no tag for", "undeclared-write"},
	{"empty chart", "empty-chart"},
	{"first task names itself", "first-task-name"},
	{"comes from a communications driver", "input-without-driver"},
	{"tag-meta documents", "tag-meta-unknown"},
	{"is older than", "stale-hmi"},
}

var (
	idSuffix = regexp.MustCompile(`\[([a-z][a-z0-9.-]*)\]\s*$`)
	idPrefix = regexp.MustCompile(`^([a-z]+[a-z0-9]*(?:[.-][a-z0-9]+)+):\s`)
	quoted   = regexp.MustCompile(`"[^"]*"|'[^']*'|\d+(\.\d+)?`)
	nonWord  = regexp.MustCompile(`[^a-z]+`)
)

// WarningID gives a warning message a stable, short id.
func WarningID(msg string) string {
	if m := idSuffix.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	if m := idPrefix.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	for _, w := range warningIDs {
		if strings.Contains(msg, w.substr) {
			return w.id
		}
	}
	s := strings.ToLower(quoted.ReplaceAllString(msg, " "))
	if i := strings.IndexAny(s, "—;:("); i > 0 {
		s = s[:i]
	}
	words := strings.Fields(nonWord.ReplaceAllString(s, " "))
	if len(words) > 5 {
		words = words[:5]
	}
	if len(words) == 0 {
		return "warning"
	}
	return strings.Join(words, "-")
}

// ParseTestJSON reads `naut test -json` NDJSON.
func ParseTestJSON(out string) TestResult {
	var r TestResult
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var ev struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Name == "" {
			continue
		}
		r.Total++
		if ev.Passed {
			r.Passed++
		} else {
			r.Failures = append(r.Failures, ev.Name)
		}
	}
	return r
}

// manifestLanguages lists the languages of the programs nautilus.yaml runs
// as tasks, sorted and deduplicated.
func manifestLanguages(dir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "nautilus.yaml"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Tasks []struct {
			Program string `yaml:"program"`
		} `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, t := range m.Tasks {
		if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(t.Program)), "."); ext != "" {
			set[ext] = true
		}
	}
	var out []string
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out, nil
}

// copyProject copies a project tree without .git, node_modules or other
// hidden directories.
func copyProject(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), raw, 0o644)
	})
}
