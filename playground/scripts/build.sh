#!/usr/bin/env bash
# Build @joyautomation/nautilus-playground into dist/:
#   naut.wasm (GOOS=js, so internal/project links no field drivers),
#   naut-hmi.wasm (the screen builder, fetched on first use), Go's
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
# The screen builder (esbuild, Svelte): a second module, fetched only when a
# screen is first asked for (hmi-worker.js); not in naut.wasm.
( cd "$repo" && GOOS=js GOARCH=wasm go build -trimpath -buildvcs=false -ldflags "-s -w" \
    -o "$out/naut-hmi.wasm" ./cmd/naut-wasm-hmi )
goroot="$(go env GOROOT)"
cp "$goroot/lib/wasm/wasm_exec.js" "$out/" 2>/dev/null || cp "$goroot/misc/wasm/wasm_exec.js" "$out/"
cp "$here"/src/*.js "$here"/src/*.d.ts "$out/"
# naut's HMI build packages, fetched by naut.wasm when a project has a screen
cp "$repo/internal/hmibuild/packs/packs.tar.gz" "$out/hmi-packs.tgz"

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
bench="$repo/examples/plcnext-bench/hmi"
( cd "$bench" && ci && HMI_BASE="$base/view/plcnext-bench" npx vite build --logLevel warn >/dev/null )
cp -r "$bench/build" "$out/hmi/plcnext-bench"
epichmi="$repo/examples/epic-bench/hmi"
( cd "$epichmi" && ci && HMI_BASE="$base/view/epic-bench" npx vite build --logLevel warn >/dev/null )
cp -r "$epichmi/build" "$out/hmi/epic-bench"

# Examples, as the JSON file maps the client loads.
demo="$(mktemp -d)"
# naut new takes a bare name (a path names the test suite after the path).
( cd "$repo" && go build -o "$demo/naut" ./cmd/naut ) && ( cd "$demo" && ./naut new --template demo --no-input demo >/dev/null )
# The playground adds an HMI drawn from the kit's built-in equipment.
cp "$here"/examples/demo/*.mimic.json "$demo/demo/"
# The PLCnext bench runs its simulated build (sim.yaml) as nautilus.yaml: a
# tab has no controller. The controller build rides along as controller.yaml.
plcnext="$demo/plcnext-bench"
cp -r "$repo/examples/plcnext-bench" "$plcnext"
mv "$plcnext/nautilus.yaml" "$plcnext/controller.yaml" && mv "$plcnext/sim.yaml" "$plcnext/nautilus.yaml"
# The groov EPIC bench the same way: sim.yaml as nautilus.yaml, the build
# that drives the processor over OptoMMP as controller.yaml.
epic="$demo/epic-bench"
cp -r "$repo/examples/epic-bench" "$epic"
mv "$epic/nautilus.yaml" "$epic/controller.yaml" && mv "$epic/sim.yaml" "$epic/nautilus.yaml"
# --try: what each example's terminal suggests first (nautShell.js).
python3 -I "$here/scripts/examples.py" "$out/examples" \
  "--try=sealin=set StartPB true;set StartPB false;tags Motor;naut test" \
  "--try=demo=set TempSP 70;watch TempC Heater;naut test" \
  "--try=lift-station=set InflowGpm 950;watch LIT101_Level;naut test" \
  "--try=plcnext-bench=set DI_02 true;set SliderV 7.5;watch DO_02 AI_00;set DI_02 false;naut test" \
  "--try=epic-bench=set EPIC_M0_DI03 true;watch EPIC_M1_DO03 EPIC_M0_DI06 EPIC_M1_DO06 EPIC_M0_DI12;set EPIC_M2_AI00 30;set EPIC_M0_DI03 false;naut test" \
  "sealin=$here/examples/sealin:program.ld:Start/stop seal-in (ladder):The first circuit everyone learns. Press StartPB in the Tags panel and the motor keeps running after you let go, because the rung reads its own Motor contact. StopPB breaks it." \
  "demo=$demo/demo:interlocks.ld,program.fbd,tank.mimic.json:Tank demo (ladder, FBD, ST, HMI):What naut new makes: a heated tank with a PI loop and pump seal-in in FBD, the alarm annunciator in ladder, the plant simulated in ST, and acceptance tests. Change TempSP and watch the loop follow, on the diagrams and in tank.mimic.json." \
  "lift-station=$repo/examples/lift-station:permissives.ld,sequence.sfc,level.fbd:Lift station (all four languages):A duplex sewage lift station: duty/standby sequencing in SFC, level control in FBD, permissives in ladder, the plant simulated in ST, with alarms and 20 tests. Set InflowGpm to 950 for a storm." \
  "plcnext-bench=$plcnext:bench.st,tags/sim.yaml:PLCnext starter kit (3D):A Phoenix Contact AXC F 2152 with four Axioline elements, as it runs on our bench, here with the board simulated. Hold a pushbutton with set DI_02 true (S1..S4 are DI_00..DI_03) and its output lights; move the slider with SliderV. The real build is controller.yaml." \
  "epic-bench=$epic:bench.st,board.st,tags/sim.yaml:groov EPIC bench (3D):An Opto 22 groov EPIC with four modules (24 DI, 12 DO, 12 ICTD, 24 V inputs), as it runs on our bench, here with the chassis simulated. Turn an input on with set EPIC_M0_DI03 true: its lamp lights, and the bench's field wires (board.st) carry it on to DI6, DO6 and DI12. Set EPIC_M2_AI00 for the temperature. The real build is controller.yaml."
rm -rf "$demo"

# What a page hosting dist/ reads first: the version and the build id, for
# the ?v= that keeps browsers off stale files.
printf '{"version":"%s","build":"%s"}\n' "$version" "$build" > "$out/engine.json"
