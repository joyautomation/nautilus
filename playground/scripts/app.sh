#!/usr/bin/env bash
# The playground app (dist/app): the extension's web build and diagram
# editors, the workbench (app/), and the extension's files beside it.
# Called by build.sh; run alone to rebuild just the app after a change to
# app/ or tools/vscode-iec (SKIP_NPM=1 skips the npm ci steps).
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
repo="$(cd "$here/.." && pwd)"
out="$here/dist"
ci() { [ -n "${SKIP_NPM:-}" ] || npm ci --no-audit --no-fund --silent; }

# The extension, built for the web (tools/vscode-iec/esbuild.web.mjs), and
# its diagram editors (webview-ui: fbd-flow.js renders LD, FBD and SFC) and
# mimic editor (mimic-editor.js, the HMI).
ext="$repo/tools/vscode-iec"
( cd "$ext" && ci && node esbuild.web.mjs --minify )
# The mimic editor draws with the HMI kit (hmi/), used as its packaged dist.
( cd "$repo/hmi" && ci && npm run --silent package >/dev/null )
( cd "$ext/webview-ui" && ci && npx vite build --logLevel warn && npx vite build --config vite.mimic.config.ts --logLevel warn )

# The workbench app, then the extension's files beside it.
( cd "$here/app" && ci && npx vite build --logLevel warn )
appext="$out/app/extension"
mkdir -p "$appext/out/web" "$appext/media/dist"
cp "$ext/package.json" "$ext/language-configuration.json" "$ext/icon.png" "$appext/"
cp "$ext/out/web/extension.js" "$appext/out/web/"
cp -r "$ext/syntaxes" "$ext/schemas" "$ext/walkthroughs" "$ext/icons" "$appext/"
mkdir -p "$appext/images" && cp "$ext/images/live-values.png" "$appext/images/"
cp "$ext"/media/dist/fbd-flow.* "$ext"/media/dist/mimic-editor.* "$appext/media/dist/"
( cd "$appext" && find . -type f ! -name files.json | sed 's|^\./||' | sort | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))' > files.json )
printf '%s raw, %s gzipped\n' "$(wc -c < "$out/naut.wasm")" "$(gzip -9 -c "$out/naut.wasm" | wc -c)"

