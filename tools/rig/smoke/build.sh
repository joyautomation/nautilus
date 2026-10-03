#!/usr/bin/env bash
# Build the release candidate the rig tests: naut + the VSIX, both from ONE
# nautilus commit.
#
#     tools/rig/smoke/build.sh                        # THIS checkout, as it is
#     NAUTILUS_SRC=~/Development/joyautomation/nautilus-x tools/rig/smoke/build.sh
#     NAUTILUS_REF=origin/main tools/rig/smoke/build.sh   # a ref, in a worktree
#
# Prints three lines run.sh / selftest.sh eval: NAUT=, VSIX=, NAUTILUS_SHA=.
# The artifacts land in tools/rig/out/build/ (SMOKE_BUILD= moves them), with
# build.log beside them.
#
# NAUTILUS_SRC (default: the checkout this script is in) is built in place —
# what the nightly wants, since its checkout IS the commit under test.
# NAUTILUS_REF instead checks that ref out, detached, in a worktree beside
# the repo (SMOKE_WORKTREE, default ../nautilus-smoke) and builds there, so
# a working checkout is never moved.
#
# Why under ~/Development and not the scratchpad or /tmp: vsce packaging
# fails from there. Why `npm ci` in webview-ui and never `vsce package
# --no-dependencies`: the VSIX must carry the runtime deps (esbuild,
# vscode-languageclient) the extension requires at activation.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
RIG_DIR=$(cd "$HERE/.." && pwd)
SRC=${NAUTILUS_SRC:-$(cd "$RIG_DIR/../.." && pwd)}
OUT=${SMOKE_BUILD:-${RIG_OUT:-$RIG_DIR/out}/build}

if [[ -n ${NAUTILUS_REF:-} ]]; then
  WT=${SMOKE_WORKTREE:-$(dirname "$SRC")/nautilus-smoke}
  git -C "$SRC" fetch -q origin
  if [[ -d $WT ]]; then
    git -C "$WT" checkout -q --detach "$NAUTILUS_REF"
  else
    git -C "$SRC" worktree add -q --detach "$WT" "$NAUTILUS_REF"
  fi
  SRC=$WT
fi
SHA=$(git -C "$SRC" rev-parse HEAD)
[[ -z $(git -C "$SRC" status --porcelain --untracked-files=no) ]] || SHA="$SHA+dirty"
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
{
  echo "building $SHA from $SRC"
  ( cd "$SRC" && go build -o "$OUT/naut" ./cmd/naut )
  ( cd "$SRC/hmi" && npm ci && npm run package )
  ( cd "$SRC/tools/vscode-iec" && npm ci && npm ci --prefix webview-ui \
      && npx --yes @vscode/vsce package -o "$OUT/vscode-iec.vsix" )
} >"$OUT/build.log" 2>&1 || { echo "build failed — $OUT/build.log" >&2; tail -20 "$OUT/build.log" >&2; exit 1; }
echo "$SHA" >"$OUT/SHA"
echo "NAUT=$OUT/naut"
echo "VSIX=$OUT/vscode-iec.vsix"
echo "NAUTILUS_SHA=$SHA"
