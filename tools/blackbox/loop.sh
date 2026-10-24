#!/usr/bin/env bash
# The brief → run → fix → retest loop, unattended. Operator side: this script
# knows both repos; neither agent it starts knows about the other.
#
#   tools/blackbox/loop.sh <brief id> [max rounds]
#       run from the nautilus worktree whose HEAD is the system under test
#       (a PR branch); the blackbox repo is BB (default ../nautilus-blackbox)
#
# Each round:
#   1. builds naut from HEAD, hands it to the blackbox repo as an artifact
#      labelled <branch>-<sha> with an origin note (bin/fetch.sh --local) and
#      DOCS (a file or directory: the guide a PR adds, as the website would
#      publish it), and brings the container up (bin/up.sh);
#   2. starts the TESTER in the blackbox directory: round 1 "Run brief NNN",
#      later "Retest run <parent> against <label>". Its hooks and deny rules
#      apply because the session starts there; the prompt carries a brief id,
#      a run id and a label, nothing else;
#   3. reads the new run folder: new findings (### F-) and reopens;
#      none of either → the loop is CLEAN and stops;
#   4. starts the DEVELOPMENT AGENT here: triage, fix on this branch or file,
#      import the checks, write statuses back, commit and push, never merge
#      (docs/design/blackbox.md). No new commit → nothing was fixable by an
#      agent (all filed or disputed) → stops for the operator;
#   5. next round retests the new HEAD.
#
# Stops: CLEAN (0), nothing left for an agent to fix (2), max rounds (3),
# a step failed (1). Reconciliation, the brief's status and the merge stay
# the operator's. Logs under tools/blackbox/out/.
#
# Env: BB, DOCS, TESTER_FLAGS (default: --permission-mode bypassPermissions —
# the wall hook still runs), DEV_FLAGS (default: --permission-mode auto),
# KEEP=1 leaves the container up.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DEV=$(cd "$HERE/../.." && pwd)
BB=${BB:-$(cd "$DEV/.." && pwd)/nautilus-blackbox}
brief=${1:?brief id, e.g. 001}; max=${2:-3}
TESTER_FLAGS=${TESTER_FLAGS:---permission-mode bypassPermissions}
DEV_FLAGS=${DEV_FLAGS:---permission-mode auto}
OUT=$HERE/out; mkdir -p "$OUT"
log=$OUT/loop-$(date +%Y%m%d-%H%M%S)-brief$brief.log
say() { printf '%s %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$log"; }
[[ -d $BB/briefs ]] || { echo "no blackbox repo at $BB (set BB)"; exit 1; }
compgen -G "$BB/briefs/$brief-*.md" >/dev/null || { echo "no brief $brief under $BB/briefs"; exit 1; }
branch=$(git -C "$DEV" rev-parse --abbrev-ref HEAD)
parent=""   # the run the next retest is scoped to

newest_run() { ls -d "$BB"/runs/20*/ 2>/dev/null | sort | tail -1 | xargs -r basename; }

for ((round=1; round<=max; round++)); do
  sha=$(git -C "$DEV" rev-parse --short HEAD); label="$branch-$sha"
  say "round $round · SUT $branch @ $sha → artifact $label"
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  (cd "$DEV" && go build -o "$tmp/naut" ./cmd/naut)
  docs=(); if [[ -n ${DOCS:-} ]]; then mkdir -p "$tmp/docs"; cp -r "$DOCS" "$tmp/docs/"; docs=("$tmp/docs"); fi
  note="operator-built from branch $branch head $sha, $(date +%F); round $round of the loop for brief $brief"
  [[ -n $parent ]] && note="$note; the fix commit for run $parent"
  "$BB/bin/fetch.sh" --local "$label" "$tmp/naut" "${docs[@]}" "$note" >>"$log" 2>&1
  "$BB/bin/up.sh" "$label" >>"$log" 2>&1

  before=$(newest_run)
  if [[ -z $parent ]]; then prompt="Run brief $brief."; else prompt="Retest run $parent against $label."; fi
  say "tester: $prompt"
  (cd "$BB" && claude -p "$prompt" $TESTER_FLAGS --output-format text) >>"$log" 2>&1 || { say "tester session failed; see $log"; exit 1; }
  run=$(newest_run)
  [[ -n $run && $run != "$before" ]] || { say "the tester left no new run folder"; exit 1; }
  (cd "$BB" && git add -A runs && git diff --cached --quiet || git commit -q -m "runs/$run: brief $brief, round $round of the loop (SUT $label)") >>"$log" 2>&1
  fnd=$BB/runs/$run/FINDINGS.md
  new=$(grep -c '^### F-' "$fnd" || true); reopen=$(grep -c 'reopens F-' "$fnd" || true)
  say "run $run: $new new finding(s), $reopen reopen(s)"
  if [[ $new -eq 0 && $reopen -eq 0 ]]; then
    say "CLEAN after $round round(s): $run has nothing new against $sha"
    [[ -n ${KEEP:-} ]] || "$BB/bin/down.sh" >>"$log" 2>&1 || true
    exit 0
  fi

  say "development agent: process $run"
  head_before=$sha
  (cd "$DEV" && claude -p "Process the black-box run runs/$run in the blackbox repo at $BB, following docs/design/blackbox.md: triage every finding (fix it on this branch when it is this branch's and the fix is small, else file it under the blackbox:<kind> label, else propose disputed), import the run's checks verbatim into tools/blackbox and run the gate locally, write each finding's status back beside it in the run folder and commit there, commit here with tests green and push this branch. Do not merge anything. Finish with one paragraph: what was fixed, what was filed, what is disputed." $DEV_FLAGS --output-format text) >>"$log" 2>&1 || { say "development session failed; see $log"; exit 1; }
  if [[ $(git -C "$DEV" rev-parse --short HEAD) == "$head_before" ]]; then
    say "no new commit on $branch: nothing an agent could fix; the rest is the operator's (disputes, filed issues)"
    exit 2
  fi
  parent=$run
done
say "max rounds ($max) reached; last run $parent still has findings for the operator"
exit 3
