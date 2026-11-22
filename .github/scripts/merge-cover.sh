#!/usr/bin/env bash
# Merges Go coverage profiles (same mode, from `go test -coverprofile` runs of
# different packages or of one package's disjoint tests) into one:
#
#   merge-cover.sh <out> <profile>...
#
# A block present in several profiles has its counts added; the first profile's
# order is kept. The Go job and the naut rig job each profile part of cmd/naut;
# the runtime evidence wants one cover.out.
set -euo pipefail
out=$1; shift
[ $# -ge 1 ] || { echo "usage: merge-cover.sh <out> <profile>..." >&2; exit 2; }
mode=$(head -n1 "$1")
for f in "$@"; do
  [ "$(head -n1 "$f")" = "$mode" ] || { echo "merge-cover: $f has '$(head -n1 "$f")', not '$mode'" >&2; exit 1; }
done
awk -v mode="$mode" '
  FNR == 1 { next }
  NF == 3 { k = $1 " " $2; if (!(k in c)) { n++; key[n] = k; c[k] = 0 } c[k] += $3 }
  END { print mode; for (i = 1; i <= n; i++) print key[i], c[key[i]] }
' "$@" >"$out.tmp"
mv "$out.tmp" "$out"
