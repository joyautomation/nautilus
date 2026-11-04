package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Agent is a headless coding agent the harness can point at a project.
// An adapter owns how its agent is sandboxed to the project directory and
// how it finds the project guidance (Claude Code reads CLAUDE.md, which
// imports AGENTS.md; Codex reads AGENTS.md itself).
type Agent interface {
	Name() string
	// Available says why the agent cannot run here, or nil.
	Available() error
	Run(ctx context.Context, req AgentRequest) (*AgentRun, error)
}

// AgentRequest is one headless session.
type AgentRequest struct {
	Dir        string        // the project; the agent may only touch this
	Prompt     string        // the task spec, verbatim
	Model      string        // adapter-specific model name or alias
	Timeout    time.Duration // wall-clock limit; the process group is killed after it
	BudgetUSD  float64       // per-run spend limit, where the agent supports one
	NautDir    string        // directory holding the naut binary, put first on PATH
	Transcript string        // file the agent's event stream is written to
}

// AgentRun is what the harness records about the session itself.
type AgentRun struct {
	ExitCode    int     `json:"exitCode"`
	TimedOut    bool    `json:"timedOut,omitempty"`
	DurationSec float64 `json:"durationSec"`
	CostUSD     float64 `json:"costUsd"`
	Turns       int     `json:"turns"`
	// Tool use, from the transcript: how the agent worked, not how it
	// scored. Useful to tell "did not read AGENTS.md" from "read it and
	// ignored it".
	ToolCalls     map[string]int `json:"toolCalls,omitempty"`
	CheckRuns     int            `json:"checkRuns"` // `naut check` invocations
	TestRuns      int            `json:"testRuns"`  // `naut test` invocations
	ReadAgentsMd  bool           `json:"readAgentsMd"`
	Denials       int            `json:"denials"` // tool calls the sandbox refused
	Result        string         `json:"result,omitempty"`
	Error         string         `json:"error,omitempty"`
	BudgetHit     bool           `json:"budgetHit,omitempty"`
	ResultSubtype string         `json:"resultSubtype,omitempty"`
}

// NewAgent returns the adapter for name.
func NewAgent(name string) (Agent, error) {
	switch name {
	case "claude":
		return claudeAgent{}, nil
	case "codex":
		return codexAgent{}, nil
	}
	return nil, fmt.Errorf("unknown agent %q (claude, codex)", name)
}

// ─── Claude Code ────────────────────────────────────────────────────

type claudeAgent struct{}

func (claudeAgent) Name() string { return "claude" }

func (claudeAgent) Available() error {
	_, err := exec.LookPath("claude")
	return err
}

// claudeTools is the whole tool set the agent gets: file tools (confined to
// the working directory by acceptEdits plus --permission-prompts none) and
// Bash for naut only. No web, no subagents: the run measures what the
// project itself teaches.
const claudeTools = "Bash,Read,Edit,Write,Glob,Grep"

func (claudeAgent) Run(ctx context.Context, req AgentRequest) (*AgentRun, error) {
	args := []string{
		"-p", req.Prompt,
		"--model", req.Model,
		// Project settings only: the developer's own hooks, plugins and
		// permission rules stay out of the run.
		"--setting-sources", "project,local",
		"--tools", claudeTools,
		// naut, plus the glue an agent puts around it (`naut check; echo
		// $?`, `naut --help | head`): a compound command is refused whole
		// when any part is not allowed. Nothing here reads files outside
		// the project except head/tail given a path, and nothing tells the
		// agent where such a path would be.
		"--allowedTools", "Bash(naut:*)", "Bash(echo:*)", "Bash(pwd)", "Bash(true)",
		"Bash(which:*)", "Bash(command -v:*)", "Bash(head:*)", "Bash(tail:*)",
		// …and naut by the absolute path `which naut` prints.
		"Bash(" + filepath.Join(req.NautDir, "naut") + ":*)",
		// naut run serves forever; the time limit would kill it, but the
		// agent would learn nothing in the meantime.
		"--disallowedTools", "Bash(naut run:*)",
		"--permission-mode", "acceptEdits",
		"--permission-prompts", "none",
		"--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
	}
	if req.BudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(req.BudgetUSD, 'f', 2, 64))
	}
	run, err := runProcess(ctx, "claude", args, req)
	if err != nil {
		return nil, err
	}
	parseClaudeTranscript(req.Transcript, run)
	return run, nil
}

