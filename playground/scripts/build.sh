#!/usr/bin/env bash
# Build @joyautomation/nautilus-playground into dist/:
#   naut.wasm (GOOS=js, so internal/project links no field drivers), Go's
#   wasm_exec.js, the worker, client, bridge and sw.js (the dashboard and
#   HMI server), examples/, hmi/ (prebuilt HMIs), and app/: the
#   workbench (app/, monaco-vscode-api) with the nautilus extension's web
#   build and its editors in app/extension/.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
repo="$(cd "$here/.." && pwd)"
out="$here/dist"
rm -rf "$out" && mkdir -p "$out"
# The build id: a hash of what the playground is built from (everything the
# repo tracks but the docs site, plus uncommitted changes), not of the time.
# The docs deploy rebuilds the playground for every docs edit; an unchanged
# id keeps visitors' cached naut.wasm and workbench (see website/nginx.conf).
# The version carries it too, so naut.wasm itself is byte-for-byte the same
# (-buildvcs=false keeps the commit out of it as well).
build="$(cd "$repo" && { git ls-files -s -- . ':!website' ':!docs' ':!.github'
  git diff HEAD -- . ':!website' ':!docs' ':!.github'
  git ls-files -o --exclude-standard -- . ':!website' ':!docs' ':!.github' | xargs -r sha256sum
} | sha256sum | cut -c1-12)"
version="${NAUT_VERSION:-$(git -C "$repo" describe --tags --abbrev=0 2>/dev/null || echo dev)-$build}"
( cd "$repo" && GOOS=js GOARCH=wasm go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/joyautomation/nautilus/internal/lsp.Version=$version" \
    -o "$out/naut.wasm" ./cmd/naut-wasm )
goroot="$(go env GOROOT)"
cp "$goroot/lib/wasm/wasm_exec.js" "$out/" 2>/dev/null || cp "$goroot/misc/wasm/wasm_exec.js" "$out/"
cp "$here"/src/*.js "$here"/src/*.d.ts "$out/"

"$here/scripts/app.sh"

# Prebuilt HMIs, served by sw.js at <base>/view/<name>/ in a tab of their
# own, as `naut run` serves server.hmi. The base is where the site mounts
# dist/ (the docs site: /playground/engine); PLAYGROUND_BASE overrides.
# An example with one gets "hmi" in its JSON (examples.py).
base="${PLAYGROUND_BASE:-/playground/engine}"
ci() { [ -n "${SKIP_NPM:-}" ] || npm ci --no-audit --no-fund --silent; }
( cd "$repo/hmi-3d" && ci && npm run --silent package >/dev/null )
lift="$repo/examples/lift-station/hmi"
( cd "$lift" && ci && HMI_BASE="$base/view/lift-station" npx vite build --logLevel warn >/dev/null )
mkdir -p "$out/hmi" && cp -r "$lift/build" "$out/hmi/lift-station"

# Examples, as the JSON file maps the client loads.
demo="$(mktemp -d)"
# naut new takes a bare name (a path names the test suite after the path).
( cd "$repo" && go build -o "$demo/naut" ./cmd/naut ) && ( cd "$demo" && ./naut new --template demo --no-input demo >/dev/null )
# The playground adds an HMI drawn from the kit's built-in equipment.
cp "$here"/examples/demo/*.mimic.json "$demo/demo/"
python3 -I "$here/scripts/examples.py" "$out/examples" \
  "sealin=$here/examples/sealin:program.ld:Start/stop seal-in (ladder):The first circuit everyone learns. Press StartPB in the Tags panel and the motor keeps running after you let go, because the rung reads its own Motor contact. StopPB breaks it." \
  "demo=$demo/demo:interlocks.ld,program.fbd,tank.mimic.json:Tank demo (ladder, FBD, ST, HMI):What naut new makes: a heated tank with a PI loop and pump seal-in in FBD, the alarm annunciator in ladder, the plant simulated in ST, and acceptance tests. Change TempSP and watch the loop follow, on the diagrams and in tank.mimic.json." \
  "lift-station=$repo/examples/lift-station:permissives.ld,sequence.sfc,level.fbd:Lift station (all four languages):A duplex sewage lift station: duty/standby sequencing in SFC, level control in FBD, permissives in ladder, the plant simulated in ST, with alarms and 15 tests. Set InflowLps to 40 for a storm."
rm -rf "$demo"

# What a page hosting dist/ reads first: the version and the build id, for
# the ?v= that keeps browsers off stale files.
printf '{"version":"%s","build":"%s"}\n' "$version" "$build" > "$out/engine.json"
