#!/usr/bin/env bash
# Table test for wasm-budget.sh's check; run from the repo root (CI's changes job).
set -u
here=$(dirname "$0")
fail=0
t() { # want-exit SIZE LIMIT [must-contain] [NAME]
  out=$("$here/wasm-budget.sh" check "$2" "$3" "${5:-}" 2>&1); rc=$?
  [ $rc = "$1" ] || { echo "FAIL size=$2 limit=$3: exit $rc, want $1"; fail=1; }
  [ -z "${4:-}" ] || case $out in *"$4"*) ;; *) echo "FAIL size=$2 limit=$3: output lacks '$4': $out"; fail=1 ;; esac
}
t 0 9239028 9300000 "9239028 bytes"
t 0 9300000 9300000                       # at the limit passes
t 1 9300001 9300000 "grew past its budget" # one byte over fails
t 1 39711899 9300000 "go list -deps"       # the leak that motivated this
t 0 7000000 9300000 "lower naut-wasm in .github/wasm-budget.txt to 7070000" # lots of headroom: say so, with size + 1%
t 1 9300001 9300000 "plus 1% (9393001)" # a failure names the limit to set, size + 1%
t 0 9239028 9300000 && "$here/wasm-budget.sh" check 9239028 9300000 2>&1 | grep -q 'notice' && { echo "FAIL a 0.7% margin should not nag"; fail=1; }
t 1 3900001 3900000 "cmd/naut-wasm-hmi grew past its budget" naut-wasm-hmi # the second module has its own budget
t 0 3825166 3885000 "naut-wasm-hmi (stripped" naut-wasm-hmi
for n in naut-wasm naut-wasm-hmi; do
  grep -vE '^[[:space:]]*(#|$)' "$here/../wasm-budget.txt" | grep -qE "^$n [0-9]+$" || { echo "FAIL wasm-budget.txt has no number for $n"; fail=1; }
done
[ $fail = 0 ] && echo "wasm-budget: all cases pass"
exit $fail
