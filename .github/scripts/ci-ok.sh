#!/usr/bin/env bash
# The verdict of CI's one required check, ci-ok (#752): every job in the
# workflow passed, or was skipped because the scope said so.
#
#   ci-ok.sh <workflow.yml>   with the gate job's `toJSON(needs)` on stdin
#
# It reads the workflow with a real YAML parser (yq, on every GitHub-hosted
# Ubuntu runner) and judges in jq, so a job added later is judged by the same
# rules without touching this script:
#
#   - `changes` (the scope) must have succeeded, or nothing counts.
#   - success passes; failure, cancelled or anything else fails.
#   - skipped passes only when the job's `if:` is, or &&-contains, the exact
#     scope gate `needs.changes.outputs.full != 'false'` (or the same for
#     `extension`) and the scope said "false" for it. An `if:` with `||`, any
#     other `if:`, or none, must never be skipped.
#   - a job missing from the gate's needs fails ("add it to ci-ok's needs").
#   - a job with `continue-on-error: true` (the literal, at job level) is
#     informational (the runtime evidence) and is not judged; the gate itself
#     is left out.
#
# Fails closed: a scope output that is missing or not exactly "false" counts
# as true, so a skip it can't vouch for fails.
#
# Inherent limit: on pull_request a PR runs its OWN ci.yml and this script,
# so a PR that edits .github/ could make its own ci-ok green. The mitigation
# is review: every .github/ change is reviewed before it merges (and runs the
# full suite: ci-scope.sh never makes .github/ light).
set -euo pipefail
workflow=$1
self=${CI_OK_JOB:-ci-ok}
command -v yq >/dev/null || { echo "ci-ok: yq not found (it ships on GitHub's Ubuntu runners)"; exit 1; }
needs=$(cat)
jq -e 'type == "object"' >/dev/null 2>&1 <<<"$needs" || { echo "ci-ok: needs is not a JSON object"; exit 1; }
jobs=$(yq -o=json '.jobs' "$workflow") || { echo "ci-ok: $workflow does not parse"; exit 1; }
jq -e 'type == "object" and length > 0' >/dev/null 2>&1 <<<"$jobs" || { echo "ci-ok: found no jobs in $workflow"; exit 1; }

verdict=$(jq -r --argjson needs "$needs" --arg self "$self" '
  def gate($o): "needs.changes.outputs.\($o) != '"'"'false'"'"'";
  # An if: as its &&-terms, whitespace normalised; null when it has ||
  # (a skip under an OR cannot be pinned on the scope).
  def terms:
    tostring
    | gsub("^\\s*\\$\\{\\{\\s*|\\s*\\}\\}\\s*$"; "")
    | if test("\\|\\|") then null
      else [split("&&")[] | gsub("^\\s+|\\s+$"; "") | gsub("\\s+"; " ")] end;
  def has_gate($t; $o): $t != null and any($t[]; . == gate($o));

  ($needs.changes.result // "missing") as $scope
  | ($needs.changes.outputs.full // "") as $full
  | ($needs.changes.outputs.extension // "") as $ext
  | [ if $scope == "success"
      then {ok: true, msg: "✓ changes (scope: full=\($full) extension=\($ext))"}
      else {ok: false, msg: "✗ changes: \($scope) (the scope job must succeed; without it no skip can be trusted)"} end ]
  + [ to_entries[]
      | select(.key != $self and .key != "changes")
      | .key as $id | .value as $job
      | if ($job | type) != "object" then {ok: false, msg: "✗ \($id): not a job mapping"}
        elif $job["continue-on-error"] == true then {ok: true, msg: "· \($id): informational (continue-on-error), not judged"}
        else ($needs[$id].result // "missing") as $r
          | if $r == "success" then {ok: true, msg: "✓ \($id)"}
            elif $r == "skipped" then
              (if $job | has("if") then $job.if | terms else [] end) as $t
              | if has_gate($t; "full") and $full == "false" then {ok: true, msg: "✓ \($id): skipped (scope: full=false)"}
                elif has_gate($t; "extension") and $ext == "false" then {ok: true, msg: "✓ \($id): skipped (scope: extension=false)"}
                else {ok: false, msg: "✗ \($id): skipped, but the scope did not skip it (if: \($job.if // "none"))"} end
            elif $r == "missing" then {ok: false, msg: "✗ \($id): not among ci-ok'"'"'s needs: add it there"}
            else {ok: false, msg: "✗ \($id): \($r)"} end
        end ]
  | (.[].msg), (if all(.ok) then "PASS" else "FAIL" end)
' <<<"$jobs")
printf '%s\n' "$verdict" | sed '$d'
if [ "$(printf '%s\n' "$verdict" | tail -n1)" = PASS ]; then
  echo "ci-ok: every job passed or was skipped by the scope"
else
  echo "ci-ok: failed"
  exit 1
fi
