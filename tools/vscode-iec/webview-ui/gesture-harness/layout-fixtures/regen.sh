#!/usr/bin/env bash
# Regenerate the diagram models diagram-layout.test.mjs renders: `naut <ld|fbd|sfc>
# graph` of every .ld/.fbd/.sfc under examples/ plus this directory's own
# reproducers. Run from anywhere; needs Go. Commit the JSON it writes.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../../../.." && pwd)"
cd "$root"
go build -o "$here/.naut" ./cmd/naut
trap 'rm -f "$here/.naut"' EXIT
rm -f "$here"/*.json
{
	find examples \( -name '*.ld' -o -name '*.fbd' -o -name '*.sfc' \)
	find "${here#"$root"/}" \( -name '*.ld' -o -name '*.fbd' -o -name '*.sfc' \)
} | sort | while read -r f; do
	ext="${f##*.}"
	name="$(echo "${f%.*}" | sed 's#^examples/##; s#^tools/.*/layout-fixtures/#repro/#; s#/#__#g').$ext"
	"$here/.naut" "$ext" graph "$f" > "$here/$name.json"
	echo "$name"
done
