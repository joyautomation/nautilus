#!/usr/bin/env bash
# The verdict of CI's one required check, ci-ok (#752): every job in the
# workflow passed, or was skipped because the scope said so.
#
#   ci-ok.sh <workflow.yml>   with the gate job's `toJSON(needs)` on stdin
#
# It reads the workflow itself, so a job added later is judged by the same
# rules without touching this script:
#
#   - `changes` (the scope) must have succeeded, or nothing counts.
#   - success passes; failure, cancelled or anything else fails.
#   - skipped passes only when the job's own `if:` names a scope gate
#     (outputs.full or outputs.extension) and the scope said "false" for it.
#     Gates combine with && (a job behind both is skipped when either says
#     false); a job with any other `if:`, or none, must never be skipped.
#   - a job missing from the gate's needs fails ("add it to ci-ok's needs").
#   - a job marked continue-on-error at job level is informational (the
#     runtime evidence) and is not judged; the gate itself is left out.
#
# Fails closed: a scope output that is missing or not exactly "false" counts
# as true, so a skip it can't vouch for fails.
set -euo pipefail
workflow=$1
self=${CI_OK_JOB:-ci-ok}
needs=$(cat)
jq -e 'type == "object"' >/dev/null <<<"$needs" || { echo "ci-ok: needs is not a JSON object"; exit 1; }

# id <TAB> job-level if <TAB> job-level continue-on-error, for every job.
jobs() {
  local in_jobs=0 id="" cond="" coe=false line
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ $line =~ ^jobs:[[:space:]]*$ ]]; then in_jobs=1; continue; fi
    [ $in_jobs = 1 ] || continue
    if [[ $line =~ ^[^[:space:]#] ]]; then break; fi
    if [[ $line =~ ^\ \ ([A-Za-z0-9_-]+):[[:space:]]*$ ]]; then
      [ -z "$id" ] || printf '%s\t%s\t%s\n' "$id" "$cond" "$coe"
      id=${BASH_REMATCH[1]} cond="" coe=false
    elif [[ $line =~ ^\ \ \ \ if:[[:space:]]*(.*)$ ]]; then
      cond=${BASH_REMATCH[1]}
    elif [[ $line =~ ^\ \ \ \ continue-on-error:[[:space:]]*true ]]; then
      coe=true
    fi
  done <"$workflow"
  [ -z "$id" ] || printf '%s\t%s\t%s\n' "$id" "$cond" "$coe"
}

result() { jq -r --arg j "$1" 'if has($j) then .[$j].result else "missing" end' <<<"$needs"; }
scope() { jq -r --arg o "$1" '.changes.outputs[$o] // ""' <<<"$needs"; }

fail=0
bad() { echo "✗ $1"; fail=1; }

scope_result=$(result changes)
if [ "$scope_result" != success ]; then
  bad "changes: $scope_result (the scope job must succeed; without it no skip can be trusted)"
fi
full=$(scope full)
extension=$(scope extension)
echo "scope: full=${full:-?} extension=${extension:-?}"

n=0
while IFS=$'\t' read -r id cond coe; do
  [ "$id" != "$self" ] && [ "$id" != changes ] || continue # judged above
  n=$((n + 1))
  if [ "$coe" = true ]; then
    echo "· $id: informational (continue-on-error), not judged"
    continue
  fi
  r=$(result "$id")
  case $r in
    success) echo "✓ $id" ;;
    skipped)
      if [[ $cond == *outputs.full* && $full == false ]]; then
        echo "✓ $id: skipped (scope: full=false)"
      elif [[ $cond == *outputs.extension* && $extension == false ]]; then
        echo "✓ $id: skipped (scope: extension=false)"
      else
        bad "$id: skipped, but the scope did not skip it (if: ${cond:-none})"
      fi
      ;;
    missing) bad "$id: not among ci-ok's needs: add it there" ;;
    *) bad "$id: $r" ;;
  esac
done < <(jobs)

[ $n -gt 0 ] || bad "found no jobs in $workflow"
if [ $fail = 0 ]; then echo "ci-ok: every job passed or was skipped by the scope"; fi
exit $fail
