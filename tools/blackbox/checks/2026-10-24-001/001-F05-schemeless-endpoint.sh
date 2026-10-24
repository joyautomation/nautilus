#!/usr/bin/env bash
# F-05: `naut check` accepts a bare host:port endpoint and reads it as opc.tcp://host:port
# (guide: "a bare host:port is read as opc.tcp://host:port"), and `naut run` serves there.
# Starts its own instance from a copy of SUT_PROJECT on ports 8090/4850 and stops it.
# Project: evidence/typed. Oracle: naut's exit code and banner (nautilus itself), uaread (asyncua).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
sed -i 's#addr: "localhost:8080"#addr: "localhost:8090"#; s#^opcua: {}$#opcua:\n  endpoint: "localhost:4850"#' nautilus.yaml
grep -n -A1 '^opcua:' nautilus.yaml
naut check . > check.log 2>&1; rc=$?; grep -v warning check.log
ok $([ $rc = 0 ] && echo 0 || echo 1) "naut check accepts endpoint \"localhost:4850\" (exit $rc)"
naut run . > run.log 2>&1 & PID=$!; sleep 2
grep -v warning run.log | head -3
ok $(grep -q "opc.tcp://localhost:4850\|opc.tcp://127.0.0.1:4850" run.log && echo 0 || echo 1) "naut run banner names opc.tcp://…:4850"
v=$(uaread -u opc.tcp://localhost:4850 -n "ns=1;s=Level" 2>&1); echo "uaread Level -> $v"
ok $([ "$v" = "1.5" ] && echo 0 || echo 1) "a client reads Level over opc.tcp://localhost:4850"
kill $PID; wait $PID 2>/dev/null; cd /; rm -rf "$T"; exit $fail
