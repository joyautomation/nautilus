#!/usr/bin/env bash
# Pass (release: compiles a binary): `naut build` output serves OPC UA the way `naut run` does.
# Builds a copy of SUT_PROJECT with ports 8091/4851, runs the binary, reads a tag, stops it.
# Project: evidence/typed. Oracle: uaread (asyncua).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
sed -i 's#addr: "localhost:8080"#addr: "localhost:8091"#; s#^name: typed$#name: typedbuilt#; s#^opcua: {}$#opcua:\n  endpoint: opc.tcp://localhost:4851#' nautilus.yaml
naut build -o ctl . > build.log 2>&1; rc=$?; grep -v warning build.log | tail -3; ls -la ctl 2>/dev/null
ok $([ $rc = 0 ] && [ -x ctl ] && echo 0 || echo 1) "naut build produced an executable (exit $rc)"
./ctl > run.log 2>&1 & PID=$!; sleep 2; grep -v warning run.log | head -3
ok $(grep -q "opc.tcp://.*4851" run.log && echo 0 || echo 1) "the built binary's banner names the endpoint"
v=$(uaread -u opc.tcp://localhost:4851 -n "ns=1;s=Level" 2>&1); echo "uaread Level -> $v"
ok $([ "$v" = "1.5" ] && echo 0 || echo 1) "a client reads Level from the built binary"
n=$(uals -u opc.tcp://localhost:4851 -n "ns=1;i=1" 2>/dev/null | grep -c "ns=1;s="); echo "folder children: $n"
ok $([ "$n" -ge 33 ] && echo 0 || echo 1) "the built binary serves the whole folder"
kill $PID; wait $PID 2>/dev/null; cd /; rm -rf "$T"; exit $fail
