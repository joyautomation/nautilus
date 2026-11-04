package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Row is one trial, compacted for summary.json (the committed baseline
// keeps these; the full record.json stays in the gitignored run directory).
type Row struct {
	Task         string         `json:"task"`
	Condition    string         `json:"condition"`
	Trial        int            `json:"trial"`
	Good         bool           `json:"good"`
	Correct      bool           `json:"correct"`
	Clean        bool           `json:"clean"`
	Structured   bool           `json:"structured"`
	Compiles     bool           `json:"compiles"`
	HiddenPassed int            `json:"hiddenPassed"`
	HiddenTotal  int            `json:"hiddenTotal"`
	Warnings     map[string]int `json:"warnings,omitempty"`
	Violations   []string       `json:"violations,omitempty"`
	Languages    []string       `json:"languages,omitempty"`
	Missing      []string       `json:"missingLanguages,omitempty"`
	AgentTests   int            `json:"agentTestFiles"`
	Turns        int            `json:"turns"`
	CostUSD      float64        `json:"costUsd"`
	DurationSec  float64        `json:"durationSec"`
	CheckRuns    int            `json:"checkRuns"`
	TestRuns     int            `json:"testRuns"`
	ReadAgentsMd bool           `json:"readAgentsMd"`
	TimedOut     bool           `json:"timedOut,omitempty"`
	Error        string         `json:"error,omitempty"`
	// SecretRedacted: a credential's value turned up in the trial's files
	// and was replaced (see Record.SecretsRedacted).
	SecretRedacted bool `json:"secretRedacted,omitempty"`
}

// Summary is summary.json.
type Summary struct {
	Meta Meta  `json:"meta"`
	Rows []Row `json:"rows"`
}

func toRow(r *Record) Row {
	row := Row{Task: r.Task, Condition: r.Condition, Trial: r.Trial, Error: r.Error, SecretRedacted: len(r.SecretsRedacted) > 0}
	if r.Run != nil {
		row.Turns, row.CostUSD, row.DurationSec = r.Run.Turns, r.Run.CostUSD, r.Run.DurationSec
		row.CheckRuns, row.TestRuns, row.ReadAgentsMd = r.Run.CheckRuns, r.Run.TestRuns, r.Run.ReadAgentsMd
		row.TimedOut = r.Run.TimedOut
	}
	if s := r.Score; s != nil {
		row.Good, row.Correct, row.Clean, row.Structured = s.Verdict.Good, s.Verdict.Correct, s.Verdict.Clean, s.Verdict.Structured
		row.Compiles = s.Compiles
		row.HiddenPassed, row.HiddenTotal = s.Hidden.Passed, s.Hidden.Total
		row.Warnings = s.Check.WarningIDs
		row.Violations = s.Violations
		row.Languages, row.Missing = s.TaskLanguages, s.MissingLanguages
		row.AgentTests = s.AgentTestFiles
	}
	return row
}

// BuildSummary orders the records into a summary.
func BuildSummary(meta Meta, recs []*Record) Summary {
	s := Summary{Meta: meta}
	for _, r := range recs {
		s.Rows = append(s.Rows, toRow(r))
	}
	sort.Slice(s.Rows, func(i, j int) bool {
		a, b := s.Rows[i], s.Rows[j]
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		if a.Condition != b.Condition {
			return a.Condition < b.Condition
		}
		return a.Trial < b.Trial
	})
	return s
}

type cell struct{ n, good, correct, clean, structured, compiles int }

func (c *cell) add(r Row) {
	c.n++
	if r.Good {
		c.good++
	}
	if r.Correct {
		c.correct++
	}
	if r.Clean {
		c.clean++
	}
	if r.Structured {
		c.structured++
	}
	if r.Compiles {
		c.compiles++
	}
}

func frac(a, n int) string {
	if n == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d", a, n)
}

func pct(a, n int) string {
	if n == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(n))
}

