#!/usr/bin/env bash
# Regenerate packs.tar.gz: the closed package set naut builds HMIs with
# (Svelte, the HMI kit, the 3D kit, three, Threlte), as package.json here
# pins it, the kits built from this repo. Run after changing a kit or a pin;
# commit the result (`go install` needs it in the tree). CI checks it is
# current.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
( cd "$repo/hmi" && npm ci --no-audit --no-fund >/dev/null && npm run package >/dev/null )
( cd "$repo/hmi-3d" && npm ci --no-audit --no-fund >/dev/null && npm run package >/dev/null )
( cd "$here" && npm ci --no-audit --no-fund >/dev/null )
# the generator needs a pack to compile against; a stale one is fine
[ -s "$here/packs.tar.gz" ] || : > "$here/packs.tar.gz"
( cd "$repo" && go run ./internal/hmibuild/genpack "$here" "$here/packs.tar.gz" )
