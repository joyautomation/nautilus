// Command agents-eval measures whether AI coding agents write good LD, FBD,
// SFC and ST for nautilus, with and without the project AGENTS.md (#564).
//
//	go run ./tools/agents-eval run [-tasks a,b] [-k 3] [-model haiku] ...
//	go run ./tools/agents-eval score -task <id> <project-dir>
//	go run ./tools/agents-eval report <results-dir>
//
// It spends tokens and is non-deterministic, so it is not a PR gate; the
// scoring it applies is deterministic and unit-tested. See README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "score":
		err = cmdScore(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	case "rescore":
		err = cmdRescore(os.Args[2:])
	case "redact":
		err = cmdRedact(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agents-eval:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  agents-eval run    [flags]                 run the suite (spends tokens)
  agents-eval score  -task <id> <dir>        score one finished project
  agents-eval report <results-dir>           rebuild summary.md/.json from a run
  agents-eval rescore [-naut p] <results-dir> re-score a run's saved projects with
                                             the current scorer (no tokens), then report
  agents-eval redact <dir>                   replace the values of ANTHROPIC_API_KEY and
                                             other credentials in the environment wherever
                                             they occur under dir; exit 1 if any was found

run 'agents-eval <command> -h' for flags.
`)
}

// repoRoot finds the nautilus checkout from the working directory.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && strings.Contains(string(raw), "module github.com/joyautomation/nautilus\n") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run from inside the nautilus checkout")
		}
		dir = parent
	}
}

func cmdRun(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	tasksFlag := fs.String("tasks", "", "comma-separated task ids (default: all)")
	k := fs.Int("k", 3, "trials per task × condition")
	model := fs.String("model", "haiku", "model for the agent (Claude Code alias or full name)")
	agentName := fs.String("agent", "claude", "agent adapter: claude (codex is a documented stub)")
	conds := fs.String("conditions", "with,without", "conditions to run: with, without")
	parallel := fs.Int("parallel", 3, "trials run at once")
	timeout := fs.Duration("timeout", 10*time.Minute, "wall-clock limit per trial")
	budget := fs.Float64("budget", 1.00, "spend limit per trial, USD (Claude Code --max-budget-usd)")
	stopAfter := fs.Float64("stop-after-usd", 0, "start no new trials once the run has spent this much (0 = no limit)")
	agentsMd := fs.String("agents-md", filepath.Join(root, "cmd/naut/templates/AGENTS.md.tmpl"), "the AGENTS.md under test")
	claudeMd := fs.String("claude-md", "", "the CLAUDE.md written beside it (default: CLAUDE.md.tmpl next to -agents-md, else an @AGENTS.md import)")
	naut := fs.String("naut", "", "naut binary (default: build ./cmd/naut into the results directory)")
	out := fs.String("out", "", "results directory (default: tools/agents-eval/results/<time>-<agent>-<model>); reuse one to resume")
	tasksDir := fs.String("tasks-dir", filepath.Join(root, "tools/agents-eval/tasks"), "task suite")
	_ = fs.Parse(args)

	var only []string
	if *tasksFlag != "" {
		only = strings.Split(*tasksFlag, ",")
	}
	tasks, err := LoadTasks(*tasksDir, only)
	if err != nil {
		return err
	}
	var conditions []string
	for _, c := range strings.Split(*conds, ",") {
		c = strings.TrimSpace(c)
		if c != condWith && c != condWithout {
			return fmt.Errorf("unknown condition %q (with, without)", c)
		}
		conditions = append(conditions, c)
	}
	agent, err := NewAgent(*agentName)
	if err != nil {
		return err
	}
	if err := agent.Available(); err != nil {
		return fmt.Errorf("%s: %w", agent.Name(), err)
	}
	md, err := os.ReadFile(*agentsMd)
	if err != nil {
		return fmt.Errorf("%w\n(before #556 merges: git show origin/agents-md:cmd/naut/templates/AGENTS.md.tmpl > /tmp/AGENTS.md and pass -agents-md /tmp/AGENTS.md)", err)
	}
	cm := []byte("@AGENTS.md\n")
	if *claudeMd != "" {
		if cm, err = os.ReadFile(*claudeMd); err != nil {
			return err
		}
	} else if raw, err := os.ReadFile(filepath.Join(filepath.Dir(*agentsMd), "CLAUDE.md.tmpl")); err == nil {
		cm = raw
	}

	if *out == "" {
		safe := strings.NewReplacer("/", "-", ":", "-").Replace(*model)
		*out = filepath.Join(root, "tools/agents-eval/results", time.Now().UTC().Format("20060102-150405")+"-"+agent.Name()+"-"+safe)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	if *naut == "" {
		*naut = filepath.Join(*out, "bin", "naut")
		build := exec.Command("go", "build", "-o", *naut, "./cmd/naut")
		build.Dir = root
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("building naut: %w", err)
		}
	}
	if *naut, err = filepath.Abs(*naut); err != nil {
		return err
	}

	meta := Meta{
		Started: time.Now().UTC(), Agent: agent.Name(), Model: *model, K: *k,
		Conditions: conditions, AgentsMdSHA: sha256Hex(md), NautCommit: gitCommit(root),
		TimeoutSec: timeout.Seconds(), BudgetUSD: *budget,
	}
	for _, t := range tasks {
		meta.Tasks = append(meta.Tasks, t.ID)
	}
	metaPath := filepath.Join(*out, "meta.json")
	if raw, err := os.ReadFile(metaPath); err == nil {
		// Resuming: keep the original start time and provenance.
		// Resuming (or extending) a run: keep its start time, and the
		// tasks, conditions and k it already covers.
		var prev Meta
		if json.Unmarshal(raw, &prev) == nil {
			meta.Started = prev.Started
			meta.Tasks = union(prev.Tasks, meta.Tasks)
			meta.Conditions = union(prev.Conditions, meta.Conditions)
			meta.K = max(meta.K, prev.K)
		}
	}
	writeJSON(metaPath, meta)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05")+" "+format+"\n", a...)
	}
	logf("%d tasks × %v × k=%d with %s %s → %s", len(tasks), conditions, *k, agent.Name(), *model, *out)
	_, runErr := Run(ctx, RunConfig{
		Tasks: tasks, Conditions: conditions, K: *k, Model: *model, Agent: agent,
		Parallel: *parallel, Timeout: *timeout, BudgetUSD: *budget, AgentsMd: md, ClaudeMd: cm,
		Naut: *naut, Out: *out, Log: logf, StopAfterUSD: *stopAfter, Secrets: secretsFromEnv(),
	})
	// Report every trial in the directory, not only this invocation's:
	// a resumed run's summary covers the whole run.
	recs, err := LoadRecords(*out)
	if err != nil {
		return err
	}
	all, err := LoadTasks(*tasksDir, nil)
	if err != nil {
		return err
	}
	if err := writeReport(*out, meta, recs, all); err != nil {
		return err
	}
	logf("summary: %s", filepath.Join(*out, "summary.md"))
	var leaked []string
	for _, r := range recs {
		if len(r.SecretsRedacted) > 0 {
			leaked = append(leaked, fmt.Sprintf("%s/%s/#%d", r.Task, r.Condition, r.Trial))
		}
	}
	if len(leaked) > 0 {
		// Redacted on disk already; the run still fails so nobody ships
		// it without looking at how the value got there.
		return fmt.Errorf("a credential's value was found (and redacted) in %v", leaked)
	}
	return runErr
}

func writeReport(out string, meta Meta, recs []*Record, tasks []*Task) error {
	s := BuildSummary(meta, recs)
	writeJSON(filepath.Join(out, "summary.json"), s)
	langs := map[string][]string{}
	for _, t := range tasks {
		langs[t.ID] = t.Languages
	}
	f, err := os.Create(filepath.Join(out, "summary.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	WriteMarkdown(f, s, langs)
	return nil
}

func cmdReport(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: agents-eval report <results-dir>")
	}
	out := args[0]
	var meta Meta
	raw, err := os.ReadFile(filepath.Join(out, "meta.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	recs, err := LoadRecords(out)
	if err != nil {
		return err
	}
	tasks, err := LoadTasks(filepath.Join(root, "tools/agents-eval/tasks"), nil)
	if err != nil {
		return err
	}
	return writeReport(out, meta, recs, tasks)
}

// cmdRedact is the last gate before results leave the machine (the
// workflow runs it before uploading): it scrubs every credential value in
// the environment from every file under dir and fails if it found one.
func cmdRedact(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: agents-eval redact <dir>")
	}
	secrets := secretsFromEnv()
	if len(secrets) == 0 {
		fmt.Fprintln(os.Stderr, "agents-eval redact: no credential set in the environment; nothing to scan for")
		return nil
	}
	found, err := RedactDir(args[0], secrets)
	if err != nil {
		return err
	}
	if len(found) > 0 {
		// The file names only: never the value.
		return fmt.Errorf("a credential's value was found and redacted in %d file(s): %v", len(found), found)
	}
	fmt.Fprintf(os.Stderr, "agents-eval redact: %s is clean of %d credential(s)\n", args[0], len(secrets))
	return nil
}

// cmdRescore re-applies the current scorer to every trial's saved project,
// so a scorer change (a new structure rule, a new naut check warning) can be
// measured on old runs without spending tokens.
func cmdRescore(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("rescore", flag.ExitOnError)
	naut := fs.String("naut", "", "naut binary (default: the run's bin/naut)")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: agents-eval rescore [-naut path] <results-dir>")
	}
	out := fs.Arg(0)
	if *naut == "" {
		*naut = filepath.Join(out, "bin", "naut")
	}
	if *naut, err = filepath.Abs(*naut); err != nil {
		return err
	}
	tasks, err := LoadTasks(filepath.Join(root, "tools/agents-eval/tasks"), nil)
	if err != nil {
		return err
	}
	byID := map[string]*Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	recs, err := LoadRecords(out)
	if err != nil {
		return err
	}
	// The scaffold's own test file; rescoring has no fresh scaffold to ask.
	s := &Scorer{Naut: *naut, TemplateTests: map[string]bool{"plant_test.yaml": true}}
	for _, r := range recs {
		task := byID[r.Task]
		if task == nil || r.Error != "" {
			continue
		}
		dir := trialDir(out, r.Task, r.Condition, r.Trial)
		sc, err := s.ScoreProject(filepath.Join(dir, "project"), task)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		before := verdictWord(r)
		r.Score = sc
		writeJSON(filepath.Join(dir, "record.json"), r)
		if after := verdictWord(r); after != before {
			fmt.Fprintf(os.Stderr, "%-14s %-7s #%d  %s → %s\n", r.Task, r.Condition, r.Trial, before, after)
		}
	}
	var meta Meta
	raw, err := os.ReadFile(filepath.Join(out, "meta.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return err
	}
	return writeReport(out, meta, recs, tasks)
}

func cmdScore(args []string) error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	taskID := fs.String("task", "", "task id")
	naut := fs.String("naut", "naut", "naut binary")
	_ = fs.Parse(args)
	if *taskID == "" || fs.NArg() != 1 {
		return errors.New("usage: agents-eval score -task <id> [-naut path] <project-dir>")
	}
	task, err := LoadTask(filepath.Join(root, "tools/agents-eval/tasks", *taskID))
	if err != nil {
		return err
	}
	bin, err := exec.LookPath(*naut)
	if err != nil {
		return err
	}
	s := &Scorer{Naut: bin}
	sc, err := s.ScoreProject(fs.Arg(0), task)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(sc)
}

// union appends b's entries missing from a, keeping a's order.
func union(a, b []string) []string {
	seen := map[string]bool{}
	out := append([]string(nil), a...)
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func gitCommit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
