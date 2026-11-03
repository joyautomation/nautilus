#!/usr/bin/env bash
# F-02: in a project `naut new` made (it runs `git init`, so .git exists, with no commits yet),
# `naut build` should not say "(not a repo)". Passes when the message does not claim that.
set -u
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cd "$T"
naut new p --template minimal </dev/null >/dev/null 2>&1; cd p
[ -d .git ] || { echo "SKIP naut new made no .git"; exit 0; }
out=$(naut build 2>&1); echo "$out"
echo "$out" | grep -q "not a repo" && { echo "FAIL says 'not a repo' inside a git repository"; exit 1; }
echo PASS
