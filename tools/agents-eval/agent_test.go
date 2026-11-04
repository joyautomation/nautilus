//go:build unix

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// cannedTranscript is what a short Claude Code session prints with
// --output-format stream-json: a few tool calls, then the result event.
var cannedTranscript = strings.Join([]string{
	`{"type":"system","subtype":"init","model":"fake"}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/p/AGENTS.md"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"text","text":"writing"},{"type":"tool_use","name":"Write","input":{"file_path":"/p/motor.ld"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"naut check; true"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"naut test -v"}}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"naut check"}}]}}`,
	`not json at all`,
	`{"type":"result","subtype":"success","is_error":false,"result":"done","total_cost_usd":0.42,"num_turns":7,` +
		`"permission_denials":[{"tool_name":"Bash","tool_input":{"command":"cat /etc/passwd"}}]}`,
}, "\n") + "\n"

// fakeClaude writes an executable stand-in for the claude CLI that records
// its arguments and environment and replays a canned transcript (or runs
// body instead, when given).
func fakeClaude(t *testing.T, body string) (bin, argsOut, envOut string) {
	t.Helper()
	dir := t.TempDir()
	argsOut = filepath.Join(dir, "args")
	envOut = filepath.Join(dir, "env")
	transcript := filepath.Join(dir, "canned.jsonl")
	if err := os.WriteFile(transcript, []byte(cannedTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		body = `cat "` + transcript + `"`
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsOut + "\"\nenv > \"" + envOut + "\"\n" + body + "\n"
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsOut, envOut
}

func TestClaudeAgentWithFakeCLI(t *testing.T) {
	bin, argsOut, envOut := fakeClaude(t, "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "outer-session")
	t.Setenv("GITHUB_TOKEN", "ghs_should_not_pass")
	t.Setenv("ACTIONS_RUNTIME_TOKEN", "should_not_pass")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-the-cli-needs-this")

	out := t.TempDir()
	nautDir := t.TempDir()
	req := AgentRequest{
		Dir: t.TempDir(), Prompt: "do the task", Model: "haiku", Timeout: 30 * time.Second,
		BudgetUSD: 1.5, NautDir: nautDir, Transcript: filepath.Join(out, "transcript.jsonl"),
	}
	run, err := claudeAgent{Bin: bin}.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// The transcript, parsed.
	if run.CostUSD != 0.42 || run.Turns != 7 || run.Denials != 1 || run.Result != "done" || run.ResultSubtype != "success" {
		t.Errorf("result fields: %+v", run)
	}
	if run.CheckRuns != 2 || run.TestRuns != 1 || !run.ReadAgentsMd {
		t.Errorf("checks %d tests %d readAgentsMd %v; want 2, 1, true", run.CheckRuns, run.TestRuns, run.ReadAgentsMd)
	}
	if want := map[string]int{"Read": 1, "Write": 1, "Bash": 3}; !mapsEqual(run.ToolCalls, want) {
		t.Errorf("tool calls %v, want %v", run.ToolCalls, want)
	}
	if run.TimedOut || run.ExitCode != 0 || run.Error != "" {
		t.Errorf("exit: %+v", run)
	}
	if raw, _ := os.ReadFile(req.Transcript); string(raw) != cannedTranscript {
		t.Error("the transcript file is not the CLI's stdout")
	}

	// The command line: the prompt, the model, the sandbox.
	rawArgs, _ := os.ReadFile(argsOut)
	args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	for _, want := range []string{"-p", "do the task", "--model", "haiku", "--permission-prompts", "none",
		"Read(./**)", "Write(./**)", "Bash(naut:*)", "Bash(" + filepath.Join(nautDir, "naut") + ":*)",
		"--max-budget-usd", "1.50", "--setting-sources", "project,local"} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	for _, a := range args {
		for _, bad := range []string{"echo", "env", "printenv", "cat", "set:", "WebFetch"} {
			if strings.HasPrefix(a, "Bash(") && strings.Contains(a, bad) {
				t.Errorf("allowed tool %q can print the environment or a file", a)
			}
		}
		// head and tail read any path, /proc/<pid>/environ included.
		for _, bad := range []string{"Bash(head", "Bash(tail", "Bash(less", "Bash(more", "Bash(od", "Bash(xxd", "Bash(strings"} {
			if strings.HasPrefix(a, bad) {
				t.Errorf("allowed tool %q reads any file outside the project", a)
			}
		}
	}

	// The environment the CLI starts with.
	rawEnv, _ := os.ReadFile(envOut)
	env := map[string]string{}
	for _, kv := range strings.Split(string(rawEnv), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for _, gone := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "GITHUB_TOKEN", "ACTIONS_RUNTIME_TOKEN"} {
		if _, ok := env[gone]; ok {
			t.Errorf("%s reached the agent CLI", gone)
		}
	}
	if env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"] != "1" {
		t.Error("CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1 not set: the agent's shell would inherit the API key")
	}
	if env["ANTHROPIC_API_KEY"] != "sk-ant-the-cli-needs-this" {
		t.Error("the CLI itself lost its API key")
	}
	if !strings.HasPrefix(env["PATH"], nautDir+string(os.PathListSeparator)) {
		t.Errorf("PATH does not start with the naut dir: %s", env["PATH"])
	}
}

// The time limit kills the agent and whatever it started.
func TestRunProcessTimeout(t *testing.T) {
	bin, _, _ := fakeClaude(t, "sleep 30 &\nwait")
	out := t.TempDir()
	start := time.Now()
	run, err := claudeAgent{Bin: bin}.Run(context.Background(), AgentRequest{
		Dir: t.TempDir(), Prompt: "x", Model: "m", Timeout: 300 * time.Millisecond,
		NautDir: t.TempDir(), Transcript: filepath.Join(out, "t.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !run.TimedOut || run.ExitCode != -1 || !strings.Contains(run.Error, "timed out") {
		t.Errorf("run %+v, want a timeout", run)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %v: the sleeping child kept the run alive", d)
	}
}

// A CLI that fails is recorded, not fatal.
func TestRunProcessExitCode(t *testing.T) {
	bin, _, _ := fakeClaude(t, "echo boom >&2\nexit 3")
	out := t.TempDir()
	run, err := claudeAgent{Bin: bin}.Run(context.Background(), AgentRequest{
		Dir: t.TempDir(), Prompt: "x", Model: "m", Timeout: 10 * time.Second,
		NautDir: t.TempDir(), Transcript: filepath.Join(out, "t.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExitCode != 3 || run.Error != "exit 3" {
		t.Errorf("run %+v, want exit 3", run)
	}
	if raw, _ := os.ReadFile(filepath.Join(out, "t.stderr")); !strings.Contains(string(raw), "boom") {
		t.Error("stderr was not kept beside the transcript")
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Once finished trials have spent StopAfterUSD, no new trial starts.
func TestStopAfterSpend(t *testing.T) {
	tasks := []*Task{{ID: "a"}, {ID: "b"}}
	calls := 0
	cfg := RunConfig{
		Tasks: tasks, Conditions: []string{condWith, condWithout}, K: 3, Parallel: 1,
		StopAfterUSD: 2.5, Out: t.TempDir(), Log: func(string, ...any) {},
		trial: func(_ context.Context, _ RunConfig, task *Task, cond string, trial int) *Record {
			calls++
			return &Record{Task: task.ID, Condition: cond, Trial: trial, Run: &AgentRun{CostUSD: 1}}
		},
	}
	recs, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// $1, $2, $3: the third passes $2.50, and the other nine never start.
	if calls != 3 || len(recs) != 3 {
		t.Errorf("%d trials ran (%d records), want 3", calls, len(recs))
	}
	// Trial-major order: the stop falls across tasks, not inside one.
	for _, r := range recs {
		if r.Trial != 1 {
			t.Errorf("ran %s/%s #%d before finishing trial 1 everywhere", r.Task, r.Condition, r.Trial)
		}
	}

	calls = 0
	cfg.StopAfterUSD = 0
	if _, err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if calls != 12 {
		t.Errorf("no limit: %d trials ran, want 12", calls)
	}
}

func TestRedactDir(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnop"
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("transcript.jsonl", `{"result":"the key is `+key+`"}`+"\n")
	write("project/notes.txt", key+" and again "+key)
	write("project/motor.ld", "PROGRAM Motor\nEND_PROGRAM\n")
	secrets := []secret{{"ANTHROPIC_API_KEY", key}}

	found, err := RedactDir(dir, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"project/notes.txt", "transcript.jsonl"}; !slices.Equal(found, want) {
		t.Errorf("found in %v, want %v", found, want)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "project/notes.txt"))
	if string(raw) != "[REDACTED:ANTHROPIC_API_KEY] and again [REDACTED:ANTHROPIC_API_KEY]" {
		t.Errorf("redacted to %q", raw)
	}
	if again, _ := RedactDir(dir, secrets); len(again) != 0 {
		t.Errorf("a second pass still found %v", again)
	}
	if none, _ := RedactDir(dir, nil); none != nil {
		t.Error("no secrets should scan nothing")
	}

	t.Setenv("ANTHROPIC_API_KEY", key)
	t.Setenv("GH_TOKEN", "short") // too short to scan for safely
	t.Setenv("OPENAI_API_KEY", "")
	got := secretsFromEnv()
	if len(got) != 1 || got[0].name != "ANTHROPIC_API_KEY" {
		t.Errorf("secretsFromEnv = %v", got)
	}
}

// A trial whose files carry a credential comes back redacted, in memory
// and on disk, and flagged.
func TestSealTrial(t *testing.T) {
	const key = "sk-ant-api03-zyxwvutsrqponm"
	dir := t.TempDir()
	rec := &Record{Task: "a", Condition: condWith, Trial: 1, Run: &AgentRun{Result: "echoed " + key}}
	writeJSON(filepath.Join(dir, "record.json"), rec)
	cfg := RunConfig{Secrets: []secret{{"ANTHROPIC_API_KEY", key}}, Log: func(string, ...any) {}}

	got := sealTrial(cfg, dir, rec)
	if !slices.Equal(got.SecretsRedacted, []string{"record.json"}) {
		t.Errorf("SecretsRedacted = %v", got.SecretsRedacted)
	}
	if strings.Contains(got.Run.Result, key) {
		t.Error("the returned record still holds the key")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "record.json"))
	var onDisk Record
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) || len(onDisk.SecretsRedacted) != 1 {
		t.Errorf("record.json on disk: %s", raw)
	}

	clean := &Record{Task: "b", Run: &AgentRun{Result: "fine"}}
	if got := sealTrial(cfg, t.TempDir(), clean); got != clean || got.SecretsRedacted != nil {
		t.Error("a clean trial should come back untouched")
	}
}