// parseClaudeTranscript fills run from Claude Code's stream-json events.
func parseClaudeTranscript(path string, run *AgentRun) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	run.ToolCalls = map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 16<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
			Result            string            `json:"result"`
			TotalCostUSD      float64           `json:"total_cost_usd"`
			NumTurns          int               `json:"num_turns"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
			IsError           bool              `json:"is_error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "tool_use" {
					continue
				}
				run.ToolCalls[c.Name]++
				var in struct {
					Command  string `json:"command"`
					FilePath string `json:"file_path"`
				}
				_ = json.Unmarshal(c.Input, &in)
				cmd := strings.TrimSpace(in.Command)
				if strings.Contains(cmd, "naut check") {
					run.CheckRuns++
				}
				if strings.Contains(cmd, "naut test") {
					run.TestRuns++
				}
				if c.Name == "Read" && strings.EqualFold(filepath.Base(in.FilePath), "AGENTS.md") {
					run.ReadAgentsMd = true
				}
			}
		case "result":
			run.CostUSD = ev.TotalCostUSD
			run.Turns = ev.NumTurns
			run.Denials = len(ev.PermissionDenials)
			run.Result = truncate(ev.Result, 2000)
			run.ResultSubtype = ev.Subtype
			if strings.Contains(ev.Subtype, "budget") {
				run.BudgetHit = true
			}
			if ev.IsError && run.Error == "" {
				run.Error = ev.Subtype
			}
		}
	}
	if len(run.ToolCalls) == 0 {
		run.ToolCalls = nil
	}
}

// ─── Codex (stub) ───────────────────────────────────────────────────

// codexAgent documents how Codex would plug in. It is not wired up: Codex
// is not installed where this harness was written, and an adapter that has
// never run would report numbers nobody has checked. To add it, implement
// Run as
//
//	codex exec --model <m> --sandbox workspace-write --skip-git-repo-check \
//	    --json -C <dir> "<spec>"
//
// writing the --json event stream to req.Transcript and parsing turns and
// token usage out of it. Codex reads AGENTS.md natively, so the "with"
// condition needs no CLAUDE.md for it (the harness writes both anyway).
// Restrict its shell to naut the way the Claude adapter does, or run it in
// a container with only naut on PATH.
type codexAgent struct{}

func (codexAgent) Name() string { return "codex" }

func (codexAgent) Available() error {
	if _, err := exec.LookPath("codex"); err != nil {
		return fmt.Errorf("codex is not installed: %w", err)
	}
	return errors.New("the codex adapter is a stub; see agent.go for the command to wire up")
}

func (codexAgent) Run(context.Context, AgentRequest) (*AgentRun, error) {
	return nil, errors.New("the codex adapter is a stub")
}

// ─── process plumbing ───────────────────────────────────────────────

// runProcess runs one agent CLI in req.Dir with a scrubbed environment,
// its stdout to req.Transcript, and kills its whole process group when the
// time limit passes.
func runProcess(ctx context.Context, bin string, args []string, req AgentRequest) (*AgentRun, error) {
	out, err := os.Create(req.Transcript)
	if err != nil {
		return nil, err
	}
	defer out.Close()
	errLog, err := os.Create(strings.TrimSuffix(req.Transcript, filepath.Ext(req.Transcript)) + ".stderr")
	if err != nil {
		return nil, err
	}
	defer errLog.Close()

	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = req.Dir
	cmd.Env = agentEnv(req.NautDir)
	cmd.Stdout = out
	cmd.Stderr = errLog
	cmd.Stdin = nil
	setProcessGroup(cmd)
	start := time.Now()
	err = cmd.Run()
	run := &AgentRun{DurationSec: time.Since(start).Seconds()}
	var ee *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		run.TimedOut = true
		run.ExitCode = -1
		run.Error = "timed out after " + req.Timeout.String()
	case err == nil:
	case errors.As(err, &ee):
		run.ExitCode = ee.ExitCode()
		run.Error = "exit " + strconv.Itoa(ee.ExitCode())
	default:
		return nil, err
	}
	return run, nil
}

// agentEnv is the harness's environment minus anything that would tell the
// agent CLI it is nested inside another agent session, with naut first on
// PATH.
func agentEnv(nautDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "CLAUDECODE", k == "CLAUDE_PID", k == "CLAUDE_EFFORT", k == "AI_AGENT",
			strings.HasPrefix(k, "CLAUDE_CODE_"):
			continue
		case k == "PATH":
			kv = "PATH=" + nautDir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
		env = append(env, kv)
	}
	return env
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
