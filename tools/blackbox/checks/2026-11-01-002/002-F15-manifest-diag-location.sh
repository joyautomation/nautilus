#!/usr/bin/env bash
# F-15: `naut check` prints gcc-style `file:line:col: message` diagnostics (structured-text page,
# Tooling). A manifest error should name nautilus.yaml (and its line), not ".". Passes when the
# diagnostic for a bad enum init starts with "nautilus.yaml:". SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"
sed -i 's/init: Pump1,/init: Pump3,/' nautilus.yaml
out=$(naut check 2>&1); echo "$out"
echo "$out" | grep -q "^nautilus.yaml:" && { echo PASS; exit 0; }
echo "FAIL manifest diagnostic not located in nautilus.yaml"; exit 1
