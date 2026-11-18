#!/usr/bin/env bash
# Fails, never skips, unless a Chrome the playground's browser tests can
# launch is on PATH. Those tests launch it through the gesture harness's
# cdp.mjs, so the names tried are read from its CHROME_CANDIDATES (the two
# cannot drift); an unreadable list fails too.
#
#   require-chrome.sh [path to cdp.mjs]
set -u
cdp=${1:-tools/vscode-iec/webview-ui/gesture-harness/cdp.mjs}
list=$(sed -n "s/^const CHROME_CANDIDATES = \[\(.*\)\];\$/\1/p" "$cdp" 2>/dev/null | tr -d "' " | tr ',' ' ')
if [ -z "$list" ]; then
  echo "::error::require-chrome: no CHROME_CANDIDATES list in $cdp"
  exit 1
fi
for c in $list; do
  if p=$(command -v "$c"); then
    echo "chrome: $p ($("$p" --version 2>/dev/null || echo 'no --version'))"
    exit 0
  fi
done
echo "::error::no Chrome/Chromium on PATH (tried: $list): the playground browser tests need one"
exit 1
