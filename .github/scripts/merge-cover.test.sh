#!/usr/bin/env bash
# Table test for merge-cover.sh; run from the repo root (CI's changes job).
set -u
here=$(dirname "$0")
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
printf 'mode: set\nx/a.go:1.1,2.2 1 1\nx/a.go:3.1,4.2 2 0\nx/b.go:5.1,6.2 1 0\n' >"$tmp/a"
printf 'mode: set\nx/a.go:3.1,4.2 2 1\nx/c.go:7.1,8.2 3 1\nx/b.go:5.1,6.2 1 0\n' >"$tmp/b"
printf 'mode: count\nx/a.go:1.1,2.2 1 4\n' >"$tmp/count"
"$here/merge-cover.sh" "$tmp/out" "$tmp/a" "$tmp/b" || { echo "FAIL merge exited non-zero"; fail=1; }
want=$'mode: set\nx/a.go:1.1,2.2 1 1\nx/a.go:3.1,4.2 2 1\nx/b.go:5.1,6.2 1 0\nx/c.go:7.1,8.2 3 1'
[ "$(cat "$tmp/out")" = "$want" ] || { echo "FAIL merged profile:"; cat "$tmp/out"; fail=1; }
"$here/merge-cover.sh" "$tmp/one" "$tmp/a" && [ "$(cat "$tmp/one")" = "$(cat "$tmp/a")" ] || { echo "FAIL one profile is unchanged"; fail=1; }
"$here/merge-cover.sh" "$tmp/bad" "$tmp/a" "$tmp/count" 2>/dev/null && { echo "FAIL mixed modes were merged"; fail=1; }
[ $fail = 0 ] && echo "merge-cover: all cases pass"
exit $fail
