#!/usr/bin/env bash
# Build @joyautomation/nautilus-playground into dist/: naut.wasm (GOOS=js,
# so internal/project links no field drivers), Go's wasm_exec.js, and the
# worker + client sources.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
repo="$(cd "$here/.." && pwd)"
out="$here/dist"
rm -rf "$out" && mkdir -p "$out"
version="${NAUT_VERSION:-$(git -C "$repo" describe --tags --always --dirty 2>/dev/null || echo dev)}"
( cd "$repo" && GOOS=js GOARCH=wasm go build -trimpath \
    -ldflags "-s -w -X github.com/joyautomation/nautilus/internal/lsp.Version=$version" \
    -o "$out/naut.wasm" ./cmd/naut-wasm )
goroot="$(go env GOROOT)"
cp "$goroot/lib/wasm/wasm_exec.js" "$out/" 2>/dev/null || cp "$goroot/misc/wasm/wasm_exec.js" "$out/"
cp "$here"/src/*.js "$here"/src/*.d.ts "$out/"
printf '%s raw, %s gzipped\n' "$(wc -c < "$out/naut.wasm")" "$(gzip -9 -c "$out/naut.wasm" | wc -c)"

# Examples, as the JSON file maps the client loads.
demo="$(mktemp -d)"
# naut new takes a bare name (a path names the test suite after the path).
( cd "$repo" && go build -o "$demo/naut" ./cmd/naut ) && ( cd "$demo" && ./naut new --template demo --no-input demo >/dev/null )
python3 -I "$here/scripts/examples.py" "$out/examples" \
  "sealin=$here/examples/sealin:program.ld:Start/stop seal-in (ladder):The first circuit everyone learns. Press StartPB in the Tags panel and the motor keeps running after you let go, because the rung reads its own Motor contact. StopPB breaks it." \
  "demo=$demo/demo:interlocks.ld,program.fbd:Tank demo (ladder, FBD, ST):What naut new makes: a heated tank with a PI loop and pump seal-in in FBD, the alarm annunciator in ladder, the plant simulated in ST, and acceptance tests. Change a setpoint and watch the loop follow." \
  "lift-station=$repo/examples/lift-station:permissives.ld,sequence.sfc,level.fbd:Lift station (all four languages):A duplex sewage lift station: duty/standby sequencing in SFC, level control in FBD, permissives in ladder, the plant simulated in ST, with alarms and 15 tests. Set InflowLps to 40 for a storm."
rm -rf "$demo"
