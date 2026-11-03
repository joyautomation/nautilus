#!/usr/bin/env bash
# pass: `naut build` emits one binary that carries the whole project. Copied alone into an empty
# directory and started with NAUTILUS_ADDR=127.0.0.1:18082, it serves the dashboard and /api/state
# with the project's tags, enum names and program locals, and the logic reacts to a forced level.
# SUT_PROJECT = evidence/pumps. Needs curl and python3 on the runner.
set -u
P=${SUT_PROJECT:?SUT_PROJECT}
T=$(mktemp -d); pid=""; trap '[ -n "$pid" ] && kill $pid 2>/dev/null; rm -rf "$T"' EXIT
cp -r "$P"/. "$T/src"/ 2>/dev/null || { mkdir -p "$T/src"; cp -r "$P"/. "$T/src"/; }
cd "$T/src" && rm -f retain.json && naut build -o pumps || { echo "FAIL build"; exit 1; }
mkdir "$T/alone" && cp pumps "$T/alone/" && cd "$T/alone" && ls -A
NAUTILUS_ADDR=127.0.0.1:18082 ./pumps > run.log 2>&1 & pid=$!
for i in $(seq 1 30); do curl -sf http://127.0.0.1:18082/api/state >/dev/null && break; sleep 0.2; done
cat run.log
U=http://127.0.0.1:18082
d=$(curl -s -o /dev/null -w '%{http_code}' $U/); echo "dashboard $d"
curl -s -X POST $U/api/forces -d '{"name":"LevelPct","value":85}' >/dev/null; sleep 0.5
curl -s $U/api/state | python3 -c '
import json,sys; d=json.load(sys.stdin); t=d["tags"]
print({k:t[k] for k in ("Lead","Station","P1_Run","P2_Run","HiLevelDelay")}, "hiTon.PT", d["locals"]["hiTon"]["PT"])
ok = t["Lead"] in ("Pump1","Pump2") and t["Station"]=="BothRunning" and t["P1_Run"] and t["P2_Run"] and d["locals"]["hiTon"]["PT"]==30000
sys.exit(0 if ok else 1)' && [ "$d" = 200 ] && { echo PASS; exit 0; }
echo FAIL; exit 1
