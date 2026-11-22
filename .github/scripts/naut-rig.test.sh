#!/usr/bin/env bash
# naut-rig-tests.txt is sound (no Go needed): every name is a cmd/naut test, no
# other package has one of the same name, the regex is the whole list. Run from
# the repo root; CI's changes job runs it.
set -u
here=$(dirname "$0")
fail=0
bad=$("$here/naut-rig.sh" problems)
[ -z "$bad" ] || { echo "FAIL the list has problems:"; echo "$bad"; fail=1; }
n=$("$here/naut-rig.sh" names | wc -l)
[ "$n" -ge 20 ] || { echo "FAIL only $n names"; fail=1; }
re=$("$here/naut-rig.sh" regex)
[ "$(printf '%s' "$re" | tr -cd '|' | wc -c)" -eq $((n - 1)) ] || { echo "FAIL the regex does not hold $n names"; fail=1; }
case $re in '^('*')$') ;; *) echo "FAIL the regex is not anchored: $re"; fail=1 ;; esac

# The checker itself: a name that is not a test, one a second package has, a
# duplicate.
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
other=$(grep -rlE '^func TestPairSectionLoads\(' --include='*_test.go' . | head -1)
mine=$(grep -hE '^func TestPair[A-Za-z0-9_]*\(' cmd/naut/*_test.go | head -1 | sed -E 's/^func (Test[A-Za-z0-9_]+).*/\1/')
printf 'TestThereIsNoSuchTest\nTestPairSectionLoads\n%s\n%s\n' "$mine" "$mine" >"$tmp/list"
got=$(NAUT_RIG_LIST="$tmp/list" "$here/naut-rig.sh" problems)
for want in 'not a test in cmd/naut: TestThereIsNoSuchTest' 'also a test elsewhere' "listed twice: $mine"; do
  grep -qF "$want" <<<"$got" || { echo "FAIL the checker missed: $want (got: $got)"; fail=1; }
done
[ -n "$other" ] || { echo "FAIL fixture: TestPairSectionLoads is gone; pick another cross-package name"; fail=1; }
[ $fail = 0 ] && echo "naut-rig: all cases pass"
exit $fail
