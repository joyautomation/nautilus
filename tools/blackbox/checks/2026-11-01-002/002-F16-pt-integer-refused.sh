#!/usr/bin/env bash
# F-16: TON's PT is a TIME. `PT := 30000` (an integer, the Logix PRE habit) should be a compile error,
# per the Studio 5000 page ("PT is a TIME, written T#5S, not a millisecond DINT") and IEC 61131-3,
# which has no implicit integer-to-TIME conversion. Today naut check passes it and the TON times
# 30 s. Passes when naut check exits non-zero. SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"
sed -i 's/PT := HiLevelDelay/PT := 30000/' program.st; grep -n "PT :=" program.st
out=$(naut check 2>&1); rc=$?; echo "$out"
[ $rc -ne 0 ] && { echo PASS; exit 0; }; echo "FAIL integer accepted as PT"; exit 1
