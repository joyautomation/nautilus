#!/usr/bin/env bash
# The cmd/naut tests that run in their own CI job (naut-rig-tests.txt).
#
#   naut-rig.sh regex       the names as one anchored regex, ^(A|B|C)$, for
#                           `go test -run` (the rig job) and `-skip` (the main
#                           Go job): the same text on both sides, so every
#                           cmd/naut test runs in exactly one of them
#   naut-rig.sh regex I N   only shard I of N (1-based, round-robin by list
#                           order): the rig job runs the shards in parallel,
#                           and every name is in exactly one shard
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
  if [ $# -ge 3 ]; then
    case $2$3 in *[!0-9]*|'') echo "naut-rig: shard needs numbers: regex I N" >&2; exit 2 ;; esac
    [ "$3" -ge 1 ] && [ "$2" -ge 1 ] && [ "$2" -le "$3" ] || { echo "naut-rig: shard $2 of $3 is out of range" >&2; exit 2; }
    n=$(names | awk -v i="$2" -v n="$3" '(NR - 1) % n == i - 1' | paste -sd'|')
  else
    n=$(names | paste -sd'|')
  fi
  [ -n "$n" ] || { echo "naut-rig: the list (or the shard) is empty" >&2; exit 1; }
  echo "^($n)\$" ;;
names) names ;;
problems)
  cd "$repo"
  names | sort | uniq -d | sed 's/^/listed twice: /'
  while IFS= read -r n; do
    grep -qE "^func $n\(" cmd/naut/*_test.go 2>/dev/null || echo "not a test in cmd/naut: $n"
    other=$(grep -rlE "^func $n\(" --include='*_test.go' . 2>/dev/null | grep -vE '^\./cmd/naut/[^/]*_test\.go$' || true)
    [ -z "$other" ] || echo "also a test elsewhere ($(echo $other | tr '\n' ' ')): $n"
  done < <(names) ;;
*) echo "usage: naut-rig.sh regex|names|problems" >&2; exit 2 ;;
esac
