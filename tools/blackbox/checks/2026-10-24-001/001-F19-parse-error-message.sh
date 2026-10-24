#!/usr/bin/env bash
# F-19 (filed nautilus#427, cli, out of the brief's surface): when one ST file fails to parse,
# `naut run` reports that parse error (as `naut check` does), not "this project declares no
# TYPE" against another file. Uses a copy of SUT_PROJECT (evidence/typed) with the reserved
# word Constant as a variable name in main.st. Oracle: naut's messages (nautilus itself).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
sed -i 's#addr: "localhost:8080"#addr: "localhost:8094"#; s#^opcua: {}$#opcua:\n  endpoint: opc.tcp://localhost:4854#' nautilus.yaml
sed -i 's/^    Fixed    : INT;/    Constant : INT;/; s/^Fixed := 7;/Constant := 7;/' main.st
echo "--- naut check"; naut check . 2>&1 | grep -v warning | tail -3; echo "--- naut run"
timeout 5 naut run . > run.log 2>&1; rc=$?; grep -v warning run.log | tail -4
ok $(grep -q 'main.st:[0-9]*:[0-9]*' run.log && echo 0 || echo 1) "naut run names main.st and the position of the parse error"
ok $(! grep -q 'declares no TYPE' run.log && echo 0 || echo 1) "naut run does not blame the TYPE declarations"
ok $([ $rc != 0 ] && [ $rc != 124 ] && echo 0 || echo 1) "naut run exits non-zero (exit $rc)"
cd /; rm -rf "$T"; exit $fail
