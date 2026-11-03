#!/usr/bin/env bash
# F-13: after `naut new p --template minimal`, `naut build` (output ./p) and a `naut run` with
# `retain: {}` (retain.json), `git status` should not offer the 28 MB binary or the retain file for
# commit. Passes when neither shows as untracked.
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"
naut new p --template minimal </dev/null >/dev/null 2>&1; cd p
naut build >/dev/null 2>&1; : > retain.json
st=$(git status --porcelain --untracked-files=all); echo "$st"
fail=0
echo "$st" | grep -qx '?? p' && { echo "FAIL build output ./p is not ignored"; fail=1; }
echo "$st" | grep -qx '?? retain.json' && { echo "FAIL retain.json is not ignored"; fail=1; }
exit $fail
