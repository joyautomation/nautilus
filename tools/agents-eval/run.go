package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Conditions. "with" puts the project AGENTS.md (and the CLAUDE.md that
// imports it) in the fresh project; "without" makes sure neither is there.
const (
	condWith    = "with"
	condWithout = "without"
)

// RunConfig is one `agents-eval run`.
type RunConfig struct {
	Tasks        []*Task
	Conditions   []string
	K            int
	Model        string
	Agent        Agent
	Parallel     int
	Timeout      time.Duration
	BudgetUSD    float64
	AgentsMd     []byte // the AGENTS.md under test
	ClaudeMd     []byte // the CLAUDE.md written beside it
	Naut         string // naut binary, used by the agent and the scorer
	Out          string // results directory for this run
	Log          func(format string, args ...any)
	StopAfterUSD float64 // stop starting new trials once spend passes this (0 = no limit)

	// agentBin is the directory put first on the agent's PATH: a copy of
	// Naut outside the checkout, so `which naut` does not lead the agent
	// to the repository (and its hidden tests).
	agentBin string
}

// Meta is the run's provenance, written to meta.json and the summary.
type Meta struct {
	Started     time.Time `json:"started"`
	Agent       string    `json:"agent"`
	Model       string    `json:"model"`
	K           int       `json:"k"`
	Tasks       []string  `json:"tasks"`
	Conditions  []string  `json:"conditions"`
	AgentsMdSHA string    `json:"agentsMdSha256"`
	NautCommit  string    `json:"nautCommit,omitempty"`
	TimeoutSec  float64   `json:"timeoutSec"`
	BudgetUSD   float64   `json:"budgetUsd"`
}

// Record is one trial: the session and its score.
type Record struct {
	Task      string    `json:"task"`
	Condition string    `json:"condition"`
	Trial     int       `json:"trial"`
	Run       *AgentRun `json:"run"`
	Score     *Score    `json:"score,omitempty"`
	Error     string    `json:"error,omitempty"` // harness failure (not the agent's)
}

func trialDir(out string, task, cond string, trial int) string {
	return filepath.Join(out, "runs", task, cond, fmt.Sprintf("trial-%d", trial))
}

// Run executes every task × condition × trial, writing each trial's
// record.json as it finishes. A trial whose record.json already exists is
// skipped, so rerunning with the same --out resumes an interrupted run.
func Run(ctx context.Context, cfg RunConfig) ([]*Record, error) {
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return nil, err
	}
	bin, err := os.MkdirTemp("", "agents-eval-bin-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(bin)
	raw, err := os.ReadFile(cfg.Naut)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(bin, "naut"), raw, 0o755); err != nil {
		return nil, err
	}
	cfg.agentBin = bin
	type job struct {
		task  *Task
		cond  string
		trial int
	}
	var jobs []job
	// Trial-major order: an early stop leaves every cell with the same
	// number of trials instead of finishing one task and skipping others.
	for trial := 1; trial <= cfg.K; trial++ {
		for _, t := range cfg.Tasks {
			for _, c := range cfg.Conditions {
				jobs = append(jobs, job{t, c, trial})
			}
		}
	}
	var (
		mu      sync.Mutex
		records []*Record
		spent   float64
		stopped bool
	)
	ch := make(chan job)
	var wg sync.WaitGroup
	for w := 0; w < max(1, cfg.Parallel); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				mu.Lock()
				skip := stopped
				mu.Unlock()
				if skip || ctx.Err() != nil {
					continue
				}
				rec := runTrial(ctx, cfg, j.task, j.cond, j.trial)
				mu.Lock()
				records = append(records, rec)
				if rec.Run != nil {
					spent += rec.Run.CostUSD
				}
				if cfg.StopAfterUSD > 0 && spent >= cfg.StopAfterUSD && !stopped {
					stopped = true
					cfg.Log("spend $%.2f passed --stop-after-usd $%.2f: no new trials", spent, cfg.StopAfterUSD)
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		ch <- j
	}
	close(ch)
	wg.Wait()
	return records, ctx.Err()
}

