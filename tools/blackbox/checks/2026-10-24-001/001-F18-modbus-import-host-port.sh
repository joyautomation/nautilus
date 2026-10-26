#!/usr/bin/env bash
# F-18 (filed nautilus#426, modbus, out of the brief's surface): a device-map host that
# carries a port (host: 127.0.0.1:5020, the address the Modbus guide tells you to point hosts
# at) imports to a manifest the driver can dial - no ":502" appended. Imports into a copy of
# SUT_PROJECT (evidence/mb, which has devices.yaml), runs it for 3 s on HTTP 8093 / OPC UA
# 4853 and reads the log. Oracle: naut's own log (nautilus itself).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
sed -i 's#addr: "localhost:8082"#addr: "localhost:8093"#; s#opc.tcp://localhost:4842#opc.tcp://localhost:4853#; s#^name: mb$#name: mbimport#' nautilus.yaml
rm -f modbus_manifest.yaml tags/modbus.yaml
naut modbus import --map devices.yaml > import.log 2>&1; rc=$?; cat import.log | tail -3
ok $([ $rc = 0 ] && echo 0 || echo 1) "naut modbus import --map devices.yaml (exit $rc)"
grep -n 'host\|port' modbus_manifest.yaml
naut check . > check.log 2>&1; ok $([ $? = 0 ] && echo 0 || echo 1) "naut check passes on the imported project"
naut run . > run.log 2>&1 & PID=$!; sleep 3; kill $PID; wait $PID 2>/dev/null
grep -i -m3 'modbus\|addr=\|dial' run.log
ok $(! grep -q '5020:502\|too many colons' run.log && echo 0 || echo 1) "the driver dials 127.0.0.1:5020, not 127.0.0.1:5020:502"
cd /; rm -rf "$T"; exit $fail
