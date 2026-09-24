#!/usr/bin/env bash
# Release-channel guard for the VS Code extension (joyauto.vscode-iec).
# See RELEASING.md for the model; in short:
#
#   pre-release  = main's tools/vscode-iec/package.json, ODD minor (0.11.x),
#                  published by publish.yml on every bump.
#   stable       = git tags vscode-vX.Y.Z, EVEN minor (0.10.x), published by
#                  vscode-stable.yml.
#
# Usage:
#   vscode-channel-guard.sh publish-pre    <version>   # publish.yml
#   vscode-channel-guard.sh publish-stable <version>   # vscode-stable.yml
#   vscode-channel-guard.sh sync <main-version|-> [<highest-stable-tag-version>]
#                                                       # ci.yml version-sync
#                                   ("-" skips the pre-release-line checks)
#
# The publish modes write mkt=true|false and ovsx=true|false (does that
# registry still need this version?) to $GITHUB_OUTPUT.
#
# Registry state comes from vscode-registry-versions.mjs. For testing, point
# VSCODE_REGISTRY_FIXTURE at a file of its key=value lines instead.
set -euo pipefail

mode=${1:?mode}
ver=${2:?version}
tagver=${3:-}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

if [ -n "${VSCODE_REGISTRY_FIXTURE:-}" ]; then
  state=$(cat "$VSCODE_REGISTRY_FIXTURE")
else
  state=$(node "$here/vscode-registry-versions.mjs")
fi
# Only the known keys, only version-shaped values: never eval arbitrary text.
while IFS='=' read -r k v; do
  case "$k" in
    mkt_ok|ovsx_ok) [[ "$v" =~ ^(true|false)$ ]] && printf -v "$k" '%s' "$v" ;;
    mkt_pre|mkt_stable|ovsx_pre|ovsx_stable)
      [[ "$v" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && printf -v "$k" '%s' "$v" ;;
  esac
done <<<"$state"
: "${mkt_ok:?} ${ovsx_ok:?} ${mkt_pre:?} ${mkt_stable:?} ${ovsx_pre:?} ${ovsx_stable:?}"

err() { echo "::error::$*"; fail=1; }
fail=0
# gt A B: A is a strictly higher version than B.
gt() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; }
maxv() { printf '%s\n' "$@" | sort -V | tail -1; }
out() { echo "$1"; [ -z "${GITHUB_OUTPUT:-}" ] || echo "$1" >>"$GITHUB_OUTPUT"; }

if [ "$mode" = sync ] && [ "$ver" = - ]; then ver=0.1.0; skip_pre=1; else skip_pre=0; fi
[[ "$ver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::'$ver' is not X.Y.Z"; exit 1; }
minor=$(echo "$ver" | cut -d. -f2)
major=$(echo "$ver" | cut -d. -f1)
stable=$(maxv "$mkt_stable" "$ovsx_stable")
pre=$(maxv "$mkt_pre" "$ovsx_pre")

echo "version=$ver  Marketplace: pre=$mkt_pre stable=$mkt_stable (ok=$mkt_ok)" \
     " Open VSX: pre=$ovsx_pre stable=$ovsx_stable (ok=$ovsx_ok)"

# A registry that could not be read proves nothing either way: warn, and let
# the publish itself (with --skip-duplicate) be the arbiter.
check_pre_not_ahead() { # $1 = version main carries
  gt "$mkt_pre" "$1" && err "the Marketplace has pre-release $mkt_pre, ahead of main's $1 -- out-of-band publish. Bump tools/vscode-iec/package.json past it."
  gt "$ovsx_pre" "$1" && err "Open VSX has pre-release $ovsx_pre, ahead of main's $1 -- out-of-band publish. Bump tools/vscode-iec/package.json past it."
  return 0
}
check_stable_not_ahead() { # $1 = highest vscode-v* tag version (or 0.0.0)
  gt "$mkt_stable" "$1" && err "the Marketplace has stable $mkt_stable, but the highest vscode-v* tag is $1 -- out-of-band publish. Tag it (vscode-v$mkt_stable) or publish past it from a tag."
  gt "$ovsx_stable" "$1" && err "Open VSX has stable $ovsx_stable, but the highest vscode-v* tag is $1 -- out-of-band publish. Tag it (vscode-v$ovsx_stable) or publish past it from a tag."
  return 0
}

case "$mode" in
  publish-pre)
    if [ $((minor % 2)) -eq 0 ]; then
      err "main's extension version $ver has an EVEN minor. main is the pre-release line and carries an ODD minor (stable, even minors, ships only from vscode-v* tags). Set it to $major.$((minor + 1)).0."
    fi
    check_pre_not_ahead "$ver"
    if gt "$stable" "$ver"; then
      err "main's $ver is below the highest stable release ($stable); a pre-release below stable is never offered to anyone. Bump main to the next odd minor above it."
    fi
    [ "$fail" = 0 ] || exit 1
    out "mkt=$([ "$mkt_ok" = true ] && [ "$mkt_pre" = "$ver" ] && echo false || echo true)"
    out "ovsx=$([ "$ovsx_ok" = true ] && [ "$ovsx_pre" = "$ver" ] && echo false || echo true)"
    ;;

  publish-stable)
    if [ $((minor % 2)) -eq 1 ]; then
      err "vscode-v$ver has an ODD minor. Stable releases carry an EVEN minor; odd minors are the pre-release line on main."
    fi
    # Equal means this tag already reached that registry (a rerun after the
    # other one failed): allowed, and that registry is skipped.
    gt "$mkt_stable" "$ver" && err "the Marketplace already has stable $mkt_stable, above vscode-v$ver."
    gt "$ovsx_stable" "$ver" && err "Open VSX already has stable $ovsx_stable, above vscode-v$ver."
    [ "$fail" = 0 ] || exit 1
    if ! gt "$pre" "$ver"; then
      echo "::warning::stable $ver is not below the highest pre-release ($pre): pre-release users will be offered the stable build until main is bumped to the next odd minor above it."
    fi
    out "mkt=$([ "$mkt_ok" = true ] && [ "$mkt_stable" = "$ver" ] && echo false || echo true)"
    out "ovsx=$([ "$ovsx_ok" = true ] && [ "$ovsx_stable" = "$ver" ] && echo false || echo true)"
    ;;

  sync)
    tagver=${tagver:-0.0.0}
    [[ "$tagver" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "::error::'$tagver' is not X.Y.Z"; exit 1; }
    if [ "$skip_pre" = 0 ]; then
      # Caught here too, so an even minor fails the PR rather than publish.yml.
      if [ $((minor % 2)) -eq 0 ]; then
        err "tools/vscode-iec/package.json is $ver, an EVEN minor. main is the pre-release line and carries an ODD minor; stable ships only from vscode-v* tags. Use $major.$((minor + 1)).0."
      fi
      check_pre_not_ahead "$ver"
    fi
    check_stable_not_ahead "$tagver"
    [ "$fail" = 0 ] || exit 1
    echo "ok: pre-release line $([ "$skip_pre" = 1 ] && echo "(not checked)" || echo "$ver"), stable line ${tagver/#0.0.0/(no vscode-v* tags yet)}"
    ;;

  *) echo "unknown mode: $mode" >&2; exit 2 ;;
esac
