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
# The mimic editor draws with the HMI kit (hmi/), used as its packaged dist.
( cd "$repo/hmi" && ci && npm run --silent package >/dev/null )
# The editors' bundles, and the runtime custom components compiled in the
# browser run on (Svelte and the HMI kit: vite.runtime.config.ts).
( cd "$ext/webview-ui" && ci && npx vite build --logLevel warn && npx vite build --config vite.mimic.config.ts --logLevel warn \
    && npx vite build --config vite.runtime.config.ts --logLevel warn \
    && npx vite build --config vite.runtime3d.config.ts --logLevel error )
# The extension itself, after webview-ui's install: its custom-component
# compiler (out/web/component-compiler.js) is built from webview-ui's svelte.
( cd "$ext" && ci && node esbuild.web.mjs --minify )

# The workbench app, then the extension's files beside it.
( cd "$here/app" && ci && npx vite build --logLevel warn )
appext="$out/app/extension"
mkdir -p "$appext/out/web" "$appext/media/dist"
cp "$ext/package.json" "$ext/language-configuration.json" "$ext/icon.png" "$appext/"
# extension.js, and the custom-component compiler it loads when a mimic
# first needs it.
cp "$ext/out/web/extension.js" "$ext/out/web/component-compiler.js" "$appext/out/web/"
cp -r "$ext/syntaxes" "$ext/schemas" "$ext/walkthroughs" "$ext/icons" "$appext/"
mkdir -p "$appext/images" && cp "$ext/images/live-values.png" "$appext/images/"
cp "$ext"/media/dist/fbd-flow.* "$ext"/media/dist/mimic-editor.* "$ext"/media/dist/user-runtime.* "$appext/media/dist/"
files_json() { ( cd "$1" && find . -type f ! -name files.json | sed 's|^\./||' | sort | python3 -c 'import json,sys; print(json.dumps([l.strip() for l in sys.stdin if l.strip()]))' > files.json ); }
files_json "$appext"

# Red Hat's YAML extension (MIT), its web build alone: completion, hover and
# validation from the schemas the nautilus extension declares
# (contributes.yamlValidation), as on the desktop. Pinned and checked; a
# new version means a new sha256 here.
yaml_version=2.0.0
yaml_sha256=4bba62ccda962b720dcb0829e85cf4109c94e86653dcd39fd7500c3687ad8c60
vsix="$(mktemp)"
trap 'rm -f "$vsix"' EXIT
curl -fsSL --retry 3 -o "$vsix" "https://open-vsx.org/api/redhat/vscode-yaml/$yaml_version/file/redhat.vscode-yaml-$yaml_version.vsix"
echo "$yaml_sha256  $vsix" | sha256sum -c --quiet
python3 -I - "$vsix" "$out/app/yaml-extension" <<'PY'
import sys, zipfile, os
vsix, dest = sys.argv[1:]
keep = ('package.json', 'language-configuration.json', 'LICENSE.txt', 'thirdpartynotices.txt', 'dist/l10n/bundle.l10n.json')
with zipfile.ZipFile(vsix) as z:
    for name in z.namelist():
        rel = name.removeprefix('extension/')
        if rel == name: continue
        web = rel.startswith('dist/') and rel.count('/') == 1 and (rel.endswith('extension-web.js') or rel == 'dist/languageserver-web.js')
        if rel in keep or web:
            path = os.path.join(dest, rel)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            with open(path, 'wb') as f: f.write(z.read(name))
PY
files_json "$out/app/yaml-extension"
printf '%s raw, %s gzipped\n' "$(wc -c < "$out/naut.wasm")" "$(gzip -9 -c "$out/naut.wasm" | wc -c)"

