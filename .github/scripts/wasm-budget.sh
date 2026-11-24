#!/usr/bin/env bash
# The playground's size budgets (cmd/naut-wasm and cmd/naut-wasm-hmi, stripped, gzip -9).
#   wasm-budget.sh                          build both, measure, check against wasm-budget.txt
#   wasm-budget.sh check SIZE LIMIT [NAME]  compare two numbers (the testable part)
# Controller-only code (redundancy, paging, the auth stores, the DB driver)
# leaking into the browser build is invisible in a diff; the number is not.
set -euo pipefail
here=$(dirname "$0")

check() { # SIZE LIMIT [NAME]
  local size=$1 limit=$2 name=${3:-naut-wasm} line
  line=$(awk -v n="$name" -v s="$size" -v l="$limit" 'BEGIN{printf "%s (stripped, gzip -9): %d bytes (%.2f MB) of a %d byte limit (%.2f MB), %+d bytes against it", n, s, s/1e6, l, l/1e6, s-l}')
  echo "$line"
  [ -z "${GITHUB_STEP_SUMMARY:-}" ] || echo "**$line**" >>"$GITHUB_STEP_SUMMARY"
  if [ "$size" -gt "$limit" ]; then
    echo "::error::cmd/$name grew past its budget ($size > $limit). Find what leaked in with: GOOS=js GOARCH=wasm go list -deps ./cmd/$name. Raising .github/wasm-budget.txt needs a reason in the PR; set it to the new size plus 1% ($(( size + size / 100 ))), never tighter, and measured by CI, not locally (a local build ran about 47 KB under CI), so concurrent merges of small legitimate growth do not turn main red."
    return 1
  fi
  # Headroom over 2%: say so, so the next PR does not spend what this one saved.
  # The suggested limit is the size plus 1%: a ratchet step tighter than that
  # turns main red when two PRs, each fine alone, land together.
  if [ $(( (limit - size) * 50 )) -gt "$limit" ]; then
    echo "::notice::the wasm is more than 2% under its limit: lower $name in .github/wasm-budget.txt to $(( size + size / 100 )) (take the size from the CI run, not a local build: they differ by about 0.5%)"
  fi
}

if [ "${1:-}" = check ]; then check "$2" "$3" "${4:-naut-wasm}"; exit; fi

tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT
rc=0
for name in naut-wasm naut-wasm-hmi; do
  limit=$(grep -vE '^[[:space:]]*(#|$)' "$here/../wasm-budget.txt" | awk -v n="$name" '$1 == n {print $2}' | tr -d ',')
  case $limit in ''|*[!0-9]*) echo "::error::.github/wasm-budget.txt has no number for $name"; exit 1 ;; esac
  GOOS=js GOARCH=wasm go build -trimpath -buildvcs=false -ldflags '-s -w' -o "$tmp" "./cmd/$name"
  size=$(gzip -9 -c "$tmp" | wc -c)
  check "$size" "$limit" "$name" || rc=1
done
exit $rc
