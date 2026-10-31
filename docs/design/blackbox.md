# Design: the black-box loop, seen from this side

**Context:** testing. A sibling repository, `nautilus-blackbox`, holds an
independent tester: an agent that gets the released `naut`, the published
VSIX, the public website, the specs and third-party clients, and never the
source, the authors' tests or the authors' memory. Its product is a run
folder: a report, a findings log, evidence, and a set of runnable checks.
This page is what the nautilus side does with one. The whole loop, with
the tester's side of it, is that repository's `PROCESS.md`; the two must
agree, and this page is the shorter one.

## Why a wall

The dogfood builds, the smoke rig and the claims inventory all catch
regressions. None of them can catch what the authors never thought to
promise, because the authors wrote the tests. The first run (2026-10-22,
the OPC UA server on PR #416) found nineteen things in an afternoon, four
of which made the guide's own example unusable from a real client, and
every one of them sat beside a green test suite. The value is exactly the
context the tester does not have, so this side must never hand it any.

Across the wall, from here: issue numbers and status lines written beside
each finding in the run folder, a built binary with its provenance, the
public docs, and a retest request naming a brief and a commit. Never:
source, Go tests, design notes, CI configuration.

## The loop

Four steps. The tester thinks in the first one only; after that the loop
is the development side against the tester's own checks, until they pass.

1. **Brief run.** A person starts it and watches. From the worktree under
   test, `tools/blackbox/handoff.sh <docs…>` builds `naut`, stages it in
   the blackbox repo as `<branch>-<sha>` with the guide a PR adds, and
   prints the label; in the blackbox directory, `bin/brief.sh <brief>
   <label>` brings the container up and starts the tester on the brief,
   streaming. Hours, by design: the tester reads the docs and the specs,
   probes, decides what is a finding, and writes a check for everything it
   did, pass or fail, with a proposed disposition. Its judgment is spent
   here and nowhere else.
2. **Development.** The development agent reads the run folder, fixes on
   the branch what is the branch's and small, files the rest under
   `blackbox:<kind>` (`bug`, `papercut`, `docs`, `gap`), proposes
   `disputed` with a reason when a finding looks wrong, imports every
   `gate` and `release` check verbatim into `tools/blackbox/`, and cites
   each check from the claims it proves. Status lines go back beside each
   finding: `filed #N`, `fixed-unverified <commit>`, `disputed <reason>`.
   Never close a finding as invalid alone: `disputed` is a proposal the
   operator rules on.
3. **Retest.** The gate: `go test ./tools/blackbox` locally and the
   `blackbox-gate` job on the PR. The tester's checks are its verdict, so
   a check that passes on a commit is the fix verified, and the development
   agent writes `verified <commit>` beside the finding when the gate is
   green there. No tester session: running the tester again only to rerun
   its own scripts spends hours on what the runner does in a minute.
4. **Repeat 2 and 3** until every check for a finding that is not filed or
   disputed passes. A check for an open finding carries `open: #N` in the
   manifest, is skipped while it fails, and fails the job the day it
   passes, which is the signal to close the issue.

A PR carrying black-box fixes is ready to merge when every finding it
claims is `verified <commit>` with the gate green on that commit, and the
rest are `filed` or ruled at reconciliation.

**Reconciliation** is the operator's, after step 2: every `disputed`
finding, every disposition changed on import (recorded in the manifest
entry's `note`), every imported check later retired from CI, and the brief
itself (retire, promote to standing, amend). A check the development side
believes is wrong is the one case where the tester is consulted again,
with the evidence from both sides, and the operator rules.

**Another pass** of step 1 is a new run of the brief against the merged
build, when the coverage below says there is more ground. The tester
carries its prior checks forward by id rather than inventing them again,
so the suite grows instead of churning.

The first loop on brief 001 (2026-10-22 to 2026-10-24) was run with a
script that alternated the two agents unattended, three rounds, tester
retests included. It found the ground above: the retests were slow, blind
and largely rerunning scripts the runner already had, while the
development rounds pushed unreviewed commits. The script is gone; the
hand-off and the gate are what it reduced to.

## Coverage

The tester cannot measure its own coverage and must not try, since any
measure of it lives on this side of the wall. Two measures this side keeps:

- **Lines.** The gate job builds `naut` coverage-instrumented
  (`NAUTILUS_BLACKBOX_COVERDIR`), and each project's `naut` writes its
  counters on exit. The job prints the per-package percentages and keeps
  the profile in the `evidence-blackbox` artifact. It says where a foreign
  client has never been, which is what the next brief points at.
- **Claims.** `go run ./tools/evidence blackbox` counts, per claims file,
  the claims that cite a black-box check and names the ones that do not.
  That is the requirements matrix of black-box testing: did the tester
  look at what we promised, as opposed to what the code does.

**Fails when it should.** Every check written for a finding was born
failing on a real build, and the manifest records that (`expected_today`).
The born-green checks, the ones that confirm a promise held, are the
uncertain ones. The honest test is mutation: break the server deliberately
and see which checks notice. With a container per mutant that is a
scheduled job on one package, not a per-PR one, and worth running only
once the line coverage says the suite reaches the code it claims to.
Until then, a check that passes against every artifact ever staged,
including the pre-fix ones under `artifacts/` in the blackbox repo, is the
one to be suspicious of.

## tools/blackbox

```
tools/blackbox/
  README.md          how to run the checks locally; the contract a check meets
  handoff.sh         step 1: build naut from HEAD and stage it for the tester
  checks.yaml        the manifest: one entry per imported check
  requirements.txt   the oracle clients, pinned to the versions the runs recorded
  checks/<run>/…     the scripts, byte for byte as the tester wrote them
  projects/<run>/…   the project files the checks run against, likewise
  blackbox_test.go   the runner: a Go test, one subtest per check
```

The runner is a Go test so that its results flow into the runtime
evidence like every other job's: `go test -json ./tools/blackbox` emits
one pass/fail per check, named by the check id, and a claim in
`docs/claims/` may cite `go ./tools/blackbox TestBlackbox/<check id>`.
That citation is the strongest evidence a claim can carry, because the
client that wrote the check never saw the code.

For each project named in the manifest the runner copies the project to a
temp dir, starts `naut run` there (the binary from `NAUTILUS_BLACKBOX_NAUT`,
else one it builds), waits for the HTTP API, and runs each check as
`python -I <script>` (or `bash`) with `SUT_HTTP`, `SUT_OPCUA`,
`SUT_MODBUS`, `SUT_MQTT` and `SUT_PROJECT` in the environment, taken from
the manifest's `projects:` map or the project's `nautilus.yaml`. Exit 0
passes. The check's stdout is the evidence. Checks for open findings run
last within a project, since one may leave the SUT damaged.

Dispositions map to jobs:

| disposition | job | SUT |
|---|---|---|
| `gate` | ci.yml `blackbox-gate`, every PR | built from the PR, coverage-instrumented |
| `release` | blackbox-release.yml, after each CLI release; nightly on main | the release tarball, checksums verified; nightly, built from main |
| `soak`, `manual`, `once` | none; listed in the manifest with `note` | — |

A claim citing a check outside the gate takes its verdict from the most
recent blackbox-release run (the runtime-evidence job downloads that run's
artifact): a failure there makes the claim failing, and before any run the
reference reads `release-only`, which is listed but is not a claim problem.
A cited id that `checks.yaml` does not have is one.

Changing a disposition on import (a `gate` check that needs a second of
wall clock, a `release` check that turned out deterministic) is allowed
and recorded in the manifest entry's `note`, and goes to reconciliation.

## What this does not do

It does not start the tester by itself. A standing-brief run on every
release is a person's decision today; a tag-triggered job on the
self-hosted runner (`tools/rig/RUNNER.md`) that holds only the release
artifact and the public docs would do it without a checkout, and is the
next step once two or three standing briefs exist. It does not let an
agent merge, rule a dispute, or retire a check.
