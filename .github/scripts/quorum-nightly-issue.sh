#!/usr/bin/env bash
# quorum-nightly.yml's rolling issue (#997): one open issue, "quorum:
# nightly simulator", for the vote protocol's nightly checks. A red run
# opens it (or comments on it if it is open) with each failing step: the
# mutation table for quorum-mutants, the failing seeds and their QUORUM_SEED
# repro lines for the seeds run. A green run closes it.
#
# Environment:
#   MUTANTS_OUTCOME, SEEDS_OUTCOME  the steps' outcomes (success, failure,
#                                   cancelled, skipped); only success is green
#   MUTANTS_LOG, SEEDS_LOG          the steps' output (a missing file is fine)
#   RUN_URL, GITHUB_SHA, GITHUB_REPOSITORY
#   GH                              the gh binary (the table test stubs it)
set -euo pipefail

gh=${GH:-gh}
repo=${GITHUB_REPOSITORY:?}
run=${RUN_URL:?}
sha=${GITHUB_SHA:-unknown}
mutants=${MUTANTS_OUTCOME:-skipped}
seeds=${SEEDS_OUTCOME:-skipped}
title="quorum: nightly simulator"

num=$("$gh" issue list --repo "$repo" --state open --search "\"$title\" in:title" \
  --json number,title --jq "[.[]|select(.title==\"$title\")][0].number // empty")

if [ "$mutants" = success ] && [ "$seeds" = success ]; then
  if [ -n "$num" ]; then
    "$gh" issue close "$num" --repo "$repo" --comment "Green again: $run"
    echo "closed #$num"
  else
    echo "green; no open issue"
  fi
  exit 0
fi

# A step that didn't succeed: what to say for its outcome.
said() {
  case $1 in
  failure) echo "failed" ;;
  cancelled) echo "was cancelled (timed out?)" ;;
  skipped) echo "did not run" ;;
  *) echo "ended $1" ;;
  esac
}

log() { [ -f "$1" ] && cat "$1" || true; }

body=$(mktemp)
trap 'rm -f "$body"' EXIT
{
  echo "Main at **${sha::8}**: the quorum nightly is red: $run"
  if [ "$mutants" != success ]; then
    echo
    echo "### \`go run ./tools/quorum-mutants\` $(said "$mutants")"
    echo
    echo "A mutation whose tests stayed green has survived: the suite has a hole. A build failure counts too."
    echo
    # The legs' tables run together (#1096): one header, then only the
    # rows that were not caught (a caught row and a green baseline say
    # nothing), with a count of the caught.
    rows=$(log "${MUTANTS_LOG:-}" | grep -E '^\| ' | grep -vE '^\| mutation \| result \|$|\| caught \(|\| green \|$' || true)
    caught=$(log "${MUTANTS_LOG:-}" | grep -cE '^\| .* \| caught \(' || true)
    if [ -n "$rows" ]; then
      echo "| mutation | result |"
      echo "|---|---|"
      echo "$rows"
      echo
      echo "The other $caught mutations were caught."
    else
      echo '```'
      log "${MUTANTS_LOG:-}" | tail -n 30
      echo '```'
    fi
  fi
  if [ "$seeds" != success ]; then
    echo
    echo "### \`QUORUM_SEEDS=200000 go test ./quorum/sim -run '^TestSeeds\$'\` $(said "$seeds")"
    echo
    repro=$(log "${SEEDS_LOG:-}" | grep -oE "QUORUM_SEED=[0-9]+ go test \./quorum/sim -run '[^']*' -v" | awk '!seen[$0]++' || true)
    if [ -n "$repro" ]; then
      echo "Reproduce each failing seed alone, with its trace:"
      echo
      echo '```'
      echo "$repro"
      echo '```'
      echo
    fi
    echo '```'
    log "${SEEDS_LOG:-}" | grep -E ': seed [0-9]+: |: seeds [0-9]+–[0-9]+: [0-9]+ failing|failing seeds in all|^(--- FAIL|FAIL|panic:)' | head -n 40 || true
    echo '```'
  fi
} >"$body"

if [ -n "$num" ]; then
  "$gh" issue comment "$num" --repo "$repo" --body-file "$body"
  echo "commented on #$num"
else
  "$gh" issue create --repo "$repo" --title "$title" --body-file "$body"
  echo "opened the issue"
fi
