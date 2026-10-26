#!/usr/bin/env bash
# Pass: a second controller whose OPC UA port is taken reports it on stderr at start
# ("continuing without it") and in its opcua-server driver row (state error), and runs
# regardless (guide, Status). Starts a copy of SUT_PROJECT on HTTP 8099 with the SUT's OPC UA
# port and stops it. Project: evidence/typed. Oracle: naut's log and GET /api/drivers (nautilus itself).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
sed -i 's#addr: "localhost:8080"#addr: "localhost:8099"#; s#^name: typed$#name: typedclash#' nautilus.yaml
naut run . > run.log 2>&1 & PID=$!; sleep 2
grep -v warning run.log | head -5
ok $(grep -q "address already in use (continuing without it)" run.log && echo 0 || echo 1) "stderr reports the port in use and continues"
row=$(curl -s localhost:8099/api/drivers); echo "$row" | tr ',' '\n' | grep -i -m4 'opcua\|state\|message'
ok $(echo "$row" | grep -q '"kind":"opcua-server"' && echo "$row" | grep -q '"state":"error"' && echo 0 || echo 1) "driver row opcua-server state error"
a=$(curl -s localhost:8099/api/state | tr ',' '\n' | grep -m1 '"Counter"'); sleep 0.5; b=$(curl -s localhost:8099/api/state | tr ',' '\n' | grep -m1 '"Counter"')
echo "Counter: $a -> $b"
ok $([ -n "$a" ] && [ "$a" != "$b" ] && echo 0 || echo 1) "the second controller's scan loop and HTTP API run regardless"
v=$(uaread -u "$SUT_OPCUA" -n "ns=1;s=Level" 2>&1); ok $([ "$v" = "1.5" ] && echo 0 || echo 1) "the first server still answers on $SUT_OPCUA ($v)"
kill $PID; wait $PID 2>/dev/null; cd /; rm -rf "$T"; exit $fail
