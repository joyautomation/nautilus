# agents-eval: does AGENTS.md make AI-written LD/FBD/SFC good?

`naut new` ships an `AGENTS.md` (#556) that tells coding agents how to
write nautilus ladder, FBD and SFC that compile, behave, and draw as
readable diagrams. This harness measures whether it works (#564): it runs a
headless coding agent on a fixed suite of control tasks, in fresh
projects with and without the file, and scores every result
deterministically.

It spends tokens and the agent is non-deterministic, so it is **not a PR
gate**. Run it when AGENTS.md, `naut check` or the language changes, and
compare pass rates against `baseline/`.

```sh
# all tasks, both conditions, k=3, the default small model (haiku)
go run ./tools/agents-eval run

# the pilot: sonnet, 4 trials at once, stop starting trials past $40
go run ./tools/agents-eval run -model sonnet -parallel 4 -stop-after-usd 40

# one task, one condition, an AGENTS.md from a branch
git show origin/agents-md:cmd/naut/templates/AGENTS.md.tmpl > /tmp/AGENTS.md
go run ./tools/agents-eval run -tasks tank-fill -conditions with -agents-md /tmp/AGENTS.md

# score a project by hand (a reference solution, or your own attempt)
go run ./tools/agents-eval score -task tank-fill tools/agents-eval/tasks/tank-fill/reference

# rebuild summary.md / summary.json from a run directory
go run ./tools/agents-eval report tools/agents-eval/results/<run>

# after changing the scorer (or naut check): re-score a run's saved
# projects, no tokens spent, and rebuild its summary
go run ./tools/agents-eval rescore tools/agents-eval/results/<run>

# before sharing a results directory: replace any credential value in the
# environment wherever it occurs under it; exits 1 if one was found
go run ./tools/agents-eval redact tools/agents-eval/results/<run>
```

Flags: `-tasks`, `-k`, `-model`, `-conditions with,without`, `-parallel`,
`-timeout` (wall clock per trial, default 10m), `-budget` (USD per trial,
passed to `claude --max-budget-usd`), `-stop-after-usd` (no new trial
starts once the finished ones have spent this much; trials already running
finish, so a run can overshoot by up to `-parallel` × `-budget`, $3 at the
defaults), `-agents-md` (default `cmd/naut/templates/AGENTS.md.tmpl`),
`-out` (reuse a directory to resume an interrupted run: finished trials
are skipped), `-naut` (default: build `./cmd/naut` into the run directory,
so the agent and the scorer use the same naut).

The **AGENTS.md eval** workflow (`.github/workflows/agents-eval.yml`) runs
the same thing by hand from the Actions tab (`workflow_dispatch`), with an
`agents_md_ref` input to evaluate an AGENTS.md on another branch. It skips
cleanly until an `ANTHROPIC_API_KEY` repository secret exists. See
[Keeping the API key in](#keeping-the-api-key-in) for what it uploads.

## The task suite

`tasks/<id>/` holds:

| file | what |
| --- | --- |
| `spec.md` | the prompt, verbatim, written the way an instructor hands out an assignment: names the language, the file, the task scan, every tag (name, role, type) and the behaviour |
| `task.yaml` | title, level, and the language(s) the spec asks for |
| `hidden_test.yaml` | acceptance tests in the project's own test format, run in virtual time. The agent never sees them |
| `reference/` | a hand-written solution that follows AGENTS.md; the unit tests require it to score Good |

| task | language | level | what it exercises |
| --- | --- | --- | --- |
| `motor-seal-in` | LD | beginner | three-wire start/stop, NC stop, overload latch with a guarded reset |
| `tank-fill` | LD | beginner | low/high hysteresis, a high-high interlock, an on-delay alarm latch, a horn with silence |
| `traffic-light` | LD | beginner | a timed cycle in ladder (the spec says the class has not done SFC), one-lamp invariants |
| `analog-pid` | FBD | intermediate | 4-20 mA scaling, a PI loop on the `PID` block, an on-delay alarm forcing the output off |
| `conveyor-fbd` | FBD | intermediate | boolean FBD: seal-in with permissives, a jam timer, a latched alarm with a guarded reset |
| `batch-sfc` | SFC | intermediate | fill / mix 30 s / drain, a 60 s fill timeout, abort paths, counting completed batches only |
| `totalizer-st` | ST | beginner | edge counting, a dose, a grand total (the control: ST has no diagram rules) |

The hidden tests check behaviour through tags only, so any correct design
passes: every test states the inputs it relies on (an agent's `init:`
choice for `StopPB` does not decide the verdict), time checks sit at least
half a second from any boundary, and the PI tests assert only direction
and clamps, not tuning. Each test file was checked against its reference
and against a mutant that breaks one requirement (`score_test.go`).

## A run

For each task × condition × trial:

1. `naut new --template minimal --no-input plant` in a fresh temp directory
   (outside any repository, so no parent `CLAUDE.md` is found).
2. Condition **with**: write the AGENTS.md under test and the `CLAUDE.md`
   that imports it (`@AGENTS.md`, as the template ships it). Condition
   **without**: remove both if the template wrote them.
3. Run the agent headless with the spec as the whole prompt. For Claude
   Code:

   ```sh
   claude -p "<spec>" --model <m> --setting-sources project,local \
     --tools Bash,Read,Edit,Write,Glob,Grep \
     --allowedTools 'Read(./**)' 'Edit(./**)' 'Write(./**)' 'Glob(./**)' \
       'Grep(./**)' 'Bash(naut:*)' 'Bash(<bin>/naut:*)' 'Bash(pwd)' \
       'Bash(true)' 'Bash(which:*)' 'Bash(command -v:*)' \
     --disallowedTools 'Bash(naut run:*)' --permission-mode acceptEdits \
     --permission-prompts none --no-session-persistence \
     --output-format stream-json --verbose --max-budget-usd <b>
   # environment: CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1, no CLAUDECODE /
   # CLAUDE_CODE_* / GITHUB_TOKEN / GH_TOKEN / ACTIONS_*, <bin> first on PATH
   ```

   File tools are confined to the project (`./**`; with no one to answer
   a prompt, anything else is refused). The shell runs `naut`, by name or
   by the absolute path `which naut` prints, plus the glue agents put
   around it (`naut check; true`). No `head` or `tail`: they read any path,
   `/proc/<pid>/environ` included, and Read covers project files. Claude Code
   refuses a compound command whole when any part is not allowed, which in
   the first pilot cost a run its `naut check`. There is no web access and
   no subagent. `--setting-sources project,local` keeps the developer's
   own hooks, plugins and permission rules out. Dropping `CLAUDECODE` /
   `CLAUDE_CODE_*` keeps a run started from inside Claude Code from being
   treated as nested. A copy of the run's `naut` goes first on `PATH` from
   a temp directory, so `which naut` does not point into this checkout,
   where the hidden tests and reference solutions live. The process group
   is killed at the time limit.

   The sandbox matters. In the pilot, agents without AGENTS.md went
   looking for nautilus examples: `find / -name '*.ld'`, Glob over the
   home directory and the Go module cache, `cd` into the checkout. All of
   it was refused. On an unsandboxed machine the same search would have
   found `tasks/*/reference/` and the hidden tests.
4. Copy the project (minus `.git`) and the transcript into
   `results/<run>/runs/<task>/<condition>/trial-<n>/`, score it, write
   `record.json`.

### Keeping the API key in

`claude -p` reads its credential from the environment (`ANTHROPIC_API_KEY`)
or from an `apiKeyHelper` command in its settings. There is no flag or
stdin path, and a helper would only move the key into a file, which is no
better. So the key stays an environment variable of the
`claude` process, and these layers keep it from the agent:

1. **Not inherited.** The harness starts `claude` with
   `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB=1`, which strips the Anthropic
   credentials from every tool subprocess. Claude Code needs bubblewrap for
   it (the workflow installs it) and turns it on by itself under GitHub
   Actions. This was checked with a fake `naut` that dumps its environment:
   a canary value in a scrubbed variable reaches it without the setting
   and does not with it. The setting forces Claude Code's permission mode
   back to default, which is why the file-tool rules are spelled out.
2. **Not printable.** No allowed command prints the environment: no
   `echo`, `env`, `printenv`, `set` or `cat`, and naut never prints its
   environment. Under these rules Claude Code also refuses `$VAR`
   expansion and reads of `/proc/*/environ` (probed with the same canary).
3. **Not stored.** Every trial directory (transcript, project copy,
   record) is scanned for the values of `ANTHROPIC_API_KEY`,
   `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`,
   `GITHUB_TOKEN` and `GH_TOKEN` as it is written. A value found is
   replaced with `[REDACTED:<NAME>]`, the trial is flagged, and `run` exits
   non-zero. `agents-eval redact <dir>` runs the same scan over a whole
   directory.
4. **Not uploaded by default.** The workflow has `permissions: contents:
   read`, checks out with `persist-credentials: false`, runs
   `agents-eval redact` over the results (failing the job on a hit), and
   then uploads only `summary.*` and `meta.json`. Transcripts and the
   agents' projects go up only when the `upload_transcripts` input is set:
   artifacts are readable by anyone with read access to the repo, and
   GitHub's log masking does not apply to them.

`agents_md_ref` puts another branch's AGENTS.md in front of the agent as
instructions. Treat it as untrusted prompt input: the layers above are
what stop it from getting the key out, so read the ref's AGENTS.md before
running it.

No web access is a deliberate choice: it measures what the project itself
teaches, which is what AGENTS.md changes. A student's agent with web
access could also find the docs site; that is a different, noisier
experiment (add `WebFetch` to the tool list in `agent.go` to run it).

### Adding an agent

`agent.go` defines the adapter interface (`Name`, `Available`, `Run`).
`codex` is a documented stub: Codex is not installed where this was
written, and an adapter nobody has run would produce numbers nobody has
checked. The comment on `codexAgent` gives the `codex exec` command to wire
up. Codex reads `AGENTS.md` natively, so it needs no `CLAUDE.md`.

## Scoring

All of it is deterministic: the same project always gets the same score.

| part | how |
| --- | --- |
| **compiles** | `naut check` exits 0 with no error diagnostics |
| **language** | every language `task.yaml` names is the language of a program the manifest runs as a task (so "write it in ladder" answered in ST fails) |
| **hidden tests** | the project is copied to a scratch directory, the agent's own `*_test.yaml` removed, `hidden_test.yaml` added, `naut test -json` |
| **warnings** | every `naut check` warning, counted and given an id. `naut check` has no JSON mode, so its gcc-style lines are parsed; a warning that carries its own id (`[rule-id]` suffix or `rule-id:` prefix, as the #561 structure rules may) keeps it, known messages map to fixed ids (`shadowed-tag`, `unbound-tag`, …), anything else becomes a slug of its first words |
| **structure** | metrics computed from the sources with nautilus's own parsers (`lang/ld`, `lang/fbd`, `lang/sfc` `Graph`), checked against AGENTS.md's rules |

Structure rules (ids as they appear in reports):

| id | rule (from AGENTS.md) |
| --- | --- |
| `ld.series>6` | more than 6 elements in series, top level or inside a leg |
| `ld.branch-depth>1` | a branch inside a branch leg |
| `ld.contact-on-every-leg` | the same contact on every leg of a branch: factor it out |
| `ld.multiple-coils` | a BOOL driven by `( X )` on more than one rung, or `( X )` mixed with `( S X )`/`( R X )` (S/R pairs are fine) |
| `ld.uncommented-rung` | a rung with neither a header `(* … *)` comment nor a `//` note right above it |
| `ld.generic-rung-name` | `r1`, `rung3`, or no name |
| `ld.multi-statement-move` | a `{ … }` box with more than one assignment: math belongs in FBD or ST |
| `fbd.logic-outside-network` | no `NETWORK`, or statements before the first one |
| `fbd.untitled-network` | `NETWORK` with no title |
| `fbd.nesting>2` | a chain of more than two unnamed block calls feeding one another (named wires end a chain) |
| `fbd.generic-name` | a wire or instance named `w1`, `t1`, `x2` |
| `fbd.layout-block` | a `(* @layout *)` block (positions are for people to drag) |
| `sfc.state-variable-transition` | a transition that only compares a variable with an integer (`State = 2`) |
| `sfc.action-steers-chart` | an `ACTION` body with `IF` or `CASE` that assigns a variable some transition condition reads: "no IF deciding where the chart goes next". A conditional counter is allowed, as AGENTS.md allows it |
| `sfc.generic-step-name` | `S1`, `Step2` |
| `sfc.unnamed-transition` | a transition with no name |
| `sfc.initial-steps!=1` | not exactly one `INITIAL_STEP` |
| `<lang>.parse-error` | the metrics parser could not read the file |

Informational metrics (recorded per file in `record.json`, not judged):
rung count, mean/max elements per rung, coils per rung, FBD fan-out of one
input chip, SFC associations and actions.

### The verdict

A run is **Good** when it is

- **correct**: compiles, the required language is a task program, and every
  hidden test passes;
- **clean**: `naut check` printed no warning (AGENTS.md says to treat
  warnings as bugs);
- **structured**: no structure rule above is broken.

The report shows Good per task × condition and splits it into the three
parts, so a drop says whether behaviour, lint or shape regressed. Thresholds
live in `metrics.go` as constants next to the AGENTS.md wording they encode.

The scorer is unit-tested without tokens: `metrics_test.go` runs the
metrics over hand-written bad samples (`testdata/bad/`: a ladder knot, a
nested FBD, a CASE-machine SFC) and the references; `verdict_test.go`
covers every way a verdict can fail; `score_test.go` builds `naut`, scores
every reference Good, and scores a mutant per requirement (a missing
seal-in, a wrong timer, a reverse-acting PID, a non-latching jam, a level
counted as an edge, a state-number transition, a check warning, the right
logic in the wrong language) as failing the right part. `go test -short`
skips the build. `agent_test.go` covers the run path with no agent at
all:
- a fake `claude` script replays a canned stream-json transcript, so the
  test checks the transcript parsing, the command line (no allowed tool
  can print the environment) and the environment (scrub on, nesting and
  CI tokens gone, naut first on PATH);
- a sleeping fake checks that the time limit kills the process group;
- a stub trial checks stop-after-spend;
- planted keys check the redaction.

## Results

`results/` is gitignored. A run writes `meta.json` (agent, model, k, the
AGENTS.md sha256, the naut commit, limits), `summary.md`, `summary.json`
(one compact row per trial) and, per trial, `record.json` (the full score
and the session: turns, cost, time, tool calls, `naut check`/`naut test`
invocations, whether it opened AGENTS.md), `transcript.jsonl` and the
finished `project/`.

`baseline/sonnet/` and `baseline/haiku/` hold the committed summaries of
the pilot (2026-11-03, AGENTS.md from #556 at sha256 `7280d6a4b760`), the
numbers to compare a change against:

| model | with AGENTS.md: Good | without: Good | without: correct · clean · structured |
| --- | --- | --- | --- |
| sonnet | 21/21 (100%) | 3/21 (14%) | 48% · 52% · 57% |
| haiku | 21/21 (100%) | 5/21 (24%) | 52% · 67% · 62% |

The suite is at its ceiling with AGENTS.md on both models, so as it
stands it catches a regression (a rule dropped from AGENTS.md, a lint that
starts firing on good code) but cannot rank two good versions of the file.
Harder tasks are the way to make it discriminate: a machine split across
SFC, LD and FBD programs (the "SFC asks, LD decides" rule), repeated
equipment as a `FUNCTION_BLOCK` in `lib/`, mode selection.

To refresh a baseline, copy a run's `summary.md` and `summary.json` over
it in the same PR as the AGENTS.md or lint change it measures.

Pass rates from k=3 are coarse: one trial is 33 points. Treat a cell that
moves by one trial as noise; a condition total over 21 runs that moves by
three or more is a signal. At k=3 and with the "with" condition at 100%,
the baseline catches regressions but cannot rank two good versions of
AGENTS.md. Ranking needs harder tasks (above) or a larger k.
