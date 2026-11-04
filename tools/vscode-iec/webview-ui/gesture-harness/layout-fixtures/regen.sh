#!/usr/bin/env bash
# Regenerate the diagram models diagram-layout.test.mjs renders: `naut <ld|fbd|sfc>
# graph` of every TRACKED .ld/.fbd/.sfc under examples/ plus this directory's
# own reproducers. Run from anywhere; needs Go. Commit the JSON it writes.
#
# Only git-tracked examples: an untracked project under examples/ (a live
# site checked out for testing) must never end up in a committed fixture.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../../../.." && pwd)"
cd "$root"
go build -o "$here/.naut" ./cmd/naut
trap 'rm -f "$here/.naut"' EXIT
rm -f "$here"/*.json
{
	git ls-files 'examples/*.ld' 'examples/*.fbd' 'examples/*.sfc'
	find "${here#"$root"/}" -maxdepth 1 \( -name '*.ld' -o -name '*.fbd' -o -name '*.sfc' \)
} | sort | while read -r f; do
	ext="${f##*.}"
	name="$(echo "${f%.*}" | sed 's#^examples/##; s#^tools/.*/layout-fixtures/#repro/#; s#/#__#g').$ext"
	"$here/.naut" "$ext" graph "$f" > "$here/$name.json"
	echo "$name"
done
