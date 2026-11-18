#!/usr/bin/env bash
# Table test for require-chrome.sh; run from the repo root.
set -u
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail=0
# A PATH holding only what the script needs, so the machine's own Chrome
# (GitHub's runners have one) never answers for a case.
mkdir -p "$tmp/tools" "$tmp/chrome"
for t in sed tr; do ln -s "$(command -v "$t")" "$tmp/tools/$t"; done
printf '#!/bin/sh\necho "Chromium 1.2.3"\n' >"$tmp/chrome/chromium-browser"
chmod +x "$tmp/chrome/chromium-browser"
printf 'const OTHER = 1;\n' >"$tmp/nolist.mjs"
real=tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs
check() { # check <want exit> <want output regexp> <PATH> [cdp.mjs]
  local want=$1 re=$2 path=$3; shift 3
  local out code
  out=$(PATH=$path /bin/bash "$here/require-chrome.sh" "$@" 2>&1); code=$?
  if [ "$code" != "$want" ] || ! grep -qE "$re" <<<"$out"; then
    printf 'FAIL PATH=%s %s\n  want exit %s matching /%s/\n  got exit %s: %s\n' "$path" "$*" "$want" "$re" "$code" "$out"
    fail=1
  fi
}
# Chrome present (the last name on the harness's list): passes, says which.
check 0 "^chrome: $tmp/chrome/chromium-browser \(Chromium 1.2.3\)$" "$tmp/tools:$tmp/chrome" "$real"
# No Chrome: fails with an error naming every name tried.
check 1 '^::error::no Chrome/Chromium on PATH \(tried: google-chrome-stable google-chrome chromium chromium-browser\)' "$tmp/tools" "$real"
# A cdp.mjs without the list (renamed, moved): fails, never passes.
check 1 '^::error::require-chrome: no CHROME_CANDIDATES list' "$tmp/tools:$tmp/chrome" "$tmp/nolist.mjs"
check 1 '^::error::require-chrome: no CHROME_CANDIDATES list' "$tmp/tools:$tmp/chrome" "$tmp/missing.mjs"
[ "$fail" = 0 ] && echo "require-chrome: all cases pass"
exit $fail
