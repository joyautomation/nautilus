#!/usr/bin/env bash
# pass: the duplex pump station's own acceptance suite (evidence/pumps/pumps_test.yaml) passes:
# lead/lag start and stop, lead alternation per pump-down, 30 s alarm delay, an 8 h delay walked in
# virtual time, run hours after 1 h. Also `naut check` is clean. SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json
naut check || { echo "FAIL naut check"; exit 1; }
out=$(naut test -v 2>&1); rc=$?; echo "$out"
[ $rc -eq 0 ] && echo "$out" | grep -q "5 tests, 5 passed" || { echo "FAIL naut test"; exit 1; }
