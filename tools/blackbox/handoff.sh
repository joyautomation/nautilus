#!/usr/bin/env bash
# Hand this checkout's HEAD to the black-box tester. Operator side: this
# script knows both repos; the tester never learns where the binary came
# from beyond the label and the origin note.
#
#   tools/blackbox/handoff.sh [docs...]
#       run from the nautilus worktree whose HEAD is under test. Builds naut,
#       stages it in the blackbox repo (BB, default ../nautilus-blackbox) as
#       artifact <branch>-<sha> with the docs given (files or directories: the
#       guide a PR adds, as the website would publish it), and prints the
#       label. The run itself starts on the tester's side:
#
#           cd $BB && bin/brief.sh 001 <label>
#
# This is step 1 of the loop in docs/design/blackbox.md. Steps 2 to 4 (fix,
# retest with the gate, repeat) never need the tester again.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
DEV=$(cd "$HERE/../.." && pwd)
BB=${BB:-$(cd "$DEV/.." && pwd)/nautilus-blackbox}
[[ -x $BB/bin/fetch.sh ]] || { echo "no blackbox repo at $BB (set BB)"; exit 1; }
branch=$(git -C "$DEV" rev-parse --abbrev-ref HEAD)
sha=$(git -C "$DEV" rev-parse --short HEAD)
label="$branch-$sha"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
(cd "$DEV" && go build -o "$tmp/naut" ./cmd/naut)
docs=()
if (( $# )); then mkdir -p "$tmp/docs"; cp -r "$@" "$tmp/docs/"; docs=("$tmp/docs"); fi
dirty=""; git -C "$DEV" diff --quiet HEAD || dirty=" with uncommitted changes"
"$BB/bin/fetch.sh" --local "$label" "$tmp/naut" "${docs[@]}" "operator-built from branch $branch head $sha$dirty, $(date +%F)"
echo "$label"
