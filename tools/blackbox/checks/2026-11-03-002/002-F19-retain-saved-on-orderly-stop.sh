#!/usr/bin/env bash
# F-19: an orderly stop (SIGTERM, and SIGINT, the banner's "Ctrl+C to stop") saves what changed since
# the last retain write. /guides/redundancy/ "Retained state": the store is written "every 2 seconds,
# only when something changed", and "A hard kill can lose at most 2 seconds of operator input" — by
# contrast, an orderly stop should lose none; a customer would expect it (CODESYS writes its retain
# data on an orderly shutdown).
# On a copy of SUT_PROJECT (retain: {}): write the setpoint LeadStartPct = 42 (POST /api/tags), stop
# the controller 0.2 s later with the signal, start it again, expect 42. Both signals.
# Wall clock ~12 s, so release. Port 18084. SUT_PROJECT = evidence/pumps.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); pid=""; trap '[ -n "$pid" ] && kill $pid 2>/dev/null; rm -rf "$T"' EXIT
cp -r "$P"/. "$T"/; cd "$T"
grep -q '^retain:' nautilus.yaml || printf '\nretain: {}\n' >> nautilus.yaml
U=http://127.0.0.1:18084
start() { NAUTILUS_ADDR=127.0.0.1:18084 naut run . > run.log 2>&1 & pid=$!
          for i in $(seq 1 30); do curl -sf $U/api/state >/dev/null && return; sleep 0.2; done; }
get() { curl -s $U/api/state | python3 -c 'import json,sys; print(json.load(sys.stdin)["tags"]["LeadStartPct"])'; }
fails=0
for SIG in TERM INT; do
  rm -f retain.json; start; sleep 2.5
  code=$(curl -s -o /dev/null -w '%{http_code}' -X POST $U/api/tags -d '{"name":"LeadStartPct","value":42}')
  sleep 0.2; live=$(get)
  kill -$SIG $pid; wait $pid; ec=$?; pid=""
  saved=$(python3 -c 'import json; print(json.load(open("retain.json"))["tags"].get("LeadStartPct"))' 2>/dev/null)
  start; after=$(get); kill $pid; wait $pid 2>/dev/null; pid=""
  if [ "$code" = 204 ] && [ "$live" = 42 ] && [ "$after" = 42 ]; then r=PASS; else r=FAIL; fails=$((fails+1)); fi
  echo "$r  SIG$SIG 0.2 s after the write: write=$code live=$live exit=$ec retain.json=$saved after restart=$after (want 42)"
done
[ $fails -eq 0 ] && { echo PASS; exit 0; }; echo "FAIL a setpoint written just before an orderly stop is lost"; exit 1
