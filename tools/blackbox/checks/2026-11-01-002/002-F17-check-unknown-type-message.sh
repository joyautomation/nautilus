#!/usr/bin/env bash
# F-17: a manifest tag with a misspelt type (`type: PumpSelect`) should get the message the tag-model
# page documents, naming the known TYPEs, from naut check as from naut test/run/build. Today
# naut check says the tag has no type. Passes when check's output names PumpSelect and is non-zero.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"
sed -i 's/type: PumpSel, /type: PumpSelect, /' nautilus.yaml
c=$(naut check 2>&1); crc=$?; echo "check rc=$crc: $c"
t=$(naut test 2>&1); echo "test: $t"
[ $crc -ne 0 ] && echo "$c" | grep -q "PumpSelect" && { echo PASS; exit 0; }
echo "FAIL naut check does not name the unknown type"; exit 1
