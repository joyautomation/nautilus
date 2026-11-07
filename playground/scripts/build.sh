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
cp "$here"/src/*.js "$here"/src/*.d.ts "$out/" 2>/dev/null || cp "$here"/src/*.js "$out/"
printf '%s raw, %s gzipped\n' "$(wc -c < "$out/naut.wasm")" "$(gzip -9 -c "$out/naut.wasm" | wc -c)"
