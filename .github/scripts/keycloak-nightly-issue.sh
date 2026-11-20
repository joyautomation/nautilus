#!/usr/bin/env bash
# keycloak-nightly.yml's rolling issue: one open issue, "keycloak: nightly
# OIDC". A red run opens it (or comments on it if it is open) with the end of
# the log and the run; a green run closes it.
#
# Environment:
#   RUN_OUTCOME   the run step's outcome (success, failure, cancelled,
#                 skipped); only success is green
#   RUN_LOG       the step's output (a missing file is fine)
#   RUN_URL, GITHUB_SHA, GITHUB_REPOSITORY
#   GH            the gh binary (the table test stubs it)
set -euo pipefail

gh=${GH:-gh}
repo=${GITHUB_REPOSITORY:?}
run=${RUN_URL:?}
sha=${GITHUB_SHA:-unknown}
outcome=${RUN_OUTCOME:-skipped}
title="keycloak: nightly OIDC"

num=$("$gh" issue list --repo "$repo" --state open --search "\"$title\" in:title" \
  --json number,title --jq "[.[]|select(.title==\"$title\")][0].number // empty")

if [ "$outcome" = success ]; then
  if [ -n "$num" ]; then
    "$gh" issue close "$num" --repo "$repo" --comment "Green again: $run"
    echo "closed #$num"
  else
    echo "green; no open issue"
  fi
  exit 0
fi

case $outcome in
failure) said="failed" ;;
cancelled) said="was cancelled (timed out?)" ;;
skipped) said="did not run" ;;
*) said="ended $outcome" ;;
esac

body=$(mktemp)
trap 'rm -f "$body"' EXIT
{
  echo "Main at **${sha::8}**: the Keycloak nightly is red: $run"
  echo
  echo "### \`tools/keycloak/run.sh\` $said"
  echo
  echo "Reproduce: \`tools/keycloak/run.sh -v\` (needs docker; \`KEEP=1\` leaves Keycloak running)."
  if [ -n "${RUN_LOG:-}" ] && [ -f "$RUN_LOG" ]; then
    echo
    echo '```'
    tail -n 60 "$RUN_LOG"
    echo '```'
  fi
} >"$body"

if [ -n "$num" ]; then
  "$gh" issue comment "$num" --repo "$repo" --body-file "$body"
  echo "commented on #$num"
else
  "$gh" issue create --repo "$repo" --title "$title" --body-file "$body"
fi
