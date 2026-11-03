#!/usr/bin/env bash
# pass: the getting-started step 2 path — `naut new <name>` (the tour), then naut check, naut test,
# naut build — each exits 0 and the build emits ./<name>. Runs in a temp dir; needs naut on PATH.
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"
fail=0
naut new my-plant </dev/null >new.log 2>&1 || { echo "FAIL naut new rc=$?"; cat new.log; exit 1; }
cd my-plant
for c in "check" "test" "build"; do
  out=$(naut $c 2>&1); rc=$?
  echo "naut $c rc=$rc: $(echo "$out" | tail -1)"
  [ $rc -eq 0 ] || { echo "FAIL naut $c"; fail=1; }
done
[ -x ./my-plant ] && echo "ok ./my-plant emitted" || { echo "FAIL no ./my-plant"; fail=1; }
exit $fail
