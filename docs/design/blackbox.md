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

## What the development agent does with a run

1. **Triage.** Read `REPORT.md` and `FINDINGS.md`. For each finding: fix it
   on the PR under review when the finding is about that PR and the fix is
   small, else file an issue under `blackbox:<kind>` (`bug`, `papercut`,
   `docs`, `gap`), else propose `disputed` with a reason. Write the status
   back beside the finding: `filed #N`, `fixed-unverified <commit>`,
   `disputed <reason>`. Never `verified`; that word belongs to the tester's
   retest. Never close a finding as invalid on your own: `disputed` is a
   proposal the operator rules on.
2. **Import.** Copy every check whose proposed disposition is `gate` or
   `release` into `tools/blackbox/` verbatim (below). A rewritten check is
   no longer the foreign client's check; if one cannot run as written, it
   is not imported, and the manifest says why.
3. **Stage the retest.** Build `naut` from the fix commit, hand it to the
   blackbox repo with `bin/fetch.sh --local <label> <binary> <docs-dir>
   "<origin note naming the commit>"`, and tell the operator the brief and
   the label. The tester's retest upgrades `fixed-unverified` to
   `verified` or reopens.
4. **Reconcile.** Bring to the operator: disputes, any disposition changed
   on import, any imported check later retired from CI. Record the ruling
   in the run folder.

A PR carrying black-box fixes is ready to merge when its findings are
`fixed-unverified`, the checks for them are imported and green, and a
retest has made them `verified` (or the operator has said why not this
time).

## tools/blackbox

```
tools/blackbox/
  README.md          how to run the checks locally; the contract a check meets
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
`python -I <script>` with `SUT_HTTP`, `SUT_OPCUA`, `SUT_MODBUS`, `SUT_MQTT`
and `SUT_PROJECT` in the environment, taken from the project's
`nautilus.yaml`. Exit 0 passes. The check's stdout is the evidence.

Dispositions map to jobs:

| disposition | job | SUT |
|---|---|---|
| `gate` | ci.yml `blackbox-gate`, every PR | built from the PR |
| `release` | blackbox-release.yml, on a published CLI release | the release tarball, checksums verified |
| `soak`, `manual`, `once` | none; listed in the manifest with `note` | — |

Changing a disposition on import (a `gate` check that needs a second of
wall clock, a `release` check that turned out deterministic) is allowed
and recorded in the manifest entry's `note`, and goes to reconciliation.

## The loop, unattended

`tools/blackbox/loop.sh <brief> [max rounds]`, run from the worktree whose
HEAD is under test, alternates the two agents until the tester has nothing
new to say. A round builds `naut` from HEAD, hands it to the blackbox repo
as `<branch>-<sha>` with the docs named in `DOCS`, brings the container up,
starts a tester session in the blackbox directory ("Run brief NNN", then
"Retest run <parent> against <label>"), reads the run folder it leaves, and,
if there are new findings or reopens, starts a development session here
with the triage-import-write-back brief above. A round with nothing new is
CLEAN and the loop stops. A development session that lands no commit means
everything left is filed or disputed, and the loop stops for the operator.
The default cap is three rounds.

What makes this safe to leave alone: the script is the only thing that
knows both paths, and each agent is started in its own directory with its
own hooks, so the tester's wall holds; the prompts that cross it carry a
brief id, a run id and a label; neither agent merges; `disputed` and
`wontfix` are still words only the operator confirms. What it does not
replace: writing the brief, ruling at reconciliation, and the merge.

The first two rounds of brief 001 were run by hand (2026-10-22 and
2026-10-24) and are what the script encodes. It belongs on the self-hosted
runner next, keyed to a PR label, once it has run unattended on a box with
a person watching.

## What this does not do

It does not run the tester. A standing-brief run on every release is the
tester's job, started by the operator today; a tag-triggered job on the
self-hosted runner (`tools/rig/RUNNER.md`) that holds only the release
artifact and the public docs would do it without a checkout, and is the
next step once two or three standing briefs exist. It also does not grade
its own fixes: `verified` is the tester's word.
