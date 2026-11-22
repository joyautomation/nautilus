#!/usr/bin/env bash
# Table test for wasm-budget.sh's check; run from the repo root (CI's changes job).
set -u
here=$(dirname "$0")
fail=0
t() { # want-exit SIZE LIMIT [must-contain]
  out=$("$here/wasm-budget.sh" check "$2" "$3" 2>&1); rc=$?
  [ $rc = "$1" ] || { echo "FAIL size=$2 limit=$3: exit $rc, want $1"; fail=1; }
  [ -z "${4:-}" ] || case $out in *"$4"*) ;; *) echo "FAIL size=$2 limit=$3: output lacks '$4': $out"; fail=1 ;; esac
}
t 0 9239028 9300000 "9239028 bytes"
t 0 9300000 9300000                       # at the limit passes
t 1 9300001 9300000 "grew past its budget" # one byte over fails
t 1 39711899 9300000 "go list -deps"       # the leak that motivated this
t 0 7000000 9300000 "lower .github/wasm-budget.txt" # lots of headroom: say so
t 0 9239028 9300000 && "$here/wasm-budget.sh" check 9239028 9300000 2>&1 | grep -q 'notice' && { echo "FAIL a 0.7% margin should not nag"; fail=1; }
grep -qE '^[0-9]+$' <(grep -vE '^[[:space:]]*(#|$)' "$here/../wasm-budget.txt") || { echo "FAIL wasm-budget.txt has no number"; fail=1; }
[ $fail = 0 ] && echo "wasm-budget: all cases pass"
exit $fail
