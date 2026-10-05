#!/usr/bin/env bash
#
# redfish-sim.sh — set up the foreign Redfish stack the redfish foreign
# test runs against, and run it:
#
#   scripts/redfish-sim.sh            # prepare (once), then run TestForeign
#   scripts/redfish-sim.sh --prepare  # prepare only; print the env line
#
# The foreign stack is DMTF's own: Redfish-Mockup-Server (the reference
# mockup server, Python) serving two mockups from DMTF's published mockup
# bundle (DSP2043, mirrored in the Redfish-Publications repository):
#
#   public-localstorage  a legacy server: Chassis/1U/Thermal + Power only
#   public-rackmount1    a current server: ThermalSubsystem/PowerSubsystem
#                        and Sensors, plus the deprecated Thermal/Power
#
# The test spawns the server itself (it must stop and restart it to prove
# session recovery), so this script only fetches and installs, then points
# NAUTILUS_REDFISH_SIM at the directory. Everything lives OUTSIDE the repo
# (under $TMPDIR or /tmp) in a venv — nothing to gitignore, never system pip.
# The pins match what the test was written against; bump them together.
#
set -euo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
server_tag=1.3.0     # DMTF/Redfish-Mockup-Server, released 2026-09-04
bundle_tag=2026.2    # DMTF/Redfish-Publications (DSP2043 mockups), 2026-09-16
dir="${NAUTILUS_REDFISH_SIM_DIR:-${TMPDIR:-/tmp}/nautilus-redfish-sim}"
mkdir -p "$dir"

if [ ! -f "$dir/Redfish-Mockup-Server-$server_tag/redfishMockupServer.py" ]; then
  echo "fetching Redfish-Mockup-Server $server_tag" >&2
  curl -fsSL "https://codeload.github.com/DMTF/Redfish-Mockup-Server/tar.gz/refs/tags/$server_tag" | tar xz -C "$dir"
fi
if [ ! -f "$dir/mockups/public-rackmount1/index.json" ] || [ ! -f "$dir/mockups/public-localstorage/index.json" ]; then
  echo "fetching the DSP2043 mockups from Redfish-Publications $bundle_tag (~40 MB, two mockups kept)" >&2
  tmp=$(mktemp -d)
  curl -fsSL "https://codeload.github.com/DMTF/Redfish-Publications/tar.gz/refs/tags/$bundle_tag" -o "$tmp/pubs.tgz"
  tar xzf "$tmp/pubs.tgz" -C "$tmp" \
    "Redfish-Publications-$bundle_tag/mockups/public-rackmount1" \
    "Redfish-Publications-$bundle_tag/mockups/public-localstorage"
  mkdir -p "$dir/mockups"
  rm -rf "$dir/mockups/public-rackmount1" "$dir/mockups/public-localstorage"
  mv "$tmp/Redfish-Publications-$bundle_tag/mockups/public-rackmount1" "$tmp/Redfish-Publications-$bundle_tag/mockups/public-localstorage" "$dir/mockups/"
  rm -rf "$tmp"
fi
if [ ! -x "$dir/venv/bin/python" ]; then
  echo "creating venv $dir/venv" >&2
  python3 -m venv "$dir/venv"
fi
if ! "$dir/venv/bin/python" -c 'import requests, grequests, multipart' 2>/dev/null; then
  echo "installing the mockup server's requirements" >&2
  "$dir/venv/bin/pip" install --quiet -r "$dir/Redfish-Mockup-Server-$server_tag/requirements.txt"
fi
ln -sfn "Redfish-Mockup-Server-$server_tag" "$dir/server"

echo "NAUTILUS_REDFISH_SIM=$dir go test ./redfish/ -run TestForeign -v -count=1" >&2
if [ "${1:-}" = "--prepare" ]; then
  exit 0
fi
cd "$repo"
NAUTILUS_REDFISH_SIM="$dir" exec go test ./redfish/ -run TestForeign -v -count=1
