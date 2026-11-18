#!/usr/bin/env bash
# Table test for example-hmi-tests.sh against fixture HMIs in a temp dir:
# a passing test, a failing one, an HMI with no test script, a project with
# no HMI. CI's examples job runs it before the script itself.
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
mkhmi() { # mkhmi <project> <test script or ""> : a dependency-free npm app
  mkdir -p "$tmp/examples/$1/hmi"
  local scripts='{}'
  [ -n "$2" ] && scripts="{\"test\": \"$2\"}"
  printf '{"name":"%s","version":"0.0.0","private":true,"scripts":%s}\n' "$1" "$scripts" >"$tmp/examples/$1/hmi/package.json"
  printf '{"name":"%s","version":"0.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"%s","version":"0.0.0"}}}\n' "$1" "$1" >"$tmp/examples/$1/hmi/package-lock.json"
}
mkhmi passes "node t.js"
echo "require('fs').writeFileSync('ran', '')" >"$tmp/examples/passes/hmi/t.js"
mkhmi fails "node t.js"
echo "process.exit(1)" >"$tmp/examples/fails/hmi/t.js"
mkhmi notest ""
mkdir -p "$tmp/examples/nohmi"
check() { # check <want exit 0|1> <want "N run, M failed"> <args...>
  local want=$1 summary=$2; shift 2
  local out code
  out=$(cd "$tmp" && EXAMPLES_DIR=examples "$here/example-hmi-tests.sh" "$@" 2>&1)
  code=$?
  if [ $((code != 0)) -ne "$want" ] || ! grep -q "example HMI tests: $summary" <<<"$out"; then
    printf 'FAIL args=(%s): exit %s, want %s nonzero; want "%s" in:\n%s\n' "$*" "$code" "$want" "$summary" "$out"
    fail=1
  fi
}
check 1 "2 run, 1 failed: examples/fails/hmi"            # every HMI: the failing one fails the run
check 0 "1 run, 0 failed" examples/passes                 # a light PR touching the passing one
check 1 "1 run, 1 failed" examples/fails                  # ... or the failing one
check 0 "0 run, 0 failed" examples/notest examples/nohmi  # nothing to run is not a failure
[ -e "$tmp/examples/passes/hmi/ran" ] || { echo "FAIL: the passing fixture's test never ran"; fail=1; }
[ $fail -eq 0 ] && echo "example-hmi-tests.sh: all cases pass"
exit $fail