// WriteMarkdown renders the summary as the report table.
func WriteMarkdown(w io.Writer, s Summary, taskLangs map[string][]string) {
	m := s.Meta
	fmt.Fprintf(w, "# AGENTS.md eval: %s, %s\n\n", m.Agent, m.Model)
	fmt.Fprintf(w, "Started %s. k = %d per cell. AGENTS.md sha256 `%.12s`. naut at `%.12s`. Limits per run: %.0f s, $%.2f.\n\n",
		m.Started.UTC().Format("2006-01-02 15:04 UTC"), m.K, m.AgentsMdSHA, m.NautCommit, m.TimeoutSec, m.BudgetUSD)
	fmt.Fprintln(w, "**Good** = compiles, the required language is a task program, every hidden acceptance test passes, `naut check` prints no warning, and no structure rule is broken. The other columns split that verdict: correct (compiles + language + hidden tests), clean (no warnings), structured (no structure rule broken).")
	fmt.Fprintln(w)

	conds := s.Meta.Conditions
	if len(conds) == 0 {
		conds = []string{condWith, condWithout}
	}
	cells := map[string]map[string]*cell{}
	var tasks []string
	for _, r := range s.Rows {
		if cells[r.Task] == nil {
			cells[r.Task] = map[string]*cell{}
			tasks = append(tasks, r.Task)
		}
		if cells[r.Task][r.Condition] == nil {
			cells[r.Task][r.Condition] = &cell{}
		}
		cells[r.Task][r.Condition].add(r)
	}

	fmt.Fprintf(w, "## Good, per task\n\n| task | lang |")
	for _, c := range conds {
		fmt.Fprintf(w, " %s: good | %s: correct · clean · structured |", c, c)
	}
	fmt.Fprintf(w, "\n|---|---|%s\n", strings.Repeat("---|---|", len(conds)))
	totals := map[string]*cell{}
	for _, c := range conds {
		totals[c] = &cell{}
	}
	for _, t := range tasks {
		fmt.Fprintf(w, "| %s | %s |", t, strings.Join(taskLangs[t], ", "))
		for _, c := range conds {
			x := cells[t][c]
			if x == nil {
				x = &cell{}
			}
			fmt.Fprintf(w, " **%s** | %s · %s · %s |", frac(x.good, x.n), frac(x.correct, x.n), frac(x.clean, x.n), frac(x.structured, x.n))
		}
		fmt.Fprintln(w)
	}
	for _, r := range s.Rows {
		if t := totals[r.Condition]; t != nil {
			t.add(r)
		}
	}
	fmt.Fprintf(w, "| **all** | |")
	for _, c := range conds {
		x := totals[c]
		fmt.Fprintf(w, " **%s (%s)** | %s · %s · %s |", frac(x.good, x.n), pct(x.good, x.n), pct(x.correct, x.n), pct(x.clean, x.n), pct(x.structured, x.n))
	}
	fmt.Fprint(w, "\n\n")

	// Failure causes.
	fmt.Fprintln(w, "## Why runs were not Good\n\nRuns showing each cause (a run can show several).")
	fmt.Fprintln(w)
	causes := map[string]map[string]int{}
	note := func(cond, cause string) {
		if causes[cause] == nil {
			causes[cause] = map[string]int{}
		}
		causes[cause][cond]++
	}
	for _, r := range s.Rows {
		seen := map[string]bool{}
		mark := func(c string) {
			if !seen[c] {
				seen[c] = true
				note(r.Condition, c)
			}
		}
		if r.SecretRedacted {
			mark("CREDENTIAL FOUND AND REDACTED")
		}
		if r.Error != "" {
			mark("harness error")
			continue
		}
		if r.TimedOut {
			mark("agent timed out")
		}
		if !r.Compiles {
			mark("does not compile")
		}
		for _, l := range r.Missing {
			mark("wrong language: no " + l + " task")
		}
		if r.Compiles && r.HiddenPassed < r.HiddenTotal {
			mark("hidden tests fail")
		}
		if r.Compiles && r.HiddenTotal == 0 {
			mark("hidden tests did not run")
		}
		for id := range r.Warnings {
			mark("warning: " + id)
		}
		for _, v := range r.Violations {
			mark("structure: " + ViolationID(v))
		}
	}
	var keys []string
	for k := range causes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprintf(w, "| cause |")
	for _, c := range conds {
		fmt.Fprintf(w, " %s |", c)
	}
	fmt.Fprintf(w, "\n|---|%s\n", strings.Repeat("---|", len(conds)))
	for _, k := range keys {
		fmt.Fprintf(w, "| %s |", k)
		for _, c := range conds {
			fmt.Fprintf(w, " %d/%d |", causes[k][c], totals[c].n)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)

	// How the agent worked.
	fmt.Fprintln(w, "## How the agent worked\n\n| condition | runs | mean turns | mean cost | mean time | ran `naut check` | ran `naut test` | wrote tests | read AGENTS.md | timed out |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|")
	for _, c := range conds {
		var n, turns, check, test, wrote, read, to int
		var cost, dur float64
		for _, r := range s.Rows {
			if r.Condition != c || r.Error != "" {
				continue
			}
			n++
			turns += r.Turns
			cost += r.CostUSD
			dur += r.DurationSec
			if r.CheckRuns > 0 {
				check++
			}
			if r.TestRuns > 0 {
				test++
			}
			if r.AgentTests > 0 {
				wrote++
			}
			if r.ReadAgentsMd {
				read++
			}
			if r.TimedOut {
				to++
			}
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(w, "| %s | %d | %.1f | $%.2f | %.0f s | %s | %s | %s | %s | %d |\n", c, n,
			float64(turns)/float64(n), cost/float64(n), dur/float64(n),
			pct(check, n), pct(test, n), pct(wrote, n), pct(read, n), to)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "\"read AGENTS.md\" counts an explicit Read of the file; Claude Code also loads it through CLAUDE.md's `@AGENTS.md` import without one.")
	fmt.Fprintln(w)

	// Per-run detail.
	fmt.Fprintln(w, "## Runs\n\n| task | cond | # | verdict | hidden | warnings | structure | turns | cost |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|")
	for _, r := range s.Rows {
		v := "fail"
		switch {
		case r.Error != "":
			v = "harness error"
		case r.Good:
			v = "**good**"
		case r.Correct:
			v = "correct"
		case !r.Compiles:
			v = "no compile"
		}
		var warns []string
		for id, n := range r.Warnings {
			warns = append(warns, fmt.Sprintf("%s×%d", id, n))
		}
		sort.Strings(warns)
		var viol []string
		for _, x := range r.Violations {
			viol = append(viol, ViolationID(x))
		}
		fmt.Fprintf(w, "| %s | %s | %d | %s | %s | %s | %s | %d | $%.2f |\n", r.Task, r.Condition, r.Trial, v,
			frac(r.HiddenPassed, r.HiddenTotal), strings.Join(warns, ", "), strings.Join(dedupe(viol), ", "), r.Turns, r.CostUSD)
	}
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
