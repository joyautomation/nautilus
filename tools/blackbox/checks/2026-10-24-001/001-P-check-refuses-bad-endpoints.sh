#!/usr/bin/env bash
# Pass: `naut check` refuses an endpoint that is not opc.tcp://host:port (guide), exit 1, with a
# message naming the rule. Project: evidence/typed. Oracle: naut's exit code and message (nautilus itself).
set -u; fail=0
ok() { if [ "$1" = 0 ]; then echo "PASS  $2"; else echo "FAIL  $2"; fail=1; fi; }
T=$(mktemp -d); cp -r "$SUT_PROJECT"/. "$T"; cd "$T"
for ep in "tcp://localhost:4840" "opc.tcp://localhost" "opc.tcp://" "opc.tcp://localhost:99999" "http://localhost:4840" "opc.tcp://localhost:4840/nautilus" "opc.tcp://:4840" "localhost:" "4840"; do
  cp "$SUT_PROJECT/nautilus.yaml" nautilus.yaml
  sed -i "s#^opcua: {}\$#opcua:\n  endpoint: \"$ep\"#" nautilus.yaml
  out=$(naut check . 2>&1 | grep -v warning); rc=${PIPESTATUS[0]}; naut check . >/dev/null 2>&1; rc=$?
  echo "  $ep -> exit $rc: $(echo "$out" | grep -i -m1 'endpoint\|opcua' || echo "$out" | tail -1)"
  ok $([ $rc != 0 ] && echo 0 || echo 1) "naut check refuses endpoint \"$ep\""
done
cd /; rm -rf "$T"; exit $fail
