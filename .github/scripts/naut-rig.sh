#!/usr/bin/env bash
# The cmd/naut tests that run in their own CI job (naut-rig-tests.txt).
#
#   naut-rig.sh regex       the names as one anchored regex, ^(A|B|C)$, for
#                           `go test -run` (the rig job) and `-skip` (the main
#                           Go job): the same text on both sides, so every
#                           cmd/naut test runs in exactly one of them
#   naut-rig.sh names       the names, one per line
#   naut-rig.sh problems    what is wrong with the list, one line each (none:
#                           the list is sound). Reads the sources only, so it
#                           needs no Go toolchain: a name that is not a test in
#                           cmd/naut, or that another package also has (the
#                           main job's -skip would skip that one too and
#                           nothing would run it)
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
list=${NAUT_RIG_LIST:-$here/naut-rig-tests.txt}
names() { grep -E '^Test[A-Za-z0-9_]+$' "$list"; }
case ${1:-} in
regex)
  n=$(names | paste -sd'|')
  [ -n "$n" ] || { echo "naut-rig: the list is empty" >&2; exit 1; }
  echo "^($n)\$" ;;
names) names ;;
problems)
  cd "$repo"
  names | sort | uniq -d | sed 's/^/listed twice: /'
  while IFS= read -r n; do
    grep -qE "^func $n\(" cmd/naut/*_test.go 2>/dev/null || echo "not a test in cmd/naut: $n"
    other=$(grep -rlE "^func $n\(" --include='*_test.go' . 2>/dev/null | grep -v '^\./cmd/naut/' || true)
    [ -z "$other" ] || echo "also a test elsewhere ($(echo $other | tr '\n' ' ')): $n"
  done < <(names) ;;
*) echo "usage: naut-rig.sh regex|names|problems" >&2; exit 2 ;;
esac
