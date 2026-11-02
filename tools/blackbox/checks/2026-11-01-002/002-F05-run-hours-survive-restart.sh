#!/usr/bin/env bash
# F-05 (gap, customer expectation): with `retain: {}` configured, a per-pump run-hours total
# (P1_Stats, role: state, written by the logic) should survive a controller restart, as a Logix tag,
# a Codesys RETAIN/PERSISTENT variable or a TIA retentive DB does. Today only role: setpoint tags are
# retained (documented: "state re-seeds"), so P1_Stats comes back {Hours: 0, Starts: 0}.
# Passes when P1_Stats.Hours is still >= 5 after a SIGTERM restart. Wall clock ~8 s. Port 18081.
# SUT_PROJECT = evidence/pumps (which has retain: {}).
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); pid=""; trap '[ -n "$pid" ] && kill $pid 2>/dev/null; rm -rf "$T"' EXIT
cp -r "$P"/. "$T"/; cd "$T"; rm -f retain.json
grep -q '^retain:' nautilus.yaml || printf '\nretain: {}\n' >> nautilus.yaml
U=http://127.0.0.1:18081
start() { NAUTILUS_ADDR=127.0.0.1:18081 naut run . > run.log 2>&1 & pid=$!
          for i in $(seq 1 30); do curl -sf $U/api/state >/dev/null && return; sleep 0.2; done; }
hours() { curl -s $U/api/state | python3 -c 'import json,sys; print(json.load(sys.stdin)["tags"]["P1_Stats"])'; }
start
curl -s -X POST $U/api/forces -d '{"name":"LevelPct","value":65}' >/dev/null
curl -s -X POST $U/api/tags -d '{"name":"P1_Stats.Hours","value":5}' >/dev/null
sleep 3; echo "before restart: $(hours)"
kill $pid; wait $pid 2>/dev/null; pid=""; cat retain.json 2>/dev/null; echo
start; after=$(hours); echo "after restart: $after"
python3 -c "import ast,sys; sys.exit(0 if ast.literal_eval('''$after''')['Hours']>=5 else 1)" && { echo PASS; exit 0; }
echo "FAIL run hours lost across restart"; exit 1
