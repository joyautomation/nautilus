#!/usr/bin/env bash
# F-12: a binary from `naut build` asked for `--help` should print usage and exit, not start the
# controller. Passes when it exits within 3 s. SUT_PROJECT = evidence/pumps. Uses port 18080.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT; cp -r "$P"/. "$T"/; cd "$T"
naut build -o pumps >/dev/null 2>&1 || { echo "FAIL build"; exit 1; }
mkdir run && cd run
out=$(NAUTILUS_ADDR=127.0.0.1:18080 timeout 3 ../pumps --help 2>&1); rc=$?; echo "$out" | head -5
[ $rc -eq 124 ] && { echo "FAIL --help started the controller (still running after 3 s)"; exit 1; }
echo "PASS exited rc=$rc"