func runTrial(ctx context.Context, cfg RunConfig, task *Task, cond string, trial int) *Record {
	dir := trialDir(cfg.Out, task.ID, cond, trial)
	recPath := filepath.Join(dir, "record.json")
	if raw, err := os.ReadFile(recPath); err == nil {
		var rec Record
		if json.Unmarshal(raw, &rec) == nil && rec.Error == "" {
			cfg.Log("%-14s %-7s #%d  (already done) %s", task.ID, cond, trial, verdictWord(&rec))
			return &rec
		}
	}
	rec := &Record{Task: task.ID, Condition: cond, Trial: trial}
	fail := func(err error) *Record {
		rec.Error = err.Error()
		cfg.Log("%-14s %-7s #%d  HARNESS ERROR %v", task.ID, cond, trial, err)
		writeJSON(recPath, rec)
		return rec
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(err)
	}
	parent, err := os.MkdirTemp("", "agents-eval-*")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(parent)
	project, templateTests, err := scaffold(cfg, parent, cond)
	if err != nil {
		return fail(err)
	}
	cfg.Log("%-14s %-7s #%d  started", task.ID, cond, trial)
	rec.Run, err = cfg.Agent.Run(ctx, AgentRequest{
		Dir: project, Prompt: task.Spec, Model: cfg.Model,
		Timeout: cfg.Timeout, BudgetUSD: cfg.BudgetUSD,
		NautDir: cfg.agentBin, Transcript: filepath.Join(dir, "transcript.jsonl"),
	})
	if err != nil {
		return fail(err)
	}
	if err := copyProject(project, filepath.Join(dir, "project")); err != nil {
		return fail(err)
	}
	scorer := &Scorer{Naut: cfg.Naut, TemplateTests: templateTests}
	if rec.Score, err = scorer.ScoreProject(project, task); err != nil {
		return fail(err)
	}
	writeJSON(recPath, rec)
	cfg.Log("%-14s %-7s #%d  %s  (%d turns, $%.2f, %.0fs)%s", task.ID, cond, trial, verdictWord(rec),
		rec.Run.Turns, rec.Run.CostUSD, rec.Run.DurationSec, reasonsTail(rec))
	return rec
}

func verdictWord(r *Record) string {
	switch {
	case r.Error != "":
		return "ERROR"
	case r.Score == nil:
		return "?"
	case r.Score.Verdict.Good:
		return "GOOD"
	case r.Score.Verdict.Correct:
		return "correct"
	}
	return "fail"
}

func reasonsTail(r *Record) string {
	if r.Score == nil || len(r.Score.Verdict.Reasons) == 0 {
		return ""
	}
	rs := r.Score.Verdict.Reasons
	if len(rs) > 3 {
		rs = append(rs[:3:3], fmt.Sprintf("+%d more", len(rs)-3))
	}
	return "  — " + strings.Join(rs, "; ")
}

// scaffold makes a fresh `naut new --template minimal` project under parent
// and sets up the condition's guidance files. It returns the project
// directory and the test files the template wrote.
func scaffold(cfg RunConfig, parent, cond string) (string, map[string]bool, error) {
	cmd := exec.Command(cfg.Naut, "new", "--template", "minimal", "--no-input", "plant")
	cmd.Dir = parent
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("naut new: %v\n%s", err, out)
	}
	project := filepath.Join(parent, "plant")
	// Whatever the template ships (it gains AGENTS.md when #556 merges),
	// the condition decides.
	for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.Remove(filepath.Join(project, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
	}
	if cond == condWith {
		if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), cfg.AgentsMd, 0o644); err != nil {
			return "", nil, err
		}
		if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), cfg.ClaudeMd, 0o644); err != nil {
			return "", nil, err
		}
	}
	tt := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(project, "*_test.yaml"))
	for _, f := range files {
		tt[filepath.Base(f)] = true
	}
	return project, tt, nil
}

func writeJSON(path string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(raw, '\n'), 0o644)
}

// LoadRecords reads every record.json under a run's results directory.
func LoadRecords(out string) ([]*Record, error) {
	var recs []*Record
	err := filepath.WalkDir(filepath.Join(out, "runs"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "record.json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		recs = append(recs, &r)
		return nil
	})
	return recs, err
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
